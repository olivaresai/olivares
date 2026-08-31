// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claude

import "strings"

// multi-dialect GenAI normalization (FASE R, CUR-9).
//
// Three GenAI telemetry generations COEXIST in real 2026 fleets, and a control
// plane that reads only one loses the others' telemetry. Every claim below is
// primary-source verified (2026-06-11; re-verified 2026-07-05 against the
// dedicated open-telemetry/semantic-conventions-genai repo at main@c321d7e — no
// mapped-shape change):
//
//  1. LEGACY OpenLLMetry/Traceloop (pre-OTel-standardization) — what Traceloop-
//     instrumented LangChain/LangGraph/CrewAI fleets pinned below openllmetry
//     v0.55.0 (released 2026-03-29) still emit: indexed span attributes
//     gen_ai.prompt.{i}.role/.content and gen_ai.completion.{i}.* (content on
//     SPAN ATTRIBUTES, not events), tokens as gen_ai.usage.prompt_tokens/
//     completion_tokens plus llm.usage.total_tokens (the total kept the llm.*
//     prefix), provider as gen_ai.system with NON-normalized values ("OpenAI",
//     "Langchain"; pre-v0.17.0 fleets used llm.vendor), plus the Traceloop
//     markers llm.request.type and traceloop.span.kind/workflow.name/entity.*.
//     Mixed-generation spans exist (openllmetry v0.54 emits CURRENT token names
//     next to legacy indexed prompts on the same span).
//  2. The v1.36-or-prior EVENTS generation (semconv ≤1.36.0, the spec's own
//     name for it) — identified by gen_ai.system (renamed away in v1.37.0):
//     five per-message log events (gen_ai.{system,user,assistant,tool}.message,
//     gen_ai.choice, introduced v1.28.0, deprecated v1.37.0), each carrying at
//     most the gen_ai.system attribute; usage already rides
//     gen_ai.usage.input_tokens/output_tokens (renamed in v1.27.0).
//  3. The v1.37+ MESSAGES generation (mapped at the verified pin v1.41.1, the
//     last VERSIONED vocabulary carrying the gen-ai conventions before semconv
//     v1.42.0 (2026-06-12, #3696) moved them from the main repo to
//     open-telemetry/semantic-conventions-genai; upstream shape pinned separately
//     as main@c321d7e, verified 2026-07-05):
//     gen_ai.provider.name (Required on spans), gen_ai.input.messages/
//     gen_ai.output.messages/gen_ai.system_instructions (Opt-In, content-capture
//     gated), and the consolidated gen_ai.client.inference.operation.details event.
//
// The normalizer DETECTS the generation per signal and stamps the pin on the
// normalized genAIEvent (.semconv). Detection keys on generation-EXCLUSIVE
// markers; a signal carrying only keys whose names are identical across
// generations is normalized under the current pin (the mapping applied is
// byte-identical either way, so the pin records which VOCABULARY was read —
// the producer's exact release is not knowable from the wire).
//
// Message CONTENT is deliberately never read from any generation (the indexed
// legacy attributes, the v1.36 event bodies, the v1.37+ messages attributes):
// the connector's minimal-data posture (OBS-10) maps structure — identity,
// usage, operations, tools — not prompts. The content keys serve ONLY as
// dialect markers.
const (
	// genAIDialectCurrent pins the v1.37+ generation to the verified semconv
	// release this profile maps (see genAISemconvVersion).
	genAIDialectCurrent = genAISemconvVersion
	// genAIDialectV136 is the v1.36-or-prior events generation — the spec's own
	// name for everything still keyed by gen_ai.system.
	genAIDialectV136 = "1.36.0"
	// genAIDialectLegacy is the pre-semconv OpenLLMetry/Traceloop convention
	// (no semconv release exists for it; the label is the honest pin).
	genAIDialectLegacy = "openllmetry"
)

// v1.36-or-prior per-message log events (verified at the v1.36.0 tag;
// introduced v1.28.0, deprecated v1.37.0). Recognized BY NAME so an event
// whose only attribute (gen_ai.system, Recommended not Required) is absent is
// still routed to the gen_ai pipeline instead of being dropped.
const (
	evtGenAISystemMessage    = "gen_ai.system.message"
	evtGenAIUserMessage      = "gen_ai.user.message"
	evtGenAIAssistantMessage = "gen_ai.assistant.message"
	evtGenAIToolMessage      = "gen_ai.tool.message"
	evtGenAIChoice           = "gen_ai.choice"
	// evtGenAIOpDetails is the v1.37.0 consolidated replacement event.
	evtGenAIOpDetails = "gen_ai.client.inference.operation.details"
)

