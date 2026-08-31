// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// createCheck POSTs a check and returns the created status DTO body. It fails the
// test if the create is not a 201.
func (h *harness) createCheck(token string, tenant model.TenantID, in map[string]any) resp {
	h.t.Helper()
	r := h.do("POST", "/v1/m/health/checks", token, in, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("create check = %d %s", r.code, r.raw)
	}
	return r
}

// report posts a probe result for a check and returns the response.
func (h *harness) report(token string, tenant model.TenantID, checkID, state string, latency int64, detail string) resp {
	h.t.Helper()
	return h.do("POST", "/v1/m/health/checks/"+checkID+"/report", token,
		map[string]any{"state": state, "latency_ms": latency, "detail": detail}, tenantHdr(tenant))
}

// listItems pulls the "items" array out of a list response.
func listItems(r resp) []any {
	items, _ := r.body["items"].([]any)
	return items
}

// TestCheckCRUD exercises the full check lifecycle through the HTTP API: create
// (201 status DTO), list, get, update, delete, the duplicate-subject 409 and the
// bad subject_kind 400 — and that each mutating call self-audits.
func TestCheckCRUD(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")
	adminTok := h.roleToken(admin, tenant, "a@x.io", "admin")

	// CREATE.
	cr := h.createCheck(editor, tenant, map[string]any{
		"name": "billing-mcp", "subject_kind": "mcp", "subject_ref": "billing.mcp",
		"expected_interval_seconds": 60, "grace_factor": 2, "sla_target_ppm": 999000,
	})
	id := strOf(cr.body["id"])
	if id == "" {
		t.Fatalf("create returned no id: %s", cr.raw)
	}
	if cr.body["state"] != stateUnknown {
		t.Errorf("fresh check must start unknown; got %v", cr.body["state"])
	}
	if cr.body["desired_status"] != "active" || intOf(cr.body["sla_target_ppm"]) != 999000 {
		t.Errorf("create DTO wrong: %s", cr.raw)
	}

	// LIST.
	lr := h.do("GET", "/v1/m/health/checks", editor, nil, tenantHdr(tenant))
	if lr.code != http.StatusOK || len(listItems(lr)) != 1 {
		t.Fatalf("list = %d items=%d %s", lr.code, len(listItems(lr)), lr.raw)
	}

	// GET by id.
	gr := h.do("GET", "/v1/m/health/checks/"+id, editor, nil, tenantHdr(tenant))
	if gr.code != http.StatusOK || strOf(gr.body["subject_ref"]) != "billing.mcp" {
		t.Fatalf("get = %d %s", gr.code, gr.raw)
	}

	// UPDATE: change cadence + pause it.
	ur := h.do("PUT", "/v1/m/health/checks/"+id, editor, map[string]any{
		"name": "billing-mcp", "expected_interval_seconds": 120, "desired_status": "paused", "sla_target_ppm": 990000,
	}, tenantHdr(tenant))
	if ur.code != http.StatusOK {
		t.Fatalf("update = %d %s", ur.code, ur.raw)
	}
	if intOf(ur.body["expected_interval_seconds"]) != 120 || ur.body["desired_status"] != "paused" {
		t.Errorf("update DTO wrong: %s", ur.raw)
	}
	if intOf(ur.body["sla_target_ppm"]) != 990000 {
		t.Errorf("explicit sla_target_ppm update lost: %s", ur.raw)
	}

	// UPDATE with sla_target_ppm OMITTED keeps the stored target (contract
	// fix: the old int64 field silently zeroed it on every partial patch).
	or := h.do("PUT", "/v1/m/health/checks/"+id, editor, map[string]any{
		"name": "billing-mcp",
	}, tenantHdr(tenant))
	if or.code != http.StatusOK || intOf(or.body["sla_target_ppm"]) != 990000 {
		t.Fatalf("omitted sla_target_ppm must keep 990000: %d %s", or.code, or.raw)
	}
	// UPDATE with an explicit 0 clears it — omission and zero are distinct.
	zr := h.do("PUT", "/v1/m/health/checks/"+id, editor, map[string]any{
		"sla_target_ppm": 0,
	}, tenantHdr(tenant))
	if zr.code != http.StatusOK || intOf(zr.body["sla_target_ppm"]) != 0 {
		t.Fatalf("explicit zero sla_target_ppm must clear: %d %s", zr.code, zr.raw)
	}

	// DUPLICATE subject → 409 (same subject_kind+subject_ref).
	if dr := h.do("POST", "/v1/m/health/checks", editor, map[string]any{
		"subject_kind": "mcp", "subject_ref": "billing.mcp",
	}, tenantHdr(tenant)); dr.code != http.StatusConflict {
		t.Fatalf("duplicate subject = %d, want 409 %s", dr.code, dr.raw)
	}

	// BAD subject_kind → 400.
	if br := h.do("POST", "/v1/m/health/checks", editor, map[string]any{
		"subject_kind": "database", "subject_ref": "x",
	}, tenantHdr(tenant)); br.code != http.StatusBadRequest {
		t.Fatalf("bad subject_kind = %d, want 400 %s", br.code, br.raw)
	}

	// DELETE is admin-tier.
	if dr := h.do("DELETE", "/v1/m/health/checks/"+id, adminTok, nil, tenantHdr(tenant)); dr.code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204 %s", dr.code, dr.raw)
	}
	if gr := h.do("GET", "/v1/m/health/checks/"+id, editor, nil, tenantHdr(tenant)); gr.code != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", gr.code)
	}

	// Self-audit: create/update/delete each appended a semantic audit event.
	actions := strings.Join(h.auditActions(tenant), ",")
	for _, want := range []string{"health.check.create", "health.check.update", "health.check.delete"} {
		if !strings.Contains(actions, want) {
			t.Errorf("missing self-audit %q; actions=%s", want, actions)
		}
	}
}

