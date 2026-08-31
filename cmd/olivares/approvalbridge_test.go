// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/deploy"
	"github.com/olivaresai/olivares/modules/orchestration"
	"github.com/olivaresai/olivares/modules/security"
	"github.com/olivaresai/olivares/modules/voice"
)

// approvalbridge_test.go is the proof for the OUTBOUND ApprovalGate bridge.
// The unit tests pin the deny-closed encoding/mapping; the E2E tests drive the four
// gates against the REAL engine (the same composition root the binary boots),
// proving an actuation cannot proceed without a human approval bound to the exact plan
// — and that the engine's own guards (SoD/threshold/expiry) are never bypassed.

// --- unit: deny-closed encoding + status mapping ----------------------------------

func TestApprovalBridgePlanBindingRoundTrip(t *testing.T) {
	cases := []struct{ subject, plan string }{
		{"svc/api", "a1b2c3d4e5f6"},
		{"agent-7", ""},                    // security posture: no plan hash
		{"weird#plan=looking", "deadbeef"}, // subject contains the marker; the appended hash still wins
	}
	for _, c := range cases {
		enc := encodeSubjectRef(c.subject, c.plan)
		if got := decodePlanHash(enc); got != c.plan {
			t.Fatalf("decodePlanHash(encode(%q,%q))=%q, want %q", c.subject, c.plan, got, c.plan)
		}
	}
	// With no plan hash the subject is stored verbatim (so a human sees a clean subject).
	if enc := encodeSubjectRef("pii", ""); enc != "pii" {
		t.Fatalf("empty-plan encode = %q, want verbatim", enc)
	}
}

func TestApprovalBridgeStatusMappingDeniesByDefault(t *testing.T) {
	// Only "approved" authorizes; every other value — including unknown, canceled and
	// the empty zero value — is a deny, for all three two-phase gates.
	if deployGateStatus(nbApproved) != deploy.StatusApproved {
		t.Fatal("approved must map to StatusApproved")
	}
	for _, s := range []string{nbPending, nbRejected, nbCanceled, nbExpired, nbNoGate, "garbage", ""} {
		if (deploy.GateDecision{Status: deployGateStatus(s)}).Allowed() {
			t.Fatalf("deploy %q must not be allowed", s)
		}
		if (orchestration.GateDecision{Status: orchestrationGateStatus(s)}).Allowed() {
			t.Fatalf("orchestration %q must not be allowed", s)
		}
		if (voice.GateDecision{Status: voiceGateStatus(s)}).Allowed() {
			t.Fatalf("voice %q must not be allowed", s)
		}
	}
	// security: only approved is Approved; no_gate is the one ungoverned case.
	if !securityDecision(nbApproved).Approved || !securityDecision(nbApproved).Governed {
		t.Fatal("approved must be approved+governed")
	}
	for _, s := range []string{nbPending, nbRejected, nbCanceled, nbExpired, nbNoGate, "garbage"} {
		if securityDecision(s).Approved {
			t.Fatalf("security %q must not approve", s)
		}
	}
	if securityDecision(nbNoGate).Governed {
		t.Fatal("no_gate (unconfigured) must be reported ungoverned")
	}
	if !securityDecision(nbPending).Governed {
		t.Fatal("a real pending decision is governed")
	}
}

