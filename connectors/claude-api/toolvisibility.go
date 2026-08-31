// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// toolvisibility.go models the advanced-tool-use visibility signals: when
// programmatic tool calling or tool search (defer_loading) is active, the per-tool
// visibility of DLP/access-map is PARTIAL, not total. This file produces the
// governance-level signals the inferenceproxy DLP consumes; the forensic under-count
// caveat (forensic.go) is the AUDIT record; this is the GOVERNANCE posture.
//
// Honesty: Olivares DECLARES the blind spot; it does not claim coverage it cannot
// provide. allowed_callers is NOT a security boundary (ANT2-15).
package claudeapi

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

const (
	subjectToolVisibility = "anthropic.tool_visibility"
	subjectToolInventory  = "anthropic.tool_inventory"

	findingGovernance = "governance"
)

// ToolVisibility classifies the per-tool visibility of a governed inference session.
type ToolVisibility string

const (
	// ToolVisibilityFull means every tool call is visible in the context and in
	// usage — the DLP/access-map covers the full tool surface.
	ToolVisibilityFull ToolVisibility = "full"
	// ToolVisibilityPartial means some tool results are OUTSIDE the context and/or
	// usage (programmatic tool calling, tool search late-loading) — the DLP/
	// access-map under-counts by design.
	ToolVisibilityPartial ToolVisibility = "partial"
)

// ToolVisibilitySignal returns the governance-level tool-visibility finding for a
// session. When programmatic tool calling is active, visibility is PARTIAL; otherwise
// it is FULL. The finding is always emitted so the governance plane has an explicit
// record rather than assuming coverage.
func ToolVisibilitySignal(sessionRef string, at time.Time, programmaticToolCalling, toolSearchActive bool) model.FindingReport {
	vis := ToolVisibilityFull
	title := "Tool visibility: FULL — all tool results are in the context and in usage"
	detail := "per-tool visibility full: every tool_use result enters the context window and usage metrics"

	if programmaticToolCalling || toolSearchActive {
		vis = ToolVisibilityPartial
		var reasons []string
		if programmaticToolCalling {
			reasons = append(reasons, "programmatic tool calling (intermediate results outside context and usage)")
		}
		if toolSearchActive {
			reasons = append(reasons, "tool search defer_loading (tools loaded post-hoc, not in initial request)")
		}
		title = "Tool visibility: PARTIAL — " + strings.Join(reasons, "; ")
		detail = "per-tool visibility partial: " + strings.Join(reasons, "; ") + "; DLP/access-map under-counts by design; allowed_callers is NOT a security boundary"
	}
	_ = vis // carried in the detail hash for downstream parsing stability
	return model.FindingReport{
		Kind:        findingGovernance,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectToolVisibility,
		SubjectRef:  sessionRef,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  at,
	}
}

// ToolInventorySignal returns the governance finding recording the declared vs
// observed tool inventory of a session. Tools in the request's `tools` array are
// "declared"; tools that appear in tool_use blocks but were NOT in the declared set
// are "observed" (loaded late by tool search / defer_loading). The finding carries
// COUNTS only, never tool names (minimal-data).
func ToolInventorySignal(sessionRef string, at time.Time, declaredTools, observedTools []string) model.FindingReport {
	declared := uniqueSorted(declaredTools)
	observed := uniqueSorted(observedTools)
	lateLoaded := setDiff(observed, declared)

	title := "Tool inventory: " + strconv.Itoa(len(declared)) + " declared"
	detail := "tool-inventory declared=" + strconv.Itoa(len(declared)) + " observed=" + strconv.Itoa(len(observed))

	if len(lateLoaded) > 0 {
		title += ", " + strconv.Itoa(len(lateLoaded)) + " loaded post-hoc by tool search (not in initial request)"
		detail += " late-loaded=" + strconv.Itoa(len(lateLoaded)) + " (defer_loading: tools outside declared set)"
	} else {
		title += ", all observed tools were declared"
	}

	return model.FindingReport{
		Kind:        findingGovernance,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectToolInventory,
		SubjectRef:  sessionRef,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  at,
	}
}

func uniqueSorted(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ss))
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if _, ok := seen[s]; !ok {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func setDiff(a, b []string) []string {
	set := make(map[string]struct{}, len(b))
	for _, s := range b {
		set[s] = struct{}{}
	}
	var diff []string
	for _, s := range a {
		if _, ok := set[s]; !ok {
			diff = append(diff, s)
		}
	}
	return diff
}
