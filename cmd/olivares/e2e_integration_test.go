// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// Integration through contracts: a single connector observation, published once
// on the bus, fans out to many modules that never import one another — the
// decoupled-by-events architecture (ARCHITECTURE.md). Plus the cost stream lighting up
// FinOps + the model catalog, and a delegation edge lighting up orchestration.

import (
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/seed"
)

// TestE2E_EventFanout_OneEdgeManyModules proves the GitHub MCP server — discovered
// from ONE session→mcp.server edge.observed event — is visible in four independent
// module views, none of which call each other.
func TestE2E_EventFanout_OneEdgeManyModules(t *testing.T) {
	h := newHarness(t)
	has := func(path, key, ref string) bool {
		m := h.getJSON(h.adminToken, h.tenantA, path)
		for _, it := range items2map(m, key) {
			if it[fieldFor(key)] == ref {
				return true
			}
		}
		return false
	}

	// 1) access-map: the edge itself.
	g := h.getJSON(h.adminToken, h.tenantA, "/v1/m/accessmap/graph?limit=200")
	if edgeByResource(items2(g, "edges"), seed.MCPGitHub) == nil {
		t.Error("access-map: github edge missing")
	}
	// 2) inventory: materialized as an mcp_server catalog row.
	if !invHasName(h, "mcp_server", seed.MCPGitHub) {
		t.Error("inventory: github mcp_server missing")
	}
	// 3) capabilities: enriched server.
	if !has("/v1/m/capabilities/servers?limit=100", "items", seed.MCPGitHub) {
		t.Error("capabilities: github server missing")
	}
	// 4) health: an auto-discovered dependency target.
	dep := h.getJSON(h.adminToken, h.tenantA, "/v1/m/health/dependencies?limit=100")
	depHit := false
	for _, e := range items2(dep, "edges") {
		if e["target"] == seed.MCPGitHub {
			depHit = true
		}
	}
	if !depHit {
		t.Error("health: github dependency missing")
	}
}

func TestE2E_FinOps_CostStreamAndModelCatalog(t *testing.T) {
	h := newHarness(t)

	// Cost samples → FinOps spend read-model (money is always integer micro-USD).
	sum := h.getJSON(h.adminToken, h.tenantA, "/v1/m/finops/spend/summary")
	if tot, _ := sum["total_micro_usd"].(float64); tot <= 0 {
		t.Errorf("finops total_micro_usd = %v, want >0", sum["total_micro_usd"])
	}

	spend := h.getJSON(h.adminToken, h.tenantA, "/v1/m/finops/spend?dimension=provider")
	anthropic := false
	for _, b := range items2(spend, "buckets") {
		if b["key"] == "anthropic" {
			if c, _ := b["cost_micro_usd"].(float64); c > 0 {
				anthropic = true
			}
		}
	}
	if !anthropic {
		t.Error("finops: no anthropic provider spend bucket")
	}

	// The model catalog is populated as a side-effect of cost ingestion (foModel).
	models := h.getJSON(h.adminToken, h.tenantA, "/v1/m/models/models")
	got := map[string]bool{}
	for _, m := range items(models) {
		if n, ok := m["name"].(string); ok {
			got[n] = true
		}
	}
	for _, want := range []string{"claude-opus-4-8", "gpt-4o"} {
		if !got[want] {
			t.Errorf("models catalog missing %q (have %v)", want, got)
		}
	}
}

func TestE2E_Orchestration_DelegationRelation(t *testing.T) {
	h := newHarness(t)
	// The session→agent.task(Task) edge derives a supervisor→worker delegation.
	g := h.getJSON(h.adminToken, h.tenantA, "/v1/m/orchestration/graph")
	edges := items2(g, "edges")
	if len(edges) == 0 {
		// Some builds key the relations under a different array; accept either.
		edges = items2(g, "relations")
	}
	if len(edges) == 0 {
		t.Fatalf("orchestration graph empty: %v", g)
	}
	found := false
	for _, e := range edges {
		// The worker reference appears as a node ref on the delegation relation.
		for _, v := range e {
			if s, ok := v.(string); ok && s == "worker-indexer" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("no delegation relation referencing worker-indexer (edges=%v)", edges)
	}
}

// --- small helpers for fan-out assertions ---

func invHasName(h *harness, kind, name string) bool {
	m := h.getJSON(h.adminToken, h.tenantA, "/v1/m/inventory/entities?kind="+kind)
	for _, e := range items(m) {
		if e["name"] == name {
			return true
		}
	}
	return false
}

func items2map(m map[string]any, key string) []map[string]any { return items2(m, key) }

// fieldFor maps a list key to the field that carries the human ref we match on.
func fieldFor(key string) string {
	if key == "items" {
		return "name"
	}
	return "ref"
}
