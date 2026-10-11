// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// epochMovingAttestation runs hook after each of the first limit work-task
// audience attestations made outside a replay transaction: the last evidence
// read of an interrupt's preparation, so each hook runs between a preparation
// and its owning transaction.
type epochMovingAttestation struct {
	next   PublicationAudienceAttestor
	tenant model.TenantID
	limit  int
	hook   func(context.Context)

	mu    sync.Mutex
	fired int
}

func (a *epochMovingAttestation) AttestPublicationAudience(
	ctx context.Context, request PublicationAudienceRequest,
) (DirectorySnapshot, PublicationAudienceAttestation, error) {
	snapshot, attestation, err := a.next.AttestPublicationAudience(ctx, request)
	if _, joined := protocolReplayScopeFromContext(ctx, a.tenant); !joined && err == nil &&
		request.MessageKind == MessageWorkTask {
		a.mu.Lock()
		fire := a.fired < a.limit
		if fire {
			a.fired++
		}
		a.mu.Unlock()
		if fire {
			a.hook(ctx)
		}
	}
	return snapshot, attestation, err
}

func (a *epochMovingAttestation) firedCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.fired
}

// moveWorkflowDirectoryEpoch onboards a member, which moves the tenant's
// directory epoch, and makes the fixture's evidence doubles answer on the
// moved epoch from then on, as the store's own resolvers would. It reports
// whether the epoch moved.
func moveWorkflowDirectoryEpoch(
	t *testing.T,
	ctx context.Context,
	fixture workflowCommunicationFixture,
	resolver *directNoticeReadDirectoryResolver,
	closure *directNoticeReadClosureResolver,
	email string,
) bool {
	t.Helper()
	before, _, err := readWorkflowAuthorityFacts(ctx, fixture)
	if err != nil {
		t.Errorf("read the directory epoch before the move: %v", err)
		return false
	}
	if _, err := fixture.authr.OnboardMember(ctx, fixture.authUser, fixture.tenant, auth.OnboardInput{
		Email: email, DisplayName: "Epoch mover", Role: auth.RoleViewer, Password: "epoch-mover-password",
	}); err != nil {
		t.Errorf("onboard %s to move the directory epoch: %v", email, err)
		return false
	}
	epoch, facts, err := readWorkflowAuthorityFacts(ctx, fixture)
	if err != nil {
		t.Errorf("read the moved directory epoch: %v", err)
		return false
	}
	fixture.attestor.epoch = epoch
	fixture.authorizer.facts = append([]store.AuthorizationFactRef(nil), facts...)
	fixture.source.evidence.Facts = append([]store.AuthorizationFactRef(nil), facts...)
	resolver.epoch = epoch
	closure.epoch = epoch
	return epoch != before
}