// TestReportTransitionsAndIncident drives the active-probe path: a down report
// transitions the subject, opens an incident, writes an event and emits a
// health_subject_down finding; a subsequent healthy report resolves the incident
// and emits health_subject_recovered.
func TestReportTransitionsAndIncident(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	cr := h.createCheck(editor, tenant, map[string]any{"subject_kind": "agent", "subject_ref": "agent-7"})
	id := strOf(cr.body["id"])

	// DOWN report.
	dr := h.report(editor, tenant, id, "down", 1200, "connection refused")
	if dr.code != http.StatusOK || dr.body["state"] != stateDown {
		t.Fatalf("down report = %d state=%v %s", dr.code, dr.body["state"], dr.raw)
	}

	// An OPEN incident exists.
	inc := h.do("GET", "/v1/m/health/incidents?state=open", editor, nil, tenantHdr(tenant))
	if inc.code != http.StatusOK || len(listItems(inc)) != 1 {
		t.Fatalf("open incidents = %d items=%d %s", inc.code, len(listItems(inc)), inc.raw)
	}
	first := listItems(inc)[0].(map[string]any)
	if first["kind"] != stateDown || first["state"] != "open" {
		t.Errorf("incident must be open+down; got %v", first)
	}
	incID := strOf(first["id"])

	// A health_event ledger row exists for the transition.
	ev := h.do("GET", "/v1/m/health/events?subject_kind=agent&subject_ref=agent-7", editor, nil, tenantHdr(tenant))
	if ev.code != http.StatusOK || len(listItems(ev)) != 1 {
		t.Fatalf("events = %d items=%d %s", ev.code, len(listItems(ev)), ev.raw)
	}
	row := listItems(ev)[0].(map[string]any)
	if row["state"] != stateDown || row["prev_state"] != stateUnknown || row["cause"] != causeReport {
		t.Errorf("event row wrong: %v", row)
	}

	// A down finding was emitted on the bus.
	h.waitBus()
	if h.countFindings(busDown) != 1 {
		t.Errorf("want exactly one %s finding; got %d", busDown, h.countFindings(busDown))
	}

	// RECOVER: a healthy report resolves the incident and emits recovered.
	rr := h.report(editor, tenant, id, "healthy", 50, "ok")
	if rr.code != http.StatusOK || rr.body["state"] != stateHealthy {
		t.Fatalf("healthy report = %d %s", rr.code, rr.raw)
	}
	gi := h.do("GET", "/v1/m/health/incidents/"+incID, editor, nil, tenantHdr(tenant))
	if gi.code != http.StatusOK || gi.body["state"] != "resolved" || strOf(gi.body["resolved_at"]) == "" {
		t.Fatalf("incident must be resolved with resolved_at; got %s", gi.raw)
	}
	if openInc := h.do("GET", "/v1/m/health/incidents?state=open", editor, nil, tenantHdr(tenant)); len(listItems(openInc)) != 0 {
		t.Errorf("no incident should remain open after recovery; got %d", len(listItems(openInc)))
	}
	h.waitBus()
	if h.countFindings(busRecovered) != 1 {
		t.Errorf("want exactly one %s finding; got %d", busRecovered, h.countFindings(busRecovered))
	}
	// last_seen must have advanced on the healthy signal.
	if g := h.do("GET", "/v1/m/health/checks/"+id, editor, nil, tenantHdr(tenant)); strOf(g.body["last_seen_at"]) == "" {
		t.Errorf("healthy report must advance last_seen_at; got %s", g.raw)
	}
}

