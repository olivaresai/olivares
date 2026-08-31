// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"regexp"
	"strconv"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// classJailbreak is the jailbreak guardrail class. A jailbreak is an attempt to
// override the model's alignment/guardrails (assume an unrestricted persona,
// suppress refusals, extract the system prompt). It maps to OWASP LLM01:2025
// (Prompt Injection — jailbreak umbrella) and MITRE ATLAS AML.T0054 (LLM
// Jailbreak); system-prompt extraction maps to LLM07:2025 / ATLAS AML.T0056.
const classJailbreak = "jailbreak"

// jailbreakShapes are the explainable, deterministic jailbreak signal patterns.
// Every shape is a SIGNAL phrase (an attack intent), not a sensitive value, so its
// excerpt is the matched phrase (clamped + scrubbed by scan, never the raw
// payload) — redact stays false. The framework references are copied verbatim from
// the verified taxonomy (/tmp/s27-taxonomies.md): OWASP LLM01:2025 /
// LLM07:2025 and ATLAS AML.T0054 / AML.T0056.
var jailbreakShapes = []shape{
	// DAN-style persona (Shen et al. 2023, arXiv:2308.03825).
	{
		rule:  "dan-persona",
		re:    regexp.MustCompile(`(?i)do\s+anything\s+now|DAN\s+mode|you\s+are\s+DAN\b|stay\s+in\s+character\s+as\s+DAN`),
		sev:   sdkmodel.SeverityHigh,
		title: "DAN-style jailbreak persona requested",
		owasp: "LLM01:2025",
		atlas: "AML.T0054",
	},
	// Mode / guardrail bypass.
	{
		rule:  "guardrail-bypass",
		re:    regexp.MustCompile(`(?i)developer\s+mode|jailbreak|no\s+restrictions|without\s+any\s+(?:filters|rules|guidelines)|ignore\s+your\s+(?:guidelines|policies|safety)|bypass\s+your\s+safety|unfiltered\s+(?:mode|response)`),
		sev:   sdkmodel.SeverityHigh,
		title: "guardrail / safety bypass requested",
		owasp: "LLM01:2025",
		atlas: "AML.T0054",
	},
	// Refusal suppression.
	{
		rule:  "refusal-suppression",
		re:    regexp.MustCompile(`(?i)do\s+not\s+refuse|never\s+say\s+you\s+can(?:no)?'?t|you\s+must\s+answer|you\s+are\s+not\s+allowed\s+to\s+say\s+no`),
		sev:   sdkmodel.SeverityMedium,
		title: "refusal suppression requested",
		owasp: "LLM01:2025",
	},
	// Persona evasion (assume an ethics-free persona).
	{
		rule:  "persona-evasion",
		re:    regexp.MustCompile(`(?i)pretend\s+you\s+have\s+no\s+(?:ethics|guidelines|rules)|act\s+as\s+an\s+(?:unfiltered|amoral)\s|as\s+an\s+AI\s+with\s+no\s+(?:ethical\s+)?guidelines`),
		sev:   sdkmodel.SeverityHigh,
		title: "ethics-free persona requested",
		owasp: "LLM01:2025",
		atlas: "AML.T0054",
	},
	// System-prompt extraction (LLM07:2025 System Prompt Leakage / ATLAS
	// AML.T0056 LLM Meta Prompt Extraction).
	{
		rule:  "system-prompt-extraction",
		re:    regexp.MustCompile(`(?i)(?:reveal|print|repeat|show)\s+your\s+(?:system\s+)?prompt|what\s+(?:are|were)\s+your\s+(?:initial\s+)?instructions|ignore\s+the\s+prompt\s+above\s+and\s+tell\s+me\s+it`),
		sev:   sdkmodel.SeverityHigh,
		title: "system-prompt extraction attempt",
		owasp: "LLM07:2025",
		atlas: "AML.T0056",
	},
}

// jbTurnMarkerRe matches a conversational turn marker at a line start: the
// alternating role labels a many-shot jailbreak stacks ("User:"/"Assistant:",
// "Q:"/"A:", "Human:"/"AI:") to drown the system policy under many faked
// demonstrations (Anil et al., Anthropic, NeurIPS 2024). Anchored to a line start
// (multiline) so prose mentioning the words does not count.
var jbTurnMarkerRe = regexp.MustCompile(`(?im)^\s*(?:user|assistant|human|ai|q|a)\s*:`)

// jbManyShotThreshold is the minimum number of stacked turn markers that flags a
// many-shot pattern. A handful of turns is normal conversation framing; a long
// stack of faked demonstrations is the attack.
const jbManyShotThreshold = 8

// jbCountTurns counts conversational turn markers in text (the many-shot signal).
// It only counts the structural markers, never echoes the turns themselves.
func jbCountTurns(text string) int {
	return len(jbTurnMarkerRe.FindAllStringIndex(text, -1))
}

// jailbreakDetector is the jailbreak guardrail. It reports, with stable rule ids
// and verified OWASP/ATLAS references, that an input attempted to override the
// model's alignment — DAN persona, guardrail bypass, refusal suppression, ethics-
// free persona, system-prompt extraction, or a many-shot demonstration stack. It
// surfaces only SIGNAL phrases / a non-sensitive count, never the raw payload
// (docs/SECURITY-HARDENING.md). It runs on every surface.
type jailbreakDetector struct{}

func newJailbreakDetector() Detector { return jailbreakDetector{} }

func (jailbreakDetector) Class() string { return classJailbreak }

func (jailbreakDetector) Inspect(in GuardrailInput) []Detection {
	out := scan(classJailbreak, in.Text, jailbreakShapes)
	// Many-shot jailbreak (Anil et al., Anthropic, NeurIPS 2024): a long stack of
	// faked User/Assistant demonstrations overwhelming the policy. The excerpt is a
	// non-sensitive summary of the count — the turns are never echoed.
	if n := jbCountTurns(in.Text); n >= jbManyShotThreshold {
		out = append(out, Detection{
			Class:    classJailbreak,
			Rule:     "many-shot-jailbreak",
			Severity: sdkmodel.SeverityHigh,
			Title:    "many-shot jailbreak pattern",
			Excerpt:  scrubExcerpt(clamp("many-shot pattern: "+strconv.Itoa(n)+" demonstrations", maxExcerptLen)),
		}.tagged("LLM01:2025", "AML.T0054"))
	}
	return out
}
