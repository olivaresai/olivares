// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import "strings"

// RedactCredentials is the CREDENTIAL-ONLY entry point, built for the console log
// viewer (wired at cmd/olivares/boot.go into api.WithLogRedactor). It applies
// the catalog's credential rules — the vendor-prefixed secret shapes, URL
// authority userinfo, and the key=value rule — and deliberately NOT the PII shapes.
//
// The exclusion is a decision, not an oversight, and it is the property the
// encargo cared about most: an operator must not lose the diagnosis. The PII
// shapes include ipv4, mac-address and email, and in a log those ARE the
// diagnosis — "dial tcp 10.9.8.7:5432: connection refused" says nothing once the
// address is a marker, and "LDAP: user alice@corp.local not found" says nothing
// once the user is. A log line is read by an authenticated system:admin inside
// the trust boundary; the trade is worth making there and NOT worth making on a
// surface that leaves the machine, which is why the support bundle keeps
// RedactText and its full catalog.
//
// A credential is different in kind: its exposure is an incident on its own, no
// matter who read it, and nothing about "10.9.8.7" is recovered by also printing
// the password that failed against it.
func RedactCredentials(s string) (string, int) {
	if s == "" {
		return "", 0
	}
	counts := map[string]int{}
	out := s
	for i := 0; i < redactMaxPasses; i++ {
		next := redactCredentialsOnce(out, counts)
		if next == out {
			break
		}
		out = next
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	return out, total
}

// redactCredentialsOnce is one ordered pass of the credential rules. The order is
// the same one redactOnce documents and for the same reasons: shapes before the
// broad rules, so a PEM block is gone whole before anything can chew its BEGIN
// line; url-userinfo before key=value, so a DSN whose username is a sensitive
// word is not mangled across the '@'.
func redactCredentialsOnce(s string, counts map[string]int) string {
	out := s
	for _, sh := range secretShapes {
		if n := len(sh.re.FindAllString(out, -1)); n > 0 {
			counts[sh.rule] += n
			out = sh.re.ReplaceAllString(out, redactionMarker(sh.rule))
		}
	}
	out = urlUserinfoRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := urlUserinfoRe.FindStringSubmatch(m)
		if len(sub) != 3 || cleanMarkerRe.MatchString(sub[2]) {
			return m
		}
		counts["url-userinfo"]++
		return sub[1] + redactionMarker("url-userinfo") + "@"
	})
	out = keyValueSecretRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := keyValueSecretRe.FindStringSubmatch(m)
		if len(sub) != 3 || cleanMarkerRe.MatchString(sub[2]) {
			return m
		}
		counts["key-value-secret"]++
		return sub[1] + redactionMarker("key-value-secret")
	})
	return out
}

// RedactText is the single reusable text-redaction entry point outside this
// module. It applies the canonical secret and PII catalog and returns the
// redacted text plus the number of redaction markers it emitted.
func RedactText(s string) (string, int) {
	out := scrubExcerpt(s)
	return out, strings.Count(out, "[redacted")
}

// ContainsSecretOrPII reports whether s matches the canonical secret or PII
// catalog without returning any matched value.
func ContainsSecretOrPII(s string) bool {
	// Redaction markers still match the broad key=value recognizer (for example,
	// token=[redacted]) but carry no secret. Report exactly those catalog matches
	// that the canonical redactor would change: for every input, a change made by
	// RedactText implies this function returns true. The support-bundle writer uses
	// that fixed-point guarantee as its fail-closed final guard.
	candidate := strings.ReplaceAll(s, "[REDACTED]", "[redacted]")
	if !containsSecretOrPII(candidate) {
		return false
	}
	return scrubExcerpt(candidate) != candidate
}
