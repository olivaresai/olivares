// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Break-glass tests: the emergency path is audited, notified, time-boxed
// and post-reviewed — never a silent bypass.

func (h *harness) activateBreakGlass(token string, tenant model.TenantID, body map[string]any) resp {
	h.t.Helper()
	return h.do("POST", govPath+"/breakglass", token, body, tenantHdr(tenant))
}

func (h *harness) consumeBreakGlass(token string, tenant model.TenantID, action string) resp {
	h.t.Helper()
	return h.do("POST", govPath+"/breakglass/consume", token,
		map[string]any{"action": action, "subject_kind": "deployment", "subject_ref": "prod-1#plan=abc"}, tenantHdr(tenant))
}

// findingKinds returns the kinds of every captured finding, in emit order.
func findingKinds(fs []sdkmodel.FindingReport) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Kind)
	}
	return out
}

// Activation is admin-only, demands a justification, and is bounded to the 24h
// cap; a system principal cannot activate.
func TestBreakGlassActivationGuards(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")

	if r := h.activateBreakGlass(editor, tenant, map[string]any{"reason": "incident"}); r.code != http.StatusForbidden {
		t.Fatalf("editor activation must be 403 (admin-tier), got %d %s", r.code, r.raw)
	}
	if r := h.activateBreakGlass(admin, tenant, map[string]any{}); r.code != http.StatusBadRequest {
		t.Fatalf("activation without a reason must be 400, got %d %s", r.code, r.raw)
	}
	if r := h.activateBreakGlass(admin, tenant, map[string]any{"reason": "incident", "expires_in_seconds": 7 * 24 * 3600}); r.code != http.StatusBadRequest {
		t.Fatalf("a window past the 24h cap must be 400, got %d %s", r.code, r.raw)
	}
	if r := h.activateBreakGlass(admin, tenant, map[string]any{"reason": "incident", "match_action": "dep*loy"}); r.code != http.StatusBadRequest {
		t.Fatalf("an inner * in match_action must be 400, got %d %s", r.code, r.raw)
	}
}