// TestStalenessSweep verifies the proactive engine: an active check whose subject
// goes silent past interval*grace is degraded by the sweep, then down past
// interval*grace*downMultiple, opening an incident and emitting health_subject_down
// — and that a FRESH check is never spuriously "recovered" by the sweep.
func TestStalenessSweep(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	// interval=10s, grace=2 → degraded after 20s, down after 60s (downMultiple=3).
	cr := h.createCheck(editor, tenant, map[string]any{
		"subject_kind": "mcp", "subject_ref": "silent.mcp",
		"expected_interval_seconds": 10, "grace_factor": 2,
	})
	id := strOf(cr.body["id"])

	// A fresh check (created at clock now): a sweep run immediately must NOT change
	// it — and must never invent a "recovered" finding for a never-healthy subject.
	if err := h.mod.sweepTenant(context.Background(), tenant); err != nil {
		t.Fatalf("sweep fresh: %v", err)
	}
	if g := h.do("GET", "/v1/m/health/checks/"+id, editor, nil, tenantHdr(tenant)); g.body["state"] != stateUnknown {
		t.Errorf("fresh check must stay unknown after a sweep; got %v", g.body["state"])
	}
	h.waitBus()
	if n := h.countFindings(busRecovered); n != 0 {
		t.Errorf("a fresh check must never emit a spurious recovered finding; got %d", n)
	}

	// Advance 25s → past degraded threshold (20s), below down (60s).
	h.clk.advance(25 * time.Second)
	if err := h.mod.sweepTenant(context.Background(), tenant); err != nil {
		t.Fatalf("sweep degraded: %v", err)
	}
	if g := h.do("GET", "/v1/m/health/checks/"+id, editor, nil, tenantHdr(tenant)); g.body["state"] != stateDegraded {
		t.Fatalf("want degraded after 25s silence; got %v", g.body["state"])
	}
	h.waitBus()
	if h.countFindings(busDegraded) != 1 {
		t.Errorf("want one degraded finding; got %d", h.countFindings(busDegraded))
	}

	// Escalate to down. Staleness is anchored to LIVENESS (last_seen→created_at), NOT
	// last_checked — so although the degraded sweep at +25s advanced last_checked, the
	// down threshold (downAfter=60s) is still measured from the original created_at. At
	// 65s of total silence (≥60s) the subject is DOWN. (A last_checked-anchored baseline
	// would leave it merely degraded here, so this pins the escalation-clock-reset fix.)
	h.clk.advance(40 * time.Second) // now +65s of total silence
	if err := h.mod.sweepTenant(context.Background(), tenant); err != nil {
		t.Fatalf("sweep down: %v", err)
	}
	if g := h.do("GET", "/v1/m/health/checks/"+id, editor, nil, tenantHdr(tenant)); g.body["state"] != stateDown {
		t.Fatalf("want down after 65s silence; got %v", g.body["state"])
	}
	// One OPEN incident, escalated from degraded to down in place (not two).
	inc := h.do("GET", "/v1/m/health/incidents?subject_ref=silent.mcp", editor, nil, tenantHdr(tenant))
	if len(listItems(inc)) != 1 {
		t.Fatalf("want exactly one incident (escalated in place); got %d %s", len(listItems(inc)), inc.raw)
	}
	row := listItems(inc)[0].(map[string]any)
	if row["state"] != "open" || row["kind"] != stateDown {
		t.Errorf("incident must be open+down after escalation; got %v", row)
	}
	h.waitBus()
	if h.countFindings(busDown) != 1 {
		t.Errorf("want one down finding from the sweep; got %d", h.countFindings(busDown))
	}

	// Idempotency: another sweep at the same instant is a no-op (no new findings).
	if err := h.mod.sweepTenant(context.Background(), tenant); err != nil {
		t.Fatalf("sweep idempotent: %v", err)
	}
	h.waitBus()
	if h.countFindings(busDown) != 1 || h.countFindings(busDegraded) != 1 {
		t.Errorf("a repeat sweep must not re-fire transitions; down=%d degraded=%d",
			h.countFindings(busDown), h.countFindings(busDegraded))
	}
}

