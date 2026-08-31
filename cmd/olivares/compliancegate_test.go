// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/compliance"
	"github.com/olivaresai/olivares/modules/models"
)

// compliancegate_test.go is the proof for the composition-root wiring:
// the compliance.ApprovalGate adapter over the bridge (no break-glass,
// quorum evidence), the knowledge hold-gate adapter over compliance.CheckHold
// (deny-closed error propagation, field-for-field mapping through the REAL
// wiring), and the Covered-Models provider-retention floor adapter.

// --- unit: status mapping + deny-closed encoding -----------------------------

func TestComplianceGateStatusMappingDeniesByDefault(t *testing.T) {
	if complianceGateStatus(nbApproved) != compliance.GateStatusApproved {
		t.Fatal("approved must map to gate_approved")
	}
	cases := map[string]string{
		nbPending: compliance.GateStatusPending,
		// Break-glass is unreachable on this gate (gateOnceNoBreakGlass only) but
		// must NEVER map to approved: no emergency lifts a preservation order.
		nbBreakGlass: compliance.GateStatusPending,
		nbRejected:   compliance.GateStatusRejected,
		nbCanceled:   compliance.GateStatusRejected,
		nbExpired:    compliance.GateStatusExpired,
		nbNoGate:     compliance.GateStatusNoGate,
		"garbage":    compliance.GateStatusNoGate,
		"":           compliance.GateStatusNoGate,
	}
	for in, want := range cases {
		if got := complianceGateStatus(in); got != want {
			t.Fatalf("complianceGateStatus(%q) = %q, want %q", in, got, want)
		}
		if in != nbApproved && complianceGateStatus(in) == compliance.GateStatusApproved {
			t.Fatalf("%q must not authorize", in)
		}
	}
}

func TestComplianceGateUnconfiguredTenantDeniesClosed(t *testing.T) {
	configured := model.NewTenantID()
	other := model.NewTenantID()
	b := newApprovalBridge(approvalBridgeConfig{
		Tenants: []approvalBridgeTenant{{Tenant: configured.String(), Token: "svc-token"}},
	}, discardLog())
	if b == nil {
		t.Fatal("bridge with one valid tenant should build")
	}
	// An UNCONFIGURED tenant denies exactly like the module's denyApprovalGate —
	// without ever touching the engine (no handler is even bound here).
	dec, err := b.complianceGate().Authorize(context.Background(), other, compliance.GateRequest{
		Action: "compliance.hold.release", SubjectKind: "legal_hold", SubjectRef: "lh-1", PlanHash: "p1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Status != compliance.GateStatusNoGate {
		t.Fatalf("unconfigured tenant = %q, want gate_no_gate deny", dec.Status)
	}
	if !strings.HasPrefix(dec.ApprovalRef, noGateRefPrefix) {
		t.Fatalf("ref = %q, want a no-gate reference", dec.ApprovalRef)
	}
	if dec.PlanHash != "p1" || len(dec.Approvers) != 0 {
		t.Fatalf("no-gate decision must echo the plan with zero approvers, got %+v", dec)
	}
}

// --- E2E: the dual-control evidence over the real engine -----------------

// TestComplianceGateDualControlEvidence proves the release path end to
// end: compliance.hold.release is CRITICAL (engine-floored at two humans), an
// active break-glass grant never touches it, and only the second distinct
// approver yields an approved decision carrying the plan binding and the ≥2
// distinct-approver evidence the module independently re-verifies.
func TestComplianceGateDualControlEvidence(t *testing.T) {
	h := newHarness(t)
	_, approverB := h.createApprover(t, "hold-b@bridge.test")
	_, approverC := h.createApprover(t, "hold-c@bridge.test")
	br := buildBridge(t, h, h.mintBoundToken(t, auth.RoleEditor))
	gate := br.complianceGate()
	tid := tenantAID(t, h)
	ctx := context.Background()

	req := compliance.GateRequest{
		Action: "compliance.hold.release", SubjectKind: "legal_hold", SubjectRef: "lh-42",
		PlanHash: "plan-hold-rel-1", Reason: "release legal hold (matter M-1)", RequestedBy: "user:legal",
	}
	d, err := gate.Authorize(ctx, tid, req)
	if err != nil || d.Status != compliance.GateStatusPending || d.ApprovalRef == "" {
		t.Fatalf("first authorize = %+v err=%v, want a pending, referenced deny", d, err)
	}
	m := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+d.ApprovalRef)
	if m["risk_tier"] != "critical" || m["required_approvals"] != float64(2) {
		t.Fatalf("compliance.hold.release must be critical/floored: tier=%v required=%v", m["risk_tier"], m["required_approvals"])
	}

	// Even an ACTIVE emergency grant cannot release a hold: the adapter only
	// ever calls gateOnceNoBreakGlass.
	h.activateBreakGlassE2E(t, "", "emergency mid-litigation")
	if d2, _ := gate.Authorize(ctx, tid, req); d2.Status != compliance.GateStatusPending {
		t.Fatalf("break-glass must not touch the hold-release path, got %v", d2.Status)
	}

	if code, body := h.decide(t, approverB, d.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("first approve = %d: %s", code, body)
	}
	if d3, _ := gate.Authorize(ctx, tid, req); d3.Status != compliance.GateStatusPending || len(d3.Approvers) != 0 {
		t.Fatalf("one approver must not release a CRITICAL hold: %+v", d3)
	}
	if code, body := h.decide(t, approverC, d.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("second approve = %d: %s", code, body)
	}
	d4, err := gate.Authorize(ctx, tid, req)
	if err != nil || d4.Status != compliance.GateStatusApproved {
		t.Fatalf("final authorize = %+v err=%v, want approved", d4, err)
	}
	if d4.PlanHash != req.PlanHash {
		t.Fatalf("approved decision must carry the BOUND plan %q, got %q", req.PlanHash, d4.PlanHash)
	}
	// The quorum evidence the module re-verifies: ≥2 DISTINCT principals.
	seen := map[string]bool{}
	for _, a := range d4.Approvers {
		if a != "" {
			seen[a] = true
		}
	}
	if len(seen) < 2 {
		t.Fatalf("approved decision must carry ≥2 distinct approver principals, got %v", d4.Approvers)
	}
}

