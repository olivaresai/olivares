// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

const securitySource = "olivares.security"

// ---------------------------------------------------------------------------
// 1. Route CRUD via API + validation + audit
// ---------------------------------------------------------------------------

func TestRouteCRUD(t *testing.T) {
	h := newHarness(t, WithDispatcher(newFakeDispatcher("d1")))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")
	adminTok := h.roleToken(admin, tenant, "a@x.io", "admin")

	// Create (201) with full predicate.
	id := h.mustCreateRoute(editor, tenant, map[string]any{
		"name": "sec-high", "destination": "d1",
		"match_kinds": []string{"security_*"}, "min_severity": "high",
		"match_sources": []string{securitySource}, "match_subject_kinds": []string{"agent"},
		"dedup_window_seconds": 60, "priority": 5,
	})
	if id == "" {
		t.Fatal("create returned empty id")
	}

	// GET list.
	r := h.do("GET", "/v1/m/notify/routes", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("list = %d %s", r.code, r.raw)
	}
	if items, _ := r.body["items"].([]any); len(items) != 1 {
		t.Fatalf("want 1 route, got %d", len(items))
	}

	// GET by id, and verify the predicate round-trips.
	r = h.do("GET", "/v1/m/notify/routes/"+id, editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("get = %d %s", r.code, r.raw)
	}
	if r.body["min_severity"] != "high" || r.body["destination"] != "d1" {
		t.Errorf("get round-trip mismatch: %s", r.raw)
	}
	if mk, _ := r.body["match_kinds"].([]any); len(mk) != 1 || mk[0] != "security_*" {
		t.Errorf("match_kinds not round-tripped: %v", r.body["match_kinds"])
	}
	if got := intOf(r.body["dedup_window_seconds"]); got != 60 {
		t.Errorf("dedup window not round-tripped: %d", got)
	}

	// PUT update: retarget + change windows.
	r = h.do("PUT", "/v1/m/notify/routes/"+id, editor, map[string]any{
		"name": "ignored-on-update", "destination": "d1",
		"min_severity": "critical", "throttle_window_seconds": 30,
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("update = %d %s", r.code, r.raw)
	}
	if r.body["min_severity"] != "critical" {
		t.Errorf("update did not change min_severity: %s", r.raw)
	}
	// The natural key (name) is NOT mutated by update (route.go comment).
	if r.body["name"] != "sec-high" {
		t.Errorf("update must not change the name; got %v", r.body["name"])
	}
	if got := intOf(r.body["throttle_window_seconds"]); got != 30 {
		t.Errorf("update throttle window = %d, want 30", got)
	}
	if got := intOf(r.body["dedup_window_seconds"]); got != 0 {
		t.Errorf("update should reset omitted dedup window to 0; got %d", got)
	}

	// DELETE is admin-tier.
	if r := h.do("DELETE", "/v1/m/notify/routes/"+id, adminTok, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/m/notify/routes/"+id, editor, nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", r.code)
	}

	// Self-audit: create/update/delete each append a semantic audit event.
	actions := strings.Join(h.auditActions(tenant), ",")
	for _, want := range []string{"notify.route.create", "notify.route.update", "notify.route.delete"} {
		if !strings.Contains(actions, want) {
			t.Errorf("missing audit action %q; actions=%s", want, actions)
		}
	}
}

func TestRouteValidation(t *testing.T) {
	h := newHarness(t, WithDispatcher(newFakeDispatcher("d1")))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	// name required.
	if r := h.createRoute(editor, tenant, map[string]any{"destination": "d1"}); r.code != http.StatusBadRequest {
		t.Errorf("missing name = %d, want 400", r.code)
	}
	// destination required.
	if r := h.createRoute(editor, tenant, map[string]any{"name": "x"}); r.code != http.StatusBadRequest {
		t.Errorf("missing destination = %d, want 400", r.code)
	}
	// bad min_severity.
	if r := h.createRoute(editor, tenant, map[string]any{"name": "x", "destination": "d1", "min_severity": "bogus"}); r.code != http.StatusBadRequest {
		t.Errorf("bad min_severity = %d, want 400", r.code)
	}

	// Duplicate name → 409.
	h.mustCreateRoute(editor, tenant, map[string]any{"name": "dup", "destination": "d1"})
	if r := h.createRoute(editor, tenant, map[string]any{"name": "dup", "destination": "d1"}); r.code != http.StatusConflict {
		t.Errorf("duplicate name = %d, want 409 (%s)", r.code, r.raw)
	}

	// Bad min_severity on update → 400.
	id := h.mustCreateRoute(editor, tenant, map[string]any{"name": "u", "destination": "d1"})
	if r := h.do("PUT", "/v1/m/notify/routes/"+id, editor, map[string]any{"destination": "d1", "min_severity": "nope"}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("update bad min_severity = %d, want 400", r.code)
	}
	// Update with missing destination → 400.
	if r := h.do("PUT", "/v1/m/notify/routes/"+id, editor, map[string]any{"min_severity": "high"}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("update missing destination = %d, want 400", r.code)
	}
}

