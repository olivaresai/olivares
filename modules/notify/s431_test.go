// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

// routes stop accepting unroutable predicates, and gain a catalog +
// dry-run surface so the console can author them without guesswork:
//
//   - match_types is validated (create AND update) against the exact set of
//     event types the module subscribes to (routedTypes) — a typo used to
//     persist a route that could never fire.
//   - GET /match-types exposes that set (the eventing GET /event-types pattern).
//   - POST /routes/evaluate answers "which routes would this signal select?"
//     without delivering anything (predicate-only: dedup/throttle/claim are
//     deliberately NOT simulated).

import (
	"net/http"
	"testing"
)

func TestS431_RouteMatchTypeValidation(t *testing.T) {
	h := newHarness(t, WithDispatcher(newFakeDispatcher("d1")))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	// Unknown match type on create → 400 (used to persist silently).
	r := h.createRoute(editor, tenant, map[string]any{
		"name": "typo", "destination": "d1", "match_types": []string{"finding.reproted"},
	})
	if r.code != http.StatusBadRequest {
		t.Errorf("create unknown match_type = %d, want 400 (%s)", r.code, r.raw)
	}

	// The three routed types are accepted.
	id := h.mustCreateRoute(editor, tenant, map[string]any{
		"name": "ok", "destination": "d1",
		"match_types": []string{"finding.reported", "approval.requested", "approval.resolved"},
	})

	// Unknown match type on update (PUT) → 400 (same hole, other verb).
	r = h.do("PUT", "/v1/m/notify/routes/"+id, editor, map[string]any{
		"destination": "d1", "match_types": []string{"cost.sampled"},
	}, tenantHdr(tenant))
	if r.code != http.StatusBadRequest {
		t.Errorf("update unknown match_type = %d, want 400 (%s)", r.code, r.raw)
	}

	// Empty match_types (match-all) stays legal on both verbs.
	if r := h.do("PUT", "/v1/m/notify/routes/"+id, editor, map[string]any{"destination": "d1"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("update with empty match_types = %d, want 200 (%s)", r.code, r.raw)
	}
}

func TestS431_MatchTypesEndpoint(t *testing.T) {
	h := newHarness(t, WithDispatcher(newFakeDispatcher("d1")))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	r := h.do("GET", "/v1/m/notify/match-types", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("match-types = %d %s", r.code, r.raw)
	}
	items, _ := r.body["match_types"].([]any)
	if len(items) != 3 {
		t.Fatalf("want 3 match types, got %d (%s)", len(items), r.raw)
	}
	seen := map[string]bool{}
	for _, it := range items {
		m, _ := it.(map[string]any)
		typ, _ := m["type"].(string)
		if desc, _ := m["description"].(string); desc == "" {
			t.Errorf("match type %q has no description", typ)
		}
		seen[typ] = true
	}
	for _, want := range []string{"finding.reported", "approval.requested", "approval.resolved"} {
		if !seen[want] {
			t.Errorf("catalog missing %q (%s)", want, r.raw)
		}
	}
}

func TestS431_RouteEvaluate(t *testing.T) {
	h := newHarness(t, WithDispatcher(newFakeDispatcher("d1")))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	secID := h.mustCreateRoute(editor, tenant, map[string]any{
		"name": "sec-high", "destination": "d1",
		"match_types": []string{"finding.reported"}, "match_kinds": []string{"security_*"},
		"min_severity": "high",
	})
	offID := h.mustCreateRoute(editor, tenant, map[string]any{
		"name": "paused", "destination": "d1", "enabled": false,
	})

	// Predicate-only dry-run is read-tier (a viewer may probe, never deliver).
	r := h.do("POST", "/v1/m/notify/routes/evaluate", viewer, map[string]any{
		"event_type": "finding.reported", "kind": "security_probe", "severity": "critical",
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("evaluate = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("want verdict per route (2), got %d (%s)", len(items), r.raw)
	}
	verdicts := map[string]map[string]any{}
	for _, it := range items {
		m, _ := it.(map[string]any)
		verdicts[m["id"].(string)] = m
	}
	if v := verdicts[secID]; v == nil || v["matched"] != true {
		t.Errorf("sec-high should match: %s", r.raw)
	}
	// The disabled route would match the predicate but is flagged, never matched.
	if v := verdicts[offID]; v == nil || v["matched"] != false {
		t.Errorf("disabled route must not report matched: %s", r.raw)
	}
	if got := intOf(r.body["matched_count"]); got != 1 {
		t.Errorf("matched_count = %d, want 1 (%s)", got, r.raw)
	}

	// A below-threshold severity names the failing dimension.
	r = h.do("POST", "/v1/m/notify/routes/evaluate", viewer, map[string]any{
		"event_type": "finding.reported", "kind": "security_probe", "severity": "low",
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("evaluate low = %d %s", r.code, r.raw)
	}
	for _, it := range r.body["items"].([]any) {
		m, _ := it.(map[string]any)
		if m["id"] != secID {
			continue
		}
		if m["matched"] != false {
			t.Errorf("low severity should not match sec-high: %s", r.raw)
		}
		found := false
		for _, mm := range m["mismatches"].([]any) {
			if mm == "severity" {
				found = true
			}
		}
		if !found {
			t.Errorf("mismatches should name severity: %s", r.raw)
		}
	}

	// Unknown event_type → 400 (same catalog as create/update validation).
	if r := h.do("POST", "/v1/m/notify/routes/evaluate", viewer, map[string]any{"event_type": "cost.sampled"}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("evaluate unknown type = %d, want 400 (%s)", r.code, r.raw)
	}
	// Garbage severity → 400.
	if r := h.do("POST", "/v1/m/notify/routes/evaluate", viewer, map[string]any{"event_type": "finding.reported", "severity": "bogus"}, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("evaluate bad severity = %d, want 400 (%s)", r.code, r.raw)
	}
}

// The route revision ledger: every mutation snapshots the routeDTO
// in-tx; restore re-applies an earlier configuration (never the name) after
// re-validation; a foreign revision is a 404; a deleted route keeps its
// history as evidence.
func TestS431_RouteRevisions(t *testing.T) {
	h := newHarness(t, WithDispatcher(newFakeDispatcher("d1", "d2")))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")
	adminTok := h.roleToken(admin, tenant, "a@x.io", "admin")

	id := h.mustCreateRoute(editor, tenant, map[string]any{
		"name": "r1", "destination": "d1", "min_severity": "high",
		"match_types": []string{"finding.reported"},
	})
	r := h.do("PUT", "/v1/m/notify/routes/"+id, editor, map[string]any{
		"destination": "d2", "min_severity": "critical",
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("update = %d %s", r.code, r.raw)
	}

	r = h.do("GET", "/v1/m/notify/routes/"+id+"/revisions", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("revisions = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("want 2 revisions (create,update), got %d (%s)", len(items), r.raw)
	}
	first := items[0].(map[string]any)
	if first["op"] != "create" || items[1].(map[string]any)["op"] != "update" {
		t.Fatalf("ops mismatch: %s", r.raw)
	}
	snap := first["snapshot"].(map[string]any)
	if snap["destination"] != "d1" || snap["min_severity"] != "high" {
		t.Fatalf("create snapshot mismatch: %v", snap)
	}

	// Restore the original configuration.
	r = h.do("POST", "/v1/m/notify/routes/"+id+"/restore", editor, map[string]any{
		"revision_id": first["id"],
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("restore = %d %s", r.code, r.raw)
	}
	if r.body["destination"] != "d1" || r.body["min_severity"] != "high" {
		t.Fatalf("restore did not re-apply: %s", r.raw)
	}
	r = h.do("GET", "/v1/m/notify/routes/"+id+"/revisions", editor, nil, tenantHdr(tenant))
	if items, _ = r.body["items"].([]any); len(items) != 3 || items[2].(map[string]any)["op"] != "restore" {
		t.Fatalf("want 3rd revision op=restore, got %s", r.raw)
	}

	// A revision belonging to ANOTHER route: 404.
	otherID := h.mustCreateRoute(editor, tenant, map[string]any{"name": "r2", "destination": "d1"})
	if r := h.do("POST", "/v1/m/notify/routes/"+otherID+"/restore", editor, map[string]any{
		"revision_id": first["id"],
	}, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Fatalf("foreign revision restore = %d, want 404 (%s)", r.code, r.raw)
	}

	// Delete keeps the history as evidence, with a final delete snapshot.
	if r := h.do("DELETE", "/v1/m/notify/routes/"+otherID, adminTok, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/notify/routes/"+otherID+"/revisions", editor, nil, tenantHdr(tenant))
	if items, _ = r.body["items"].([]any); len(items) != 2 || items[1].(map[string]any)["op"] != "delete" {
		t.Fatalf("deleted route must keep create+delete revisions, got %s", r.raw)
	}
}
