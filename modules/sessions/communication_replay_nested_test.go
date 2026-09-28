// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// T3, the nested inbound case: a delivery shaped as the A2A inbound router
// makes it. The owning prepared replay declares the message identity's claim,
// the owner as an account and a participant, and the reply's publish; its
// nested plain replay creates a user-owned work item, makes it ready and
// publishes the inbound reply. No port is called while the owning transaction
// is open.
func TestANestedInboundReplayCallsNoPortInsideTheOwningTransaction(t *testing.T) {
	t.Parallel()

	fixture := newWorkflowCommunicationFixture(t, false)
	fixture.m.UseWorkContentGuard(allowWorkContent{})
	binding := protocolReplyBindingForTest(
		t, fixture, BindingInbound, ProtocolBindingResultTask,
		"task-nested-inbound", "context-nested-inbound", "", WorkflowWorkTaskResult{},
	)
	command := ProtocolReplyCommand{
		Flow:      ProtocolReplyFlowInbound,
		BindingID: binding.ID, Generation: binding.Generation, Route: protocolReplyRouteForTest(t, fixture),
		PeerAuthority: binding.PeerAuthority, Kind: ProtocolReplyMessage,
		TaskID: binding.ExternalID, ContextID: binding.ContextID, MessageID: "nested-inbound-message",
		SourceDigest: protocolReplyDigestForTest("nested-inbound-message"),
		Parts: []ProtocolReplyPart{{
			Kind: ProtocolReplyPartText, Text: "Authenticated nested inbound request.",
			Digest: protocolReplyDigestForTest("nested-inbound-text"),
		}},
	}
	owner := model.ID(fixture.target.Ref)
	messageClaim := ProtocolReplayClaim{
		WorkspaceID: fixture.workspace, Protocol: BindingProtocolA2A,
		PeerAuthority: binding.PeerAuthority, Kind: ProtocolReplayMessageID,
		ReplayID: command.MessageID, ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	plan := ProtocolReplayPlan{
		Accounts: []model.ID{owner},
		Nested:   []ProtocolReplayClaim{messageClaim},
		Participants: []ProtocolReplayParticipant{{
			WorkspaceID: fixture.workspace, Kind: "user", Ref: owner.String(),
		}},
		Publishes: []ProtocolReplayPublish{ProtocolReplyPublish(command.Route, command.Flow)},
	}
	calls := installJoinedPortChecks(fixture.m, fixture.tenant)
	fixture.m.workIdentity = joinedCheckedParticipants{calls: calls}
	principal := WorkPrincipal{
		ActorKind: model.ActorUser, ActorRef: model.NewID().String(),
		Actor: "user:" + model.NewID().String(), Admin: true,
	}
	itemsBefore := len(communicationRowsForTest(t, fixture.directNoticeFixture, workItemKind))
	messagesBefore := len(communicationRowsForTest(t, fixture.directNoticeFixture, messageKind))
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommunicationTimeout)
	defer cancel()
	started := time.Now()
	_, err := fixture.m.ApplyPreparedProtocolReplay(ctx, fixture.tenant, ProtocolReplayClaim{
		WorkspaceID: fixture.workspace, Protocol: BindingProtocolA2A,
		PeerAuthority: binding.PeerAuthority, Kind: ProtocolReplayJTI,
		ReplayID: "nested-inbound-jti", ExpiresAt: time.Now().UTC().Add(time.Hour),
	}, plan, func(joined context.Context) (ProtocolReplaySettlement, error) {
		nested, err := fixture.m.ApplyProtocolReplay(joined, fixture.tenant, messageClaim,
			func(messageCtx context.Context) (ProtocolReplaySettlement, error) {
				created, err := fixture.m.Apply(messageCtx, fixture.tenant, principal, WorkCommand{
					Command: "item.create", WorkspaceID: fixture.workspace, WorkKind: "implementation",
					Title: "Nested inbound work", BriefMD: "Perform the routed work.", ContextRefs: []ContextRef{},
					Priority: "p1", OwnerKind: "user", OwnerRef: owner.String(),
					ProvenanceKind: "human", ProvenanceRef: "test:nested-inbound",
					Acceptance: []AcceptanceInput{{
						Key: "remote_result_review", Ordinal: 0, Statement: "Review the remote result.", Required: true,
					}},
					IdempotencyKey: model.NewID().String(), HTTPMethod: http.MethodPost,
					CommandScope: "a2a.inbound.create:nested",
				})
				if err != nil {
					return ProtocolReplaySettlement{}, err
				}
				if _, err := fixture.m.Apply(messageCtx, fixture.tenant, principal, WorkCommand{
					Command: "item.ready", WorkItemID: created.ResultID, WorkspaceID: fixture.workspace,
					ExpectedVersion: created.Version, IdempotencyKey: model.NewID().String(),
					HTTPMethod: http.MethodPost, CommandScope: "a2a.inbound.ready:nested",
				}); err != nil {
					return ProtocolReplaySettlement{}, err
				}
				if _, err := fixture.m.ProjectProtocolReply(messageCtx, fixture.tenant, command); err != nil {
					return ProtocolReplaySettlement{}, err
				}
				return ProtocolReplaySettlement{}, nil
			})
		if err != nil {
			return ProtocolReplaySettlement{}, err
		}
		if nested.Replayed {
			return ProtocolReplaySettlement{}, errors.New("the message's first delivery was answered as a replay")
		}
		return ProtocolReplaySettlement{}, nil
	})
	elapsed := time.Since(started)
	if inside := calls.insideCalls(); len(inside) != 0 {
		t.Fatalf("ports called inside the owning replay transaction: %v (the replay answered %v after %s)",
			inside, err, elapsed.Round(time.Millisecond))
	}
	// CM-10: the inbound reply is an A2A path. It is refused while its publish
	// is prepared, before the owning transaction, so the nested work item is
	// never created and no Message is written.
	requireA2ARefused(t, err, "nested inbound delivery")
	if elapsed >= joinedReplayBound {
		t.Fatalf("nested inbound delivery answered after %s, want an answer within %s",
			elapsed.Round(time.Millisecond), joinedReplayBound)
	}
	if items := len(communicationRowsForTest(t, fixture.directNoticeFixture, workItemKind)); items != itemsBefore {
		t.Fatalf("work items after the refusal = %d, want %d", items, itemsBefore)
	}
	if messages := len(communicationRowsForTest(t, fixture.directNoticeFixture, messageKind)); messages != messagesBefore {
		t.Fatalf("messages after the refusal = %d, want %d", messages, messagesBefore)
	}
}

