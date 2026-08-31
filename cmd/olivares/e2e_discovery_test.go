// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// Discovery layer (modules I, II, V, XXII): the fleet inventory, the live
// operation view, the dependency/health map and the capabilities catalog — all
// materialized from the SAME seed observation stream the access graph is built
// from, proving one connector stream fans out to many modules through the bus.

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/seed"
)

func TestE2E_Inventory_FleetDiscovered(t *testing.T) {
	h := newHarness(t)
	sum := h.getJSON(h.adminToken, h.tenantA, "/v1/m/inventory/summary")
	byKind, _ := sum["by_kind"].(map[string]any)
	if byKind == nil {
		t.Fatalf("no by_kind in summary: %v", sum)
	}
	total := func(kind string) float64 {
		k, _ := byKind[kind].(map[string]any)
		if k == nil {
			return 0
		}
		n, _ := k["total"].(float64)
		return n
	}
	// Three cooperative agents, two live sessions, and ≥1 identity/tool/model/provider.
	if total("agent") < 3 {
		t.Errorf("agent total = %v, want >=3", total("agent"))
	}
	if total("session") < 2 {
		t.Errorf("session total = %v, want >=2", total("session"))
	}
	for _, k := range []string{"identity", "tool", "model", "provider", "mcp_server"} {
		if total(k) < 1 {
			t.Errorf("%s total = %v, want >=1", k, total(k))
		}
	}
	// by_source carries the real signal-source provenance, not a collapsed blob.
	bySrc, _ := sum["by_source"].(map[string]any)
	for _, want := range []string{"pg_audit", "otel", "cost"} {
		if _, ok := bySrc[want]; !ok {
			t.Errorf("by_source missing %q (have %v)", want, bySrc)
		}
	}

	// The coder agent is a navigable catalog row with provenance.
	list := h.getJSON(h.adminToken, h.tenantA, "/v1/m/inventory/entities?kind=agent")
	var found map[string]any
	for _, e := range items(list) {
		if e["name"] == seed.AgentCoder {
			found = e
		}
	}
	if found == nil {
		t.Fatalf("coder agent not in inventory catalog")
	}
	assertEq(t, "coder.status", found["status"], "active")
	if ss, _ := found["signal_sources"].([]any); len(ss) == 0 {
		t.Error("coder agent has no signal_sources")
	}
}

func TestE2E_Sessions_LiveOperation(t *testing.T) {
	h := newHarness(t)
	live := h.getJSON(h.adminToken, h.tenantA, "/v1/m/sessions/live/"+seed.SessionLive)

	assertEq(t, "current_action", live["current_action"], seed.ToolCreateIss)
	assertEq(t, "current_resource", live["current_resource"], seed.MCPGitHub+"/"+seed.ToolCreateIss)
	assertEq(t, "current_mode", live["current_mode"], "write")
	assertEq(t, "cc_state", live["cc_state"], "active")
	assertEq(t, "model_ref", live["model_ref"], "claude-opus-4-8")
	assertEq(t, "input_tokens", live["input_tokens"], float64(1200))
	assertEq(t, "output_tokens", live["output_tokens"], float64(800))
	assertEq(t, "cost_micro_usd", live["cost_micro_usd"], float64(42000))
	if tc, _ := live["tool_call_count"].(float64); tc < 1 {
		t.Errorf("tool_call_count = %v, want >=1", live["tool_call_count"])
	}

	// The timeline reconstructs tool + cost events for the session.
	tl := h.getJSON(h.adminToken, h.tenantA, "/v1/m/sessions/live/"+seed.SessionLive+"/timeline?limit=50")
	kinds := map[string]bool{}
	for _, ev := range items(tl) {
		if k, ok := ev["kind"].(string); ok {
			kinds[k] = true
		}
	}
	if !kinds["cost"] {
		t.Errorf("timeline missing a cost event (have %v)", kinds)
	}
	if !kinds["tool"] && !kinds["mcp"] {
		t.Errorf("timeline missing a tool/mcp event (have %v)", kinds)
	}

	// The anti-evasion correlation flips the evade session to silent_evasion.
	evade := h.getJSON(h.adminToken, h.tenantA, "/v1/m/sessions/live/"+seed.SessionEvade)
	assertEq(t, "evade.cc_state", evade["cc_state"], "silent_evasion")
}

