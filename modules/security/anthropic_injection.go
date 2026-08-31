// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"regexp"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// classUntrusted is the structural prompt-injection guardrail class. Where the
// generic prompt-injection detector (injection.go) matches override PHRASES on any
// surface, this detector encodes Anthropic's STRUCTURAL prescription: third-party /
// tool-returned content (the tool_result and tool_args surfaces) must be inert
// DATA — it must not carry an injected conversation/role structure, redirect the
// assistant's task, impersonate the user, or direct exfiltration. Anthropic states
// that keyword screening alone is insufficient; this is the deterministic floor,
// and the optional Haiku-class Classifier seam (screen.go) is the fuzzy second
// opinion Anthropic recommends on top.
// Source: https://platform.claude.com/docs/en/test-and-evaluate/strengthen-guardrails/mitigate-jailbreaks
const classUntrusted = "untrusted_content"

// untrustedStructureShapes are HIGH-PRECISION structural-injection markers. Each
// is unambiguous enough that its presence in third-party content is itself the
// signal (regex over-matching benign data is the risk a structural check must
// avoid; the fuzzy cases are the Classifier's job, not regex's).
var untrustedStructureShapes = []shape{
	{
		rule:  "chat-template-injection",
		re:    regexp.MustCompile(`(?i)<\|im_start\|>|<\|im_end\|>|\[/?INST\]|<\|system\|>|<\|assistant\|>`),
		sev:   sdkmodel.SeverityHigh,
		title: "untrusted content carries chat/role template markers",
		owasp: "LLM01:2025", atlas: "AML.T0051.001",
	},
	{
		rule:  "injected-role-directive",
		re:    regexp.MustCompile(`(?i)(?:^|\n)\s*(?:system|assistant)\s*:\s*(?:you\b|ignore\b|disregard\b|from now\b|now you\b)`),
		sev:   sdkmodel.SeverityHigh,
		title: "untrusted content injects a system/assistant turn with a directive",
		owasp: "LLM01:2025", atlas: "AML.T0051.001",
	},
	{
		rule:  "task-redirection",
		re:    regexp.MustCompile(`(?i)your\s+(?:real|actual|true)\s+(?:task|goal|job|instructions?)\s+(?:is|are)\b`),
		sev:   sdkmodel.SeverityHigh,
		title: "untrusted content redirects the assistant's task",
		owasp: "LLM01:2025", atlas: "AML.T0051.001",
	},
	{
		rule:  "user-intent-impersonation",
		re:    regexp.MustCompile(`(?i)the\s+user\s+(?:actually|really|secretly)\s+wants?\b`),
		sev:   sdkmodel.SeverityHigh,
		title: "untrusted content impersonates the user's intent",
		owasp: "LLM01:2025", atlas: "AML.T0051.001",
	},
	{
		rule:  "embedded-exfil-directive",
		re:    regexp.MustCompile(`(?i)\b(?:send|post|upload|exfiltrate|email|forward|leak)\b[^.\n]{0,40}\b(?:to|at)\s+(?:https?://|[^\s@]+@)`),
		sev:   sdkmodel.SeverityCritical,
		title: "untrusted content directs data exfiltration",
		owasp: "LLM01:2025", atlas: "AML.T0051.001",
	},
}

// untrustedContentDetector verifies the Anthropic prompt-injection structure on the
// surfaces that carry third-party content. It is a no-op on trusted surfaces (the
// operator's own input, the model's own output): the structural rule is precisely
// that UNTRUSTED content must not behave like instructions. On the tool_result
// surface — where Anthropic says untrusted content belongs and where indirect
// injection lives — every structural hit is escalated to Critical.
type untrustedContentDetector struct{}

func newUntrustedContentDetector() Detector { return untrustedContentDetector{} }

func (untrustedContentDetector) Class() string { return classUntrusted }

func (untrustedContentDetector) Inspect(in GuardrailInput) []Detection {
	if !untrustedSurface(in.Surface) {
		return nil
	}
	out := scan(classUntrusted, in.Text, untrustedStructureShapes)
	if in.Surface == SurfaceToolResult {
		for i := range out {
			out[i].Severity = sdkmodel.SeverityCritical
		}
	}
	return out
}
