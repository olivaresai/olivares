// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file models the AGENTIC RUNTIME request parameters the InferenceClient
// previously omitted because the judge/embedder (observe) never needed them, but a run
// Olivares CONDUCTS does (FASE W; consumed by FASE V and the inference PEP
//): the thinking config, tool_choice, stop_sequences and the top_p/top_k sampling
// params. It carries the model-aware deprecation handling — top_p/top_k are withheld on
// the models that reject them (Opus 4.7+/Fable 5/Mythos 5, RejectsSamplingParams) just
// like temperature, and the thinking config is normalized to the shape the target model
// accepts so a foreseeable 400 never fails a conducted run (the legacy fixed budget is
// rewritten to adaptive on those same models; an explicit disabled is dropped on the
// always-on Fable 5/Mythos 5). It is Anthropic's deprecation, not a product bug
//: withheld/normalized, never silently wrong, never a fabricated capability.
//
// Authority (verbatim, jun-2026): …/build-with-claude/{extended-thinking,adaptive-
// thinking}; …/agents-and-tools/tool-use/overview (tool_choice); …/about-claude/models/
// migration-guide (budget_tokens / sampling-param removal on Opus 4.7+ / Fable 5).
package claudeapi

import (
	"fmt"
	"strings"
)

// ---- thinking config --------------------------------------------------------------

// Thinking type values. adaptive lets the model decide depth (the current shape on
// Opus 4.6+/Fable 5); enabled is the LEGACY fixed-budget thinking (older models only —
// budget_tokens was removed on Opus 4.7+/Fable 5/Mythos 5); disabled turns thinking off
// (rejected on the always-on Fable 5/Mythos 5).
const (
	ThinkingAdaptive = "adaptive"
	ThinkingEnabled  = "enabled"
	ThinkingDisabled = "disabled"
)

// Thinking display values. summarized streams a readable summary of the reasoning;
// omitted (the default on Fable 5/Mythos 5/Opus 4.8/4.7) opens thinking blocks with
// empty text — controls visibility only, never whether thinking is billed.
const (
	ThinkingDisplaySummarized = "summarized"
	ThinkingDisplayOmitted    = "omitted"
)

// Thinking is the extended/adaptive thinking config. BudgetTokens applies only to the
// legacy type "enabled" (omitted otherwise); Display opts summarized reasoning back in.
type Thinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	Display      string `json:"display,omitempty"`
}

// AdaptiveThinking builds an adaptive thinking config (the current shape). display "" =
// the API default (omitted on Fable 5/Mythos 5/Opus 4.8/4.7); pass
// ThinkingDisplaySummarized to stream a reasoning summary to a live portal.
func AdaptiveThinking(display string) *Thinking {
	return &Thinking{Type: ThinkingAdaptive, Display: display}
}

// EnabledThinking builds a LEGACY fixed-budget thinking config (older models only). On a
// model that rejects budget_tokens (Opus 4.7+/Fable 5/Mythos 5) preflight rewrites it to
// adaptive — see normalizeThinkingForModel.
func EnabledThinking(budgetTokens int) *Thinking {
	return &Thinking{Type: ThinkingEnabled, BudgetTokens: budgetTokens}
}

// DisabledThinking builds a thinking-off config. On the always-on Fable 5/Mythos 5
// preflight drops it (an explicit disabled 400s there).
func DisabledThinking() *Thinking { return &Thinking{Type: ThinkingDisabled} }

// RejectsThinkingBudget reports whether a model rejects the legacy fixed thinking budget
// thinking{type:"enabled", budget_tokens:N} with a 400. budget_tokens was removed in the
// SAME release that removed the sampling params (Opus 4.7+, Fable 5, Mythos 5), so the
// model set coincides with RejectsSamplingParams (verified jun-2026, migration guide).
// It delegates rather than duplicates the version parse so the two stay in lockstep.
func RejectsThinkingBudget(modelID string) bool { return RejectsSamplingParams(modelID) }

