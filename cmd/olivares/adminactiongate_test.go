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
	"github.com/olivaresai/olivares/core/model"
)

// TestAdminGateWorkspaceAdminGrantNoBreakGlassAndEvidence proves the privilege-critical
// workspace-admin grant routes through GateOnceNoBreakGlass and returns approver evidence
// only after the two-human quorum is satisfied.
func TestAdminGateWorkspaceAdminGrantNoBreakGlassAndEvidence(t *testing.T) {
	h := newHarness(t)
	_, approverB := h.createApprover(t, "adm-grant-b@bridge.test")
	_, approverC := h.createApprover(t, "adm-grant-c@bridge.test")
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	tid := tenantAID(t, h)
	ctx := context.Background()
	gate := br.AdminGate(tid)

	req := claudeapi.AdminActionRequest{
		Tenant: tid.String(), Action: claudeapi.ActionGrantWorkspaceAdmin,
		SubjectKind: "workspace_member", SubjectRef: "wrkspc_e2e:user_e2e",
		PlanHash: "plan-ws-admin-1", RequestedBy: "tester",
	}
	dec, err := gate.Authorize(ctx, req)
	if err != nil || dec.Status != claudeapi.AdminPending {
		t.Fatalf("first authorize = %q err=%v, want pending", dec.Status, err)
	}
	m := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+dec.ApprovalRef)
	if m["required_approvals"] != float64(2) || m["risk_tier"] != "critical" {
		t.Fatalf("workspace-admin grant must be floored at 2 (critical): required=%v tier=%v", m["required_approvals"], m["risk_tier"])
	}

	if code, body := h.decide(t, approverB, dec.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("first approve = %d: %s", code, body)
	}
	if d, _ := gate.Authorize(ctx, req); d.Status != claudeapi.AdminPending || d.HasDualControl() {
		t.Fatalf("one approver must not release workspace-admin grant, got %+v", d)
	}
	if code, body := h.decide(t, approverC, dec.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("second approve = %d: %s", code, body)
	}
	d, err := gate.Authorize(ctx, req)
	if err != nil || d.Status != claudeapi.AdminApproved || !d.Allowed() {
		t.Fatalf("two approvers must release workspace-admin grant, got %+v err=%v", d, err)
	}
	if d.PlanHash != req.PlanHash || !d.HasDualControl() {
		t.Fatalf("approved grant must carry bound plan and dual-control evidence, got %+v", d)
	}
}

func mustTenant(t *testing.T) model.TenantID {
	t.Helper()
	tid, err := model.ParseTenantID("11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("parse tenant: %v", err)
	}
	return tid
}