// --- the knowledge hold-gate adapter ------------------------------------------

func TestComplianceHoldGateAdapterPropagatesErrors(t *testing.T) {
	// A compliance module with no data handle errors on CheckHold; the adapter
	// must PROPAGATE that error AS-IS (knowledge then denies 503, fail closed) —
	// never swallow it into a held=false "go ahead and delete".
	gate := complianceHoldGate{m: compliance.New()}
	held, holds, err := gate.Check(context.Background(), model.NewTenantID(), "kb", "kb-1", "knowledge.content")
	if err == nil {
		t.Fatal("a failing CheckHold must propagate its error")
	}
	if held || holds != nil {
		t.Fatalf("an errored check must not also claim a hold: held=%v holds=%v", held, holds)
	}
}

// TestHoldGateBlocksKnowledgeKBDeleteE2E drives the PRODUCTION wiring (the
// buildModules graph the harness reuses): a hold set through the compliance API
// vetoes a knowledge KB delete with 423 legal_hold, the blocking hold mapped
// field-for-field (id/matter_ref/scope_kind) through the composition-root
// adapter — knowledge never imports compliance.
func TestHoldGateBlocksKnowledgeKBDeleteE2E(t *testing.T) {
	h := newHarness(t)

	var kb struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/m/knowledge/kbs", h.adminToken, h.tenantA,
		map[string]any{"name": "legal-kb"}, &kb); code != http.StatusCreated || kb.ID == "" {
		t.Fatalf("create kb = %d (id=%q)", code, kb.ID)
	}
	var hold struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/m/compliance/holds", h.adminToken, h.tenantA, map[string]any{
		"matter_ref": "M-2026-7", "title": "litigation hold", "scope_kind": "tenant", "reason": "pending litigation",
	}, &hold); code != http.StatusCreated || hold.ID == "" {
		t.Fatalf("create hold = %d (id=%q)", code, hold.ID)
	}

	code, raw := h.req("DELETE", "/v1/m/knowledge/kbs/"+kb.ID, h.adminToken, h.tenantA, nil)
	if code != http.StatusLocked {
		t.Fatalf("kb delete under an active hold = %d, want 423: %s", code, raw)
	}
	var body struct {
		Error struct {
			Code  string `json:"code"`
			Holds []struct {
				ID        string `json:"id"`
				MatterRef string `json:"matter_ref"`
				ScopeKind string `json:"scope_kind"`
			} `json:"holds"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("423 body: %v: %s", err, raw)
	}
	if body.Error.Code != "legal_hold" || len(body.Error.Holds) != 1 {
		t.Fatalf("423 body = %s, want code legal_hold with the one blocking hold", raw)
	}
	got := body.Error.Holds[0]
	if got.ID != hold.ID || got.MatterRef != "M-2026-7" || got.ScopeKind != "tenant" {
		t.Fatalf("hold mapped through the adapter = %+v, want id=%s matter=M-2026-7 scope=tenant", got, hold.ID)
	}
}

// --- the provider-retention floor adapter --------------------------------------

func TestModelsProviderRetentionAdapterReportsCoveredFloor(t *testing.T) {
	days, source := modelsProviderRetention{}.MaxForcedRetentionDays(context.Background())
	wantDays, families := models.MaxCoveredRetentionDays()
	if days != wantDays {
		t.Fatalf("adapter days = %d, want the models reference's %d (pass-through, never fabricated)", days, wantDays)
	}
	if wantDays < 30 || len(families) == 0 {
		t.Fatalf("the reference must report the Covered-Models floor (≥30d, uplift 2026-06-09): days=%d families=%v", wantDays, families)
	}
	if !strings.HasPrefix(source, "models.reference") {
		t.Fatalf("source = %q, want models.reference provenance", source)
	}
	for _, f := range families {
		if !strings.Contains(source, f) {
			t.Fatalf("source %q must name covered family %q", source, f)
		}
	}
}
