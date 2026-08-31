// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"testing"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/auth"
)

// adminaction_bridge_test.go proves, against the REAL engine, the two
// approval tiers the governed Admin-API actuator depends on: the irreversible
// workspace-archive is floored at TWO distinct human approvers (CRITICAL) and surfaces
// the dual-control evidence the connector re-verifies, while a recoverable key
// deactivation is released by a SINGLE approval. It mirrors nhilifecycle_bridge_test.go.

// TestAdminGateWorkspaceArchiveFlooredAtTwoApprovers: claude.admin.workspace.archive is in
// the default CRITICAL set (risktier.go), so the engine floors its threshold at two
// distinct humans; the adapter reports the distinct approvers and HasDualControl only once
// the quorum is met.
func TestAdminGateWorkspaceArchiveFlooredAtTwoApprovers(t *testing.T) {
	h := newHarness(t)
	_, approverB := h.createApprover(t, "adm-b@bridge.test")
	_, approverC := h.createApprover(t, "adm-c@bridge.test")
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	tid := tenantAID(t, h)
	ctx := context.Background()
	gate := br.adminGate(tid)

	req := claudeapi.AdminActionRequest{
		Tenant: tid.String(), Action: claudeapi.ActionArchiveWorkspace, SubjectKind: "workspace",
		SubjectRef: "wrkspc_e2e", PlanHash: "plan-ws-1", RequestedBy: "tester",
	}
	dec, err := gate.Authorize(ctx, req)
	if err != nil || dec.Status != claudeapi.AdminPending {
		t.Fatalf("first authorize = %q err=%v, want pending", dec.Status, err)
	}
	m := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+dec.ApprovalRef)
	if m["required_approvals"] != float64(2) || m["risk_tier"] != "critical" {
		t.Fatalf("workspace archive must be floored at 2 (critical): required=%v tier=%v", m["required_approvals"], m["risk_tier"])
	}

	// One approver is not enough: still pending, not dual-control.
	if code, body := h.decide(t, approverB, dec.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("first approve = %d: %s", code, body)
	}
	if d, _ := gate.Authorize(ctx, req); d.Status != claudeapi.AdminPending || d.HasDualControl() {
		t.Fatalf("one approver must not release an irreversible archive, got %q dual=%v", d.Status, d.HasDualControl())
	}

	// The second distinct approver crosses the quorum → approved + dual-control evidence.
	if code, body := h.decide(t, approverC, dec.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("second approve = %d: %s", code, body)
	}
	d, err := gate.Authorize(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != claudeapi.AdminApproved || !d.Allowed() {
		t.Fatalf("two distinct approvers must release it, got %q", d.Status)
	}
	if !d.HasDualControl() {
		t.Fatalf("approved archive must carry the dual-control quorum, approvers=%v", d.Approvers)
	}
}

// TestAdminGateRecoverableSingleApproval: a recoverable admin action (key deactivate) is
// HIGH, so a SINGLE human approval releases it — the tiers diverge.
func TestAdminGateRecoverableSingleApproval(t *testing.T) {
	h := newHarness(t)
	_, approverB := h.createApprover(t, "adm-r@bridge.test")
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	tid := tenantAID(t, h)
	ctx := context.Background()
	gate := br.adminGate(tid)

	req := claudeapi.AdminActionRequest{
		Tenant: tid.String(), Action: claudeapi.ActionDeactivateKey, SubjectKind: "api_key",
		SubjectRef: "apikey_e2e", PlanHash: "plan-key-1", RequestedBy: "tester",
	}
	dec, err := gate.Authorize(ctx, req)
	if err != nil || dec.Status != claudeapi.AdminPending {
		t.Fatalf("first authorize = %q err=%v, want pending", dec.Status, err)
	}
	m := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+dec.ApprovalRef)
	if m["required_approvals"] != float64(1) {
		t.Fatalf("recoverable key-deactivate must need a single approval, got required=%v", m["required_approvals"])
	}
	if code, body := h.decide(t, approverB, dec.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("approve = %d: %s", code, body)
	}
	d, err := gate.Authorize(ctx, req)
	if err != nil || d.Status != claudeapi.AdminApproved || !d.Allowed() {
		t.Fatalf("single approval must release a recoverable action, got %q err=%v", d.Status, err)
	}
}
