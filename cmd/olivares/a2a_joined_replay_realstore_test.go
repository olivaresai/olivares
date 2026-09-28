// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	a2a "github.com/olivaresai/olivares/connectors/a2a"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// acceptingA2ASpecValidator validates an A2A binding spec as the operator's
// configured peer would, so the estate can store the route's spec.
type acceptingA2ASpecValidator struct{}

func (acceptingA2ASpecValidator) ValidateProtocolBindingSpec(
	context.Context, model.TenantID, sessions.ProtocolBindingSpecInput,
) (sessions.ProtocolBindingValidation, error) {
	return sessions.ProtocolBindingValidation{
		Verdict: sessions.ProtocolObservationClean, Code: "fixture_peer_validated", ObservedAt: time.Now().UTC(),
	}, nil
}

// liveEpochOperationAuthorizer is a C5 operation authorizer bound by a test
// only; the Community composition binds none. It allows every operation with
// a witness on the tenant's live authorization and directory epochs, which it
// reads from the store each time it is asked, so a call made inside an owning
// replay transaction would wait on that transaction's connection on SQLite.
type liveEpochOperationAuthorizer struct {
	st store.Store
}

func (a liveEpochOperationAuthorizer) AuthorizeEntityOperation(
	ctx context.Context,
	principal sessions.CommunicationPrincipal,
	entity sessions.EntityRef,
	operation sessions.CommunicationOperation,
) (sessions.ReadWitness, error) {
	var facts []store.AuthorizationFactRef
	if err := a.st.View(ctx, entity.TenantID, func(sc store.Scope) error {
		directory, ok := sc.(store.DirectorySnapshotReader)
		if !ok {
			return errors.New("the scope reads no directory epoch")
		}
		epoch, err := directory.ReadDirectoryEpoch(ctx)
		if err != nil {
			return err
		}
		authorization, ok := sc.(store.AuthorizationEpochReader)
		if !ok {
			return errors.New("the scope reads no authorization epoch")
		}
		fact, err := authorization.ReadAuthorizationEpoch(ctx)
		if err != nil {
			return err
		}
		facts = []store.AuthorizationFactRef{
			fact, {Kind: model.DirectoryEpochKind, ID: model.ID(entity.TenantID), Version: epoch.Version},
		}
		return nil
	}); err != nil {
		return sessions.ReadWitness{}, err
	}
	now := time.Now().UTC()
	clean := func(code string) sessions.AuthorityEvidence {
		return sessions.AuthorityEvidence{Verdict: sessions.VerdictClean, Code: code, EvidenceRef: "c5_test_" + code}
	}
	return sessions.ReadWitness{
		Outcome: sessions.ReadAllow, Code: "message_send_decided", Entity: entity,
		Operation: operation, Principal: principal, ObservedAt: now, FreshUntil: now.Add(5 * time.Minute),
		CorePermission: clean("core_permission"), ResourceGuard: clean("resource_guard"),
		ForbidAbsence: clean("forbid_absence"), EvidenceRef: "c5_test_authority", Facts: facts,
	}, nil
}

// activateA2AInboundSpec stores and activates, through the kernel, the inbound
// A2A spec an operator configures for peer in workspace.
func activateA2AInboundSpec(
	t *testing.T,
	sm *sessions.Module,
	tenant model.TenantID,
	workspace model.ID,
	peer string,
) sessions.ProtocolBindingSpec {
	t.Helper()
	ctx := context.Background()
	input := sessions.ProtocolBindingSpecInput{
		WorkspaceID: workspace, BindingKey: "a2a-inbound", Generation: 1,
		Protocol: sessions.BindingProtocolA2A, ProtocolVersion: a2a.ProtocolVersion,
		Direction: sessions.BindingInbound, LocalKind: sessions.BindingLocalWorkItem,
		LocalSelector: json.RawMessage(`{"work_kind":"operations"}`),
		PeerAuthority: peer, RemoteResourceKind: "agent", RemoteResourceRef: "agent:peer",
		MappingSchema: sessions.ProtocolBindingMappingSchemaV1,
		Mapping: []sessions.ProtocolMappingRule{{
			Source: "message.text", Target: "work.brief",
			Cardinality: sessions.ProtocolMappingOneToOne, Transform: sessions.ProtocolTransformText,
		}},
		KnownLosses: []sessions.ProtocolBindingLoss{}, RuleRefs: []string{"rule:a2a-inbound"},
		PermissionProfileRef: "permission:a2a-inbound", CurrencyPolicy: sessions.BindingCurrencyPinned,
		Validation: sessions.ProtocolBindingValidation{
			Verdict: sessions.ProtocolObservationClean, Code: "fixture_peer_validated", ObservedAt: time.Now().UTC(),
		},
	}
	draft := sessions.ProtocolBindingSpecCommand{
		Operation: sessions.ProtocolBindingSpecCreateDraft, WorkspaceID: workspace,
		Input: &input, IdempotencyKey: model.NewID().String(),
	}
	plan, err := sm.PlanProtocolBindingSpec(ctx, tenant, draft)
	if err != nil {
		t.Fatalf("plan the inbound A2A spec: %v", err)
	}
	draft.ExpectedPlanHash = plan.PlanHash
	created, err := sm.ApplyProtocolBindingSpec(ctx, tenant, draft)
	if err != nil {
		t.Fatalf("create the inbound A2A spec: %v", err)
	}
	activate := sessions.ProtocolBindingSpecCommand{
		Operation: sessions.ProtocolBindingSpecActivate, WorkspaceID: workspace,
		SpecID: created.Spec.ID, ExpectedVersion: created.Spec.Version,
	}
	activationPlan, err := sm.PlanProtocolBindingSpec(ctx, tenant, activate)
	if err != nil {
		t.Fatalf("plan the inbound A2A spec's activation: %v", err)
	}
	activate.ExpectedPlanHash = activationPlan.PlanHash
	active, err := sm.ApplyProtocolBindingSpec(ctx, tenant, activate)
	if err != nil {
		t.Fatalf("activate the inbound A2A spec: %v", err)
	}
	return active.Spec
}

