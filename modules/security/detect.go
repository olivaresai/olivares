// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"regexp"
	"strings"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// This file holds the SHARED detection primitives every guardrail detector uses:
// the labeled-pattern type, the scanner that turns a pattern set into minimal-data
// detections, and the excerpt scrubber that guarantees no detector ever surfaces a
// raw secret/PII value (docs/SECURITY-HARDENING.md). A detector is one explainable, reproducible
// rule family; the catalog of patterns IS the rule, so a finding can always be
// traced to a named rule + a framework reference (OWASP/ATLAS) for forensics and
// compliance.

// shape is one labeled detection pattern: a regexp, the severity and title it
// yields, the framework references, and whether its match is a SECRET/PII value
// that must never be echoed (redact=true → the excerpt is a label only).
type shape struct {
	rule  string
	re    *regexp.Regexp
	sev   sdkmodel.Severity
	title string
	// owasp and atlas are the rule's PRIMARY framework ids. owasp holds either an
	// OWASP LLM id ("LLM01:2025") or an OWASP-Agentic id ("ASI01"); scan() routes it
	// onto the correct axis by prefix, so existing rule literals are unchanged.
	owasp string
	atlas string
	// also carries ADDITIONAL cross-taxonomy ids a single rule evidences beyond its
	// primary pair — e.g. an indirect-injection rule that is LLM01:2025 (owasp) AND
	// AML.T0051.001 (atlas) AND ASI01 (also). Each id is routed onto its axis by prefix.
	also   []string
	redact bool
}

// scan runs the shapes over text and returns one detection per shape that matches
// (a rule fires at most once per inspection — the presence of the class is the
// signal, not the count). Excerpts are always redacted/clamped so no raw value
// leaves the detector.
func scan(class, text string, shapes []shape) []Detection {
	if text == "" {
		return nil
	}
	var out []Detection
	for _, s := range shapes {
		if m := s.re.FindString(text); m != "" {
			d := Detection{
				Class:    class,
				Rule:     s.rule,
				Severity: s.sev,
				Title:    s.title,
				Excerpt:  excerptFor(s, m),
			}
			// Route the rule's framework ids onto their axes by prefix: the
			// primary owasp/atlas pair plus any cross-taxonomy ids in `also`.
			d.addTaxonomy(s.owasp)
			d.addTaxonomy(s.atlas)
			for _, id := range s.also {
				d.addTaxonomy(id)
			}
			out = append(out, d)
		}
	}
	return out
}

// excerptFor builds the short, NON-SENSITIVE excerpt for a match. A secret/PII
// match (redact=true) is reduced to a label; any other match is clamped and then
// scrubbed so an incidental secret captured in the window is still removed.
func excerptFor(s shape, match string) string {
	if s.redact {
		return "[redacted:" + s.rule + "]"
	}
	return scrubExcerpt(clamp(strings.TrimSpace(match), maxExcerptLen))
}

// scrubExcerpt removes any secret OR PII shape from a free excerpt, so a non-PII
// detector (e.g. prompt-injection, a tool-misuse rule with a broad .* span) that
// captured a window containing a key, a token, an email, an SSN, an IBAN or a card
// number never surfaces it. It is the last line of the minimal-data guarantee
// (docs/SECURITY-HARDENING.md) — the excerpt that reaches a finding/response carries no raw value.
// scrubExcerptOnce is a single ordered pass of every rule.
func scrubExcerptOnce(s string) string {
	// Apply shaped secrets first so a broad key=value match cannot consume the
	// BEGIN line of a multi-line PEM before the private-key block shape sees it.
	out := s
	for _, p := range secretShapes {
		out = p.re.ReplaceAllString(out, "[redacted:"+p.rule+"]")
	}
	// URL userinfo before key=value AND before the PII shapes. Before key=value
	// because a DSN whose USERNAME happens to be a sensitive word
	// (https://token:x@host) would otherwise be consumed by the broad rule and
	// mangled across the '@'. Before the PII shapes because the email rule would
	// otherwise swallow "password@host.tld" and label a credential as contact PII
	// — which is how a DSN password used to disappear here: by accident, and only
	// when the host looked like a domain.
	out = urlUserinfoRe.ReplaceAllString(out, "$1[redacted:url-userinfo]@")
	out = keyValueSecretRe.ReplaceAllString(out, "$1[redacted]")
	for _, p := range piiShapes {
		out = p.re.ReplaceAllString(out, "[redacted:"+p.rule+"]")
	}
	// Credit cards are validated by Luhn (the generic piiShapes do not include them,
	// to avoid clobbering any benign long digit run); redact only valid candidates.
	out = ccCandidateRe.ReplaceAllStringFunc(out, func(m string) string {
		if luhnValid(m) {
			return "[redacted:credit-card]"
		}
		return m
	})
	return out
}

// scrubExcerptMaxPasses bounds the fixed-point loop. Three is already one more
// than any observed case needs; the bound exists so a future rule that rewrites
// its own output cannot spin here.
const scrubExcerptMaxPasses = 4

// scrubExcerpt redacts every secret and PII shape from an excerpt, REPEATEDLY,
// until the text stops changing.
//
// One pass was not enough, and the gap was a partial leak rather than a tidiness
// problem (found by FuzzScrubExcerpt, 2026-08-05). The rules run in a fixed order
// and one rule's match can be BLOCKED by a character another rule would have
// removed. Witness, reproduced deterministically:
//
//	in    "Api_keY=4 111111111111111-0000"
//	once  "Api_keY=[redacted:credit-card]0000"   <-- four digits of the card survive
//	twice "Api_keY=[redacted]"
//
// The space after `=` keeps keyValueSecretRe from matching (its value class stops
// at whitespace and then falls under the 4-character minimum), so the card rule
// runs on a candidate it can only consume up to a word boundary — and leaves the
// tail in the excerpt. An excerpt is what gets STORED and SHOWN, so those four
// digits were a real disclosure from the code whose job is to prevent exactly that.
//
// Iterating to a fixed point closes it structurally: whatever one rule leaves
// behind is offered to every rule again. It also makes the function idempotent,
// which is what the fuzzer asserts — but idempotence is the SYMPTOM being tested,
// not the property that matters.
func scrubExcerpt(s string) string {
	out := s
	for i := 0; i < scrubExcerptMaxPasses; i++ {
		next := scrubExcerptOnce(out)
		if next == out {
			return out
		}
		out = next
	}
	return out
}

// containsSecretOrPII reports whether s holds any recognized secret or PII shape.
// Used to gate output-validation findings (a response that leaks a secret/PII) and
// to corroborate exfil heuristics, without revealing the value.
func containsSecretOrPII(s string) bool {
	if keyValueSecretRe.MatchString(s) {
		return true
	}
	// This must move in lockstep with scrubExcerptOnce. ContainsSecretOrPII (the
	// support bundle's fail-closed final guard) documents the invariant "for every
	// input, a change made by RedactText implies this function returns true" — a
	// rule the scrubber knows and this predicate does not would break exactly that
	// guarantee, silently, on the one path whose job is to fail closed.
	if urlUserinfoRe.MatchString(s) {
		return true
	}
	for _, p := range append(append([]shape{}, secretShapes...), piiShapes...) {
		if p.re.MatchString(s) {
			return true
		}
	}
	for _, candidate := range ccCandidateRe.FindAllString(s, -1) {
		if luhnValid(candidate) {
			return true
		}
	}
	return false
}