// The full audited lifecycle: activate (audit + CRITICAL finding) → consume in
// scope (use trail + audit + finding) → out-of-scope consume denied → revoke →
// consume denied → post-review forced through SoD.
func TestBreakGlassLifecycleAuditedAndScoped(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	_, a2 := h.roleUser(admin, tenant, "a2@x.io", "admin")

	r := h.activateBreakGlass(admin, tenant, map[string]any{"reason": "deploy approver unreachable, prod incident", "match_action": "deploy.*"})
	if r.code != http.StatusCreated || r.body["status"] != "active" {
		t.Fatalf("activate = %d %s", r.code, r.raw)
	}
	grant := r.body["id"].(string)

	// In-scope consume is granted and leaves evidence everywhere.
	c := h.consumeBreakGlass(editor, tenant, "deploy.apply")
	if c.code != http.StatusOK || c.body["granted"] != true || c.body["grant"] != grant {
		t.Fatalf("in-scope consume = %d %s", c.code, c.raw)
	}
	// Out-of-scope consume is NOT granted (scope is real, not cosmetic).
	c = h.consumeBreakGlass(editor, tenant, "voice.session.open")
	if c.code != http.StatusOK || c.body["granted"] != false {
		t.Fatalf("out-of-scope consume must not grant: %d %s", c.code, c.raw)
	}

	// The use trail records exactly the granted action.
	u := h.do("GET", govPath+"/breakglass/"+grant+"/uses", admin, nil, tenantHdr(tenant))
	if u.code != http.StatusOK {
		t.Fatalf("uses = %d %s", u.code, u.raw)
	}
	uses := u.body["items"].([]any)
	if len(uses) != 1 {
		t.Fatalf("use trail must hold exactly the granted use, got %d", len(uses))
	}
	use := uses[0].(map[string]any)
	if use["action"] != "deploy.apply" || use["grant_id"] != grant {
		t.Fatalf("use record = %v", use)
	}
	g := h.do("GET", govPath+"/breakglass/"+grant, admin, nil, tenantHdr(tenant))
	if g.body["use_count"] != float64(1) {
		t.Fatalf("use_count = %v, want 1", g.body["use_count"])
	}

	// Ledger: activation and use are self-audited.
	acts := h.auditActions(tenant)
	for _, want := range []string{"governance.breakglass.activate", "governance.breakglass.use"} {
		if !slices.Contains(acts, want) {
			t.Fatalf("ledger missing %s (got %v)", want, acts)
		}
	}

	// Notification rail: activation emitted CRITICAL, the use emitted too.
	kinds := findingKinds(h.host.findings())
	if !slices.Contains(kinds, "governance_breakglass_activated") || !slices.Contains(kinds, "governance_breakglass_used") {
		t.Fatalf("findings missing activation/use: %v", kinds)
	}
	for _, f := range h.host.findings() {
		if f.Kind == "governance_breakglass_activated" && f.Severity != sdkmodel.SeverityCritical {
			t.Fatalf("activation finding severity = %v, want critical", f.Severity)
		}
	}

	// Revoke closes the window: no further grant.
	if rr := h.do("POST", govPath+"/breakglass/"+grant+"/revoke", admin, nil, tenantHdr(tenant)); rr.code != http.StatusOK || rr.body["status"] != "revoked" {
		t.Fatalf("revoke = %d %s", rr.code, rr.raw)
	}
	if c = h.consumeBreakGlass(editor, tenant, "deploy.apply"); c.body["granted"] != false {
		t.Fatalf("a revoked grant must not authorize: %s", c.raw)
	}

	// Forced post-review with SoD: the activator cannot review their own grant;
	// a different admin must, with a note.
	if rr := h.do("POST", govPath+"/breakglass/"+grant+"/review", admin, map[string]any{"note": "verified"}, tenantHdr(tenant)); rr.code != http.StatusForbidden {
		t.Fatalf("activator reviewing own grant must be 403 (SoD), got %d %s", rr.code, rr.raw)
	}
	if rr := h.do("POST", govPath+"/breakglass/"+grant+"/review", a2, map[string]any{}, tenantHdr(tenant)); rr.code != http.StatusBadRequest {
		t.Fatalf("review without a note must be 400, got %d %s", rr.code, rr.raw)
	}
	rr := h.do("POST", govPath+"/breakglass/"+grant+"/review", a2, map[string]any{"note": "one deploy under emergency; justified, see INC-42"}, tenantHdr(tenant))
	if rr.code != http.StatusOK || rr.body["reviewed"] != true {
		t.Fatalf("review = %d %s", rr.code, rr.raw)
	}
	if rr = h.do("POST", govPath+"/breakglass/"+grant+"/review", a2, map[string]any{"note": "again"}, tenantHdr(tenant)); rr.code != http.StatusConflict {
		t.Fatalf("a second review must be 409, got %d %s", rr.code, rr.raw)
	}
	if !slices.Contains(h.auditActions(tenant), "governance.breakglass.review") {
		t.Fatalf("review must be ledgered")
	}
}

// The time-box is real: a lapsed grant stops authorizing immediately (lazy
// expiry, no sweep needed), the sweep materializes + notifies it, and a new
// activation is BLOCKED until the lapsed grant is post-reviewed.
func TestBreakGlassExpiryAndForcedReview(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, editor := h.roleUser(admin, tenant, "ed@x.io", "editor")
	_, a2 := h.roleUser(admin, tenant, "a2@x.io", "admin")

	r := h.activateBreakGlass(admin, tenant, map[string]any{"reason": "incident", "expires_in_seconds": 60})
	if r.code != http.StatusCreated {
		t.Fatalf("activate = %d %s", r.code, r.raw)
	}
	grant := r.body["id"].(string)

	// While active an unscoped grant covers any action.
	if c := h.consumeBreakGlass(editor, tenant, "deploy.apply"); c.body["granted"] != true {
		t.Fatalf("active grant must authorize: %s", c.raw)
	}

	// Past the window: deny BEFORE any sweep (lazy effective status).
	h.clk.advance(2 * time.Minute)
	if c := h.consumeBreakGlass(editor, tenant, "deploy.apply"); c.body["granted"] != false {
		t.Fatalf("an expired grant must not authorize: %s", c.raw)
	}
	if g := h.do("GET", govPath+"/breakglass/"+grant, admin, nil, tenantHdr(tenant)); g.body["status"] != "expired" {
		t.Fatalf("effective status = %v, want expired", g.body["status"])
	}

	// A new activation is blocked while the lapsed grant is unreviewed.
	if rr := h.activateBreakGlass(admin, tenant, map[string]any{"reason": "second incident"}); rr.code != http.StatusConflict {
		t.Fatalf("activation over an unreviewed grant must be 409, got %d %s", rr.code, rr.raw)
	}

	// The sweep materializes the expiry and emits the post-review reminder.
	if s := h.do("POST", govPath+"/approvals/sweep", admin, nil, tenantHdr(tenant)); s.code != http.StatusOK || s.body["breakglass_expired"] != float64(1) {
		t.Fatalf("sweep = %d %s", s.code, s.raw)
	}
	if !slices.Contains(findingKinds(h.host.findings()), "governance_breakglass_expired") {
		t.Fatalf("sweep must emit the expiry finding: %v", findingKinds(h.host.findings()))
	}
	if !slices.Contains(h.auditActions(tenant), "governance.breakglass.sweep") {
		t.Fatalf("sweep must be ledgered")
	}

	// Post-review by a different admin unblocks the next emergency.
	if rr := h.do("POST", govPath+"/breakglass/"+grant+"/review", a2, map[string]any{"note": "expired with one use; reviewed"}, tenantHdr(tenant)); rr.code != http.StatusOK {
		t.Fatalf("review = %d %s", rr.code, rr.raw)
	}
	if rr := h.activateBreakGlass(admin, tenant, map[string]any{"reason": "second incident"}); rr.code != http.StatusCreated {
		t.Fatalf("activation after review must succeed, got %d %s", rr.code, rr.raw)
	}
}

