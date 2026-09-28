// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"errors"
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

// A test-only C5 authorizer cannot replace the missing run credential. Exercise
// the real stored spec, both engines and a fresh provider replay id each time.
func TestJoinedReplaysCannotBypassTheCredentialWithAC5Authorizer(t *testing.T) {
	for _, engineName := range []string{"sqlite", "postgres"} {
		t.Run(engineName, func(t *testing.T) {
			r := bootRealStoreInbound(t, engineName, "realstore")
			before, protocol := communicationHTTPTestEffects(t, r.e.eng, r.e.tT), a2aProtocolRows(t, r.e)
			message := r.message("realstore")
			for _, replayID := range []string{"realstore-jti-1", "realstore-jti-2"} {
				message.ReplayID = replayID
				_, elapsed, err := r.route(message)
				requireA2ACredentialHold(t, err, elapsed)
				requireA2ANoEffects(t, r.e, before, protocol)
			}
			r.kernel.mu.Lock()
			applies, replayErr := r.kernel.applies, r.kernel.replayErr
			r.kernel.mu.Unlock()
			requireA2ACredentialHold(t, replayErr, 0)
			if applies != 0 {
				t.Fatalf("refused A2A ran %d work commands", applies)
			}
		})
	}
}