// TestLivenessFromEdge verifies the passive liveness path: an edge.observed event
// naming a tracked MCP subject refreshes the check's last_seen, recovers a down
// subject to healthy (emitting recovered), and records a dependency edge. Liveness
// only refreshes a check that EXISTS.
func TestLivenessFromEdge(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	cr := h.createCheck(editor, tenant, map[string]any{"subject_kind": "mcp", "subject_ref": "github.mcp"})
	id := strOf(cr.body["id"])

	// Drive it down first via a report so the edge can recover it.
	if r := h.report(editor, tenant, id, "down", 0, "down"); r.code != http.StatusOK {
		t.Fatalf("seed down = %d %s", r.code, r.raw)
	}

	// An agent session touches the MCP server: positive liveness evidence.
	at := h.clk.now().Add(time.Second)
	h.publishEdge(tenant, sdkmodel.EdgeObservation{
		OriginKind: "agent", OriginRef: "agent-42",
		ResourceKind: "mcp.server", ResourceRef: "github.mcp",
		Mode: sdkmodel.ModeRead, ObservedAt: at,
	})

	// Async: wait for WHAT THE ASSERTIONS BELOW DEPEND ON, not for the first observable
	// sign that something happened. The state flips to healthy as soon as the check row is
	// updated, but the `recovered` finding travels the bus afterwards — so a barrier that
	// stops at the state was satisfied by an earlier event than the one being asserted, and
	// lost the race under load. Fourth test of this class: the sweep that fixed
	// TestHealthForwardOnlyOrdering found three and this one was not among them.
	h.waitUntil("edge to recover the subject AND publish its recovered finding", func() bool {
		g := h.do("GET", "/v1/m/health/checks/"+id, editor, nil, tenantHdr(tenant))
		return g.body["state"] == stateHealthy && h.countFindings(busRecovered) >= 1
	})
	g := h.do("GET", "/v1/m/health/checks/"+id, editor, nil, tenantHdr(tenant))
	if strOf(g.body["last_seen_at"]) == "" {
		t.Errorf("edge liveness must advance last_seen_at; got %s", g.raw)
	}
	if h.countFindings(busRecovered) < 1 {
		t.Errorf("a down→healthy edge recovery must emit recovered; got %d", h.countFindings(busRecovered))
	}

	// A dependency edge (agent uses_mcp github.mcp) was recorded, with both nodes.
	dep := h.do("GET", "/v1/m/health/dependencies", editor, nil, tenantHdr(tenant))
	if dep.code != http.StatusOK {
		t.Fatalf("dependencies = %d %s", dep.code, dep.raw)
	}
	edges, _ := dep.body["edges"].([]any)
	nodes, _ := dep.body["nodes"].([]any)
	if len(edges) != 1 {
		t.Fatalf("want one dependency edge; got %d %s", len(edges), dep.raw)
	}
	e0 := edges[0].(map[string]any)
	if e0["source"] != "agent-42" || e0["target"] != "github.mcp" || e0["relation"] != relUsesMCP {
		t.Errorf("dependency edge wrong: %v", e0)
	}
	if len(nodes) != 2 {
		t.Errorf("want two nodes (agent-42, github.mcp); got %d", len(nodes))
	}
	// The github.mcp node must be annotated healthy (the check tracks it).
	for _, n := range nodes {
		nm := n.(map[string]any)
		if nm["ref"] == "github.mcp" && nm["health"] != stateHealthy {
			t.Errorf("github.mcp node should be annotated healthy; got %v", nm["health"])
		}
	}
}

