// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestRiskDeclaredIntentThroughComposition(t *testing.T) {
	for _, backend := range consentEngines {
		t.Run(backend, func(t *testing.T) {
			e := bootConsentEstate(t, backend)
			agent := e.do("POST", "/v1/agents", e.admin, e.tT, map[string]any{"name": "c52-risk-agent", "kind": "claude-code", "status": "active"})
			if agent.code != http.StatusCreated {
				t.Fatalf("agent create=%d %s", agent.code, agent.raw)
			}
			agentID := agent.body["id"].(string)
			classify := func(tenant model.TenantID, state, tier string) map[string]any {
				t.Helper()
				r := e.do("POST", "/v1/m/compliance/risk/classify", e.admin, tenant, map[string]any{"subject_ref": agentID})
				if r.code != http.StatusCreated {
					t.Fatalf("classify=%d %s", r.code, r.raw)
				}
				signals := r.body["signals"].(map[string]any)
				d, ok := signals["declared_autonomy"].(map[string]any)
				if !ok || d["state"] != state || r.body["suggested_tier"] != tier {
					t.Fatalf("declared risk state=%v tier=%v; wanted %s/%s", d, r.body["suggested_tier"], state, tier)
				}
				return signals
			}
			classify(e.tT, "none_declared", "minimal")
			rec := scheduleRecord("c52-intent", model.ID(agentID))
			rec["subject_ref"] = agentID
			rec["trigger_kind"] = "event"
			rec["cadence_spec"] = "c52-cadence-canary"
			scheduleID := e.restoreRow(e.tT, "orchestration.schedule", rec)
			declared := classify(e.tT, "declared", "limited")
			if declared["total_edges"] != float64(0) {
				t.Fatal("declaration fabricated observed activity")
			}
			classify(e.tB, "none_declared", "minimal")
			// Native observed findings continue to dominate declaration absence.
			if err := e.eng.store.Mutate(context.Background(), e.tT, func(sc store.Scope) error {
				_, err := sc.Findings().Create(context.Background(), model.Finding{Kind: "guardrail", Severity: model.SeverityHigh, Status: model.FindingOpen, Source: "fixture", SubjectKind: "agent", SubjectID: model.ID(agentID), Title: "observed conflict", OccurredAt: (model.SystemClock{}).Now()})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := e.eng.store.Mutate(context.Background(), e.tT, func(sc store.Scope) error {
				repo, err := sc.Ext("orchestration.schedule")
				if err != nil {
					return err
				}
				row, err := repo.Get(context.Background(), scheduleID)
				if err != nil {
					return err
				}
				row["desired_status"] = "paused"
				_, err = repo.Update(context.Background(), row)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			classify(e.tT, "none_declared", "high")
			read := e.do("GET", "/v1/m/compliance/risk", e.admin, e.tT, nil)
			if read.code != http.StatusOK || strings.Contains(read.raw, "c52-cadence-canary") {
				t.Fatalf("risk retained read-back=%d %s", read.code, read.raw)
			}
			if _, err := (orchestrationAutonomy{}).Autonomy(context.Background(), e.tT, agentID); err == nil {
				t.Fatal("missing module invented absence")
			}
		})
	}
}