// T6b and T5b, the real-store deciders: on the production composition with a
// test C5 authorizer bound (not production, which binds none), the evidence
// ports are the store's own resolvers, so a read left inside the owning
// replay transaction would wait on SQLite's one connection. An inbound message
// to a user-owned route commits its work item, binding and reply within the
// bound, and a redelivery under a fresh token answers the original task. A
// push interrupt for that task then commits its message and link within the
// bound, and the same delivery again is answered as a replay.
func TestJoinedReplaysCommitOnTheRealStoreWithAC5Authorizer(t *testing.T) {
	for _, engineName := range []string{"sqlite", "postgres"} {
		t.Run(engineName, func(t *testing.T) {
			e := bootMessagingEstate(t, engineName)
			g := e.comm
			sm := e.eng.sessionsMod
			sm.UseCommunicationCoreEntityOperationAuthorizer(liveEpochOperationAuthorizer{st: e.eng.store})
			sm.UseProtocolBindingSpecValidator(sessions.BindingProtocolA2A, acceptingA2ASpecValidator{})
			peer := "https://realstore-" + engineName + ".example"
			spec := activateA2AInboundSpec(t, sm, e.tT, g.handoffs.workspace, peer)
			kernel := &joinedReplayKernel{a2aInboundKernel: sm, spec: spec}
			router, err := newA2AInboundRouter(kernel, []a2aInboundRouteConfig{{
				PeerAuthority: peer, Tenant: e.tT.String(), WorkspaceID: g.handoffs.workspace.String(),
				BindingSpecID: spec.ID.String(), BindingSpecGeneration: spec.Generation,
				ChannelID: g.handoffs.channelID.String(), SenderUserID: g.sender.id.String(),
				RecipientUserID: g.control.id.String(),
				OwnerKind:       "user", OwnerRef: g.control.id.String(),
			}}, []string{peer})
			if err != nil {
				t.Fatalf("new router: %v", err)
			}
			message := a2a.InboundMessage{
				PeerAuthority: peer, PeerSubject: "peer-agent", Protocol: a2a.ProtocolVersion,
				MessageID: "realstore-message", ContextID: "realstore-context", Role: "ROLE_AGENT",
				Parts: []a2a.InboundPart{
					{Kind: "text", Text: "Perform the routed work.", Digest: strings.Repeat("a", 64)},
				},
				ReplayID: "realstore-jti-1", ReplayExpiresAt: time.Now().UTC().Add(time.Hour),
			}
			route := func(delivery a2a.InboundMessage) (a2a.InboundResult, time.Duration, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				started := time.Now()
				result, err := router.RouteInboundA2A(ctx, delivery)
				return result, time.Since(started), err
			}

			// T6b.
			first, elapsed, err := route(message)
			if err != nil || elapsed >= joinedReplayKernelBound {
				t.Fatalf("inbound message answered %v after %s, want it committed within %s",
					err, elapsed.Round(time.Millisecond), joinedReplayKernelBound)
			}
			if first.ResultKind != "task" || first.TaskID == "" || first.ContextID != message.ContextID {
				t.Fatalf("inbound result = %+v, want the routed task", first)
			}
			kernel.mu.Lock()
			applies, replayErr := kernel.applies, kernel.replayErr
			kernel.mu.Unlock()
			if applies != 2 || replayErr != nil {
				t.Fatalf("the kernel ran %d work commands and its replay answered %v, want item.create and item.ready committed",
					applies, replayErr)
			}
			redelivery := message
			redelivery.ReplayID = "realstore-jti-2"
			again, elapsed, err := route(redelivery)
			if err != nil || elapsed >= joinedReplayKernelBound || again.TaskID != first.TaskID {
				t.Fatalf("redelivery under a fresh token answered %+v, %v after %s, want task %s within %s",
					again, err, elapsed.Round(time.Millisecond), first.TaskID, joinedReplayKernelBound)
			}
			kernel.mu.Lock()
			applies = kernel.applies
			kernel.mu.Unlock()
			if applies != 2 {
				t.Fatalf("the redelivery ran %d work commands in all, want the first two only", applies)
			}

			// T5b.
			settlement := &a2aPushSettlement{store: sm, routes: map[string]parsedA2APushRoute{
				peer: {tenant: e.tT, workspace: g.handoffs.workspace, interrupt: sessions.ProtocolInterruptRoute{
					ChannelID: g.handoffs.channelID, SenderUserID: g.sender.id, RecipientUserID: g.control.id,
				}},
			}}
			update := a2a.TaskUpdate{
				TaskID: first.TaskID, ContextID: message.ContextID, State: a2a.TaskStateInputReq, Interrupt: true,
				Sender: peer, ReplayID: "realstore-push-jti", ReplayExpiresAt: time.Now().UTC().Add(time.Hour),
			}
			for _, delivery := range []string{"first", "again"} {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				started := time.Now()
				err := settlement.Record(ctx, update)
				elapsed := time.Since(started)
				cancel()
				want := error(nil)
				if delivery == "again" {
					want = a2a.ErrReplay
				}
				if !errors.Is(err, want) || (want == nil && err != nil) || elapsed >= joinedReplayKernelBound {
					t.Fatalf("%s push interrupt answered %v after %s, want %v within %s",
						delivery, err, elapsed.Round(time.Millisecond), want, joinedReplayKernelBound)
				}
			}
		})
	}
}
