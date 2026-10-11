// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	claudecompliance "github.com/olivaresai/olivares/connectors/claude-compliance"
	"github.com/olivaresai/olivares/core/auth"
)

// decisionReads wraps the engine handler and counts the loopback reads of an
// approval's decision trail, so a test can tell which path produced the evidence.
func decisionReads(h *harness) (http.Handler, *atomic.Int32) {
	var n atomic.Int32
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/decisions") {
			n.Add(1)
		}
		h.h.ServeHTTP(w, r)
	}), &n
}

// localDefaultBridge is the bridge of a default install: no
// OLIVARES_APPROVAL_BRIDGE_CONFIG, so it holds no service token and proposes
// through the engine's local approval service (boot.go).
func localDefaultBridge(t *testing.T, h *harness, handler http.Handler) *approvalBridge {
	t.Helper()
	b := newApprovalBridge(approvalBridgeConfig{}, discardLog())
	b.UseHandler(handler)
	b.UseLocalProposer(h.set.gov.EngineApprovals())
	return b
}

// A default install must complete an irreversible gate that two distinct
// accounts approved. The dual-control evidence read has no service token to
// authenticate with, so it must come from the local approval service.
func TestLocalDefaultBridgeCarriesTwoPersonEvidence(t *testing.T) {
	h := newHarness(t)
	userB, approverB := h.createApprover(t, "local-b@bridge.test")
	userC, approverC := h.createApprover(t, "local-c@bridge.test")
	handler, loopbackReads := decisionReads(h)
	br := localDefaultBridge(t, h, handler)
	tid := tenantAID(t, h)
	ctx := t.Context()

	eraseReq := claudecompliance.EraseRequest{
		Tenant: tid.String(), Target: claudecompliance.EraseChat, SubjectRef: "chat_local",
		CaseRef: "rtbf-1", PlanHash: "plan-erase-local", RequestedBy: "tester",
	}
	adminReq := claudeapi.AdminActionRequest{
		Tenant: tid.String(), Action: claudeapi.ActionArchiveWorkspace, SubjectKind: "workspace",
		SubjectRef: "wrkspc_local", PlanHash: "plan-ws-local", RequestedBy: "tester",
	}
	erase, admin := br.EraseGate(tid), br.AdminGate(tid)

	e, err := erase.Authorize(ctx, eraseReq)
	if err != nil || e.Status != claudecompliance.ErasePending {
		t.Fatalf("erase first authorize = %q err=%v, want pending", e.Status, err)
	}
	a, err := admin.Authorize(ctx, adminReq)
	if err != nil || a.Status != claudeapi.AdminPending {
		t.Fatalf("admin first authorize = %q err=%v, want pending", a.Status, err)
	}

	for _, ref := range []string{e.ApprovalRef, a.ApprovalRef} {
		if code, body := h.decide(t, approverB, ref, "approve"); code != http.StatusOK {
			t.Fatalf("first approve = %d: %s", code, body)
		}
	}
	// One account is not a quorum: the approval stays pending and releases nothing.
	if d, _ := erase.Authorize(ctx, eraseReq); d.Status != claudecompliance.ErasePending || d.Allowed() || d.HasDualControl() {
		t.Fatalf("one approver released or half-released an erasure: %+v", d)
	}
	for _, ref := range []string{e.ApprovalRef, a.ApprovalRef} {
		if code, body := h.decide(t, approverC, ref, "approve"); code != http.StatusOK {
			t.Fatalf("second approve = %d: %s", code, body)
		}
	}

	d, err := erase.Authorize(ctx, eraseReq)
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != claudecompliance.EraseApproved || !d.HasDualControl() || !d.Allowed() {
		t.Fatalf("two approving accounts did not release the erasure on a default install: %+v", d)
	}
	// The quorum counts accounts; the credentials stay as provenance.
	if got := slices.Sorted(slices.Values(d.ApproverPersons)); !slices.Equal(got, slices.Sorted(slices.Values([]string{userB, userC}))) {
		t.Fatalf("erase persons = %v, want the accounts %q and %q", d.ApproverPersons, userB, userC)
	}
	if len(d.Approvers) != 2 || !slices.ContainsFunc(d.Approvers, func(s string) bool { return !slices.Contains(d.ApproverPersons, s) }) {
		t.Fatalf("erase actors = %v, want two credentials distinct from the account ids", d.Approvers)
	}
	ad, err := admin.Authorize(ctx, adminReq)
	if err != nil {
		t.Fatal(err)
	}
	if ad.Status != claudeapi.AdminApproved || !ad.HasDualControl() {
		t.Fatalf("admin archive on a default install: status=%q approvers=%v", ad.Status, ad.Approvers)
	}
	if n := loopbackReads.Load(); n != 0 {
		t.Fatalf("a default install made %d loopback decision reads, want the local service only", n)
	}

	// A read that fails is no evidence at all, never a fabricated approver.
	cred, ok := br.Cred(tid)
	if !ok {
		t.Fatal("default install has no credential")
	}
	cred.Tenant = ""
	if ev := br.ApprovalApproverEvidence(ctx, cred, e.ApprovalRef); len(ev.Actors)+len(ev.Persons)+ev.Unattributed != 0 {
		t.Fatalf("unreadable trail produced evidence: %+v", ev)
	}
}

// A bridge configured with a tenant service token keeps the governed API read
// even when the engine's local service is bound: only a default install reads
// the trail in-process.
func TestConfiguredBridgeKeepsGovernedAPIEvidenceRead(t *testing.T) {
	h := newHarness(t)
	_, approverB := h.createApprover(t, "cfg-b@bridge.test")
	_, approverC := h.createApprover(t, "cfg-c@bridge.test")
	handler, loopbackReads := decisionReads(h)
	br := newApprovalBridge(approvalBridgeConfig{
		Tenants: []approvalBridgeTenant{{Tenant: h.tenantA, Token: h.mintBoundToken(t, auth.RoleEditor)}},
	}, discardLog())
	br.UseHandler(handler)
	br.UseLocalProposer(h.set.gov.EngineApprovals())
	tid := tenantAID(t, h)
	req := claudeapi.AdminActionRequest{
		Tenant: tid.String(), Action: claudeapi.ActionArchiveWorkspace, SubjectKind: "workspace",
		SubjectRef: "wrkspc_cfg", PlanHash: "plan-ws-cfg", RequestedBy: "tester",
	}
	gate := br.AdminGate(tid)
	dec, err := gate.Authorize(t.Context(), req)
	if err != nil || dec.Status != claudeapi.AdminPending {
		t.Fatalf("authorize = %q err=%v, want pending", dec.Status, err)
	}
	for _, token := range []string{approverB, approverC} {
		if code, body := h.decide(t, token, dec.ApprovalRef, "approve"); code != http.StatusOK {
			t.Fatalf("approve = %d: %s", code, body)
		}
	}
	d, err := gate.Authorize(t.Context(), req)
	if err != nil || d.Status != claudeapi.AdminApproved || !d.HasDualControl() {
		t.Fatalf("configured bridge: status=%q dual=%v err=%v", d.Status, d.HasDualControl(), err)
	}
	if loopbackReads.Load() == 0 {
		t.Fatal("a configured bridge stopped reading the trail through the governed API")
	}
}