// TestLivenessOnlyForExistingCheck confirms an edge for an UNTRACKED subject records
// the dependency edge but creates no check and no health transition.
func TestLivenessOnlyForExistingCheck(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	h.publishEdge(tenant, sdkmodel.EdgeObservation{
		OriginKind: "agent", OriginRef: "loner",
		ResourceKind: "mcp.server", ResourceRef: "untracked.mcp",
		Mode: sdkmodel.ModeRead, ObservedAt: h.clk.now(),
	})
	// Wait for the dependency edge to land.
	h.waitUntil("dependency edge for untracked subject", func() bool {
		dep := h.do("GET", "/v1/m/health/dependencies", editor, nil, tenantHdr(tenant))
		edges, _ := dep.body["edges"].([]any)
		return len(edges) == 1
	})
	// No check was created for the untracked subject.
	lr := h.do("GET", "/v1/m/health/checks", editor, nil, tenantHdr(tenant))
	if len(listItems(lr)) != 0 {
		t.Errorf("an edge must not create a check; got %d checks", len(listItems(lr)))
	}
	// the dependency-map nodes for the subjects this edge proved alive read
	// "observed" — seen alive, health NOT measured — never the silent "unknown" that
	// would be indistinguishable from a never-seen subject, and never a fabricated
	// "healthy" (no check ever signaled). The touched MCP server and the agent that
	// acted are both observed; the honest state is a read-time annotation only.
	dep := h.do("GET", "/v1/m/health/dependencies", editor, nil, tenantHdr(tenant))
	nodeHealth := map[string]string{}
	nodes, _ := dep.body["nodes"].([]any)
	for _, n := range nodes {
		nm := n.(map[string]any)
		nodeHealth[strOf(nm["ref"])] = strOf(nm["health"])
	}
	if nodeHealth["untracked.mcp"] != stateObserved {
		t.Errorf("touched-but-untracked untracked.mcp must be %q; got %q", stateObserved, nodeHealth["untracked.mcp"])
	}
	if nodeHealth["loner"] != stateObserved {
		t.Errorf("acting-but-untracked agent loner must be %q; got %q", stateObserved, nodeHealth["loner"])
	}
	if nodeHealth["untracked.mcp"] == stateHealthy || nodeHealth["loner"] == stateHealthy {
		t.Errorf("observed-alive nodes must NOT be fabricated as healthy (no declared check); got %v", nodeHealth)
	}
	// No health transition findings at all.
	h.waitBus()
	if got := len(h.deliveredFindings()); got != 0 {
		t.Errorf("an untracked-subject edge must emit no findings; got %d", got)
	}
}

// TestSLAReport drives a measured down period through the event ledger and verifies
// GET /sla reconstructs downtime and uptime_ppm from it.
func TestSLAReport(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	cr := h.createCheck(editor, tenant, map[string]any{
		"subject_kind": "mcp", "subject_ref": "sla.mcp", "sla_target_ppm": 999000,
	})
	id := strOf(cr.body["id"])

	// healthy@t0, down@t0+10s, healthy@t0+40s → 30s of downtime inside the window.
	if r := h.report(editor, tenant, id, "healthy", 10, ""); r.code != http.StatusOK {
		t.Fatalf("healthy0 = %d %s", r.code, r.raw)
	}
	h.clk.advance(10 * time.Second)
	if r := h.report(editor, tenant, id, "down", 0, "boom"); r.code != http.StatusOK {
		t.Fatalf("down = %d %s", r.code, r.raw)
	}
	h.clk.advance(30 * time.Second)
	if r := h.report(editor, tenant, id, "healthy", 12, ""); r.code != http.StatusOK {
		t.Fatalf("healthy1 = %d %s", r.code, r.raw)
	}

	// Query a 1-hour window. Uptime is measured over the OBSERVED span (first event
	// to now = 40s), NOT the full 3600s window — the pre-history "unknown" lead-in is
	// excluded so it cannot inflate the figure and mask the breach. 30s down out of
	// 40s observed → uptime = floor(1e6 * (40-30)/40) = 250000 ppm (25%).
	sla := h.do("GET", "/v1/m/health/sla?subject_kind=mcp&subject_ref=sla.mcp&window_seconds=3600", editor, nil, tenantHdr(tenant))
	if sla.code != http.StatusOK {
		t.Fatalf("sla = %d %s", sla.code, sla.raw)
	}
	if got := intOf(sla.body["downtime_seconds"]); got != 30 {
		t.Errorf("downtime must be 30s; got %d (%s)", got, sla.raw)
	}
	if got := intOf(sla.body["observed_seconds"]); got != 40 {
		t.Errorf("observed_seconds must be 40 (first event to now); got %d (%s)", got, sla.raw)
	}
	if !boolOf(sla.body["has_data"]) {
		t.Errorf("has_data must be true with history; got %s", sla.raw)
	}
	if !boolOf(sla.body["has_check"]) || intOf(sla.body["sla_target_ppm"]) != 999000 {
		t.Errorf("sla DTO must carry the check target; got %s", sla.raw)
	}
	if got := intOf(sla.body["uptime_ppm"]); got != 250000 {
		t.Errorf("uptime_ppm must be 250000 (30s down of 40s observed); got %d (%s)", got, sla.raw)
	}
	// 250000 < 999000 target → breaching is true and current state healthy.
	if !boolOf(sla.body["breaching"]) {
		t.Errorf("uptime below target must report breaching=true; got %s", sla.raw)
	}
	if sla.body["current_state"] != stateHealthy {
		t.Errorf("current_state must reflect the check's healthy state; got %v", sla.body["current_state"])
	}

	// Missing required params → 400.
	if r := h.do("GET", "/v1/m/health/sla?subject_kind=mcp", editor, nil, tenantHdr(tenant)); r.code != http.StatusBadRequest {
		t.Errorf("sla without subject_ref = %d, want 400", r.code)
	}
}

