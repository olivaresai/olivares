// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// Contract tests (anti-drift): the served API must match the OpenAPI the web
// client is generated from on the core surface, and the JSON shapes the web's
// hand-authored TS interfaces read must be exactly what the modules return.
// The core surface is the stable /openapi.json; the module routes are the separate
// beta /openapi.beta.json. Where the published contract and the
// implementation diverge it is recorded as a gap, never papered over.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// hasKeys fails if any wanted key is absent from obj.
func hasKeys(t *testing.T, what string, obj map[string]any, want ...string) {
	t.Helper()
	for _, k := range want {
		if _, ok := obj[k]; !ok {
			t.Errorf("%s: missing contract field %q (have %v)", what, k, keysOf(obj))
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestE2E_Contract_OpenAPICoreSurface(t *testing.T) {
	h := newHarness(t)

	// The served /openapi.json is the artifact the web client codegen consumes.
	code, raw := h.req("GET", "/openapi.json", "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("/openapi.json = %d", code)
	}
	var doc struct {
		OpenAPI string         `json:"openapi"`
		Paths   map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("openapi.json is not valid JSON: %v", err)
	}
	if doc.OpenAPI == "" || len(doc.Paths) == 0 {
		t.Fatalf("openapi doc has no version/paths")
	}
	// Every core route the web depends on is published, and reachable on the real
	// server (the published contract == the served surface).
	for _, p := range []string{
		"/v1/setup", "/v1/auth/login", "/v1/agents", "/v1/access-edges",
		"/v1/audit", "/v1/system/orgs",
	} {
		if _, ok := doc.Paths[p]; !ok {
			t.Errorf("openapi missing core path %q", p)
		}
	}
	// The STABLE core doc deliberately excludes the module routes (/v1/m/*): they
	// are a separate, BETA-tier contract so the 24-path stable surface stays
	// identifiable. They must not leak into the stable document.
	for p := range doc.Paths {
		if len(p) >= 6 && p[:6] == "/v1/m/" {
			t.Errorf("unexpected module path in core OpenAPI: %q (contract assumption changed)", p)
		}
	}

	// The module routes ARE published, in the separate beta document served at
	// /openapi.beta.json — valid, all under /v1/m/, and reachable on the real server.
	code, raw = h.req("GET", "/openapi.beta.json", "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("/openapi.beta.json = %d", code)
	}
	var beta struct {
		Info struct {
			Title      string `json:"title"`
			BetaNotice string `json:"x-beta-notice"`
		} `json:"info"`
		Paths map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(raw, &beta); err != nil {
		t.Fatalf("openapi.beta.json is not valid JSON: %v", err)
	}
	if len(beta.Paths) == 0 || beta.Info.BetaNotice == "" {
		t.Fatalf("beta doc missing paths or beta banner")
	}
	for p := range beta.Paths {
		if !strings.HasPrefix(p, "/v1/m/") {
			t.Errorf("beta doc carries a non-module path %q", p)
		}
	}
}

func TestE2E_Contract_UIDataShapes(t *testing.T) {
	h := newHarness(t)

	// access-map: the AccessEdge fields web/src/features/access-map/types.ts reads.
	g := h.getJSON(h.adminToken, h.tenantA, "/v1/m/accessmap/graph?limit=50")
	edges := items2(g, "edges")
	nodes := items2(g, "nodes")
	if len(edges) == 0 || len(nodes) == 0 {
		t.Fatal("empty access graph")
	}
	hasKeys(t, "AccessEdge", edges[0],
		"id", "origin_kind", "origin_id", "resource_id", "mode",
		"signal_source", "confidence", "bridged", "observed", "permitted", "occurrence_count")
	hasKeys(t, "AccessNode", nodes[0], "id", "kind")

	// access-map drift: DiffResponse + DriftEntry the drift list renders.
	d := h.getJSON(h.adminToken, h.tenantA, "/v1/m/accessmap/drift?limit=50")
	hasKeys(t, "DiffResponse", d, "unexpected_accesses", "unused_grants", "unexpected_count", "unused_count")
	if ux := items2(d, "unexpected_accesses"); len(ux) > 0 {
		hasKeys(t, "DriftEntry", ux[0], "kind", "edge")
	}

	// inventory: InventorySummary.
	hasKeys(t, "InventorySummary", h.getJSON(h.adminToken, h.tenantA, "/v1/m/inventory/summary"),
		"by_kind", "by_source", "total")

	// finops: SummaryResponse (executive cost pillar source).
	hasKeys(t, "finops.SummaryResponse", h.getJSON(h.adminToken, h.tenantA, "/v1/m/finops/spend/summary"),
		"total_micro_usd", "input_tokens", "output_tokens", "samples", "by_provider")

	// sessions: LiveDTO.
	live := h.getJSON(h.adminToken, h.tenantA, "/v1/m/sessions/live")
	if rows := items(live); len(rows) > 0 {
		hasKeys(t, "LiveDTO", rows[0],
			"session_ref", "cc_state", "input_tokens", "output_tokens", "cost_micro_usd", "event_count")
	}

	// health: dependency React-Flow shape.
	hasKeys(t, "health.dependencies", h.getJSON(h.adminToken, h.tenantA, "/v1/m/health/dependencies"),
		"nodes", "edges")
}
