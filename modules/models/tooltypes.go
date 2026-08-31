// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

// CLA-08 — dated Anthropic tool-type catalog + cost_type cross-walk.
//
// The capability matrix carries coarse booleans (CapComputerUse, CapMemoryTool…)
// that say a model SUPPORTS a tool but not WHICH dated tool-type version, where it
// EXECUTES (Anthropic server-side vs the agent's client), whether it is ZDR-eligible,
// or how it is BILLED. A senior/security audience needs the exact identifiers: a
// catalog listing "computer use: true" instead of "computer_20251124 (client,
// ZDR-eligible, billed as tokens)" reads as superficial, and the identifiers drift
// quarterly. This file is the declared, AsOf-stamped tool-type table — the
// cost-side/governance companion to the pricing table — and the cross-walk from the
// cost_report cost_type dimension (tokens|web_search|code_execution|session_usage)
// back to the named server tools, so server-tool spend is attributable.
// Source: https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-reference

// toolTypesAsOf stamps the declared tool-type identifiers with when they were
// recorded — the freshness mechanism (identifiers change quarterly; verify against
// the tool reference and override).
const toolTypesAsOf = "2026-06-05"

// ToolType is one dated Anthropic-provided tool type: its exact identifier, family,
// where it executes, ZDR eligibility, and the cost_report cost_type it bills under.
type ToolType struct {
	// ID is the exact dated identifier the API expects (e.g. "web_search_20260209").
	ID string `json:"id"`
	// Family is the version-independent tool family (e.g. "web_search").
	Family string `json:"family"`
	// Execution is "server" (Anthropic-hosted, data leaves the client) or "client"
	// (the agent runs it; data stays on the operator's host).
	Execution DataSurface `json:"execution"`
	// ZDREligible derives from the execution surface: client tools keep data local
	// (ZDR-compatible); server tools send data to Anthropic's tool infrastructure
	// (not ZDR by default) — the memory tool is the documented server-side exception
	// (see claudeDataGovernance / CLA-15). Verify against the ZDR matrix.
	ZDREligible bool `json:"zdr_eligible"`
	// CostType is the cost_report cost_type this tool bills under: "tokens" (priced as
	// model tokens) or a server-tool line ("web_search"|"code_execution"|
	// "session_usage"). It is the cross-walk that attributes billed server-tool spend.
	CostType string `json:"cost_type"`
	// Beta marks a tool-type still gated behind a beta header.
	Beta bool `json:"beta,omitempty"`
}

// claudeToolTypes is the declared, dated Claude tool-type catalog. Both the current
// and prior dated versions are listed so usage of either resolves; operators refresh
// from the tool reference. ZDR follows execution (client=true) except memory
// (server-but-ZDR-eligible, per the CLA-15 matrix).
var claudeToolTypes = []ToolType{
	// Server-executed tools (data leaves the client; billed as their own cost_type).
	{ID: "web_search_20260209", Family: "web_search", Execution: SurfaceServer, ZDREligible: false, CostType: "web_search"},
	{ID: "web_search_20250305", Family: "web_search", Execution: SurfaceServer, ZDREligible: false, CostType: "web_search"},
	{ID: "web_fetch_20260209", Family: "web_fetch", Execution: SurfaceServer, ZDREligible: false, CostType: "web_search"},
	{ID: "web_fetch_20250910", Family: "web_fetch", Execution: SurfaceServer, ZDREligible: false, CostType: "web_search"},
	{ID: "code_execution_20260120", Family: "code_execution", Execution: SurfaceServer, ZDREligible: false, CostType: "code_execution"},
	{ID: "code_execution_20250825", Family: "code_execution", Execution: SurfaceServer, ZDREligible: false, CostType: "code_execution"},
	{ID: "advisor_20260301", Family: "advisor", Execution: SurfaceServer, ZDREligible: false, CostType: "tokens", Beta: true},
	{ID: "tool_search_tool_regex_20251119", Family: "tool_search", Execution: SurfaceServer, ZDREligible: false, CostType: "tokens"},
	{ID: "tool_search_tool_bm25_20251119", Family: "tool_search", Execution: SurfaceServer, ZDREligible: false, CostType: "tokens"},
	// Memory: server-managed but ZDR-eligible (documented exception, CLA-15 matrix).
	{ID: "memory_20250818", Family: "memory", Execution: SurfaceServer, ZDREligible: true, CostType: "tokens"},
	// Client-executed tools (the agent host runs them; data stays local → ZDR-eligible).
	{ID: "bash_20250124", Family: "bash", Execution: SurfaceClient, ZDREligible: true, CostType: "tokens"},
	{ID: "text_editor_20250728", Family: "text_editor", Execution: SurfaceClient, ZDREligible: true, CostType: "tokens"},
	{ID: "text_editor_20250124", Family: "text_editor", Execution: SurfaceClient, ZDREligible: true, CostType: "tokens"},
	{ID: "computer_20251124", Family: "computer", Execution: SurfaceClient, ZDREligible: true, CostType: "tokens"},
	{ID: "computer_20250124", Family: "computer", Execution: SurfaceClient, ZDREligible: true, CostType: "tokens"},
}

// ClaudeToolTypes returns a copy of the declared tool-type catalog for the
// capabilities (V), access-map (III) and red-team (XVIII) modules.
func ClaudeToolTypes() []ToolType {
	out := make([]ToolType, len(claudeToolTypes))
	copy(out, claudeToolTypes)
	return out
}