// TestSLABreachFindingViaSweep verifies the SLA-breach finding fires exactly once
// when the trailing-window uptime drops below the target during a sweep, and is
// de-duplicated by the sticky sla_breach_open flag on subsequent sweeps.
func TestSLABreachFindingViaSweep(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	// A short SLA window so a small down period breaches; high target so any
	// downtime breaches. Long interval so the staleness sweep does not also mark it
	// down (we only want the SLA path here).
	mod := h.mod
	mod.slaWindow = 100 * time.Second

	cr := h.createCheck(editor, tenant, map[string]any{
		"subject_kind": "agent", "subject_ref": "sla-agent",
		"expected_interval_seconds": 100000, "sla_target_ppm": 999999,
	})
	id := strOf(cr.body["id"])

	// healthy then a 20s down period then healthy → ~20% downtime in a 100s window.
	if r := h.report(editor, tenant, id, "healthy", 5, ""); r.code != http.StatusOK {
		t.Fatalf("healthy0 = %d %s", r.code, r.raw)
	}
	h.clk.advance(5 * time.Second)
	if r := h.report(editor, tenant, id, "down", 0, ""); r.code != http.StatusOK {
		t.Fatalf("down = %d %s", r.code, r.raw)
	}
	h.clk.advance(20 * time.Second)
	if r := h.report(editor, tenant, id, "healthy", 5, ""); r.code != http.StatusOK {
		t.Fatalf("healthy1 = %d %s", r.code, r.raw)
	}

	// Sweep: the trailing window now has ~20s of downtime, below the 99.9999% target.
	if err := mod.sweepTenant(context.Background(), tenant); err != nil {
		t.Fatalf("sweep breach: %v", err)
	}
	h.waitBus()
	if h.countFindings(busSLABreach) != 1 {
		t.Fatalf("want exactly one SLA-breach finding; got %d", h.countFindings(busSLABreach))
	}
	// The check's sla_breach_open flag is now sticky.
	if g := h.do("GET", "/v1/m/health/checks/"+id, editor, nil, tenantHdr(tenant)); !boolOf(g.body["sla_breach_open"]) {
		t.Errorf("sla_breach_open must be set after a breach; got %s", g.raw)
	}

	// A second sweep at the same instant must NOT re-alert (de-dup).
	if err := mod.sweepTenant(context.Background(), tenant); err != nil {
		t.Fatalf("sweep repeat: %v", err)
	}
	h.waitBus()
	if h.countFindings(busSLABreach) != 1 {
		t.Errorf("SLA breach must not re-fire while open; got %d", h.countFindings(busSLABreach))
	}
}

// TestTenantIsolation proves a check created in tenant A is invisible from tenant B.
func TestTenantIsolation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")
	edA := h.roleToken(admin, tenantA, "a@x.io", "editor")
	edB := h.roleToken(admin, tenantB, "b@x.io", "editor")

	cr := h.createCheck(edA, tenantA, map[string]any{"subject_kind": "mcp", "subject_ref": "secret.mcp"})
	id := strOf(cr.body["id"])

	// B cannot GET A's check by id (cross-tenant read is a 404, not a leak).
	if r := h.do("GET", "/v1/m/health/checks/"+id, edB, nil, tenantHdr(tenantB)); r.code != http.StatusNotFound {
		t.Errorf("cross-tenant get = %d, want 404", r.code)
	}
	// B's check list is empty.
	if r := h.do("GET", "/v1/m/health/checks", edB, nil, tenantHdr(tenantB)); len(listItems(r)) != 0 {
		t.Errorf("tenant B must see no checks; got %d", len(listItems(r)))
	}
	// A still sees its own.
	if r := h.do("GET", "/v1/m/health/checks", edA, nil, tenantHdr(tenantA)); len(listItems(r)) != 1 {
		t.Errorf("tenant A must see its own check; got %d", len(listItems(r)))
	}
}