// Exactly one active grant per tenant: emergencies do not stack.
func TestBreakGlassSingleActiveGrant(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	if r := h.activateBreakGlass(admin, tenant, map[string]any{"reason": "incident"}); r.code != http.StatusCreated {
		t.Fatalf("activate = %d %s", r.code, r.raw)
	}
	r := h.activateBreakGlass(admin, tenant, map[string]any{"reason": "another"})
	if r.code != http.StatusConflict || !strings.Contains(r.raw, "active break-glass grant already exists") {
		t.Fatalf("a second active grant must be 409, got %d %s", r.code, r.raw)
	}
}

// A grant still active cannot be post-reviewed (the review examines a CLOSED
// window), and consume validates its input bounds.
func TestBreakGlassReviewRequiresClosureAndConsumeBounds(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	_, a2 := h.roleUser(admin, tenant, "a2@x.io", "admin")

	r := h.activateBreakGlass(admin, tenant, map[string]any{"reason": "incident"})
	grant := r.body["id"].(string)
	if rr := h.do("POST", govPath+"/breakglass/"+grant+"/review", a2, map[string]any{"note": "premature"}, tenantHdr(tenant)); rr.code != http.StatusConflict {
		t.Fatalf("reviewing an ACTIVE grant must be 409, got %d %s", rr.code, rr.raw)
	}
	if c := h.do("POST", govPath+"/breakglass/consume", admin, map[string]any{}, tenantHdr(tenant)); c.code != http.StatusBadRequest {
		t.Fatalf("consume without action must be 400, got %d %s", c.code, c.raw)
	}
}

// break-glass is MANDATORILY recorded. With no recording gate wired the
// deny-closed default refuses activation (412); with the gate denying, same;
// and a successful activation stamps the grant onto its recording session.
func TestBreakGlassRequiresRecording(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// The wired gate denying => 412, never a silent unrecorded window.
	h.recGate.mu.Lock()
	h.recGate.denied = true
	h.recGate.mu.Unlock()
	if r := h.activateBreakGlass(admin, tenant, map[string]any{"reason": "incident"}); r.code != http.StatusPreconditionFailed {
		t.Fatalf("activation with recording denied must be 412, got %d %s", r.code, r.raw)
	}

	// Gate restored => activation succeeds and the grant is bound to the
	// recording session (the BindGrant linkage the replay console joins on).
	h.recGate.mu.Lock()
	h.recGate.denied = false
	h.recGate.mu.Unlock()
	r := h.activateBreakGlass(admin, tenant, map[string]any{"reason": "incident"})
	if r.code != http.StatusCreated {
		t.Fatalf("activation = %d %s", r.code, r.raw)
	}
	grant, _ := r.body["id"].(string)
	h.recGate.mu.Lock()
	bound := h.recGate.binds[grant]
	h.recGate.mu.Unlock()
	if bound != "rec-session-1" {
		t.Fatalf("grant %s not bound to its recording session: binds=%v", grant, h.recGate.binds)
	}
}

// The deny-closed DEFAULT: a governance module with NO recording gate wired
// must refuse activation — break-glass without recording is exactly the silent
// emergency power SEC-G5 forbids.
func TestBreakGlassDefaultGateDenies(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.gov.UseRecordingGate(nil) // nil restores the deny-closed default
	if r := h.activateBreakGlass(admin, tenant, map[string]any{"reason": "incident"}); r.code != http.StatusPreconditionFailed {
		t.Fatalf("activation without a recording gate must be 412, got %d %s", r.code, r.raw)
	}
}
