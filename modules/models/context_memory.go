// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

// CLA-15 — context-management / memory-tool governance + ZDR matrix.
//
// CapContextManagement and CapMemoryTool are coarse booleans on the capability
// matrix; they say a model SUPPORTS the feature but not what it means for the
// audit trail or data residency. This file models the depth a security/compliance
// audience needs: that the model's working context can be CLEARED server-side (so
// the OTEL/audit trail may not reflect what the model actually saw at decision
// time — a forensics concern), that the memory tool PERSISTS state client-side (a
// data-governance boundary), and which features are ZDR-eligible (a residency
// concern). The beta identifiers are Anthropic's exact dated tokens.
// Source: https://platform.claude.com/docs/en/build-with-claude/context-editing

// Claude context-management / memory beta identifiers (exact dated tokens).
const (
	BetaContextManagement = "context-management-2025-06-27"
	BetaClearToolUses     = "clear_tool_uses_20250919"
	BetaClearThinking     = "clear_thinking_20251015"
	BetaMemoryTool        = "memory_20250818"
)

// DataSurface is where a feature's data lives relative to the ZDR boundary.
type DataSurface string

const (
	// SurfaceServer is server-side (Anthropic) context editing — within ZDR.
	SurfaceServer DataSurface = "server"
	// SurfaceClient is client-side persistence on the agent host — the operator
	// owns retention/erasure.
	SurfaceClient DataSurface = "client"
	// SurfaceExternal is a third-party destination outside the ZDR boundary.
	SurfaceExternal DataSurface = "external"
)

// DataGovernanceFeature is one context/memory governance fact: what a feature does
// to the model's context or persisted state, whether it is ZDR-eligible, and the
// forensic/data-governance implication a control plane must surface.
type DataGovernanceFeature struct {
	Feature      string      `json:"feature"`
	BetaID       string      `json:"beta_id,omitempty"`
	Surface      DataSurface `json:"surface"`
	ServerClears bool        `json:"server_clears"` // can drop content from the model's working context
	ZDREligible  bool        `json:"zdr_eligible"`
	Persistence  string      `json:"persistence"`
	ForensicNote string      `json:"forensic_note"`
}

// claudeDataGovernance is the declared Claude context/memory governance matrix. It
// is versioned-in-repo reference data (like the pricing table), not telemetry.
var claudeDataGovernance = []DataGovernanceFeature{
	{
		Feature: "context management (server-side compaction)", BetaID: BetaContextManagement,
		Surface: SurfaceServer, ServerClears: true, ZDREligible: true,
		Persistence:  "none — the working context is compacted/rewritten server-side",
		ForensicNote: "compaction can drop earlier turns; the OTEL/audit trail may not reflect the full context the model saw at decision time",
	},
	{
		Feature: "clear tool results", BetaID: BetaClearToolUses,
		Surface: SurfaceServer, ServerClears: true, ZDREligible: true,
		Persistence:  "none — tool_result blocks are cleared from context",
		ForensicNote: "tool results can be cleared mid-conversation; an observed tool_result may no longer be in the model's context when it acts",
	},
	{
		Feature: "clear thinking", BetaID: BetaClearThinking,
		Surface: SurfaceServer, ServerClears: true, ZDREligible: true,
		Persistence:  "none — prior extended-thinking is cleared",
		ForensicNote: "extended-thinking can be server-cleared; the reasoning is not retained and is not reconstructable from the trail",
	},
	{
		Feature: "memory tool (/memories)", BetaID: BetaMemoryTool,
		Surface: SurfaceClient, ServerClears: false, ZDREligible: true,
		Persistence:  "client-side — the agent host's memory store (operator-controlled filesystem)",
		ForensicNote: "the memory tool persists state OUTSIDE the model on the client; it is a data-governance boundary the control plane should inventory — retention/erasure live with the operator",
	},
	{
		Feature: "MCP connector", BetaID: "",
		Surface: SurfaceExternal, ServerClears: false, ZDREligible: false,
		Persistence:  "external — a third-party MCP server",
		ForensicNote: "MCP connectors are NOT ZDR-eligible; data sent to a third-party MCP server leaves the ZDR boundary",
	},
}

// ClaudeDataGovernance returns a copy of the context/memory + ZDR governance
// matrix for the forensics (IX), knowledge (VIII) and compliance (XIII) modules.
func ClaudeDataGovernance() []DataGovernanceFeature {
	out := make([]DataGovernanceFeature, len(claudeDataGovernance))
	copy(out, claudeDataGovernance)
	return out
}

// ContextMayBeServerCleared reports whether any of the given active beta tokens can
// clear content from the model's working context. Forensics uses it to annotate
// that the captured trail may not reflect the model's full working context (so an
// investigator does not over-trust an OTEL gap as "nothing happened").
func ContextMayBeServerCleared(activeBetas []string) bool {
	for _, b := range activeBetas {
		for _, f := range claudeDataGovernance {
			if f.ServerClears && f.BetaID == b {
				return true
			}
		}
	}
	return false
}