// TestRBAC asserts the permission tiers: a viewer reads but cannot write; an editor
// writes but cannot delete; an admin can delete.
func TestRBAC(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")
	adminTok := h.roleToken(admin, tenant, "a@x.io", "admin")

	// viewer can GET.
	if r := h.do("GET", "/v1/m/health/checks", viewer, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("viewer GET checks = %d, want 200", r.code)
	}
	// viewer cannot POST a check (write-tier).
	if r := h.do("POST", "/v1/m/health/checks", viewer, map[string]any{
		"subject_kind": "mcp", "subject_ref": "x.mcp",
	}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("viewer POST = %d, want 403", r.code)
	}
	// editor can POST.
	cr := h.createCheck(editor, tenant, map[string]any{"subject_kind": "mcp", "subject_ref": "y.mcp"})
	id := strOf(cr.body["id"])
	// editor cannot DELETE (admin-tier).
	if r := h.do("DELETE", "/v1/m/health/checks/"+id, editor, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("editor DELETE = %d, want 403", r.code)
	}
	// admin can DELETE.
	if r := h.do("DELETE", "/v1/m/health/checks/"+id, adminTok, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("admin DELETE = %d, want 204 %s", r.code, r.raw)
	}
}

// TestDependencyMapGraph builds a small dependency graph from several edge kinds and
// verifies the React-Flow node+edge contract, including a mix of tracked (annotated
// with health) and untracked (annotated unknown) nodes.
func TestDependencyMapGraph(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	// Track the MCP server so its node is health-annotated; leave the agent untracked.
	h.createCheck(editor, tenant, map[string]any{"subject_kind": "mcp", "subject_ref": "db.mcp"})

	// uses_mcp edge.
	h.publishEdge(tenant, sdkmodel.EdgeObservation{
		OriginKind: "agent", OriginRef: "worker-1",
		ResourceKind: "mcp.server", ResourceRef: "db.mcp", Mode: sdkmodel.ModeRead, ObservedAt: h.clk.now(),
	})
	// delegates_to edge (Task).
	h.publishEdge(tenant, sdkmodel.EdgeObservation{
		OriginKind: "agent", OriginRef: "worker-1",
		ResourceKind: "agent.task", ResourceRef: "worker-2", ToolRef: "Task", Mode: sdkmodel.ModeRead, ObservedAt: h.clk.now(),
	})

	h.waitUntil("two dependency edges", func() bool {
		dep := h.do("GET", "/v1/m/health/dependencies", editor, nil, tenantHdr(tenant))
		edges, _ := dep.body["edges"].([]any)
		return len(edges) == 2
	})
	dep := h.do("GET", "/v1/m/health/dependencies", editor, nil, tenantHdr(tenant))
	if dep.code != http.StatusOK {
		t.Fatalf("dependencies = %d %s", dep.code, dep.raw)
	}
	nodes, _ := dep.body["nodes"].([]any)
	// Distinct refs: worker-1, db.mcp, worker-2 → 3 nodes.
	if len(nodes) != 3 {
		t.Fatalf("want 3 distinct nodes; got %d %s", len(nodes), dep.raw)
	}
	health := map[string]string{}
	for _, n := range nodes {
		nm := n.(map[string]any)
		health[strOf(nm["ref"])] = strOf(nm["health"])
	}
	// db.mcp is tracked AND the uses_mcp edge proved it alive → its check is now
	// healthy (liveness refresh), so the node is annotated healthy.
	if health["db.mcp"] != stateHealthy {
		t.Errorf("tracked db.mcp touched by an edge must be healthy; got %q", health["db.mcp"])
	}
	// worker-1 is the agent ORIGIN of both edges: proven alive, no declared check →
	// "observed" (seen alive, health not measured), NOT the silent "unknown" that
	// would be indistinguishable from a never-seen subject.
	if health["worker-1"] != stateObserved {
		t.Errorf("untracked-but-observed-alive worker-1 must be %q; got %q", stateObserved, health["worker-1"])
	}
	// worker-2 is only ever a delegate-to TARGET — named, never proven alive, no
	// check → "unknown". The honest split: observed != unknown != healthy.
	if health["worker-2"] != stateUnknown {
		t.Errorf("named-only worker-2 must be unknown; got %q", health["worker-2"])
	}
	if health["worker-1"] == health["worker-2"] {
		t.Errorf("observed-alive worker-1 must differ from named-only worker-2; both %q", health["worker-1"])
	}

	relations := map[string]bool{}
	edges, _ := dep.body["edges"].([]any)
	for _, e := range edges {
		relations[strOf(e.(map[string]any)["relation"])] = true
	}
	if !relations[relUsesMCP] || !relations[relDelegatesTo] {
		t.Errorf("want uses_mcp and delegates_to relations; got %v", relations)
	}
}