// isAlwaysOnThinkingModel reports whether thinking is ALWAYS ON for the model, so an
// explicit thinking{type:"disabled"} 400s — Fable 5 / Mythos 5 (verified jun-2026; the
// 2026-06-09 launch page states thinking cannot be disabled and the param must be
// omitted). claude-mythos-preview is NOT asserted (fail-closed; only verified ids opt in).
func isAlwaysOnThinkingModel(modelID string) bool {
	id := strings.TrimSpace(modelID)
	return strings.HasPrefix(id, "claude-fable-5") || strings.HasPrefix(id, "claude-mythos-5")
}

// normalizeThinkingForModel rewrites a thinking config to the shape the target model
// accepts, mirroring the sampling-param withholding (a foreseeable 400 must not fail a
// conducted run — it is Anthropic's deprecation, not a product bug). Two model-specific
// 400s are folded:
//   - the legacy fixed budget (type "enabled") is REMOVED on Opus 4.7+/Fable 5/Mythos 5
//     → rewritten to {type:"adaptive"} (the documented replacement), preserving Display.
//   - an explicit type "disabled" 400s on the always-on Fable 5/Mythos 5 → the thinking
//     field is dropped entirely (the documented fix is to omit it).
//
// On models that still accept the legacy shape (Opus 4.6/Sonnet 4.6: budget_tokens is
// deprecated but FUNCTIONAL) nothing is rewritten. It never mutates the caller's struct:
// an unchanged config returns the same pointer, a rewrite returns a NEW struct. nil-safe.
func normalizeThinkingForModel(modelID string, t *Thinking) *Thinking {
	if t == nil {
		return nil
	}
	switch t.Type {
	case ThinkingEnabled:
		if RejectsThinkingBudget(modelID) {
			return &Thinking{Type: ThinkingAdaptive, Display: t.Display}
		}
	case ThinkingDisabled:
		if isAlwaysOnThinkingModel(modelID) {
			return nil
		}
	}
	return t
}

// ---- tool_choice ------------------------------------------------------------------

// ToolChoice type values. auto (default) lets the model decide; any forces at least one
// tool; tool forces a NAMED tool; none forbids tool use.
const (
	ToolChoiceTypeAuto = "auto"
	ToolChoiceTypeAny  = "any"
	ToolChoiceTypeTool = "tool"
	ToolChoiceTypeNone = "none"
)

// ToolChoice controls when/whether the model calls a tool. Name is required for type
// "tool". DisableParallelToolUse caps the turn at one tool call (valid on any type).
type ToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

// AutoToolChoice / AnyToolChoice / SpecificToolChoice / NoToolChoice build the four
// tool_choice shapes. disableParallel caps the turn at a single tool call.
func AutoToolChoice(disableParallel bool) *ToolChoice {
	return &ToolChoice{Type: ToolChoiceTypeAuto, DisableParallelToolUse: disableParallel}
}

func AnyToolChoice(disableParallel bool) *ToolChoice {
	return &ToolChoice{Type: ToolChoiceTypeAny, DisableParallelToolUse: disableParallel}
}

func SpecificToolChoice(name string, disableParallel bool) *ToolChoice {
	return &ToolChoice{Type: ToolChoiceTypeTool, Name: name, DisableParallelToolUse: disableParallel}
}

func NoToolChoice() *ToolChoice { return &ToolChoice{Type: ToolChoiceTypeNone} }

// validateToolChoice rejects a malformed tool_choice BEFORE the upstream 400: type
// "tool" requires a name, and the type must be one of the four. nil is valid (default).
func validateToolChoice(tc *ToolChoice) error {
	if tc == nil {
		return nil
	}
	switch tc.Type {
	case ToolChoiceTypeAuto, ToolChoiceTypeAny, ToolChoiceTypeNone:
		return nil
	case ToolChoiceTypeTool:
		if strings.TrimSpace(tc.Name) == "" {
			return fmt.Errorf("claudeapi: tool_choice type %q requires a tool name", ToolChoiceTypeTool)
		}
		return nil
	default:
		return fmt.Errorf("claudeapi: tool_choice type %q invalid (auto|any|tool|none)", tc.Type)
	}
}
