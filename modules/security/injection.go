// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"regexp"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// classInjection is the prompt-injection guardrail class.
const classInjection = "prompt_injection"

// This detector covers OWASP LLM01:2025 (Prompt Injection) and MITRE ATLAS
// AML.T0051 (LLM Prompt Injection: .000 Direct, .001 Indirect). It is purely
// deterministic and explainable: each rule is a tight (?i) phrase/marker pattern
// keyed to a stable rule id and the verified taxonomy reference. A guardrail may
// over-flag but must not under-flag a real attack, so the phrase set errs toward
// detecting; minimal-data is preserved by routing matches through scan() (which
// auto-clamps + scrubs the excerpt) and by redacting the one shape — an
// obfuscated base64 blob — whose match could itself encode a sensitive payload.

// injDirectShapes are the surface-independent direct/role-override and
// payload-splitting/obfuscation rules. Signal phrases are not sensitive, so they
// carry a clamped+scrubbed excerpt (redact=false); only the base64 blob redacts.
var injDirectShapes = []shape{
	// --- Direct override: cancel the prior instructions (AML.T0051.000). ---
	{
		rule:  "ignore-previous-instructions",
		re:    regexp.MustCompile(`(?i)ignore\s+(?:all\s+)?(?:the\s+)?previous\s+instructions`),
		sev:   sdkmodel.SeverityHigh,
		title: "instruction-override: ignore previous instructions",
		owasp: "LLM01:2025", atlas: "AML.T0051.000",
	},
	{
		rule:  "disregard-above",
		re:    regexp.MustCompile(`(?i)disregard\s+(?:the\s+)?(?:above|previous|prior)`),
		sev:   sdkmodel.SeverityHigh,
		title: "instruction-override: disregard the above/previous",
		owasp: "LLM01:2025", atlas: "AML.T0051.000",
	},
	{
		rule:  "forget-instructions",
		re:    regexp.MustCompile(`(?i)forget\s+(?:everything|your\s+instructions|all\s+prior)`),
		sev:   sdkmodel.SeverityHigh,
		title: "instruction-override: forget your instructions",
		owasp: "LLM01:2025", atlas: "AML.T0051.000",
	},
	// --- Instruction / role override: re-seat the system/persona (AML.T0051.000). ---
	{
		rule:  "new-instructions-header",
		re:    regexp.MustCompile(`(?i)new\s+instructions\s*:`),
		sev:   sdkmodel.SeverityHigh,
		title: "role-override: injected \"new instructions:\" header",
		owasp: "LLM01:2025", atlas: "AML.T0051.000",
	},
	{
		rule:  "system-prompt-header",
		re:    regexp.MustCompile(`(?i)system\s+prompt\s*:`),
		sev:   sdkmodel.SeverityHigh,
		title: "role-override: injected \"system prompt:\" header",
		owasp: "LLM01:2025", atlas: "AML.T0051.000",
	},
	{
		rule:  "you-are-now",
		re:    regexp.MustCompile(`(?i)you\s+are\s+now\b`),
		sev:   sdkmodel.SeverityHigh,
		title: "role-override: \"you are now ...\" persona reset",
		owasp: "LLM01:2025", atlas: "AML.T0051.000",
	},
	{
		rule:  "from-now-on-you",
		re:    regexp.MustCompile(`(?i)from\s+now\s+on,?\s+you\b`),
		sev:   sdkmodel.SeverityHigh,
		title: "role-override: \"from now on you ...\" directive",
		owasp: "LLM01:2025", atlas: "AML.T0051.000",
	},
	{
		rule:  "chat-role-marker",
		re:    regexp.MustCompile(`(?i)(?:<\|im_start\|>\s*system|\[INST\]|#{2,}\s*system)`),
		sev:   sdkmodel.SeverityHigh,
		title: "role-override: injected chat/role template marker",
		owasp: "LLM01:2025", atlas: "AML.T0051.000",
	},
	// --- Payload splitting: assemble a payload across pieces (LLM01:2025). ---
	{
		rule:  "concatenate-following",
		re:    regexp.MustCompile(`(?i)concatenate\s+the\s+following`),
		sev:   sdkmodel.SeverityMedium,
		title: "payload-splitting: \"concatenate the following\"",
		owasp: "LLM01:2025", atlas: "AML.T0051.000",
	},
	{
		rule:  "assemble-fragments",
		re:    regexp.MustCompile(`(?i)assemble\s+these\s+(?:parts|fragments)`),
		sev:   sdkmodel.SeverityMedium,
		title: "payload-splitting: \"assemble these parts/fragments\"",
		owasp: "LLM01:2025", atlas: "AML.T0051.000",
	},
	{
		rule:  "combine-next-messages",
		re:    regexp.MustCompile(`(?i)combine\s+the\s+next\s+messages`),
		sev:   sdkmodel.SeverityMedium,
		title: "payload-splitting: \"combine the next messages\"",
		owasp: "LLM01:2025", atlas: "AML.T0051.000",
	},
	// --- Obfuscation/encoding: a long base64-looking blob could encode anything,
	// so redact the excerpt to a label (the match value is never surfaced). ---
	{
		rule:  "base64-blob",
		re:    regexp.MustCompile(`[A-Za-z0-9+/]{40,}={0,2}`),
		sev:   sdkmodel.SeverityMedium,
		title: "obfuscation: long base64-looking payload",
		owasp: "LLM01:2025", atlas: "AML.T0051.000",
		redact: true,
	},
}