func TestE2E_Health_DependencyMapAndChecks(t *testing.T) {
	h := newHarness(t)

	// The dependency map auto-discovers the GitHub MCP usage from the bus (no probe).
	dep := h.getJSON(h.adminToken, h.tenantA, "/v1/m/health/dependencies?limit=100")
	edges := items2(dep, "edges")
	var usesMCP map[string]any
	for _, e := range edges {
		if e["relation"] == "uses_mcp" && e["target"] == seed.MCPGitHub {
			usesMCP = e
		}
	}
	if usesMCP == nil {
		t.Fatalf("no uses_mcp→github dependency edge (edges=%v)", edges)
	}

	// A declared check + a healthy probe report drives the status grid.
	var chk struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/m/health/checks", h.adminToken, h.tenantA, map[string]any{
		"subject_kind": "mcp", "subject_ref": seed.MCPGitHub,
		"expected_interval_seconds": 60, "grace_factor": 2, "sla_target_ppm": 999000,
	}, &chk); code != http.StatusCreated || chk.ID == "" {
		t.Fatalf("create check = %d id=%q", code, chk.ID)
	}
	if code, raw := h.req("POST", "/v1/m/health/checks/"+chk.ID+"/report", h.adminToken, h.tenantA, map[string]any{
		"state": "healthy", "latency_ms": 42,
	}); code != http.StatusOK {
		t.Fatalf("report = %d: %s", code, raw)
	}
	st := h.getJSON(h.adminToken, h.tenantA, "/v1/m/health/status?subject_kind=mcp")
	var github map[string]any
	for _, s := range items(st) {
		if s["subject_ref"] == seed.MCPGitHub {
			github = s
		}
	}
	if github == nil {
		t.Fatal("github check not in status grid")
	}
	assertEq(t, "github.state", github["state"], "healthy")
}

func TestE2E_Capabilities_McpCatalogAndWiring(t *testing.T) {
	h := newHarness(t)

	servers := h.getJSON(h.adminToken, h.tenantA, "/v1/m/capabilities/servers?limit=100")
	var github map[string]any
	for _, s := range items(servers) {
		if s["name"] == seed.MCPGitHub {
			github = s
		}
	}
	if github == nil {
		t.Fatal("github MCP server not in capabilities catalog")
	}
	// The cooperative connection signal marks it connected (derived, forward-only).
	assertEq(t, "github.connection", github["connection"], "connected")
	if tc, _ := github["tool_count"].(float64); tc < 1 {
		t.Errorf("github tool_count = %v, want >=1", github["tool_count"])
	}

	// The wiring graph captures session→tool capability edges (distinct from R/RW).
	wiring := h.getJSON(h.adminToken, h.tenantA, "/v1/m/capabilities/wiring")
	wedges := items2(wiring, "edges")
	var toolWire map[string]any
	for _, e := range wedges {
		if e["origin_ref"] == seed.SessionLive && e["capability_ref"] == seed.MCPGitHub+"/"+seed.ToolCreateIss {
			toolWire = e
		}
	}
	if toolWire == nil {
		t.Errorf("no session→create_issue wiring edge (edges=%v)", wedges)
	}

	// The tool's MCP annotations are surfaced as UNTRUSTED, never as enforcement.
	id, _ := github["id"].(string)
	detail := h.getJSON(h.adminToken, h.tenantA, "/v1/m/capabilities/servers/"+id)
	tools := items2(detail, "tools")
	if len(tools) == 0 {
		t.Fatal("github server detail has no tools")
	}
	for _, tl := range tools {
		assertEq(t, "tool.annotation_trust", tl["annotation_trust"], "untrusted")
	}
}