// isGenAIPerMessageEvent reports whether name is one of the five deprecated
// v1.36-or-prior per-message events.
func isGenAIPerMessageEvent(name string) bool {
	switch name {
	case evtGenAISystemMessage, evtGenAIUserMessage, evtGenAIAssistantMessage,
		evtGenAIToolMessage, evtGenAIChoice:
		return true
	}
	return false
}

// v1.37+ generation markers (verified verbatim at the v1.37.0/v1.41.1 tags).
// The messages/system-instructions values are CONTENT and are never read; the
// keys are detection markers only.
const (
	attrGenAIInputMessages      = "gen_ai.input.messages"
	attrGenAIOutputMessages     = "gen_ai.output.messages"
	attrGenAISystemInstructions = "gen_ai.system_instructions"
)

// Legacy OpenLLMetry/Traceloop markers (verified against the openllmetry repo
// at tags v0.17.0/v0.40.14/v0.54.0). The indexed prompt/completion keys are
// matched by prefix+digit (gen_ai.prompt.0.role), which deliberately does NOT
// match gen_ai.prompt.name (the v1.39 MCP prompt attribute).
const (
	attrLegacyPromptPrefix     = "gen_ai.prompt."
	attrLegacyCompletionPrefix = "gen_ai.completion."
	attrLegacyTotalTokens      = "llm.usage.total_tokens"
	attrLegacyRequestType      = "llm.request.type"
	attrLegacyVendor           = "llm.vendor" // pre-v0.17.0 provider key
	attrTraceloopSpanKind      = "traceloop.span.kind"
	attrTraceloopWorkflowName  = "traceloop.workflow.name"
	attrTraceloopEntityName    = "traceloop.entity.name"
)

// hasLegacyIndexedContent reports whether the attribute set carries the
// OpenLLMetry indexed prompt/completion shape (gen_ai.prompt.{i}.* /
// gen_ai.completion.{i}.*). Only the key NAMES are inspected; the content
// values are never read.
func hasLegacyIndexedContent(a attrs) bool {
	for k := range a {
		if isLegacyIndexedKey(k) {
			return true
		}
	}
	return false
}

// isLegacyIndexedKey reports whether k is an indexed legacy content key. The
// character after the prefix must be a digit, so gen_ai.prompt.name (v1.39,
// MCP prompts) is NOT legacy.
func isLegacyIndexedKey(k string) bool {
	for _, p := range []string{attrLegacyPromptPrefix, attrLegacyCompletionPrefix} {
		if rest, ok := strings.CutPrefix(k, p); ok && rest != "" && rest[0] >= '0' && rest[0] <= '9' {
			return true
		}
	}
	return false
}

// detectGenAIDialect classifies one signal's generation from its
// generation-EXCLUSIVE markers and returns the semconv pin to stamp on the
// normalized event. Precedence: current (v1.37+) markers win — a producer
// straddling a migration (openllmetry ≥v0.55 emits gen_ai.input.messages while
// keeping traceloop.* markers) is read under the vocabulary it migrated TO —
// then legacy-exclusive markers, then the gen_ai.system-keyed v1.36-or-prior
// generation. A signal with no distinguishing marker is normalized under the
// current pin (its key names are identical across generations).
func detectGenAIDialect(a attrs, eventName string) string {
	if eventName == evtGenAIOpDetails ||
		a.has(attrGenAIProvider) || a.has(attrGenAIInputMessages) ||
		a.has(attrGenAIOutputMessages) || a.has(attrGenAISystemInstructions) ||
		a.has(attrGenAIWorkflowName) {
		return genAIDialectCurrent
	}
	if hasLegacyIndexedContent(a) ||
		a.has(attrGenAIPromptTokens) || a.has(attrGenAICompletionTokens) ||
		a.has(attrLegacyTotalTokens) || a.has(attrLegacyRequestType) ||
		a.has(attrTraceloopSpanKind) || a.has(attrLegacyVendor) {
		return genAIDialectLegacy
	}
	if isGenAIPerMessageEvent(eventName) || a.has(attrGenAISystem) {
		return genAIDialectV136
	}
	return genAIDialectCurrent
}