// afterWorkTaskAttestation runs hook once, after the first work-task audience
// attestation made outside a replay transaction: the last evidence read an
// interrupt's preparation makes, so the hook runs between the preparation and
// the owning transaction.
type afterWorkTaskAttestation struct {
	next   PublicationAudienceAttestor
	tenant model.TenantID
	hook   func(context.Context)

	once sync.Once
}

func (a *afterWorkTaskAttestation) AttestPublicationAudience(
	ctx context.Context, request PublicationAudienceRequest,
) (DirectorySnapshot, PublicationAudienceAttestation, error) {
	snapshot, attestation, err := a.next.AttestPublicationAudience(ctx, request)
	if _, joined := protocolReplayScopeFromContext(ctx, a.tenant); !joined && err == nil &&
		request.MessageKind == MessageWorkTask {
		a.once.Do(func() { a.hook(ctx) })
	}
	return snapshot, attestation, err
}

// readWorkflowAuthorityFacts reads the tenant's live directory epoch and the
// authority facts a workflow fixture's evidence doubles answer with.
func readWorkflowAuthorityFacts(
	ctx context.Context, fixture workflowCommunicationFixture,
) (int64, []store.AuthorizationFactRef, error) {
	var epoch int64
	var authorization store.AuthorizationFactRef
	err := fixture.m.viewCommunication(ctx, fixture.scope, func(sc store.Scope) error {
		directory, readErr := sc.(store.DirectorySnapshotReader).ReadDirectoryEpoch(ctx)
		if readErr != nil {
			return readErr
		}
		epoch = directory.Version
		authorization, readErr = sc.(store.AuthorizationEpochReader).ReadAuthorizationEpoch(ctx)
		return readErr
	})
	return epoch, []store.AuthorizationFactRef{
		authorization, {Kind: model.DirectoryEpochKind, ID: model.ID(fixture.tenant), Version: epoch},
	}, err
}