// ---------------------------------------------------------------------------
// 2. Routing match → delivery + ledger
// ---------------------------------------------------------------------------

func TestRoutingMatchDelivers(t *testing.T) {
	disp := newFakeDispatcher("d1")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.mustCreateRoute(editor, tenant, map[string]any{
		"name": "sec", "destination": "d1",
		"match_kinds": []string{"security_*"}, "min_severity": "high",
	})

	h.publishFinding(tenant, securitySource, finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a1", "t"))

	got := disp.all()
	if len(got) != 1 {
		t.Fatalf("want 1 delivered notification, got %d", len(got))
	}
	n := got[0]
	if n.Type != "finding.reported" {
		t.Errorf("notification Type = %q, want finding.reported", n.Type)
	}
	if n.Severity != sdkmodel.SeverityHigh {
		t.Errorf("notification Severity = %q, want high", n.Severity)
	}
	if n.Fields["kind"] != "security_guardrail" {
		t.Errorf("Fields[kind] = %q, want security_guardrail", n.Fields["kind"])
	}
	if n.Tenant != tenant.String() {
		t.Errorf("notification Tenant = %q, want %q", n.Tenant, tenant.String())
	}

	// Ledger: claim-then-send appends a CLAIM before the external send
	// and the terminal outcome after — two rows, immutable evidence of both the
	// reservation and the result.
	all := h.deliveries(editor, tenant, "")
	if len(all) != 2 {
		t.Fatalf("want claim + outcome rows, got %d: %v", len(all), all)
	}
	dels := h.terminalDeliveries(editor, tenant, "")
	if len(dels) != 1 {
		t.Fatalf("want 1 terminal delivery row, got %d", len(dels))
	}
	if dels[0]["status"] != statusDelivered {
		t.Errorf("delivery status = %v, want delivered", dels[0]["status"])
	}
	if dels[0]["finding_kind"] != "security_guardrail" {
		t.Errorf("delivery finding_kind = %v", dels[0]["finding_kind"])
	}

	// A non-matching finding (different kind) does NOT deliver.
	disp.reset()
	h.publishFinding(tenant, securitySource, finding("finops_budget", sdkmodel.SeverityHigh, "agent", "a1", "t"))
	if disp.count() != 0 {
		t.Errorf("non-matching kind should not deliver; got %d", disp.count())
	}
	// And a low-severity matching-kind finding does not deliver either.
	h.publishFinding(tenant, securitySource, finding("security_guardrail", sdkmodel.SeverityLow, "agent", "a1", "t"))
	if disp.count() != 0 {
		t.Errorf("below-threshold severity should not deliver; got %d", disp.count())
	}
	// The ledger only grew by zero rows (suppressions / non-matches are not recorded).
	if dels := h.deliveries(editor, tenant, ""); len(dels) != 2 {
		t.Errorf("non-matches must not write ledger rows; got %d", len(dels))
	}
}

// ---------------------------------------------------------------------------
// 3. Severity threshold
// ---------------------------------------------------------------------------

func TestSeverityThreshold(t *testing.T) {
	disp := newFakeDispatcher("d1")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.mustCreateRoute(editor, tenant, map[string]any{
		"name": "high-only", "destination": "d1", "min_severity": "high",
	})

	// medium < high → suppressed.
	h.publishFinding(tenant, securitySource, finding("security_guardrail", sdkmodel.SeverityMedium, "agent", "a1", "t"))
	if disp.count() != 0 {
		t.Errorf("medium must not pass a high threshold; got %d", disp.count())
	}
	// critical >= high → delivered.
	h.publishFinding(tenant, securitySource, finding("security_guardrail", sdkmodel.SeverityCritical, "agent", "a1", "t"))
	if disp.count() != 1 {
		t.Errorf("critical must pass a high threshold; got %d", disp.count())
	}
}

// ---------------------------------------------------------------------------
// 4. Glob + sources + subject_kinds matching (each ANDed)
// ---------------------------------------------------------------------------

func TestMatchDimensionsANDed(t *testing.T) {
	disp := newFakeDispatcher("d1")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.mustCreateRoute(editor, tenant, map[string]any{
		"name": "health", "destination": "d1",
		"match_kinds":         []string{"health_*"},
		"match_sources":       []string{"olivares.health"},
		"match_subject_kinds": []string{"service"},
	})

	// Full match across all three dimensions.
	h.publishFinding(tenant, "olivares.health", finding("health_subject_down", sdkmodel.SeverityHigh, "service", "svc1", "down"))
	if disp.count() != 1 {
		t.Fatalf("full match should deliver; got %d", disp.count())
	}

	// Glob miss (kind not health_*).
	disp.reset()
	h.publishFinding(tenant, "olivares.health", finding("security_guardrail", sdkmodel.SeverityHigh, "service", "svc1", "x"))
	if disp.count() != 0 {
		t.Errorf("kind glob miss should not deliver; got %d", disp.count())
	}

	// Source miss (right kind+subject, wrong source).
	h.publishFinding(tenant, "olivares.security", finding("health_subject_down", sdkmodel.SeverityHigh, "service", "svc1", "x"))
	if disp.count() != 0 {
		t.Errorf("source miss should not deliver; got %d", disp.count())
	}

	// Subject-kind miss (right kind+source, wrong subject kind).
	h.publishFinding(tenant, "olivares.health", finding("health_subject_down", sdkmodel.SeverityHigh, "agent", "a1", "x"))
	if disp.count() != 0 {
		t.Errorf("subject-kind miss should not deliver; got %d", disp.count())
	}
}

// ---------------------------------------------------------------------------
// 5. Dedup window
// ---------------------------------------------------------------------------

func TestDedupWindow(t *testing.T) {
	disp := newFakeDispatcher("d1")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.mustCreateRoute(editor, tenant, map[string]any{
		"name": "dd", "destination": "d1", "dedup_window_seconds": 300,
	})

	f := finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a1", "t")

	// First delivers.
	h.publishFinding(tenant, securitySource, f)
	if disp.count() != 1 {
		t.Fatalf("first finding must deliver; got %d", disp.count())
	}
	// Identical (same kind+subject) within the window → suppressed.
	h.publishFinding(tenant, securitySource, f)
	if disp.count() != 1 {
		t.Errorf("duplicate within window must be suppressed; got %d", disp.count())
	}
	// A DIFFERENT subject is a different dedup key → delivers.
	h.publishFinding(tenant, securitySource, finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a2", "t"))
	if disp.count() != 2 {
		t.Errorf("different subject must not be deduped; got %d", disp.count())
	}

	// Advance the clock past the window → the original key delivers again.
	h.clk.advance(301 * time.Second)
	h.publishFinding(tenant, securitySource, f)
	if disp.count() != 3 {
		t.Errorf("after window expiry the duplicate must deliver again; got %d", disp.count())
	}
}

// ---------------------------------------------------------------------------
// 6. Throttle window
// ---------------------------------------------------------------------------

func TestThrottleWindow(t *testing.T) {
	disp := newFakeDispatcher("d1")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.mustCreateRoute(editor, tenant, map[string]any{
		"name": "thr", "destination": "d1", "throttle_window_seconds": 300,
	})

	// First delivery for the route.
	h.publishFinding(tenant, securitySource, finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a1", "t"))
	if disp.count() != 1 {
		t.Fatalf("first finding must deliver; got %d", disp.count())
	}
	// A DIFFERENT-kind finding on the SAME route within the window is throttled.
	h.publishFinding(tenant, securitySource, finding("eval_regression", sdkmodel.SeverityHigh, "agent", "a2", "t"))
	if disp.count() != 1 {
		t.Errorf("throttle must suppress a second delivery on the route; got %d", disp.count())
	}
	// Past the throttle window → delivers again.
	h.clk.advance(301 * time.Second)
	h.publishFinding(tenant, securitySource, finding("eval_regression", sdkmodel.SeverityHigh, "agent", "a3", "t"))
	if disp.count() != 2 {
		t.Errorf("after throttle window the route delivers again; got %d", disp.count())
	}
}

// ---------------------------------------------------------------------------
// 7. Unknown destination + no dispatcher
// ---------------------------------------------------------------------------

func TestUnknownDestinationStatus(t *testing.T) {
	// The destination EXISTS when the route is authored and is gone by the time a
	// finding routes through it — an operator removed it. That is the scenario the
	// delivery-time status exists for, and since authoring now refuses a destination
	// the tenant cannot address, it is also the only way to reach it.
	disp := newFakeDispatcher("d1", "ghost")
	disp.failOn["ghost"] = ErrUnknownDestination
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.mustCreateRoute(editor, tenant, map[string]any{"name": "ghost-route", "destination": "ghost"})
	disp.forget("ghost")
	h.publishFinding(tenant, securitySource, finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a1", "t"))

	if disp.count() != 0 {
		t.Errorf("unknown destination must not record a delivered notification; got %d", disp.count())
	}
	dels := h.terminalDeliveries(editor, tenant, "")
	if len(dels) != 1 || dels[0]["status"] != statusUnknownDest {
		t.Fatalf("want 1 terminal ledger row status=unknown_destination; got %v", dels)
	}
}

func TestNoDispatcherStatus(t *testing.T) {
	// Construct WITHOUT WithDispatcher → the nopDispatcher fail-closed default.
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.mustCreateRoute(editor, tenant, map[string]any{"name": "nowhere", "destination": "d1"})
	h.publishFinding(tenant, securitySource, finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a1", "t"))

	dels := h.terminalDeliveries(editor, tenant, "")
	if len(dels) != 1 || dels[0]["status"] != statusNoDispatcher {
		t.Fatalf("want 1 terminal ledger row status=no_dispatcher; got %v", dels)
	}
}

// ---------------------------------------------------------------------------
// 8. Test endpoint
// ---------------------------------------------------------------------------

func TestRouteTestEndpoint(t *testing.T) {
	disp := newFakeDispatcher("d1")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	adminTok := h.roleToken(admin, tenant, "a@x.io", "admin")

	id := h.mustCreateRoute(adminTok, tenant, map[string]any{"name": "tr", "destination": "d1"})

	r := h.do("POST", "/v1/m/notify/routes/"+id+"/test", adminTok, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("test route = %d %s", r.code, r.raw)
	}
	if r.body["status"] != statusDelivered || r.body["destination"] != "d1" {
		t.Errorf("test result = %s", r.raw)
	}

	// A synthetic notification was actually delivered through the seam.
	got := disp.all()
	if len(got) != 1 || got[0].Fields["kind"] != "notify_test" {
		t.Fatalf("test must deliver a synthetic notify_test notification; got %v", got)
	}

	// Ledger rows recorded for the test attempt (claim + outcome).
	dels := h.terminalDeliveries(adminTok, tenant, "finding_kind=notify_test")
	if len(dels) != 1 || dels[0]["status"] != statusDelivered {
		t.Errorf("test must record a terminal ledger row; got %v", dels)
	}

	// Audited.
	if !strings.Contains(strings.Join(h.auditActions(tenant), ","), "notify.route.test") {
		t.Errorf("route test must self-audit")
	}
}

// ---------------------------------------------------------------------------
// 9. GET /destinations
// ---------------------------------------------------------------------------

func TestDestinationsEndpoint(t *testing.T) {
	disp := newFakeDispatcher("slack-ops", "pagerduty")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	r := h.do("GET", "/v1/m/notify/destinations", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("destinations = %d %s", r.code, r.raw)
	}
	dests, _ := r.body["destinations"].([]any)
	if len(dests) != 2 {
		t.Fatalf("want 2 destinations, got %d (%s)", len(dests), r.raw)
	}
	set := map[string]bool{}
	for _, d := range dests {
		set[d.(string)] = true
	}
	if !set["slack-ops"] || !set["pagerduty"] {
		t.Errorf("destinations mismatch: %v", dests)
	}

	// With no dispatcher the list is an empty array, never null.
	h2 := newHarness(t)
	admin2 := h2.adminLogin()
	t2 := h2.createOrg(admin2, "acme")
	v2 := h2.roleToken(admin2, t2, "v@x.io", "viewer")
	r2 := h2.do("GET", "/v1/m/notify/destinations", v2, nil, tenantHdr(t2))
	if d, _ := r2.body["destinations"].([]any); d == nil || len(d) != 0 {
		t.Errorf("nop dispatcher should yield empty (non-null) destinations; got %s", r2.raw)
	}
}

// ---------------------------------------------------------------------------
// 10. Tenant isolation
// ---------------------------------------------------------------------------

func TestTenantIsolation(t *testing.T) {
	disp := newFakeDispatcher("d1")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")
	editorA := h.roleToken(admin, tenantA, "a@x.io", "editor")
	editorB := h.roleToken(admin, tenantB, "b@x.io", "editor")

	// A route only in tenant A.
	h.mustCreateRoute(editorA, tenantA, map[string]any{"name": "a-route", "destination": "d1"})

	// Tenant B's finding must NOT match A's route (no route in B at all).
	h.publishFinding(tenantB, securitySource, finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a1", "t"))
	if disp.count() != 0 {
		t.Errorf("tenant B finding must not route through tenant A's route; got %d", disp.count())
	}
	if len(h.deliveries(editorB, tenantB, "")) != 0 {
		t.Errorf("tenant B should have no deliveries")
	}

	// Tenant A's finding delivers and is recorded only under A.
	h.publishFinding(tenantA, securitySource, finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a1", "t"))
	if disp.count() != 1 {
		t.Fatalf("tenant A finding must deliver; got %d", disp.count())
	}
	if len(h.terminalDeliveries(editorA, tenantA, "")) != 1 {
		t.Errorf("tenant A should have 1 terminal delivery")
	}
	if len(h.deliveries(editorB, tenantB, "")) != 0 {
		t.Errorf("tenant B ledger must stay empty; deliveries are tenant-scoped")
	}
}

// ---------------------------------------------------------------------------
// 11. RBAC tiers
// ---------------------------------------------------------------------------

func TestRBACTiers(t *testing.T) {
	disp := newFakeDispatcher("d1")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")
	adminTok := h.roleToken(admin, tenant, "a@x.io", "admin")

	// Seed a route as admin to exercise read/delete/test paths.
	id := h.mustCreateRoute(adminTok, tenant, map[string]any{"name": "r", "destination": "d1"})

	// viewer: GET ok, POST/DELETE/test forbidden.
	if r := h.do("GET", "/v1/m/notify/routes", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("viewer GET routes = %d, want 200", r.code)
	}
	if r := h.do("GET", "/v1/m/notify/deliveries", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("viewer GET deliveries = %d, want 200", r.code)
	}
	if r := h.createRoute(viewer, tenant, map[string]any{"name": "x", "destination": "d1"}); r.code != http.StatusForbidden {
		t.Errorf("viewer POST route = %d, want 403", r.code)
	}

	// editor: POST/PUT ok, DELETE/test forbidden (admin-tier).
	eid := h.mustCreateRoute(editor, tenant, map[string]any{"name": "ed", "destination": "d1"})
	if r := h.do("PUT", "/v1/m/notify/routes/"+eid, editor, map[string]any{"destination": "d1"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("editor PUT route = %d, want 200 (%s)", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/m/notify/routes/"+eid, editor, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("editor DELETE route = %d, want 403", r.code)
	}
	if r := h.do("POST", "/v1/m/notify/routes/"+id+"/test", editor, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("editor test route = %d, want 403", r.code)
	}

	// admin: DELETE + test ok.
	if r := h.do("POST", "/v1/m/notify/routes/"+id+"/test", adminTok, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("admin test route = %d, want 200 (%s)", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/m/notify/routes/"+id, adminTok, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Errorf("admin DELETE route = %d, want 204", r.code)
	}
}

// ---------------------------------------------------------------------------
// Extra: priority ordering of routes + disabled routes are skipped
// ---------------------------------------------------------------------------

func TestDisabledRouteSkipped(t *testing.T) {
	disp := newFakeDispatcher("d1")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	enabled := false
	h.mustCreateRoute(editor, tenant, map[string]any{"name": "off", "destination": "d1", "enabled": &enabled})
	h.publishFinding(tenant, securitySource, finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a1", "t"))
	if disp.count() != 0 {
		t.Errorf("a disabled route must not deliver; got %d", disp.count())
	}
}

func TestMultipleRoutesBothDeliver(t *testing.T) {
	disp := newFakeDispatcher("d1", "d2")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.mustCreateRoute(editor, tenant, map[string]any{"name": "r1", "destination": "d1", "match_kinds": []string{"security_*"}, "priority": 1})
	h.mustCreateRoute(editor, tenant, map[string]any{"name": "r2", "destination": "d2", "match_kinds": []string{"security_guardrail"}, "priority": 2})

	h.publishFinding(tenant, securitySource, finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a1", "t"))
	if disp.count() != 2 {
		t.Fatalf("a finding matching two routes delivers to both; got %d", disp.count())
	}
	dests := map[string]int{}
	for _, n := range disp.all() {
		dests[n.Fields["route"]]++
	}
	if dests["r1"] != 1 || dests["r2"] != 1 {
		t.Errorf("both routes should each deliver once; got %v", dests)
	}
}

// intOf extracts an int from a decoded JSON number (float64) or returns 0.
func intOf(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

// ensure model import is used (TenantID in helper signatures) even if a future edit
// removes its only direct reference in this file.
var _ = model.TenantID("")

// TestConcurrentDuplicateSingleSend pins the claim-then-send property:
// two concurrent processings of the SAME finding — the shape of two HA nodes
// receiving one event over the NATS bridge, or two racing handler goroutines —
// produce exactly ONE external send. Both pass the read-only pre-match
// concurrently; the claim transaction serializes them and the loser is
// dedup-suppressed by the winner's claim row.
func TestConcurrentDuplicateSingleSend(t *testing.T) {
	disp := newFakeDispatcher("d1")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")
	h.mustCreateRoute(editor, tenant, map[string]any{
		"name": "dup-race", "destination": "d1", "dedup_window_seconds": 300,
	})

	f := finding("security_guardrail", sdkmodel.SeverityHigh, "agent", "a1", "t")
	e := event.Event{Type: event.TypeFindingReported, Tenant: tenant.String(), Source: securitySource, Payload: f}

	const dupes = 4
	var wg sync.WaitGroup
	for i := 0; i < dupes; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = h.mod.processFinding(context.Background(), tenant, e, f)
		}()
	}
	wg.Wait()
	// Concurrent claims serialize on the route row, so only ONE outbox row is enqueued;
	// draining it must produce exactly one external send.
	h.pumpOutbox(tenant)

	if got := disp.count(); got != 1 {
		t.Fatalf("concurrent duplicates must produce exactly 1 external send, got %d", got)
	}
	terminal := h.terminalDeliveries(editor, tenant, "")
	if len(terminal) != 1 || terminal[0]["status"] != statusDelivered {
		t.Fatalf("want exactly 1 terminal delivered row, got %v", terminal)
	}
}

// ---------------------------------------------------------------------------
// 12. Approval origination: approval.requested -> interactive card
// ---------------------------------------------------------------------------

const governanceSource = "olivares.governance"

// sampleApproval is a critical-tier approval like the governance module opens.
func sampleApproval(id string) event.ApprovalRequest {
	return event.ApprovalRequest{
		ApprovalID:        id,
		Action:            "sessions.run.launch",
		SubjectKind:       "session",
		RiskTier:          "critical",
		RequiredApprovals: 2,
		PolicyRef:         "pol_42",
	}
}

func sampleApprovalResolution(id string) event.ApprovalResolution {
	return event.ApprovalResolution{
		ApprovalID:        id,
		Action:            "sessions.run.launch",
		SubjectKind:       "session",
		RiskTier:          "critical",
		Outcome:           "approved",
		RequiredApprovals: 2,
		ApproveCount:      2,
		RejectCount:       0,
		PolicyRef:         "pol_42",
		DecidedAt:         time.Date(2026, 6, 4, 12, 3, 0, 0, time.UTC),
	}
}

// TestApprovalRequestedDeliversInteractiveCard is the origination proof: an
// opened approval routed to a destination arrives as a minimal-data notification
// carrying the approve/deny actions in the exact shape cmd/olivares/hitl.go parses.
func TestApprovalRequestedDeliversInteractiveCard(t *testing.T) {
	disp := newFakeDispatcher("approvals")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.mustCreateRoute(editor, tenant, map[string]any{
		"name": "approvals", "destination": "approvals",
		"match_types": []string{"approval.requested"},
	})

	h.publishApproval(tenant, governanceSource, sampleApproval("appr_123"))

	got := disp.all()
	if len(got) != 1 {
		t.Fatalf("want 1 delivered approval card, got %d", len(got))
	}
	n := got[0]
	if n.Type != "approval.requested" {
		t.Errorf("Type = %q, want approval.requested", n.Type)
	}
	if n.Severity != sdkmodel.SeverityCritical {
		t.Errorf("Severity = %q, want critical (from risk tier)", n.Severity)
	}
	if !strings.Contains(n.Title, "sessions.run.launch") {
		t.Errorf("Title = %q, want it to name the action", n.Title)
	}
	for k, want := range map[string]string{
		"approval_id": "appr_123", "action": "sessions.run.launch",
		"subject_kind": "session", "risk_tier": "critical", "required_approvals": "2",
		"policy_ref": "pol_42",
	} {
		if n.Fields[k] != want {
			t.Errorf("Fields[%q] = %q, want %q", k, n.Fields[k], want)
		}
	}
	// Minimal-data: never the (absent) subject reference or requester reason.
	if _, ok := n.Fields["subject_ref"]; ok {
		t.Errorf("approval card must not carry a subject_ref (absent from the event)")
	}

	// The interactive actions are the exact inbound contract: action_id repeats the
	// decision (olivares_ prefix), value packs "decision:approval_id".
	if len(n.Actions) != 2 {
		t.Fatalf("actions = %d, want approve+deny", len(n.Actions))
	}
	wantActions := []struct{ id, value, style string }{
		{"olivares_approve", "approve:appr_123", "primary"},
		{"olivares_deny", "deny:appr_123", "danger"},
	}
	for i, w := range wantActions {
		a := n.Actions[i]
		if a.ID != w.id || a.Value != w.value || a.Style != w.style || a.Label == "" {
			t.Errorf("action[%d] = %+v, want id=%s value=%s style=%s", i, a, w.id, w.value, w.style)
		}
	}

	// Ledger: claim + terminal outcome, keyed per approval id.
	if all := h.deliveries(editor, tenant, ""); len(all) != 2 {
		t.Fatalf("want claim + outcome rows, got %d", len(all))
	}
	dels := h.terminalDeliveries(editor, tenant, "")
	if len(dels) != 1 || dels[0]["status"] != statusDelivered {
		t.Fatalf("want 1 delivered terminal row, got %v", dels)
	}
}

func TestApprovalResolvedDeliversResolutionNotice(t *testing.T) {
	disp := newFakeDispatcher("approvals")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.mustCreateRoute(editor, tenant, map[string]any{
		"name": "approval-resolutions", "destination": "approvals",
		"match_types": []string{"approval.resolved"},
	})

	res := sampleApprovalResolution("appr_done")
	if err := h.bus.Publish(context.Background(), event.ApprovalResolved(tenant.String(), governanceSource, h.clk.now(), res)); err != nil {
		t.Fatalf("publish approval.resolved: %v", err)
	}
	h.waitDelivery()
	h.pumpOutbox(tenant)

	got := disp.all()
	if len(got) != 1 {
		t.Fatalf("want 1 delivered resolution notice, got %d", len(got))
	}
	n := got[0]
	if n.Type != "approval.resolved" {
		t.Errorf("Type = %q, want approval.resolved", n.Type)
	}
	if n.Title != "Approval resolved: approved" {
		t.Errorf("Title = %q, want resolved outcome title", n.Title)
	}
	if len(n.Actions) != 0 {
		t.Fatalf("resolution notice must not carry interactive actions, got %+v", n.Actions)
	}
	for k, want := range map[string]string{
		"approval_id": "appr_done", "action": "sessions.run.launch",
		"subject_kind": "session", "risk_tier": "critical", "required_approvals": "2",
		"policy_ref": "pol_42", "outcome": "approved", "approve_count": "2", "reject_count": "0",
	} {
		if n.Fields[k] != want {
			t.Errorf("Fields[%q] = %q, want %q", k, n.Fields[k], want)
		}
	}
	if n.Fields["decided_at"] == "" {
		t.Error("decided_at should be projected when present")
	}
	if _, ok := n.Fields["subject_ref"]; ok {
		t.Errorf("approval resolution notice must not carry a subject_ref")
	}
}

// TestApprovalLifecycleNotDedupedAcrossEventTypes proves the dedup key carries
// the event type: an approval's opened card and its terminal notice share kind
// (the action) and subjectRef (the approval id), so a route matching BOTH types
// with a dedup window must still deliver the resolution — it is a distinct
// notification, not a duplicate of the request. A repeated resolution within
// the window IS suppressed (dedup still works within one type).
func TestApprovalLifecycleNotDedupedAcrossEventTypes(t *testing.T) {
	disp := newFakeDispatcher("approvals")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.mustCreateRoute(editor, tenant, map[string]any{
		"name": "approval-lifecycle", "destination": "approvals",
		"match_types":          []string{"approval.requested", "approval.resolved"},
		"dedup_window_seconds": 300,
	})

	h.publishApproval(tenant, governanceSource, sampleApproval("appr_fast"))
	if disp.count() != 1 {
		t.Fatalf("the opened approval card must deliver; got %d", disp.count())
	}

	res := sampleApprovalResolution("appr_fast")
	if err := h.bus.Publish(context.Background(), event.ApprovalResolved(tenant.String(), governanceSource, h.clk.now(), res)); err != nil {
		t.Fatalf("publish approval.resolved: %v", err)
	}
	h.waitDelivery()
	h.pumpOutbox(tenant)
	if disp.count() != 2 {
		t.Fatalf("resolution within the request's dedup window must deliver; got %d notices", disp.count())
	}

	// The SAME resolution again within the window is a true duplicate → suppressed.
	if err := h.bus.Publish(context.Background(), event.ApprovalResolved(tenant.String(), governanceSource, h.clk.now(), res)); err != nil {
		t.Fatalf("re-publish approval.resolved: %v", err)
	}
	h.waitDelivery()
	h.pumpOutbox(tenant)
	if disp.count() != 2 {
		t.Errorf("identical resolution within the window must be suppressed; got %d", disp.count())
	}
}

// TestApprovalRiskTierGatesOnSeverity proves the risk tier maps onto the
// severity scale and is filtered by a route's min-severity, like a finding.
func TestApprovalRiskTierGatesOnSeverity(t *testing.T) {
	disp := newFakeDispatcher("d1")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.mustCreateRoute(editor, tenant, map[string]any{
		"name": "high-only", "destination": "d1",
		"match_types": []string{"approval.requested"}, "min_severity": "high",
	})

	// A low-risk approval is below the floor -> not delivered.
	low := sampleApproval("appr_low")
	low.RiskTier = "low"
	h.publishApproval(tenant, governanceSource, low)
	if disp.count() != 0 {
		t.Fatalf("low-risk approval must not pass a high min-severity route; got %d", disp.count())
	}

	// A high-risk approval passes.
	high := sampleApproval("appr_high")
	high.RiskTier = "high"
	h.publishApproval(tenant, governanceSource, high)
	if disp.count() != 1 {
		t.Fatalf("high-risk approval must pass a high min-severity route; got %d", disp.count())
	}
	if got := disp.all(); got[0].Severity != sdkmodel.SeverityHigh {
		t.Fatalf("high-risk approval Severity = %q, want high", got[0].Severity)
	}
}

// TestApprovalDedupPerApproval proves a re-published approval within the dedup
// window does not double-notify (dedup keys on the approval id).
func TestApprovalDedupPerApproval(t *testing.T) {
	disp := newFakeDispatcher("d1")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.mustCreateRoute(editor, tenant, map[string]any{
		"name": "appr", "destination": "d1",
		"match_types": []string{"approval.requested"}, "dedup_window_seconds": 3600,
	})

	h.publishApproval(tenant, governanceSource, sampleApproval("appr_dup"))
	h.publishApproval(tenant, governanceSource, sampleApproval("appr_dup"))
	if got := disp.count(); got != 1 {
		t.Fatalf("duplicate approval within dedup window must deliver once, got %d", got)
	}

	// A different approval id is a distinct alert -> delivers.
	h.publishApproval(tenant, governanceSource, sampleApproval("appr_other"))
	if got := disp.count(); got != 2 {
		t.Fatalf("a distinct approval id must deliver its own card; got %d", got)
	}
}

// TestApprovalMissingIDIgnored proves an approval event with no id is dropped
// (nothing actionable), never delivered or recorded.
func TestApprovalMissingIDIgnored(t *testing.T) {
	disp := newFakeDispatcher("d1")
	h := newHarness(t, WithDispatcher(disp))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.mustCreateRoute(editor, tenant, map[string]any{
		"name": "appr", "destination": "d1", "match_types": []string{"approval.requested"},
	})

	h.publishApproval(tenant, governanceSource, event.ApprovalRequest{Action: "x"}) // no ApprovalID
	if disp.count() != 0 {
		t.Fatalf("approval with no id must not deliver; got %d", disp.count())
	}
}
