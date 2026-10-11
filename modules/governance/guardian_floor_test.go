// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestGuardianSweepHonorsLiveCriticalFloor(t *testing.T) {
	for _, arm := range []string{"stop_agent", "stop_estate", "quarantine_nhi"} {
		t.Run(arm, func(t *testing.T) {
			h := newHarness(t)
			admin := h.adminLogin()
			tenant := h.createOrg(admin, "guardian-live-floor")
			_, reviewer := h.roleUser(admin, tenant, "guardian-reviewer@x.io", "admin")
			targetKind, targetRef := "agent", "guardian-target"
			switch arm {
			case "stop_agent":
				h.createAgent(tenant, "guardian-target", targetRef)
			case "stop_estate":
				targetKind, targetRef = "estate", ""
			case "quarantine_nhi":
				targetKind = "identity"
				h.seedIdentity(tenant, targetRef, "vault_entity", "vault", "service", false)
			}
			rule := h.do("POST", govPath+"/guardian/rules", admin, map[string]any{
				"name": "live-floor", "match_kinds": "anomaly_detected", "min_severity": "high", "action": arm, "mode": "approval",
			}, tenantHdr(tenant))
			if rule.code != http.StatusCreated {
				t.Fatalf("guardian rule = %d %s", rule.code, rule.raw)
			}
			action := "security.guardian." + arm
			subjectRef := targetRef
			if subjectRef == "" {
				subjectRef = "estate"
			}
			request := h.createApproval(admin, tenant, map[string]any{
				"action": action, "subject_kind": targetKind, "subject_ref": subjectRef,
			})
			if request.code != http.StatusCreated || request.body["required_approvals"] != float64(1) {
				t.Fatalf("original HIGH request = %d %s", request.code, request.raw)
			}
			approvalID := request.body["id"].(string)
			if vote := h.decide(reviewer, tenant, approvalID, "approve"); vote.code != http.StatusOK || vote.body["status"] != "approved" {
				t.Fatalf("original independent approval = %d %s", vote.code, vote.raw)
			}
			// The pending action binds a genuinely issued one-person approval.
			// Seed only the bus-created action trail; the decision, policy and
			// containment paths use the real module and store.
			var actionID model.ID
			if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(model.Kind("governance.guardian_action"))
				if err != nil {
					return err
				}
				rec, err := repo.Create(t.Context(), model.Record{
					"rule_id": rule.body["id"].(string), "rule_name": "live-floor", "finding_kind": "anomaly_detected",
					"finding_ref": "synthetic-finding", "finding_severity": "high", "target_kind": targetKind,
					"target_ref": targetRef, "action": arm, "mode": "approval", "status": "pending", "approval_id": approvalID,
				})
				if err == nil {
					actionID = model.ID(rec.String(model.ColID))
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			h.createApprovalPolicy(admin, tenant, "raise-before-execution", map[string]any{
				"risk_tier": "critical", "required_approvals": 2, "match": map[string]any{"action": action},
			})
			if read := h.do("GET", govPath+"/approvals/"+approvalID, admin, nil, tenantHdr(tenant)); read.code != http.StatusOK || read.body["status"] != "expired" {
				t.Errorf("raised approval read = %d %s, want unavailable", read.code, read.raw)
			}
			if spent, err := h.gov.EngineApprovals().Consume(t.Context(), tenant, approvalID, "guardian-live-floor-probe", ""); err != nil || spent.Granted {
				t.Errorf("live CRITICAL grant consumed with one human: %+v %v", spent, err)
			}
			result, err := h.gov.GuardianSweep(t.Context(), tenant)
			if err != nil || result.Executed != 0 || result.Expired != 1 {
				t.Errorf("live CRITICAL guardian grant executed with one human: %+v %v", result, err)
			}
			if err := h.st.View(t.Context(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(model.Kind("governance.guardian_action"))
				if err != nil {
					return err
				}
				rec, err := repo.Get(t.Context(), actionID)
				if err == nil && rec.String("status") != "expired" {
					t.Errorf("guardian action status = %s, want expired", rec.String("status"))
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			// Availability must not rewrite the historical human decision.
			if err := h.st.View(t.Context(), tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(model.Kind("governance.approval"))
				if err != nil {
					return err
				}
				rec, err := repo.Get(t.Context(), model.ID(approvalID))
				if err == nil && (rec.String("status") != "approved" || rec.Int("approve_count") != 1) {
					t.Errorf("historical guardian decision changed: %+v", rec)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
