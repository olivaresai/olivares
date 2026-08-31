// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferenceproxy

// ToolVisibility classifies the per-tool DLP/access-map coverage of a governed
// inference request. When advanced-tool-use features are active (programmatic
// tool calling, tool search with defer_loading), intermediate results or late-loaded
// tools are OUTSIDE the per-tool-call visibility the DLP/access-map was built on.
//
// This is a REQUEST-LEVEL annotation, not a tenant-level policy toggle. The
// composition root stamps it on the request context after inspecting the request's
// tool configuration; the DLP gates read it to decide whether to log the partial-
// coverage caveat alongside their per-tool findings.
//
// Honesty: visibility=partial is a FACT, not a violation. The DLP still runs on what
// IS visible; it just declares honestly that it CANNOT see everything.
type ToolVisibility string

const (
	// ToolVisibilityFull: every tool call enters the context and usage — DLP/access-map
	// covers the full tool surface.
	ToolVisibilityFull ToolVisibility = "full"

	// ToolVisibilityPartial: some tool results are OUTSIDE context and/or usage —
	// DLP/access-map under-counts by design. Reasons: programmatic tool calling
	// (code_execution_20260120), tool search defer_loading.
	ToolVisibilityPartial ToolVisibility = "partial"
)

// RequestToolVisibility resolves the tool-visibility classification for a request
// based on whether advanced-tool-use features are active.
func RequestToolVisibility(programmaticToolCalling, toolSearchActive bool) ToolVisibility {
	if programmaticToolCalling || toolSearchActive {
		return ToolVisibilityPartial
	}
	return ToolVisibilityFull
}

// DeclaredVsObserved tracks the tool inventory divergence within a single request:
// how many tools were DECLARED in the request vs how many were actually OBSERVED
// executing. A positive LateLoaded count means tool search loaded tools post-hoc
// that were not in the initial declared set.
type DeclaredVsObserved struct {
	DeclaredCount   int
	ObservedCount   int
	LateLoadedCount int
}
