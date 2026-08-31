// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"regexp"
	"sort"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// This file is the module's EXPORTED sensitivity-classification surface.
// It groups the existing deterministic detection catalogs (pii.go: secretShapes,
// piiShapes, keyValueSecretRe, Luhn-validated cards) into named SENSITIVITY
// CLASSES so a sibling data plane (knowledge, via a composition-root adapter) can
// discover and label PII/secrets in the content it governs WITHOUT reimplementing
// the rule catalog — the detectors stay single-owner here, the labels and
// the DLP policy that consumes them live with the data. Unlike the
// guardrail scan() (presence is the signal, detect.go), discovery needs OCCURRENCE
// COUNTS per rule, so ClassifySensitivity counts matches. It never returns a raw
// matched value: a hit is {class, rule, count, severity} — explainable, minimal
// data (docs/SECURITY-HARDENING.md).

// Sensitivity classes: the DLP-facing grouping of the detection rules. They are a
// SEPARATE axis from the knowledge classification ladder (public…secret — an
// access-control clearance): a class names WHAT kind of sensitive value content
// carries; the DLP policy decides per class whether that content may egress.
const (
	// SensSecretCredential — credentials and key material (the secretShapes
	// catalog + the key=value secret rule). Exposure is an immediate incident.
	SensSecretCredential = "secret.credential"
	// SensPIIFinancial — financial identifiers (IBAN, Luhn-valid cards, wallets).
	SensPIIFinancial = "pii.financial"
	// SensPIIGovernmentID — government-issued personal ids (SSN, ES NIF/NIE).
	SensPIIGovernmentID = "pii.government_id"
	// SensPIIContact — direct contact identifiers (email).
	SensPIIContact = "pii.contact"
	// SensPIINetwork — network identifiers attributable to a person (IP, MAC).
	SensPIINetwork = "pii.network"
)

// SensitivityCatalogVersion identifies the rule catalog backing the classifier.
// It is recorded on scan evidence so a result is reproducible: the same
// content and the same version always yield the same hits. Bump it whenever the
// shapes or the rule→class mapping change.
//
// v2 adds the url-userinfo rule: a credential inside a URL authority
// (postgres://user:pw@host) matched nothing in v1, and its value only ever
// disappeared by accident — when the email shape happened to consume
// "password@host.tld", which stops the moment the host is `localhost` or an IP.
const SensitivityCatalogVersion = "s27-pii.v2"

// SensitivityHit is one classified rule family found in a text: the sensitivity
// class, the named rule (explainability — the same stable rule ids the guardrail
// detections use), how many times it matched, and the rule's severity. It NEVER
// carries the matched value.
type SensitivityHit struct {
	Class    string
	Rule     string
	Count    int
	Severity sdkmodel.Severity
}

// sensitivityClassFor maps a catalog rule id to its sensitivity class. Every rule
// in secretShapes is a credential by construction; the PII shapes are grouped by
// what the identifier reveals.
func sensitivityClassFor(rule string) string {
	switch rule {
	case "iban", "crypto-wallet", "credit-card":
		return SensPIIFinancial
	case "us-ssn", "es-nif", "es-nie":
		return SensPIIGovernmentID
	case "email":
		return SensPIIContact
	case "ipv4", "mac-address":
		return SensPIINetwork
	default:
		return SensSecretCredential
	}
}

