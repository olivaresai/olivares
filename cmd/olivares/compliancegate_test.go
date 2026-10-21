// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
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
	dec, err := b.ComplianceGate().Authorize(context.Background(), other, compliance.GateRequest{
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
	gate := br.ComplianceGate()
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

// A default install has no OLIVARES_APPROVAL_BRIDGE_CONFIG, so the bridge holds no
// service token and the decision trail is read from the engine's local approval
// service. The compliance gate is the adapter the erasure and hold routes read
// their quorum from; it shares ApprovalApproverEvidence with the audit-recovery
// and agent-core export gates, so this one test covers that function for all three.
func TestComplianceGateDefaultInstallCarriesTwoPersonEvidence(t *testing.T) {
	h := newHarness(t)
	userB, approverB := h.createApprover(t, "dflt-hold-b@bridge.test")
	userC, approverC := h.createApprover(t, "dflt-hold-c@bridge.test")
	handler, loopbackReads := decisionReads(h)
	gate := localDefaultBridge(t, h, handler).ComplianceGate()
	tid := tenantAID(t, h)
	ctx := t.Context()

	req := compliance.GateRequest{
		Action: "compliance.hold.release", SubjectKind: "legal_hold", SubjectRef: "lh-default",
		PlanHash: "plan-hold-default", Reason: "release legal hold (matter M-9)", RequestedBy: "user:legal",
	}
	d, err := gate.Authorize(ctx, tid, req)
	if err != nil || d.Status != compliance.GateStatusPending || d.ApprovalRef == "" {
		t.Fatalf("first authorize = %+v err=%v, want a pending, referenced deny", d, err)
	}
	if code, body := h.decide(t, approverB, d.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("first approve = %d: %s", code, body)
	}
	if d1, err := gate.Authorize(ctx, tid, req); err != nil || d1.Status != compliance.GateStatusPending || d1.Quorum() != 0 {
		t.Fatalf("one approver on a default install = %+v err=%v, want pending with no quorum", d1, err)
	}
	if code, body := h.decide(t, approverC, d.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("second approve = %d: %s", code, body)
	}
	d2, err := gate.Authorize(ctx, tid, req)
	if err != nil || d2.Status != compliance.GateStatusApproved {
		t.Fatalf("final authorize = %+v err=%v, want approved", d2, err)
	}
	if d2.Quorum() != 2 {
		t.Fatalf("a default install reported quorum %d (%+v), want the two approving accounts", d2.Quorum(), d2)
	}
	if got := slices.Sorted(slices.Values(d2.ApproverPersons)); !slices.Equal(got, slices.Sorted(slices.Values([]string{userB, userC}))) {
		t.Fatalf("approver persons = %v, want the accounts %q and %q", got, userB, userC)
	}
	if n := loopbackReads.Load(); n != 0 {
		t.Fatalf("a default install made %d loopback decision reads, want the local service only", n)
	}
}

// The failing path of a default install, end to end: the data-subject erase route
// answers 403 "lacks dual-control quorum evidence" when the gate hands back no
// evidence. Two distinct humans approving must let the same request execute.
func TestDataSubjectEraseDefaultInstallRunsAfterTwoApprovals(t *testing.T) {
	// Erasure with key shred is a Business capability: compose the module set as the Business edition.
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	thisEdition.name = "enterprise"
	h := newHarness(t)
	_, approverB := h.createApprover(t, "dflt-erase-b@bridge.test")
	_, approverC := h.createApprover(t, "dflt-erase-c@bridge.test")
	// Bind the engine's own bridge as boot.go does on a default install: the local
	// approval service and the engine handler, no service token.
	h.set.approvalBridge.UseLocalProposer(h.set.gov.EngineApprovals())
	h.set.approvalBridge.UseHandler(h.h)

	const path = "/v1/m/compliance/data-subjects/ds-default/erase"
	body := map[string]any{"subject_kind": "agent", "case_ref": "rtbf-default", "reason": "gdpr art 17"}
	var pending struct {
		Status      string `json:"status"`
		ApprovalRef string `json:"approval_ref"`
	}
	if code := h.reqInto("POST", path, h.adminToken, h.tenantA, body, &pending); code != http.StatusAccepted ||
		pending.Status != "pending_approval" || pending.ApprovalRef == "" {
		t.Fatalf("first erase = %d %+v, want 202 pending_approval with an approval ref", code, pending)
	}

	if code, out := h.decide(t, approverB, pending.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("first approve = %d: %s", code, out)
	}
	// One human is not a quorum: the erasure stays pending and nothing is destroyed.
	if code, raw := h.req("POST", path, h.adminToken, h.tenantA, body); code != http.StatusAccepted {
		t.Fatalf("erase with one approver = %d, want 202 still pending: %s", code, raw)
	}

	if code, out := h.decide(t, approverC, pending.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("second approve = %d: %s", code, out)
	}
	code, raw := h.req("POST", path, h.adminToken, h.tenantA, body)
	if code == http.StatusForbidden {
		t.Fatalf("erase after two distinct approvals = 403, the gate gave no quorum evidence on a default install: %s", raw)
	}
	var done map[string]any
	if err := json.Unmarshal(raw, &done); err != nil {
		t.Fatalf("erase response decode: %v (%s)", err, raw)
	}
	if code != http.StatusOK || done["key_shredded"] != true {
		t.Fatalf("erase after two approvals = %d %s, want 200 with a shredded-key receipt", code, raw)
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
	// Placing a hold is a Business capability: compose the module set as the Business edition.
	previous := thisEdition
	t.Cleanup(func() { thisEdition = previous })
	thisEdition.name = "enterprise"
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
