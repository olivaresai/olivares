// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"regexp"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// classContent is the content-policy guardrail class.
const classContent = "content"

// This file is a CONTENT POLICY filter. Unlike the prompt-injection / jailbreak /
// PII detectors there is no single canonical OWASP id for "harmful content" in the
// LLM/Agentic taxonomies (the closest framework signal is misinformation, LLM09,
// which is a different concern), so these shapes carry no OWASP id (owasp: "").
//
// The catalog is INTENTIONALLY CONSERVATIVE and NON-EXHAUSTIVE: it is a defensible
// STARTER set that targets a handful of high-confidence, instruction-shaped requests
// for harm (malware authorship, weapon/explosive synthesis, illicit drug synthesis,
// self-harm method, violence planning). It is meant to be extended — adding a
// category is adding a shape. The filter is DETECTIVE: it FLAGS a surface for human
// review, it does not pass moral judgement and it never blocks on its own (blocking
// requires the persisted, governed enforcement policy; docs/SECURITY-HARDENING.md).
//
// Patterns key on INSTRUCTIONS/REQUESTS to do harm ("how to make a bomb", "write a
// keylogger"), NOT on mere mention of a topic, to keep false positives low — while
// still erring toward DETECTING a real request (a guardrail may over-flag; it must
// not silently miss an attack). Matches here are short signal phrases, not sensitive
// values, so scan()'s clamped+scrubbed excerpt is sufficient and no shape needs
// redact:true; if a future shape could capture a sensitive specific, set redact:true.

// contentShapes is the conservative, extensible catalog of harmful-content request
// patterns. Each rule names its category in the title.
var contentShapes = []shape{
	// Malware / offensive-tooling authorship: a request to PRODUCE malicious code.
	{
		rule:  "malware-creation",
		re:    regexp.MustCompile(`(?i)write (a )?(ransomware|keylogger|rootkit|botnet|trojan)|build a botnet|create malware|exploit (a )?(zero[- ]day|cve)`),
		sev:   sdkmodel.SeverityHigh,
		title: "content: malware / offensive-tooling creation request",
	},
	// Weapons / explosives synthesis instructions.
	{
		rule:  "weapon-synthesis",
		re:    regexp.MustCompile(`(?i)how to (make|build) a bomb|synthesi(z|s)e (a )?(nerve agent|explosive)|build an? (ied|explosive device)`),
		sev:   sdkmodel.SeverityHigh,
		title: "content: weapon / explosive synthesis request",
	},
	// Illicit drug synthesis instructions.
	{
		rule:  "drug-synthesis",
		re:    regexp.MustCompile(`(?i)how to (make|synthesi(z|s)e|cook) (meth|methamphetamine|fentanyl|mdma)`),
		sev:   sdkmodel.SeverityMedium,
		title: "content: illicit drug synthesis request",
	},
	// Self-harm / suicide method instructions.
	{
		rule:  "self-harm",
		re:    regexp.MustCompile(`(?i)how to (kill myself|commit suicide)|most (effective|painless) way to (die|kill myself)`),
		sev:   sdkmodel.SeverityHigh,
		title: "content: self-harm / suicide instructions request",
	},
	// Violence / harm planning against a person.
	{
		rule:  "violence",
		re:    regexp.MustCompile(`(?i)how to (poison|kill) (someone|a person)|untraceable (poison|murder)`),
		sev:   sdkmodel.SeverityHigh,
		title: "content: violence / harm planning request",
	},
}

// contentDetector is the content-policy guardrail. It is a deterministic, explainable
// FLAG over a conservative catalog of harmful-content requests. It runs on every
// surface: a harmful request can arrive as an input prompt, leak into an output, or
// be smuggled through tool arguments.
type contentDetector struct{}

func newContentDetector() Detector { return contentDetector{} }

func (contentDetector) Class() string { return classContent }

func (contentDetector) Inspect(in GuardrailInput) []Detection {
	return scan(classContent, in.Text, contentShapes)
}