// ClassifySensitivity runs the module's deterministic PII/secret catalog over one
// text and returns the sensitivity hits, counting occurrences per rule. It is
// pure, zero-egress and reproducible (the forensic contract): no model, no
// network, no state. The output is sorted (class, then rule) so two runs over the
// same text are byte-identical. An empty text yields nil.
func ClassifySensitivity(text string) []SensitivityHit {
	if text == "" {
		return nil
	}
	var out []SensitivityHit
	for _, s := range secretShapes {
		if n := len(s.re.FindAllString(text, -1)); n > 0 {
			out = append(out, SensitivityHit{
				Class: sensitivityClassFor(s.rule), Rule: s.rule, Count: n, Severity: s.sev,
			})
		}
	}
	if n := len(urlUserinfoRe.FindAllString(text, -1)); n > 0 {
		out = append(out, SensitivityHit{
			Class: SensSecretCredential, Rule: "url-userinfo", Count: n, Severity: sdkmodel.SeverityHigh,
		})
	}
	if n := len(keyValueSecretRe.FindAllString(text, -1)); n > 0 {
		out = append(out, SensitivityHit{
			Class: SensSecretCredential, Rule: "key-value-secret", Count: n, Severity: sdkmodel.SeverityHigh,
		})
	}
	for _, s := range piiShapes {
		if n := len(s.re.FindAllString(text, -1)); n > 0 {
			out = append(out, SensitivityHit{
				Class: sensitivityClassFor(s.rule), Rule: s.rule, Count: n, Severity: s.sev,
			})
		}
	}
	// Credit cards: only Luhn-valid candidates count (the same validation the
	// guardrail applies — a random 16-digit id is not a card).
	cards := 0
	for _, cand := range ccCandidateRe.FindAllString(text, -1) {
		if luhnValid(cand) {
			cards++
		}
	}
	if cards > 0 {
		out = append(out, SensitivityHit{
			Class: SensPIIFinancial, Rule: "credit-card", Count: cards, Severity: sdkmodel.SeverityHigh,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Class != out[j].Class {
			return out[i].Class < out[j].Class
		}
		return out[i].Rule < out[j].Rule
	})
	return out
}

// RedactSensitive removes every value the catalog recognizes from text, replacing
// each match with a labeled marker, and reports what was removed as the same
// SensitivityHit shape ClassifySensitivity produces.
//
// It exists because for a long time the catalog only ever LABELED. The knowledge
// plane's own redactor removed ten credential shapes and exactly one PII shape
// (email), so an IBAN, a card number, an SSN or a NIF/NIE survived ingest byte for
// byte and reached the chunk store and the embedder — while this catalog, running
// AFTER that scrub, dutifully reported them. Detection that only labels is not
// minimization: the value is still there.
//
// Order matters and is not an implementation detail. The key=value rule runs
// first, so a secret that ALSO matches a shape is removed once and keeps its key
// ("api_key=[REDACTED:key-value-secret]") — structure without the secret. Cards
// run last because their candidate pattern is the broadest: by then the digits of
// an IBAN or an SSN are already gone, so the Luhn check sees only what is left.
//
// Like ClassifySensitivity it is pure, zero-egress and reproducible: no model, no
// network, no state, and the hits come back sorted, so two runs over the same text
// are byte-identical. It never returns, logs or embeds a matched value.
func RedactSensitive(text string) (string, []SensitivityHit) {
	if text == "" {
		return "", nil
	}
	counts := map[string]int{}
	out := text
	for i := 0; i < redactMaxPasses; i++ {
		next := redactOnce(out, counts)
		if next == out {
			break
		}
		out = next
	}

	hits := make([]SensitivityHit, 0, len(counts))
	for rule, n := range counts {
		hits = append(hits, SensitivityHit{
			Class: sensitivityClassFor(rule), Rule: rule, Count: n, Severity: severityForRule(rule),
		})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Class != hits[j].Class {
			return hits[i].Class < hits[j].Class
		}
		return hits[i].Rule < hits[j].Rule
	})
	return out, hits
}

// redactMaxPasses bounds the fixed-point loop, mirroring scrubExcerptMaxPasses.
const redactMaxPasses = 4