func TestApprovalBridgeApprovedGrantWindow(t *testing.T) {
	base := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	b := &approvalBridge{clock: func() time.Time { return base }}
	cred := serviceCred{expiresIn: 3600} // 1h grant window
	fresh := approvalView{status: nbApproved, decidedAt: model.NewTimestamp(base.Add(-30 * time.Minute)).String()}
	stale := approvalView{status: nbApproved, decidedAt: model.NewTimestamp(base.Add(-2 * time.Hour)).String()}

	// Pending is always reusable (idempotent open). Approved is reusable ONLY for the
	// one-shot security gate (reuseApproved) AND only inside its grant window.
	if !b.reusable(cred, approvalView{status: nbPending}, false) {
		t.Fatal("pending must be reusable")
	}
	if b.reusable(cred, fresh, false) {
		t.Fatal("two-phase gate must NOT reuse an approved approval")
	}
	if !b.reusable(cred, fresh, true) {
		t.Fatal("security gate must reuse a fresh approved grant")
	}
	if b.reusable(cred, stale, true) {
		t.Fatal("an approved grant past its window must NOT be reused (time-box)")
	}
	// Fail-closed: an unparseable/empty decided_at or a zero window is never reusable.
	if b.reusable(cred, approvalView{status: nbApproved, decidedAt: "garbage"}, true) {
		t.Fatal("unparseable decided_at must fail closed")
	}
	if b.reusable(cred, approvalView{status: nbApproved, decidedAt: ""}, true) {
		t.Fatal("empty decided_at must fail closed")
	}
	if b.reusable(serviceCred{expiresIn: 0}, fresh, true) {
		t.Fatal("a zero grant window must never reuse an approved approval")
	}
	// Terminal states are never reusable.
	for _, s := range []string{nbRejected, nbExpired, nbCanceled, "garbage", ""} {
		if b.reusable(cred, approvalView{status: s, decidedAt: fresh.decidedAt}, true) {
			t.Fatalf("status %q must not be reusable", s)
		}
	}
}

