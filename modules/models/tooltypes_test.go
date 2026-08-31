// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import "testing"

func TestClaudeToolTypesCrossWalkAndExecution(t *testing.T) {
	byID := map[string]ToolType{}
	for _, tt := range ClaudeToolTypes() {
		byID[tt.ID] = tt
	}

	// Server tools bill under their own cost_type and are not ZDR-eligible.
	ws, ok := byID["web_search_20260209"]
	if !ok || ws.Execution != SurfaceServer || ws.CostType != "web_search" || ws.ZDREligible {
		t.Errorf("web_search_20260209 = %+v, want server/web_search/zdr=false", ws)
	}
	ce, ok := byID["code_execution_20260120"]
	if !ok || ce.CostType != "code_execution" {
		t.Errorf("code_execution cost_type = %q, want code_execution", ce.CostType)
	}

	// Client tools execute on the operator host (ZDR-eligible) and bill as tokens.
	comp, ok := byID["computer_20251124"]
	if !ok || comp.Execution != SurfaceClient || !comp.ZDREligible || comp.CostType != "tokens" {
		t.Errorf("computer_20251124 = %+v, want client/zdr=true/tokens", comp)
	}

	// Memory is the documented server-but-ZDR-eligible exception (CLA-15 matrix).
	mem, ok := byID["memory_20250818"]
	if !ok || mem.Execution != SurfaceServer || !mem.ZDREligible {
		t.Errorf("memory_20250818 = %+v, want server/zdr=true (exception)", mem)
	}

	// Every cost_type used must be a valid cost_report cost_type (no invented values).
	valid := map[string]bool{"tokens": true, "web_search": true, "code_execution": true, "session_usage": true}
	for id, tt := range byID {
		if !valid[tt.CostType] {
			t.Errorf("%s has invalid cost_type %q", id, tt.CostType)
		}
	}
}
