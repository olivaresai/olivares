// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"strings"
	"testing"

	claudecompliance "github.com/olivaresai/olivares/connectors/claude-compliance"
	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func tenantAID(t *testing.T, h *harness) model.TenantID {
	t.Helper()
	tid, err := model.ParseTenantID(h.tenantA)
	if err != nil {
		t.Fatalf("parse tenant: %v", err)
	}
	return tid
}

func TestBridgeCriticalActionFlooredAtTwoApprovers(t *testing.T) {
	h := newHarness(t)
	_, approverB := h.createApprover(t, "crit-b@bridge.test")
	_, approverC := h.createApprover(t, "crit-c@bridge.test")
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	tid := tenantAID(t, h)
	ctx := context.Background()

	ref, st, _, err := br.GateOnce(ctx, tid, "deploy.apply", "deployment", "svc/api", "plan-crit-1", "prod deploy", "tester")
	if err != nil || st != nbPending {
		t.Fatalf("GateOnce = %q err=%v", st, err)
	}
	m := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+ref)
	if m["required_approvals"] != float64(2) || m["risk_tier"] != "critical" {
		t.Fatalf("engine must floor a bridge-opened critical action at 2: required=%v tier=%v", m["required_approvals"], m["risk_tier"])
	}

	if code, body := h.decide(t, approverB, ref, "approve"); code != http.StatusOK {
		t.Fatalf("first approve = %d: %s", code, body)
	}
	if _, st, _, _ = br.GateOnce(ctx, tid, "deploy.apply", "deployment", "svc/api", "plan-crit-1", "prod deploy", "tester"); st != nbPending {
		t.Fatalf("one approver must NOT release a critical action, got %q", st)
	}
	if code, body := h.decide(t, approverC, ref, "approve"); code != http.StatusOK {
		t.Fatalf("second approve = %d: %s", code, body)
	}
	if _, st, _, _ = br.GateOnce(ctx, tid, "deploy.apply", "deployment", "svc/api", "plan-crit-1", "prod deploy", "tester"); st != nbApproved {
		t.Fatalf("two distinct approvers must release it, got %q", st)
	}
}

func TestMCPToolGateApprovalTakesEffect(t *testing.T) {
	h := newHarness(t)
	_, approverB := h.createApprover(t, "mcp-b@bridge.test")
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	gate := mcpToolGate{bridge: br, tenant: tenantAID(t, h)}
	ctx := context.Background()

	req := mcpc.ToolApprovalRequest{Tenant: h.tenantA, Tool: "db.drop_table", PlanHash: "plan-mcp-1", RequestedBy: "agent"}
	d, err := gate.Authorize(ctx, req)
	if err != nil || d.Status != mcpc.StatusPending {
		t.Fatalf("first authorize = %v err=%v", d.Status, err)
	}
	m := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+d.ApprovalRef)
	if m["risk_tier"] != "high" || m["required_approvals"] != float64(1) {
		t.Fatalf("mcp.tool.call must default to high with one approval: tier=%v required=%v", m["risk_tier"], m["required_approvals"])
	}
	if code, body := h.decide(t, approverB, d.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("approve = %d: %s", code, body)
	}
	d, err = gate.Authorize(ctx, req)
	if err != nil || d.Status != mcpc.StatusApproved || !d.Allowed() {
		t.Fatalf("the approval must take effect on the retry, got %v err=%v", d.Status, err)
	}
}

func TestMCPToolGateNamesTheAskingCondition(t *testing.T) {
	h := newHarness(t)
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	gate := mcpToolGate{bridge: br, tenant: tenantAID(t, h)}
	for rule, want := range map[string]string{
		"tool:fs_write/condition:recursive": "MCP tools/call held by tool:fs_write/condition:recursive: fs_write",
		"tool:fs_write":                     "MCP destructive tools/call: fs_write",
	} {
		d, err := gate.Authorize(context.Background(), mcpc.ToolApprovalRequest{Tenant: h.tenantA, Tool: "fs_write", PlanHash: "plan-" + rule, RequestedBy: "agent", Rule: rule})
		if err != nil || d.Status != mcpc.StatusPending {
			t.Fatalf("%s: authorize = %v err=%v", rule, d.Status, err)
		}
		m := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+d.ApprovalRef)
		if reason, _ := m["reason"].(string); !strings.Contains(reason, want) {
			t.Errorf("%s: approval reason %q, want it to contain %q", rule, reason, want)
		}
	}
}

