// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/modules/governance"
)

// nhilifecycle_bridge_test.go is the proof at the composition root, against
// the REAL engine: the NHI lifecycle gate inherits the CRITICAL
// two-person floor with zero cooperation, and the irreversible finalize forbids
// the break-glass emergency path (the erase-gate precedent) while rotation permits
// it.

// A governed nhi.rotate opened through the lifecycle gate is floored at two
// distinct approvers BY THE ENGINE (nhi.rotate is in the default CRITICAL set), and
// the adapter maps the engine's status onto the module's gate vocabulary.
func TestNHILifecycleGateFlooredAtTwoApprovers(t *testing.T) {
	h := newHarness(t)
	_, approverB := h.createApprover(t, "nhi-b@bridge.test")
	_, approverC := h.createApprover(t, "nhi-c@bridge.test")
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	tid := tenantAID(t, h)
	ctx := context.Background()
	gate := br.LifecycleGate()

	req := governance.LifecycleGateRequest{
		Action: "nhi.rotate", SubjectKind: "nhi", SubjectRef: "vault:approle:ci",
		PlanHash: "plan-nhi-1", Reason: "key rotation", RequestedBy: "tester", AllowBreakGlass: true,
	}
	dec, err := gate.Authorize(ctx, tid, req)
	if err != nil || dec.Status != governance.GateStatusPending {
		t.Fatalf("first authorize = %q err=%v", dec.Status, err)
	}
	m := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+dec.ApprovalRef)
	if m["required_approvals"] != float64(2) || m["risk_tier"] != "critical" {
		t.Fatalf("nhi.rotate must be floored at 2 (critical): required=%v tier=%v", m["required_approvals"], m["risk_tier"])
	}

	if code, body := h.decide(t, approverB, dec.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("first approve = %d: %s", code, body)
	}
	if d, _ := gate.Authorize(ctx, tid, req); d.Status != governance.GateStatusPending {
		t.Fatalf("one approver must not release a critical rotation, got %q", d.Status)
	}
	if code, body := h.decide(t, approverC, dec.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("second approve = %d: %s", code, body)
	}
	if d, _ := gate.Authorize(ctx, tid, req); d.Status != governance.GateStatusApproved {
		t.Fatalf("two distinct approvers must release it, got %q", d.Status)
	}
}

// An irreversible nhi.offboard.finalize never consults the break-glass emergency
// path: even with an ACTIVE grant scoped to it, the action stays pending for the two
// humans — whereas a rotation under the same grant proceeds under break-glass.
