// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import "regexp"

// This is the knowledge module's OWN redactor. It deliberately does NOT import
// connectors/internal/redact (an Apache connector-internal package the AGPL module
// must not depend on, and which a future ingest "fetch" stage could apply at the
// wrong stage): the module owns the AUTHORITATIVE redaction that runs before any
// content is chunked, embedded, indexed, hashed-for-storage or returned (docs/SECURITY-HARDENING.md
// §3). A scrub may OVER-redact (a false positive costs a less precise chunk); it
// must never UNDER-redact a value it was asked to clean. The same redactor cleans
// chunk text, prompt templates and agent memory.

// redactPlaceholder is the marker substituted for a redacted secret. It stays
// greppable so a test can assert both "the raw secret is gone" and "redaction
// happened" on the cleaned value.
const redactPlaceholder = "[REDACTED]"

// secretShape is one labeled secret/PII pattern. The label names what was removed
// (e.g. "[REDACTED:aws-access-key]") without revealing the value.
type secretShape struct {
	label string
	re    *regexp.Regexp
}

// wholeShapes redact the entire match: high-precision secret shapes (fixed prefix
// + charset) plus email PII. Order is irrelevant; the shapes do not overlap.
var wholeShapes = []secretShape{
	{"aws-access-key", regexp.MustCompile(`(?:AKIA|ASIA|AGPA|AIDA|AROA|ANPA|ANVA)[0-9A-Z]{16}`)},
	{"github-token", regexp.MustCompile(`gh[pousr]_[0-9A-Za-z]{36,255}`)},
	{"github-pat", regexp.MustCompile(`github_pat_[0-9A-Za-z_]{22,255}`)},
	{"slack-token", regexp.MustCompile(`xox[baprs]-[0-9A-Za-z-]{10,}`)},
	{"google-api-key", regexp.MustCompile(`AIza[0-9A-Za-z\-_]{35}`)},
	{"anthropic-key", regexp.MustCompile(`sk-ant-[0-9A-Za-z_-]{20,}`)},
	{"openai-key", regexp.MustCompile(`sk-[0-9A-Za-z]{20,}`)},
	{"jwt", regexp.MustCompile(`eyJ[0-9A-Za-z_-]{8,}\.[0-9A-Za-z_-]{8,}\.[0-9A-Za-z_-]{8,}`)},
	{"private-key", regexp.MustCompile(`-----BEGIN (?:[A-Z]+ )?PRIVATE KEY-----`)},
	{"bearer-token", regexp.MustCompile(`(?i)bearer\s+[0-9A-Za-z._~+/=-]{8,}`)},
	{"email", regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)},
}

// keyValueRe matches a sensitive key followed by its value (=, :, or key="value").
// Submatch 1 is the key+separator to preserve; submatch 2 is the value to redact,
// so the cleaned string keeps the structure ("api_key=[REDACTED]") while losing
// the secret.
var keyValueRe = regexp.MustCompile(
	`(?i)((?:api[_-]?key|secret|token|password|passwd|pwd|access[_-]?key|auth|client[_-]?secret)["']?\s*[:=]\s*["']?)([^\s"'&;]{4,})`)

// scrub returns s with every recognized secret/PII shape replaced by a labeled
// placeholder, and the count of distinct redactions performed. It applies the
// key=value rule first (so a value that also matches a token shape is removed once,
// keeping its key) then the standalone shapes. It never returns a raw secret.
func scrub(s string) (string, int) {
	if s == "" {
		return "", 0
	}
	count := 0

	out := keyValueRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := keyValueRe.FindStringSubmatch(m)
		if len(sub) != 3 {
			return m
		}
		count++
		return sub[1] + redactPlaceholder
	})

	for _, p := range wholeShapes {
		matches := p.re.FindAllString(out, -1)
		if len(matches) == 0 {
			continue
		}
		count += len(matches)
		out = p.re.ReplaceAllString(out, "[REDACTED:"+p.label+"]")
	}
	return out, count
}

// containsSecret reports whether s contains a recognized secret/PII shape (the
// detection half of scrub), used to gate findings without cleaning.
func containsSecret(s string) bool {
	if keyValueRe.MatchString(s) {
		return true
	}
	for _, p := range wholeShapes {
		if p.re.MatchString(s) {
			return true
		}
	}
	return false
}

// scrubWith runs the module's authoritative write-path minimization: the WIRED
// redactor when the composition root supplied one (the security module's full
// catalog — the same rules the classifier reports on), and the built-in shapes
// otherwise.
//
// The fallback is deliberate and it is a floor, not a default worth having: it
// removes credentials and email and NOTHING else, which is precisely the gap that
// let IBANs, cards, SSNs and NIF/NIEs reach the chunk store in the clear. An
// unwired minimizer must degrade to the historical behavior, never below it — but
// a deployment that leaves it unwired is running the weaker guarantee, and
// Start() says so.
//
// It returns the cleaned text, the number of values removed, and WHAT was removed
// (class, rule, count — never a value), so a document whose IBAN is gone can still
// be reported as having carried one. Detection that survives minimization is the
// point: the governance signal must not die with the data.
func (m *Module) scrubWith(s string) (string, int, []SensitivityHit) {
	if s == "" {
		return "", 0, nil
	}
	if m != nil && m.redactor != nil {
		clean, hits := m.redactor.Redact(s)
		n := 0
		for _, h := range hits {
			n += h.Count
		}
		return clean, n, hits
	}
	clean, n := scrub(s)
	return clean, n, nil
}