// TestDeriveStaleStateBaselines is a focused unit test of the staleness derivation
// over its three baselines (last_seen > last_checked > created_at) and thresholds,
// independent of the store.
func TestDeriveStaleStateBaselines(t *testing.T) {
	h := newHarness(t)
	mod := h.mod
	base := h.clk.now()

	// interval=10, grace=2 → degradedAfter=20s, downAfter=60s (downMultiple=3).
	mk := func(set func(model.Record)) model.Record {
		rec := model.Record{
			colExpectedIvl: int64(10), colGraceFactor: int64(2),
			model.ColCreatedAt: model.NewTimestamp(base).String(),
		}
		set(rec)
		return rec
	}

	// Fresh from created_at: 5s old → no change.
	if s := mod.deriveStaleState(mk(func(model.Record) {}), base.Add(5*time.Second)); s != "" {
		t.Errorf("5s old must be no-change; got %q", s)
	}
	// 25s old → degraded.
	if s := mod.deriveStaleState(mk(func(model.Record) {}), base.Add(25*time.Second)); s != stateDegraded {
		t.Errorf("25s old must be degraded; got %q", s)
	}
	// 65s old → down.
	if s := mod.deriveStaleState(mk(func(model.Record) {}), base.Add(65*time.Second)); s != stateDown {
		t.Errorf("65s old must be down; got %q", s)
	}
	// last_seen takes precedence over created_at: a recent last_seen keeps it fresh.
	rec := mk(func(r model.Record) {
		r[colLastSeenAt] = model.NewTimestamp(base.Add(60 * time.Second)).String()
	})
	if s := mod.deriveStaleState(rec, base.Add(65*time.Second)); s != "" {
		t.Errorf("recent last_seen (5s ago) must be no-change despite old created_at; got %q", s)
	}
	// last_checked does NOT reset the escalation clock: a never-seen subject with a
	// recent last_checked (e.g. set by the prior degraded sweep) must still escalate
	// to DOWN at downAfter from created_at — staleness is anchored to liveness, not to
	// when the sweep last looked. (Under a last_checked-anchored baseline this would be
	// merely degraded, which is the bug this asserts is fixed.)
	stale := mk(func(r model.Record) {
		r[colLastChecked] = model.NewTimestamp(base.Add(25 * time.Second)).String()
	})
	if s := mod.deriveStaleState(stale, base.Add(65*time.Second)); s != stateDown {
		t.Errorf("recent last_checked must NOT reset escalation; want down at 65s from created_at, got %q", s)
	}
}

// TestReportValidation checks the report endpoint rejects an invalid state and that
// a report on a missing check is a 404.
func TestReportValidation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	cr := h.createCheck(editor, tenant, map[string]any{"subject_kind": "mcp", "subject_ref": "v.mcp"})
	id := strOf(cr.body["id"])

	// "unknown" is not a postable probe state.
	if r := h.report(editor, tenant, id, "unknown", 0, ""); r.code != http.StatusBadRequest {
		t.Errorf("report state=unknown = %d, want 400", r.code)
	}
	// report on a non-existent check.
	if r := h.report(editor, tenant, model.NewID().String(), "down", 0, ""); r.code != http.StatusNotFound {
		t.Errorf("report on missing check = %d, want 404", r.code)
	}
}

// TestHealthStatusMirrorForCoreID verifies the mirror into the core HealthStatus
// entity when a subject ref IS a core entity id (so other planes can read an
// agent's health via Scope.Health()).
func TestHealthStatusMirrorForCoreID(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	subjectID := model.NewID()
	cr := h.createCheck(editor, tenant, map[string]any{"subject_kind": "agent", "subject_ref": subjectID.String()})
	id := strOf(cr.body["id"])

	if r := h.report(editor, tenant, id, "down", 0, "boom"); r.code != http.StatusOK {
		t.Fatalf("down report = %d %s", r.code, r.raw)
	}

	// The core HealthStatus row mirrors the down state.
	var found model.HealthState
	h.view(tenant, func(sc store.Scope) error {
		existing, _, err := sc.Health().List(context.Background(), model.Query{
			Filters: []model.Filter{
				{Column: "subject_kind", Op: model.OpEq, Value: "agent"},
				{Column: "subject_id", Op: model.OpEq, Value: subjectID.String()},
			}, Limit: 1,
		})
		if err != nil {
			return err
		}
		if len(existing) == 1 {
			found = existing[0].State
		}
		return nil
	})
	if found != model.HealthDown {
		t.Errorf("core HealthStatus must mirror down for a core-id subject; got %q", found)
	}
}