// injIndirectShapes are indirect-injection rules: payload phrases that address the
// model itself, characteristic of instructions smuggled in via retrieved/tool
// content rather than the user turn (AML.T0051.001). They are weighted higher when
// the surface is tool/RAG content (see Inspect).
//
// Multi-taxonomy: an indirect injection is the canonical example of one
// signal mapping to THREE frameworks at once — OWASP LLM01:2025 (Prompt Injection) is
// the LLM lens, MITRE ATLAS AML.T0051.001 (Indirect) is the technique, and OWASP-
// Agentic ASI01 (Agent Goal Hijack) is the agentic lens, because smuggled instructions
// in retrieved/tool content are precisely how an agent's goal is hijacked. So these
// rules carry `also: ["ASI01"]`, and a single trip surfaces on an LLM-keyed query, an
// ASI-keyed query and an ATLAS-keyed query alike.
var injIndirectShapes = []shape{
	{
		rule:  "when-you-read-this",
		re:    regexp.MustCompile(`(?i)when\s+you\s+read\s+this`),
		sev:   sdkmodel.SeverityHigh,
		title: "indirect-injection: payload addressed to the reader",
		owasp: "LLM01:2025", atlas: "AML.T0051.001", also: []string{"ASI01"},
	},
	{
		rule:  "ai-assistant-execute",
		re:    regexp.MustCompile(`(?i)ai\s+assistant,?\s+(?:execute|run|do)\b`),
		sev:   sdkmodel.SeverityHigh,
		title: "indirect-injection: \"AI assistant, execute/run/do ...\"",
		owasp: "LLM01:2025", atlas: "AML.T0051.001", also: []string{"ASI01"},
	},
	{
		rule:  "as-an-ai-you-must",
		re:    regexp.MustCompile(`(?i)as\s+an\s+ai\s+language\s+model,?\s+you\s+must`),
		sev:   sdkmodel.SeverityHigh,
		title: "indirect-injection: \"as an AI language model, you must ...\"",
		owasp: "LLM01:2025", atlas: "AML.T0051.001", also: []string{"ASI01"},
	},
	{
		rule:  "to-the-assistant-reading",
		re:    regexp.MustCompile(`(?i)to\s+the\s+assistant\s+reading\s+this`),
		sev:   sdkmodel.SeverityHigh,
		title: "indirect-injection: \"to the assistant reading this\"",
		owasp: "LLM01:2025", atlas: "AML.T0051.001", also: []string{"ASI01"},
	},
}

// injectionDetector is the prompt-injection guardrail. It runs on every surface;
// indirect-injection markers are escalated to Critical on the tool_args surface,
// where smuggled instructions in tool/RAG content are the live AML.T0051.001 risk.
type injectionDetector struct{}

func newInjectionDetector() Detector { return injectionDetector{} }

func (injectionDetector) Class() string { return classInjection }

func (injectionDetector) Inspect(in GuardrailInput) []Detection {
	out := scan(classInjection, in.Text, injDirectShapes)
	indirect := scan(classInjection, in.Text, injIndirectShapes)
	// Indirect markers arriving via tool/RAG content (SurfaceToolArgs) are the
	// canonical AML.T0051.001 attack vector — weight them higher there.
	if in.Surface == SurfaceToolArgs {
		for i := range indirect {
			indirect[i].Severity = sdkmodel.SeverityCritical
		}
	}
	return append(out, indirect...)
}