// T7, a stale epoch N=2, before CM-10
// (TestAJoinedInterruptAnswersAMoveAfterTheEpochMovesTwice). An interrupt is an
// A2A path, so it is now refused while its publish is prepared, before the
// audience attestation after which the epoch would move: no hook fires and
// nothing is written. A prepared publish's stale-epoch retry is reachable only
// through an A2A publish, which CM-10 refuses; it has no positive test here.
func TestAJoinedInterruptIsRefusedBeforeAnEpochThatMovesTwice(t *testing.T) {
	t.Parallel()

	fixture := newWorkflowCommunicationFixture(t, false)
	makeProtocolInterruptRecipientWriter(t, fixture)
	binding := protocolInterruptBindingForTest(t, fixture, BindingProtocolA2A)
	resolver, _ := fixture.m.CommunicationDirectoryResolver.(*directNoticeReadDirectoryResolver)
	closure, _ := fixture.m.CommunicationGrantClosure.(*directNoticeReadClosureResolver)
	if resolver == nil || closure == nil {
		t.Fatal("the workflow fixture's read resolvers are not the doubles this test refreshes")
	}
	calls := installJoinedPortChecks(fixture.m, fixture.tenant)
	moving := &movingStanding{next: fixture.m.Standing}
	fixture.m.Standing = moving
	moved := 0
	mover := &epochMovingAttestation{
		next: fixture.m.CommunicationAudienceAttestor, tenant: fixture.tenant, limit: 2,
	}
	mover.hook = func(ctx context.Context) {
		if moveWorkflowDirectoryEpoch(t, ctx, fixture, resolver, closure,
			fmt.Sprintf("epoch-mover-%d@communication.test", mover.firedCount())) {
			moved++
		}
	}
	fixture.m.CommunicationAudienceAttestor = mover
	_, elapsed, err := applyJoinedInterrupt(fixture,
		joinedInterruptClaim(fixture, binding, "stale-epoch-twice"),
		joinedInterruptCommand(fixture, binding, "stale-epoch-twice-request"))
	// CM-10: the interrupt is an A2A path. It is refused while its publish is
	// prepared, before any audience attestation, so neither epoch-moving hook
	// fires and nothing is written.
	if moved != 0 {
		t.Fatalf("the refused interrupt reached its audience attestation %d times", moved)
	}
	if inside := calls.insideCalls(); len(inside) != 0 {
		t.Fatalf("ports called inside the owning replay transaction: %v (the replay answered %v after %s)",
			inside, err, elapsed.Round(time.Millisecond))
	}
	requireA2ARefused(t, err, "an interrupt whose epoch would move twice")
	if elapsed >= joinedReplayBound {
		t.Fatalf("the refusal took %s, want an answer within %s", elapsed.Round(time.Millisecond), joinedReplayBound)
	}
	_ = moving
	wantJoinedInterruptRows(t, fixture, 0, "after the refusal")
}

// A prepared replay cannot open inside another replay's transaction, where its
// standing read and preparation could only wait on that transaction: it
// refuses with its typed error, and the owning replay rolls back with it.
func TestAPreparedReplayRefusesToOpenInsideAnotherReplay(t *testing.T) {
	t.Parallel()

	f := newWorkFixture(t, filepath.Join(t.TempDir(), "prepared-nested.db"), nil)
	defer f.st.Close()
	claim := func(replayID string) ProtocolReplayClaim {
		return ProtocolReplayClaim{
			WorkspaceID: f.workspace, Protocol: BindingProtocolA2A,
			PeerAuthority: "https://prepared-nested.example", Kind: ProtocolReplayJTI,
			ReplayID: replayID, ExpiresAt: time.Now().UTC().Add(time.Hour),
		}
	}
	var nestedErr error
	ctx, cancel := context.WithTimeout(context.Background(), workflowCommunicationTimeout)
	defer cancel()
	started := time.Now()
	_, err := f.m.ApplyProtocolReplay(ctx, f.tenant, claim("prepared-nested-outer"),
		func(joined context.Context) (ProtocolReplaySettlement, error) {
			_, nestedErr = f.m.ApplyPreparedProtocolReplay(joined, f.tenant, claim("prepared-nested-inner"),
				ProtocolReplayPlan{}, func(context.Context) (ProtocolReplaySettlement, error) {
					return ProtocolReplaySettlement{}, errors.New("the nested prepared replay ran its mutation")
				})
			return ProtocolReplaySettlement{}, nestedErr
		})
	elapsed := time.Since(started)
	if !errors.Is(nestedErr, ErrProtocolReplayPreparedNested) || !errors.Is(nestedErr, ErrInvalidProtocolReplay) ||
		elapsed >= joinedReplayBound {
		t.Fatalf("a prepared replay inside another answered %v after %s, want %v within %s",
			nestedErr, elapsed.Round(time.Millisecond), ErrProtocolReplayPreparedNested, joinedReplayBound)
	}
	if !errors.Is(err, ErrProtocolReplayPreparedNested) {
		t.Fatalf("the owning replay answered %v, want it rolled back with %v", err, ErrProtocolReplayPreparedNested)
	}
}