func TestApprovalBridgeUnconfiguredTenantDeniesClosed(t *testing.T) {
	configured := model.NewTenantID()
	other := model.NewTenantID()
	b := newApprovalBridge(approvalBridgeConfig{
		Tenants: []approvalBridgeTenant{{Tenant: configured.String(), Token: "svc-token"}},
	}, discardLog())
	if b == nil {
		t.Fatal("bridge with one valid tenant should build")
	}
	ctx := context.Background()
	// An UNCONFIGURED tenant denies exactly like the module's own denyGate — without
	// ever touching the engine (no handler is even bound here).
	dec, err := b.deployGate().Request(ctx, deploy.ApprovalRequest{
		Tenant: other, Action: "deploy.apply", SubjectKind: "deployment", SubjectRef: "svc/x", PlanHash: "p",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Allowed() || dec.Status != deploy.StatusNoGate {
		t.Fatalf("unconfigured deploy = %q, want no_gate deny", dec.Status)
	}
	if !strings.HasPrefix(dec.ApprovalRef, noGateRefPrefix) {
		t.Fatalf("ref = %q, want a no-gate reference", dec.ApprovalRef)
	}
	sdec, err := b.securityGate().Authorize(ctx, other, security.ApprovalRequest{
		Action: "security.enforcement.enable", SubjectKind: "guardrail_class", SubjectRef: "pii",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sdec.Approved || sdec.Governed {
		t.Fatalf("unconfigured security = %+v, want ungoverned deny", sdec)
	}
}

func TestApprovalBridgeHandlerNotBoundFailsClosed(t *testing.T) {
	tenant := model.NewTenantID()
	b := newApprovalBridge(approvalBridgeConfig{
		Tenants: []approvalBridgeTenant{{Tenant: tenant.String(), Token: "svc-token"}},
	}, discardLog())
	if b == nil {
		t.Fatal("bridge should build")
	}
	// A CONFIGURED tenant but the engine handler is not yet bound (a boot race): opening
	// an approval must FAIL (deny via error), never silently succeed.
	if _, err := b.deployGate().Request(context.Background(), deploy.ApprovalRequest{
		Tenant: tenant, Action: "deploy.apply", SubjectKind: "deployment", SubjectRef: "svc/x", PlanHash: "p",
	}); err == nil {
		t.Fatal("request with no bound handler must error (fail-closed)")
	}
}

// --- E2E helpers (over the real engine) -------------------------------------------

// mintBoundToken mints an API token bound to tenant A with the given role and returns
// the secret. The bridge proposes as this SERVICE credential.
func (h *harness) mintBoundToken(t *testing.T, role string) string {
	t.Helper()
	var out struct {
		Token string `json:"token"`
	}
	if code := h.reqInto("POST", "/v1/tokens", h.adminToken, "", map[string]any{
		"name": "approval-bridge-svc", "tenant": h.tenantA, "role": role,
	}, &out); code != http.StatusCreated || out.Token == "" {
		t.Fatalf("mint %s token = %d", role, code)
	}
	return out.Token
}

// decide records a human decision through the REAL governed API as the given token.
func (h *harness) decide(t *testing.T, token, approvalID, decision string) (int, []byte) {
	t.Helper()
	return h.req("POST", "/v1/m/governance/approvals/"+approvalID+"/decisions", token, h.tenantA,
		map[string]any{"decision": decision})
}

// buildBridge wires the bridge to the running engine, proposing as the editor service
// token for tenant A.
func buildBridge(t *testing.T, h *harness, serviceToken string) *approvalBridge {
	t.Helper()
	b := newApprovalBridge(approvalBridgeConfig{
		Tenants: []approvalBridgeTenant{{Tenant: h.tenantA, Token: serviceToken}},
	}, discardLog())
	if b == nil {
		t.Fatal("bridge should build")
	}
	b.useHandler(h.h)
	return b
}

// --- E2E: two-phase actuation gates -----------------------------------------------

func TestApprovalBridgeDeployTwoPhaseGovernsAndBindsPlan(t *testing.T) {
	h := newHarness(t)
	// deploy.apply is in the default CRITICAL set: the engine floors its
	// threshold at TWO distinct approvers, so the proof needs a second human.
	_, bToken := h.createApprover(t, "approver-deploy@bridge.test")
	_, cToken := h.createApprover(t, "approver-deploy2@bridge.test")
	svc := h.mintBoundToken(t, auth.RoleEditor)
	gate := buildBridge(t, h, svc).deployGate()
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	planHash := "p1a1b2c3d4e5f6a7b8c9d0"

	// PHASE 1 — request opens a pending approval; it does NOT authorize.
	dec, err := gate.Request(ctx, deploy.ApprovalRequest{
		Tenant: tenant, Action: "deploy.apply", SubjectKind: "deployment",
		SubjectRef: "svc/api", PlanHash: planHash, RequestedBy: "user:svc",
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if dec.Status != deploy.StatusPending || dec.Allowed() || dec.ApprovalRef == "" {
		t.Fatalf("phase 1 = %+v, want a pending, non-allowed, referenced decision", dec)
	}
	ref := dec.ApprovalRef

	// Status before a decision: still pending (deny).
	if s0, err := gate.Status(ctx, tenant, ref, planHash); err != nil || s0.Allowed() {
		t.Fatalf("status before approve = %+v err=%v, want a deny", s0, err)
	}

	// IDEMPOTENCY — a repeated request for the same plan reuses the SAME approval.
	dec2, err := gate.Request(ctx, deploy.ApprovalRequest{
		Tenant: tenant, Action: "deploy.apply", SubjectKind: "deployment",
		SubjectRef: "svc/api", PlanHash: planHash, RequestedBy: "user:svc",
	})
	if err != nil || dec2.ApprovalRef != ref {
		t.Fatalf("idempotency: ref=%q err=%v, want reuse of %q", dec2.ApprovalRef, err, ref)
	}

	// A human (B, not the proposer) approves through the REAL governed decision API.
	if code, body := h.decide(t, bToken, ref, "approve"); code != http.StatusOK {
		t.Fatalf("approve = %d: %s", code, body)
	}
	// One approval does NOT release a CRITICAL action (the dual floor)...
	if s, err := gate.Status(ctx, tenant, ref, planHash); err != nil || s.Allowed() {
		t.Fatalf("status after ONE approval = %+v err=%v, want still-deny (dual control)", s, err)
	}
	// ...the second distinct human does.
	if code, body := h.decide(t, cToken, ref, "approve"); code != http.StatusOK {
		t.Fatalf("second approve = %d: %s", code, body)
	}

	// PHASE 2 — approved AND bound to the exact plan a human saw.
	s1, err := gate.Status(ctx, tenant, ref, planHash)
	if err != nil {
		t.Fatalf("status after approve: %v", err)
	}
	if !s1.Allowed() || s1.PlanHash != planHash {
		t.Fatalf("phase 2 = %+v, want approved bound to %q", s1, planHash)
	}

	// ANTI-TOCTOU — querying with a DIFFERENT plan hash still returns the STORED bound
	// hash, so the module's PlanHash-match denies a re-planned, un-approved change.
	s2, err := gate.Status(ctx, tenant, ref, "a-different-plan")
	if err != nil {
		t.Fatalf("status with mismatched plan: %v", err)
	}
	if s2.PlanHash != planHash {
		t.Fatalf("anti-toctou: bound plan = %q, want the stored %q (never the queried one)", s2.PlanHash, planHash)
	}
}

func TestApprovalBridgeRejectionDeniesActuation(t *testing.T) {
	h := newHarness(t)
	_, bToken := h.createApprover(t, "approver-rej@bridge.test")
	svc := h.mintBoundToken(t, auth.RoleEditor)
	gate := buildBridge(t, h, svc).orchestrationGate()
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	planHash := "sched-plan-xyz-987"

	dec, err := gate.Request(ctx, orchestration.ApprovalRequest{
		Tenant: tenant, Action: "orchestration.schedule.fire", SubjectKind: "schedule",
		SubjectRef: "sch-1", PlanHash: planHash, RequestedBy: "user:svc",
	})
	if err != nil || dec.ApprovalRef == "" {
		t.Fatalf("request: %+v err=%v", dec, err)
	}
	if code, body := h.decide(t, bToken, dec.ApprovalRef, "reject"); code != http.StatusOK {
		t.Fatalf("reject = %d: %s", code, body)
	}
	s, err := gate.Status(ctx, orchestration.ApprovalCheck{
		Tenant: tenant, ApprovalRef: dec.ApprovalRef, PlanHash: planHash,
		Action: "orchestration.schedule.fire", SubjectKind: "schedule", SubjectRef: "sch-1",
	})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if s.Allowed() || s.Status != orchestration.StatusRejected {
		t.Fatalf("rejected fire = %+v, want a rejected deny", s)
	}
}

// Item 2 (authorization substitution): an approval authored for a DIFFERENT
// (lower-risk) action must NOT authorize a schedule fire even if its subject
// encodes the target plan hash — the scope guard denies on the action mismatch.
func TestApprovalBridgeRejectsActionSubstitution(t *testing.T) {
	h := newHarness(t)
	svc := h.mintBoundToken(t, auth.RoleEditor)
	gate := buildBridge(t, h, svc).orchestrationGate()
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	planHash := "sched-plan-substitution-1"

	// A governance writer mints an approval for a LOW-RISK action, with the
	// schedule subject bound to the TARGET fire's plan hash.
	dec, err := gate.Request(ctx, orchestration.ApprovalRequest{
		Tenant: tenant, Action: "orchestration.schedule.view", SubjectKind: "schedule",
		SubjectRef: "sch-1", PlanHash: planHash, RequestedBy: "user:attacker",
	})
	if err != nil || dec.ApprovalRef == "" {
		t.Fatalf("request: %+v err=%v", dec, err)
	}
	// Submitting THAT approval to a schedule FIRE must be refused: the stored
	// action (schedule.view) does not authorize schedule.fire.
	s, err := gate.Status(ctx, orchestration.ApprovalCheck{
		Tenant: tenant, ApprovalRef: dec.ApprovalRef, PlanHash: planHash,
		Action: "orchestration.schedule.fire", SubjectKind: "schedule", SubjectRef: "sch-1",
	})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if s.Allowed() || s.Status != orchestration.StatusExpired {
		t.Fatalf("action-substituted approval = %+v, want a scope-mismatch deny (expired)", s)
	}
}

func TestApprovalBridgeVoiceGateProceedsOnlyWhenApproved(t *testing.T) {
	h := newHarness(t)
	_, bToken := h.createApprover(t, "approver-voice@bridge.test")
	svc := h.mintBoundToken(t, auth.RoleEditor)
	gate := buildBridge(t, h, svc).voiceGate()
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	planHash := "voice-plan-abc-123"

	dec, err := gate.Request(ctx, voice.ApprovalRequest{
		Tenant: tenant, Action: "voice.session.open", SubjectKind: "agent",
		SubjectRef: "agent-9", PlanHash: planHash, RequestedBy: "user:svc",
	})
	if err != nil || dec.Status != voice.StatusPending {
		t.Fatalf("request: %+v err=%v", dec, err)
	}
	if code, body := h.decide(t, bToken, dec.ApprovalRef, "approve"); code != http.StatusOK {
		t.Fatalf("approve = %d: %s", code, body)
	}
	s, err := gate.Status(ctx, tenant, dec.ApprovalRef, planHash)
	if err != nil || !s.Allowed() || s.PlanHash != planHash {
		t.Fatalf("approved open = %+v err=%v, want approved bound to %q", s, err, planHash)
	}
}

// --- E2E: the security posture gate (one-shot Authorize) --------------------------

func TestApprovalBridgeSecurityPostureGoverned(t *testing.T) {
	h := newHarness(t)
	// security.enforcement.enable is in the default CRITICAL set: two
	// distinct approvers are floored by the engine.
	_, bToken := h.createApprover(t, "approver-sec@bridge.test")
	_, cToken := h.createApprover(t, "approver-sec2@bridge.test")
	svc := h.mintBoundToken(t, auth.RoleEditor)
	gate := buildBridge(t, h, svc).securityGate()
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	req := security.ApprovalRequest{
		Action: "security.enforcement.enable", SubjectKind: "guardrail_class",
		SubjectRef: "pii", Reason: "block PII egress", Actor: "user:svc",
	}

	// First Authorize opens a pending approval → governed, but NOT yet approved.
	d0, err := gate.Authorize(ctx, tenant, req)
	if err != nil {
		t.Fatalf("authorize (open): %v", err)
	}
	if d0.Approved || !d0.Governed {
		t.Fatalf("first authorize = %+v, want governed-but-not-approved", d0)
	}

	// Locate the opened approval and approve it as a human (B).
	list := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals?action=security.enforcement.enable")
	its := items(list)
	if len(its) != 1 {
		t.Fatalf("opened %d approvals, want exactly 1 (idempotent)", len(its))
	}
	id, _ := its[0]["id"].(string)
	if code, body := h.decide(t, bToken, id, "approve"); code != http.StatusOK {
		t.Fatalf("approve = %d: %s", code, body)
	}
	// One approval does not release a CRITICAL posture change (dual floor).
	if d, err := gate.Authorize(ctx, tenant, req); err != nil || d.Approved {
		t.Fatalf("after ONE approval = %+v err=%v, want still-not-approved (dual control)", d, err)
	}
	if code, body := h.decide(t, cToken, id, "approve"); code != http.StatusOK {
		t.Fatalf("second approve = %d: %s", code, body)
	}

	// Re-Authorize: now approved + governed (the posture change may proceed).
	d1, err := gate.Authorize(ctx, tenant, req)
	if err != nil {
		t.Fatalf("authorize (recheck): %v", err)
	}
	if !d1.Approved || !d1.Governed {
		t.Fatalf("recheck authorize = %+v, want approved+governed", d1)
	}
}

// TestApprovalBridgeSecurityGrantIsTimeBoxed proves a human-approved enforcement grant
// is NOT permanent: it is reusable within the time-box of its decision, but a re-enable
// after the window opens a FRESH request and needs fresh approval — so a stale approval
// can never re-authorize a production-affecting posture change (the time-box doctrine).
func TestApprovalBridgeSecurityGrantIsTimeBoxed(t *testing.T) {
	h := newHarness(t)
	// Two approvers: the action is CRITICAL under the default set.
	_, bToken := h.createApprover(t, "approver-tbox@bridge.test")
	_, cToken := h.createApprover(t, "approver-tbox2@bridge.test")
	svc := h.mintBoundToken(t, auth.RoleEditor)
	bridge := buildBridge(t, h, svc)
	now := time.Now()
	bridge.clock = func() time.Time { return now } // controllable; grant window = 24h default
	gate := bridge.securityGate()
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	req := security.ApprovalRequest{
		Action: "security.enforcement.enable", SubjectKind: "guardrail_class",
		SubjectRef: "secrets", Reason: "block secrets", Actor: "user:svc",
	}

	// Open + approve the first grant.
	if d, err := gate.Authorize(ctx, tenant, req); err != nil || d.Approved {
		t.Fatalf("first authorize = %+v err=%v, want pending", d, err)
	}
	list := items(h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals?action=security.enforcement.enable"))
	if len(list) != 1 {
		t.Fatalf("opened %d approvals, want 1", len(list))
	}
	id1, _ := list[0]["id"].(string)
	if code, body := h.decide(t, bToken, id1, "approve"); code != http.StatusOK {
		t.Fatalf("approve = %d: %s", code, body)
	}
	if code, body := h.decide(t, cToken, id1, "approve"); code != http.StatusOK {
		t.Fatalf("second approve = %d: %s", code, body)
	}

	// Within the grant window: reused → approved (no new approval).
	if d, err := gate.Authorize(ctx, tenant, req); err != nil || !d.Approved {
		t.Fatalf("in-window authorize = %+v err=%v, want approved", d, err)
	}

	// Advance the clock PAST the grant window: the stale grant is NOT reused — a fresh
	// request is opened and the gate denies (pending) until a human approves anew.
	now = now.Add(25 * time.Hour)
	if d, err := gate.Authorize(ctx, tenant, req); err != nil || d.Approved {
		t.Fatalf("post-window authorize = %+v err=%v, want a fresh deny (pending)", d, err)
	}
	list2 := items(h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals?action=security.enforcement.enable"))
	if len(list2) != 2 {
		t.Fatalf("after the grant window a 2nd (fresh) approval must be opened; got %d", len(list2))
	}
}

// TestApprovalBridgeProposerCannotDecide proves the SoD-safe-by-construction property:
// the editor SERVICE token the bridge proposes as can open an approval but can NEVER
// decide one (deciding is admin-tier), so the proposer can never approve its own work.
func TestApprovalBridgeProposerCannotDecide(t *testing.T) {
	h := newHarness(t)
	svc := h.mintBoundToken(t, auth.RoleEditor)
	gate := buildBridge(t, h, svc).deployGate()
	dec, err := gate.Request(context.Background(), deploy.ApprovalRequest{
		Tenant: model.TenantID(h.tenantA), Action: "deploy.apply", SubjectKind: "deployment",
		SubjectRef: "svc/x", PlanHash: "h1", RequestedBy: "user:svc",
	})
	if err != nil || dec.ApprovalRef == "" {
		t.Fatalf("request: %+v err=%v", dec, err)
	}
	if code, _ := h.req("POST", "/v1/m/governance/approvals/"+dec.ApprovalRef+"/decisions", svc, h.tenantA,
		map[string]any{"decision": "approve"}); code != http.StatusForbidden {
		t.Fatalf("editor service token decide = %d, want 403 (cannot decide)", code)
	}
}

// A loopback issued from INSIDE a chi-served request must route fresh on the
// engine handler. chi v5 treats a context that already carries a RouteContext
// as a subrouter continuation (leftover RoutePath of the outer match), which
// 404'd every in-request bridge call — a schedule/workflow fire opening its
// approval, a hook-PEP gateOnce — until loopbackContext stripped it. This
// pins the fix with the exact polluted-context shape a live handler produces.
func TestApprovalBridgeLoopbackInsideChiRequestContext(t *testing.T) {
	h := newHarness(t)
	_, _ = h.createApprover(t, "approver-loopback@bridge.test")
	svc := h.mintBoundToken(t, auth.RoleEditor)
	gate := buildBridge(t, h, svc).orchestrationGate()

	// The context an in-flight chi handler passes down: a RouteContext whose
	// RoutePath was already consumed by the outer route match.
	rctx := chi.NewRouteContext()
	rctx.RoutePath = ""
	ctx := context.WithValue(context.Background(), chi.RouteCtxKey, rctx)

	dec, err := gate.Request(ctx, orchestration.ApprovalRequest{
		Tenant: model.TenantID(h.tenantA), Action: "orchestration.workflow.run", SubjectKind: "workflow",
		SubjectRef: "wf-loopback", PlanHash: "plan-loopback", RequestedBy: "user:svc",
	})
	if err != nil {
		t.Fatalf("in-request loopback Request failed: %v", err)
	}
	if dec.ApprovalRef == "" || dec.Status != orchestration.StatusPending {
		t.Fatalf("in-request loopback = %+v, want a pending referenced approval", dec)
	}
	if s, err := gate.Status(ctx, orchestration.ApprovalCheck{
		Tenant: model.TenantID(h.tenantA), ApprovalRef: dec.ApprovalRef, PlanHash: "plan-loopback",
		Action: "orchestration.workflow.run", SubjectKind: "workflow", SubjectRef: "wf-loopback",
	}); err != nil || s.Status != orchestration.StatusPending {
		t.Fatalf("in-request loopback Status = %+v err=%v, want pending", s, err)
	}
}