// redactOnce is ONE ordered pass of every catalog rule, accumulating per-rule
// counts. The order and the outer loop are both load-bearing, and this package
// has already paid for learning why (see scrubExcerpt in detect.go):
//
//   - SHAPES BEFORE key=value. The key=value rule's value class stops at
//     whitespace, so on "secret=-----BEGIN PRIVATE KEY-----\n…-----END…" it
//     consumes only "-----BEGIN" — and the PEM block shape, which needs its own
//     BEGIN, can then never match, so the KEY BODY survives. Running the shapes
//     first means the whole block is gone before the broad rule sees anything.
//   - ITERATE TO A FIXED POINT. One rule's match can be blocked by a character
//     another rule would have removed; a fuzzer found four digits of a card
//     surviving a single pass here. Whatever one rule leaves behind is offered to
//     every rule again.
func redactOnce(s string, counts map[string]int) string {
	out := s
	for _, sh := range secretShapes {
		if n := len(sh.re.FindAllString(out, -1)); n > 0 {
			counts[sh.rule] += n
			out = sh.re.ReplaceAllString(out, redactionMarker(sh.rule))
		}
	}
	// URL userinfo keeps the scheme and the '@', so the HOST survives: a redaction
	// that also removed where the connection was going would cost the reader the
	// diagnosis and buy nothing. It runs before key=value and before the PII
	// shapes for the reasons scrubExcerptOnce spells out.
	out = urlUserinfoRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := urlUserinfoRe.FindStringSubmatch(m)
		if len(sub) != 3 || cleanMarkerRe.MatchString(sub[2]) {
			return m
		}
		counts["url-userinfo"]++
		return sub[1] + redactionMarker("url-userinfo") + "@"
	})
	// key=value keeps submatch 1 (the key and separator) and drops the value —
	// unless the value is ALREADY a marker a shape left behind, which would
	// relabel a precisely-identified secret as a generic one and count it twice.
	out = keyValueSecretRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := keyValueSecretRe.FindStringSubmatch(m)
		if len(sub) != 3 {
			return m
		}
		// Skip a value that is EXACTLY a marker a precise shape already left:
		// re-redacting it would relabel an identified secret as a generic one and
		// count it twice. A value that is a marker PLUS residue is NOT skipped —
		// that residue is what a partial match left behind, and removing it is the
		// whole reason this runs to a fixed point.
		if cleanMarkerRe.MatchString(sub[2]) {
			return m
		}
		counts["key-value-secret"]++
		return sub[1] + redactionMarker("key-value-secret")
	})
	for _, sh := range piiShapes {
		if n := len(sh.re.FindAllString(out, -1)); n > 0 {
			counts[sh.rule] += n
			out = sh.re.ReplaceAllString(out, redactionMarker(sh.rule))
		}
	}
	// Cards last and Luhn-checked one candidate at a time, so a long digit run
	// that is not a card is left exactly as it was.
	out = ccCandidateRe.ReplaceAllStringFunc(out, func(m string) string {
		if !luhnValid(m) {
			return m
		}
		counts["credit-card"]++
		// The candidate pattern allows a separator after EVERY digit, including
		// the last, so a match can swallow the space that follows the number.
		// Give the trailing separators back rather than eating a character of the
		// document. (Classification never noticed because it only counts.)
		return redactionMarker("credit-card") + trailingSeparators(m)
	})
	return out
}

// cleanMarkerRe matches a value that is EXACTLY a redaction marker and nothing
// else, so a later rule can tell "already handled" from "handled, with residue".
var cleanMarkerRe = regexp.MustCompile(`^\[REDACTED:[a-z0-9-]+\]$`)

// trailingSeparators returns the run of spaces and dashes at the END of a card
// candidate. The candidate pattern allows a separator after every digit, so a
// match can swallow the space that follows the number; only that suffix is given
// back, never any digit — an earlier version trimmed digits from BOTH ends and
// re-inserted a slice of the MIDDLE of the match, putting fifteen digits of the
// card back into the document.
func trailingSeparators(m string) string {
	i := len(m)
	for i > 0 && (m[i-1] == ' ' || m[i-1] == '-') {
		i--
	}
	return m[i:]
}

// redactionMarker is the replacement left in place of a removed value. It names
// the RULE and never the value, so a reader (and a test) can see that redaction
// happened and which detector fired.
func redactionMarker(rule string) string { return "[REDACTED:" + rule + "]" }

// severityForRule returns a catalog rule's severity, so a redaction hit carries
// the same severity the equivalent classification hit would.
func severityForRule(rule string) sdkmodel.Severity {
	for _, s := range secretShapes {
		if s.rule == rule {
			return s.sev
		}
	}
	for _, s := range piiShapes {
		if s.rule == rule {
			return s.sev
		}
	}
	switch rule {
	case "key-value-secret", "url-userinfo", "credit-card":
		return sdkmodel.SeverityHigh
	default:
		return sdkmodel.SeverityMedium
	}
}
