// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import "testing"

func TestClaudeDataGovernanceMatrix(t *testing.T) {
	feats := ClaudeDataGovernance()
	if len(feats) == 0 {
		t.Fatal("data-governance matrix is empty")
	}
	byBeta := map[string]DataGovernanceFeature{}
	for _, f := range feats {
		byBeta[f.BetaID] = f
	}

	// Server-side context editing is ZDR-eligible AND can clear the model's context.
	for _, beta := range []string{BetaContextManagement, BetaClearToolUses, BetaClearThinking} {
		f, ok := byBeta[beta]
		if !ok {
			t.Fatalf("missing feature for beta %q", beta)
		}
		if !f.ZDREligible || !f.ServerClears || f.Surface != SurfaceServer {
			t.Errorf("%s = %+v, want ZDR-eligible server-clearing", beta, f)
		}
		if f.ForensicNote == "" {
			t.Errorf("%s missing forensic note", beta)
		}
	}

	// The memory tool persists CLIENT-side, ZDR-eligible, does not clear context.
	mem := byBeta[BetaMemoryTool]
	if mem.Surface != SurfaceClient || !mem.ZDREligible || mem.ServerClears {
		t.Errorf("memory tool = %+v, want client-side ZDR-eligible non-clearing", mem)
	}

	// The MCP connector is explicitly NOT ZDR-eligible (external boundary).
	var mcp DataGovernanceFeature
	for _, f := range feats {
		if f.Surface == SurfaceExternal {
			mcp = f
		}
	}
	if mcp.ZDREligible {
		t.Errorf("MCP connector must NOT be ZDR-eligible: %+v", mcp)
	}

	// The returned slice is a copy — mutating it must not affect the source.
	feats[0].Feature = "tampered"
	if ClaudeDataGovernance()[0].Feature == "tampered" {
		t.Error("ClaudeDataGovernance must return a defensive copy")
	}
}

func TestContextMayBeServerCleared(t *testing.T) {
	if !ContextMayBeServerCleared([]string{BetaClearThinking}) {
		t.Error("clear_thinking active → context may be server-cleared")
	}
	if !ContextMayBeServerCleared([]string{"other", BetaContextManagement}) {
		t.Error("context-management active → context may be server-cleared")
	}
	// The memory tool does not clear server context; an unknown beta does nothing.
	if ContextMayBeServerCleared([]string{BetaMemoryTool}) {
		t.Error("memory tool does not clear the model's server-side context")
	}
	if ContextMayBeServerCleared([]string{"unknown_beta"}) {
		t.Error("unknown beta must not imply context clearing")
	}
	if ContextMayBeServerCleared(nil) {
		t.Error("no active betas → no clearing")
	}
}
