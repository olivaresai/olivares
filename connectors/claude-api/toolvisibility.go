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
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// ToolVisibilityFeatures describes configured advanced-tool features, not an
// authorization decision or proof that a tool ran. It contains no tool names,
// schemas, input, prompt or credential material.
type ToolVisibilityFeatures struct {
	ProgrammaticToolCalling bool
	ToolSearchActive        bool
}

type toolVisibilityRequest struct {
	Tools []json.RawMessage `json:"tools"`
}

type toolVisibilityConfig struct {
	Enabled      *bool `json:"enabled"`
	DeferLoading *bool `json:"defer_loading"`
}

func (c toolVisibilityConfig) enabledDeferred(defaults toolVisibilityConfig) bool {
	enabled, deferred := true, false
	for _, config := range []toolVisibilityConfig{defaults, c} {
		if config.Enabled != nil {
			enabled = *config.Enabled
		}
		if config.DeferLoading != nil {
			deferred = *config.DeferLoading
		}
	}
	return enabled && deferred
}

// ToolVisibilityFeatures inspects the immutable bytes the governed request will
// forward. A later mutation of the caller's opaque Tools cannot alter the facts.
func (p PreparedRequest) ToolVisibilityFeatures() ToolVisibilityFeatures {
	var request toolVisibilityRequest
	_ = json.Unmarshal(p.body, &request) // constructed by MarshalPrepared
	return request.toolVisibilityFeatures()
}

// ToolVisibilityFeatures combines every entry in the frozen submission. A batch
// has partial visibility when any entry configures the corresponding feature.
func (p PreparedBatch) ToolVisibilityFeatures() ToolVisibilityFeatures {
	var batch struct {
		Requests []struct {
			Params toolVisibilityRequest `json:"params"`
		} `json:"requests"`
	}
	_ = json.Unmarshal(p.body, &batch) // constructed by MarshalPreparedBatch
	var features ToolVisibilityFeatures
	for _, entry := range batch.Requests {
		f := entry.Params.toolVisibilityFeatures()
		features.ProgrammaticToolCalling = features.ProgrammaticToolCalling || f.ProgrammaticToolCalling
		features.ToolSearchActive = features.ToolSearchActive || f.ToolSearchActive
	}
	return features
}

func (r toolVisibilityRequest) toolVisibilityFeatures() ToolVisibilityFeatures {
	var features ToolVisibilityFeatures
	for _, raw := range r.Tools {
		var tool struct {
			Type           string                          `json:"type"`
			AllowedCallers []string                        `json:"allowed_callers"`
			DeferLoading   bool                            `json:"defer_loading"`
			DefaultConfig  toolVisibilityConfig            `json:"default_config"`
			Configs        map[string]toolVisibilityConfig `json:"configs"`
		}
		// Tools are opaque at this connector boundary. Read only these documented
		// feature fields; other tool data never enters the returned projection.
		_ = json.Unmarshal(raw, &tool)
		for _, caller := range tool.AllowedCallers {
			features.ProgrammaticToolCalling = features.ProgrammaticToolCalling || strings.HasPrefix(caller, "code_execution_")
		}
		features.ToolSearchActive = features.ToolSearchActive || strings.HasPrefix(tool.Type, "tool_search_tool_") || tool.DeferLoading
		if tool.Type == "mcp_toolset" {
			// The frozen request cannot enumerate the remote inventory. An enabled,
			// deferred default therefore remains a conservative partial annotation.
			features.ToolSearchActive = features.ToolSearchActive || tool.DefaultConfig.enabledDeferred(toolVisibilityConfig{})
		}
		if tool.Type == "mcp_toolset" || strings.HasPrefix(tool.Type, "computer_toolset_") || strings.HasPrefix(tool.Type, "browser_toolset_") {
			defaults := toolVisibilityConfig{}
			if tool.Type == "mcp_toolset" {
				defaults = tool.DefaultConfig
			}
			for _, config := range tool.Configs {
				features.ToolSearchActive = features.ToolSearchActive || config.enabledDeferred(defaults)
			}
		}
	}
	return features
}

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
