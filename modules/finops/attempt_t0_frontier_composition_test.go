// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The empty-census tests cover the producer and its refusals. This case connects
// that normal durable frontier to T0 without repairing its document in a fixture.
// Only the active-state transition and binding authority remain laboratory facts.
func TestT0UsesTheNormalEmptyFrontier(t *testing.T) {
	eachAttemptBackend(t, func(t *testing.T, cfg store.Config) {
		m, st, tenant, _ := openFinCfg(t, cfg)
		v := t0Wire(m, tenant)
		// Use the engine's actual lock and TransactionNow throughout this journey.
		m.UseData(api.NewModuleData(st))
		req := t0Request(t, st, tenant)
		req.ReviewAfter = model.NewTimestamp(time.Now().Add(time.Hour))
		v.approve(req)
		t0Policy(t, st, tenant, policyKindBudget, t0Budget(100, "block"))
		t0Quiescing(t, m, st, tenant)

		quiescing, found, err := readScopeThrough(t, st, tenant)
		if err != nil || !found || quiescing.State != lifecycleQuiescing || quiescing.Version != 1 {
			t.Fatalf("normal Begin must leave one readable initial version: %+v found=%v err=%v", quiescing, found, err)
		}
		doc := frontierDocOf(t, st, tenant)
		if doc.PendingGroups == nil || len(doc.PendingGroups) != 0 || doc.PendingGroupCount != 0 {
			t.Fatalf("normal Begin did not persist a known-empty census: %+v", doc)
		}
		body := storedFrontierBody(t, st, tenant)
		frontierBefore := scopeRows(t, st, tenant)
		auditBefore := countAuditAction(t, st, tenant, auditActionBeginActivation)
		if auditBefore != 1 {
			t.Fatalf("normal Begin audit events = %d, want 1", auditBefore)
		}
		beginReq := LifecycleActivationRequest{Evidence: []EvidenceRef{labEvidence("fixture-quiescence")}}
		replayed, err := m.BeginLifecycleActivation(context.Background(), tenant, beginReq)
		if err != nil || !reflect.DeepEqual(replayed, quiescing) ||
			!reflect.DeepEqual(frontierBefore, scopeRows(t, st, tenant)) ||
			countAuditAction(t, st, tenant, auditActionBeginActivation) != auditBefore {
			t.Fatalf("normal Begin replay changed the boundary or audit: %+v err=%v", replayed, err)
		}

		if _, err := m.prepareAttempt(context.Background(), tenant, req); attemptCode(err) != errCodeLifecycleActivation {
			t.Fatalf("quiescing frontier must still refuse admission: %v", err)
		}
		t0NoRows(t, st, tenant)
		t0Active(t, m, st, tenant)
		if storedFrontierBody(t, st, tenant) != body {
			t.Fatal("laboratory activation changed the normal frontier document")
		}
		view := t0Admit(t, m, tenant, req)
		if view.Phase != phasePrepared || len(view.Targets) != 1 {
			t.Fatalf("T0 did not prepare its budget target: %+v", view)
		}
		got, err := m.GetAttempt(context.Background(), tenant, req.AttemptRef)
		if err != nil || !reflect.DeepEqual(got, view) {
			t.Fatalf("durable T0 reader: %+v err=%v", got, err)
		}
		parents, children := attemptRows(t, st, tenant), countReservations(t, st, tenant)
		if len(parents) != 1 || len(children) != 1 {
			t.Fatalf("T0 persisted %d parents and %d children", len(parents), len(children))
		}
		replay, err := m.prepareAttempt(context.Background(), tenant, req)
		if err != nil || !replay.Replayed || replay.Attempt == nil || !reflect.DeepEqual(*replay.Attempt, view) ||
			!reflect.DeepEqual(parents, attemptRows(t, st, tenant)) ||
			!reflect.DeepEqual(children, countReservations(t, st, tenant)) ||
			storedFrontierBody(t, st, tenant) != body ||
			countAuditAction(t, st, tenant, auditActionBeginActivation) != auditBefore {
			t.Fatalf("T0 replay changed the admitted group or frontier: %+v err=%v", replay, err)
		}
		t.Logf("backend=%s normal_frontier_version=1 begin_replay=stable quiescing_admission=refused active_state=fixture T0=prepared T0_replay=stable parents=1 children=1", cfg.Engine)
	})
}