func TestMCPToolGateRoundTripSpendsApprovalOnce(t *testing.T) {
	h := newHarness(t)
	_, approverB := h.createApprover(t, "mcp-rt@bridge.test")
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	gate := mcpToolGate{bridge: br, tenant: tenantAID(t, h)}
	ctx := context.Background()

	req := mcpc.ToolApprovalRequest{Tenant: h.tenantA, Tool: "db.drop_table", PlanHash: "plan-mcp-rt", RequestedBy: "agent"}
	d, err := gate.Authorize(ctx, req)
	if err != nil || d.Status != mcpc.StatusPending {
		t.Fatalf("first authorize = %v err=%v", d.Status, err)
	}
	if code, body := h.decide(t, approverB, d.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("approve = %d: %s", code, body)
	}
	first, second := req, req
	first.ConsumerID, second.ConsumerID = "round-trip-1", "round-trip-2"
	if d, err = gate.Authorize(ctx, first); err != nil || !d.Allowed() || !d.Spent {
		t.Fatalf("the round trip must spend the approval, got %v spent=%v err=%v", d.Status, d.Spent, err)
	}
	if d, err = gate.Authorize(ctx, second); err != nil || d.Status != mcpc.StatusRejected {
		t.Fatalf("a second round trip must not reuse the spent approval, got %v err=%v", d.Status, err)
	}
	actions := h.tenantLedgerActions(t, tenantAID(t, h))
	if !actions["governance.approval.consume"] || !actions["governance.approval.replay_denied"] {
		t.Errorf("the spend and the refused reuse must be in the signed ledger, got %v", actions)
	}
	if d, err = gate.Authorize(ctx, req); err != nil || !d.Allowed() || d.Spent {
		t.Fatalf("the published retry without a round trip changed: %v spent=%v err=%v", d.Status, d.Spent, err)
	}
}

func TestEraseGateDualControlEvidence(t *testing.T) {
	h := newHarness(t)
	_, approverB := h.createApprover(t, "erase-b@bridge.test")
	_, approverC := h.createApprover(t, "erase-c@bridge.test")
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	tid := tenantAID(t, h)
	gate := br.EraseGate(tid)
	ctx := context.Background()

	req := claudecompliance.EraseRequest{
		Tenant: h.tenantA, Target: claudecompliance.EraseChat, SubjectRef: "chat-123",
		CaseRef: "RTBF-77", PlanHash: "plan-erase-1", RequestedBy: "dpo@x.test",
	}
	d, err := gate.Authorize(ctx, req)
	if err != nil || d.Status != claudecompliance.ErasePending || d.Allowed() {
		t.Fatalf("first authorize = %v err=%v", d.Status, err)
	}
	m := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+d.ApprovalRef)
	if m["required_approvals"] != float64(2) || m["risk_tier"] != "critical" {
		t.Fatalf("compliance.content.erase must be critical/floored: required=%v tier=%v", m["required_approvals"], m["risk_tier"])
	}

	if code, body := h.decide(t, approverB, d.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("first approve = %d: %s", code, body)
	}
	if d, _ = gate.Authorize(ctx, req); d.Status != claudecompliance.ErasePending || d.HasDualControl() {
		t.Fatalf("one approver must not satisfy the erase quorum: %v approvers=%v", d.Status, d.Approvers)
	}
	if code, body := h.decide(t, approverC, d.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("second approve = %d: %s", code, body)
	}
	d, err = gate.Authorize(ctx, req)
	if err != nil || d.Status != claudecompliance.EraseApproved {
		t.Fatalf("final authorize = %v err=%v", d.Status, err)
	}
	if !d.HasDualControl() || !d.Allowed() {
		t.Fatalf("the decision must carry ≥2 distinct approver principals, got %v", d.Approvers)
	}
}
