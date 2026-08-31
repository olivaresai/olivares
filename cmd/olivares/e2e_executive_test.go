// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// The executive dashboard (module XXI) is a pure web-side rollup over the same
// module hooks the technical views use — it recomputes nothing (ARCHITECTURE.md). The
// backend E2E therefore proves the FIVE pillar SOURCES return coherent real data,
// and that the cross-module number the dashboard headlines (firm drift) is
// derivable from the access-map diff exactly as the web derives it (derive.ts).

import "testing"

func TestE2E_ExecutiveDashboard_PillarSources(t *testing.T) {
	h := newHarness(t)

	// Cost pillar — FinOps spend.
	cost := h.getJSON(h.adminToken, h.tenantA, "/v1/m/finops/spend/summary")
	if tot, _ := cost["total_micro_usd"].(float64); tot <= 0 {
		t.Errorf("cost pillar: total_micro_usd=%v", cost["total_micro_usd"])
	}

	// Usage pillar — active agents + live sessions.
	inv := h.getJSON(h.adminToken, h.tenantA, "/v1/m/inventory/summary")
	byKind, _ := inv["by_kind"].(map[string]any)
	agent, _ := byKind["agent"].(map[string]any)
	if a, _ := agent["active"].(float64); a <= 0 {
		t.Errorf("usage pillar: active agents=%v", agent["active"])
	}
	if len(items(h.getJSON(h.adminToken, h.tenantA, "/v1/m/sessions/live"))) == 0 {
		t.Error("usage pillar: no live sessions")
	}

	// Risk pillar — the access drift is the headline signal; security anomalies back it.
	drift := h.getJSON(h.adminToken, h.tenantA, "/v1/m/accessmap/drift?limit=200")
	unexpected := items2(drift, "unexpected_accesses")
	pending := 0
	for _, e := range unexpected {
		if p, _ := e["reconciliation_pending"].(bool); p {
			pending++
		}
	}
	firm := len(unexpected) - pending // == derive.ts risk.drift.unexpectedFirm
	if firm <= 0 {
		t.Errorf("risk pillar: firm unexpected accesses=%d (unexpected=%d pending=%d)", firm, len(unexpected), pending)
	}
	if len(items(h.getJSON(h.adminToken, h.tenantA, "/v1/m/security/anomalies"))) == 0 {
		t.Error("risk pillar: no security anomalies despite seeded anti-evasion")
	}

	// Compliance pillar — framework summary.
	if len(items2(h.getJSON(h.adminToken, h.tenantA, "/v1/m/compliance/summary"), "frameworks")) == 0 {
		// Some builds return the frameworks under the top-level list; fall back.
		if len(items(h.getJSON(h.adminToken, h.tenantA, "/v1/m/compliance/frameworks"))) == 0 {
			t.Error("compliance pillar: no frameworks")
		}
	}

	// Reliability pillar — the dependency/health map.
	if len(items2(h.getJSON(h.adminToken, h.tenantA, "/v1/m/health/dependencies"), "edges")) == 0 {
		t.Error("reliability pillar: no health dependencies")
	}
}