// T7, a stale epoch N=1, before CM-10
// (TestAJoinedInterruptPreparesAgainAfterTheDirectoryEpochMoves). An interrupt
// is an A2A path, so it is now refused while its publish is prepared, before
// the epoch could move, and nothing is written.
func TestAJoinedInterruptIsRefusedBeforeAMovingDirectoryEpoch(t *testing.T) {
	t.Parallel()

	fixture := newWorkflowCommunicationFixture(t, false)
	makeProtocolInterruptRecipientWriter(t, fixture)
	binding := protocolInterruptBindingForTest(t, fixture, BindingProtocolA2A)
	resolver, _ := fixture.m.communicationDirectoryResolver.(*directNoticeReadDirectoryResolver)
	closure, _ := fixture.m.communicationGrantClosure.(*directNoticeReadClosureResolver)
	if resolver == nil || closure == nil {
		t.Fatal("the workflow fixture's read resolvers are not the doubles this test refreshes")
	}
	calls := installJoinedPortChecks(fixture.m, fixture.tenant)
	moving := &movingStanding{next: fixture.m.standing}
	fixture.m.standing = moving
	epochBefore, _, err := readWorkflowAuthorityFacts(context.Background(), fixture)
	if err != nil {
		t.Fatalf("read the directory epoch: %v", err)
	}
	epochMoved := false
	fixture.m.communicationAudienceAttestor = &afterWorkTaskAttestation{
		next: fixture.m.communicationAudienceAttestor, tenant: fixture.tenant,
		hook: func(ctx context.Context) {
			if _, err := fixture.authr.OnboardMember(ctx, fixture.authUser, fixture.tenant, auth.OnboardInput{
				Email: "epoch-mover@communication.test", DisplayName: "Epoch mover",
				Role: auth.RoleViewer, Password: "epoch-mover-password",
			}); err != nil {
				t.Errorf("onboard a member to move the directory epoch: %v", err)
				return
			}
			epoch, facts, err := readWorkflowAuthorityFacts(ctx, fixture)
			if err != nil {
				t.Errorf("read the moved directory epoch: %v", err)
				return
			}
			epochMoved = epoch != epochBefore
			// The evidence doubles answer on the moved epoch from now on, as
			// the store's own resolvers would.
			fixture.attestor.epoch = epoch
			fixture.authorizer.facts = append([]store.AuthorizationFactRef(nil), facts...)
			fixture.source.evidence.Facts = append([]store.AuthorizationFactRef(nil), facts...)
			resolver.epoch = epoch
			closure.epoch = epoch
		},
	}
	_, elapsed, err := applyJoinedInterrupt(fixture,
		joinedInterruptClaim(fixture, binding, "stale-epoch"),
		joinedInterruptCommand(fixture, binding, "stale-epoch-request"))
	// CM-10: the interrupt is an A2A path. It is refused while its publish is
	// prepared, before any audience attestation, so the epoch-moving hook never
	// fires, no port is called inside a transaction and nothing is written.
	if epochMoved {
		t.Fatalf("the refused interrupt reached its audience attestation and moved the epoch from %d", epochBefore)
	}
	if inside := calls.insideCalls(); len(inside) != 0 {
		t.Fatalf("ports called inside the owning replay transaction: %v (the replay answered %v after %s)",
			inside, err, elapsed.Round(time.Millisecond))
	}
	requireA2ARefused(t, err, "an interrupt whose epoch would move")
	if elapsed >= joinedReplayBound {
		t.Fatalf("the refusal took %s, want an answer within %s", elapsed.Round(time.Millisecond), joinedReplayBound)
	}
	_ = moving
	wantJoinedInterruptRows(t, fixture, 0, "after the refusal")
}

// movingOnReadStanding answers the reads named in moveOn with every existing
// account's authority version one ahead of the stored one, so the barrier
// that pins that answer conflicts.
type movingOnReadStanding struct {
	next   auth.StandingReader
	moveOn map[int]bool

	mu    sync.Mutex
	reads int
}

func (s *movingOnReadStanding) Standing(
	ctx context.Context, tenant model.TenantID, users []model.ID,
) (map[model.ID]auth.Standing, error) {
	standing, err := s.next.Standing(ctx, tenant, users)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if err == nil && s.moveOn[s.reads] {
		for id, current := range standing {
			if current.Exists {
				current.AuthorityVersion++
				standing[id] = current
			}
		}
	}
	return standing, err
}

func (s *movingOnReadStanding) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reads
}

// T7, the mixed pairs: the owning replay answers the classification of its
// second attempt. A move and then a store conflict answer claim_conflict; a
// store conflict and then a move answer the typed authority move.
func TestAJoinedReplayAnswersItsSecondAttemptOnAMixedPair(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		moveOn int
		want   error
		not    error
	}{
		{name: "move then conflict", moveOn: 1, want: ErrProtocolReplayConflict, not: ErrProtocolReplayAuthorityMoved},
		{name: "conflict then move", moveOn: 2, want: ErrProtocolReplayAuthorityMoved, not: ErrProtocolReplayConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fixture := newWorkflowCommunicationFixture(t, false)
			moving := &movingOnReadStanding{next: fixture.m.standing, moveOn: map[int]bool{tc.moveOn: true}}
			fixture.m.standing = moving
			recipient := model.ID(fixture.target.Ref)
			plan := ProtocolReplayPlan{Accounts: []model.ID{fixture.sender, recipient}}
			claim := ProtocolReplayClaim{
				WorkspaceID: fixture.workspace, Protocol: BindingProtocolA2A,
				PeerAuthority: "https://mixed-pair.example", Kind: ProtocolReplayJTI,
				ReplayID:  "mixed-pair-" + strings.ReplaceAll(tc.name, " ", "-"),
				ExpiresAt: time.Now().UTC().Add(time.Hour),
			}
			mutations := 0
			ctx, cancel := context.WithTimeout(context.Background(), workflowCommunicationTimeout)
			defer cancel()
			started := time.Now()
			_, err := fixture.m.ApplyPreparedProtocolReplay(ctx, fixture.tenant, claim, plan,
				func(context.Context) (ProtocolReplaySettlement, error) {
					mutations++
					return ProtocolReplaySettlement{}, fmt.Errorf("injected store conflict: %w", store.ErrConflict)
				})
			elapsed := time.Since(started)
			if !errors.Is(err, tc.want) || errors.Is(err, tc.not) || errors.Is(err, store.ErrConflict) ||
				elapsed >= joinedReplayBound {
				t.Fatalf("%s answered %v after %s, want %v within %s",
					tc.name, err, elapsed.Round(time.Millisecond), tc.want, joinedReplayBound)
			}
			if reads := moving.readCount(); reads != 2 || mutations != 1 {
				t.Fatalf("%s read standing %d times and ran the mutation %d times, want 2 and 1",
					tc.name, reads, mutations)
			}
		})
	}
}
