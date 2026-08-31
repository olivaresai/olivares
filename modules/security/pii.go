// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"regexp"

	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// classPII is the PII/secret guardrail class.
const classPII = "pii"

// keyValueSecretRe matches a sensitive key followed by its value (= or :), keeping
// the key (submatch 1) so a scrub yields "api_key=[redacted]" — structure without
// the secret. Shared with detect.go's excerpt scrubber.
var keyValueSecretRe = regexp.MustCompile(
	`(?i)((?:api[_-]?key|secret|token|password|passwd|pwd|access[_-]?key|auth|client[_-]?secret|cookie|hmac|session[_-]?id|credential|nonce|salt|signature|session[_-]?key|csrf|xsrf)["']?\s*[:=]\s*["']?)([^\s"'&;]{4,})`)

// urlUserinfoRe matches the userinfo of a URL authority (scheme://userinfo@host),
// keeping the scheme (submatch 1) so a scrub yields "postgres://[redacted]@host"
// — the WHERE survives and only the credential goes. Like keyValueSecretRe it is
// submatch-preserving rather than a whole-match `shape`, because replacing the
// whole match would take the scheme and the '@' with it.
//
// It closes a real gap, and the tree had already paid for it once: a database DSN
// (postgres://user:password@host/db) matched NO rule in this catalog. The value
// only ever disappeared by accident, when the email shape happened to consume
// "password@host.tld" — which stops working the moment the host is `localhost` or
// a bare IP. cmd/olivares/supportbundle.go:262 had to hand-roll
// supportValueHasAuthorityUserinfo for the config projection precisely because
// this catalog could not answer.
//
// It requires the PASSWORD SEPARATOR — `scheme://user:secret@host`, not
// `scheme://user@host` — and that boundary is a decision with a measured cost on
// both sides.
//
// The support bundle's own hand-rolled check treats ANY userinfo as credential
// material (cmd/olivares/supportbundle.go), and for its purpose that is right: the
// question there is "may this config value be PUBLISHED", and the answer for a
// bare username is reasonably no. This rule answers a different question in
// several more places. A hit here can DENY a governed file read outright
// (modules/sessions/workspace_dlp.go classifyContent, deny mode: one hit denies),
// label knowledge at ingest, and gate the inference proxy. And this repository
// explicitly documents `postgres://olivares_app@db/olivares?sslmode=verify-full`
// as a VALID, safe configuration (cmd/olivares/envfile_test.go, "passwordless
// postgres URL ok") — as is `git+ssh://git@host/repo`, which is how most of the
// world clones. Flagging those as HIGH credentials would deny reads over strings
// the product tells operators to use.
//
// The console log floor (core/api/log_redact.go) keeps the wider "any userinfo"
// rule on purpose: there the only cost of over-redacting is a username on a screen,
// and nothing is denied. The narrow rule lives where a false positive is an outage.
var urlUserinfoRe = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)([^\s/?#@"']*:[^\s/?#@"']*)@`)

// Honest boundary: an opaque secret in free text with neither a recognized
// key/value label, a URL authority nor a high-precision secret shape cannot be
// detected without treating arbitrary diagnostic text as secret and producing
// broad false positives.

// secretShapes are high-precision credential shapes (fixed prefix + charset). They
// map to OWASP LLM02:2025 (Sensitive Information Disclosure) and are HIGH severity —
// a leaked credential is an immediate exposure. Presidio ships no secret recognizer
// (the verified-taxonomy gap), so these are the module's own, mirroring the
// knowledge module's redactor and the common detect-secrets/gitleaks shapes.
var secretShapes = []shape{
	{rule: "aws-access-key", re: regexp.MustCompile(`(?:AKIA|ASIA|AGPA|AIDA|AROA|ANPA|ANVA)[0-9A-Z]{16}`), sev: sdkmodel.SeverityHigh, title: "AWS access key exposed", owasp: "LLM02:2025", redact: true},
	{rule: "github-token", re: regexp.MustCompile(`gh[pousr]_[0-9A-Za-z]{36,255}`), sev: sdkmodel.SeverityHigh, title: "GitHub token exposed", owasp: "LLM02:2025", redact: true},
	{rule: "github-pat", re: regexp.MustCompile(`github_pat_[0-9A-Za-z_]{22,255}`), sev: sdkmodel.SeverityHigh, title: "GitHub fine-grained PAT exposed", owasp: "LLM02:2025", redact: true},
	{rule: "slack-token", re: regexp.MustCompile(`xox[baprs]-[0-9A-Za-z-]{10,}`), sev: sdkmodel.SeverityHigh, title: "Slack token exposed", owasp: "LLM02:2025", redact: true},
	{rule: "google-api-key", re: regexp.MustCompile(`AIza[0-9A-Za-z\-_]{35}`), sev: sdkmodel.SeverityHigh, title: "Google API key exposed", owasp: "LLM02:2025", redact: true},
	{rule: "anthropic-key", re: regexp.MustCompile(`sk-ant-[0-9A-Za-z_-]{20,}`), sev: sdkmodel.SeverityHigh, title: "Anthropic API key exposed", owasp: "LLM02:2025", redact: true},
	{rule: "openai-key", re: regexp.MustCompile(`sk-[0-9A-Za-z]{20,}`), sev: sdkmodel.SeverityHigh, title: "OpenAI-style API key exposed", owasp: "LLM02:2025", redact: true},
	{rule: "jwt", re: regexp.MustCompile(`eyJ[0-9A-Za-z_-]{8,}\.[0-9A-Za-z_-]{8,}\.[0-9A-Za-z_-]{8,}`), sev: sdkmodel.SeverityHigh, title: "JWT exposed", owasp: "LLM02:2025", redact: true},
	{rule: "private-key", re: regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z ]+ )?PRIVATE KEY-----.*?-----END (?:[A-Z ]+ )?PRIVATE KEY-----`), sev: sdkmodel.SeverityCritical, title: "Private key exposed", owasp: "LLM02:2025", redact: true},
	{rule: "bearer-token", re: regexp.MustCompile(`(?i)bearer\s+[0-9A-Za-z._~+/=-]{12,}`), sev: sdkmodel.SeverityHigh, title: "Bearer token exposed", owasp: "LLM02:2025", redact: true},
}

// piiShapes are personal-data shapes (the locale-independent Presidio core set plus
// ES NIF/NIE). They map to OWASP LLM02:2025 and are MEDIUM severity. Credit cards
// are handled separately (Luhn-validated) to avoid the regex over-matching any long
// digit run. Every PII shape redacts its match.
var piiShapes = []shape{
	{rule: "email", re: regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`), sev: sdkmodel.SeverityMedium, title: "email address (PII)", owasp: "LLM02:2025", redact: true},
	{rule: "iban", re: regexp.MustCompile(`\b[A-Z]{2}[0-9]{2}[A-Z0-9]{11,30}\b`), sev: sdkmodel.SeverityMedium, title: "IBAN (PII)", owasp: "LLM02:2025", redact: true},
	{rule: "ipv4", re: regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4][0-9]|1?[0-9]?[0-9])\.){3}(?:25[0-5]|2[0-4][0-9]|1?[0-9]?[0-9])\b`), sev: sdkmodel.SeverityLow, title: "IP address (PII)", owasp: "LLM02:2025", redact: true},
	{rule: "mac-address", re: regexp.MustCompile(`\b(?:[0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}\b`), sev: sdkmodel.SeverityLow, title: "MAC address (PII)", owasp: "LLM02:2025", redact: true},
	{rule: "crypto-wallet", re: regexp.MustCompile(`\b(?:bc1|[13])[a-zA-HJ-NP-Z0-9]{25,39}\b`), sev: sdkmodel.SeverityMedium, title: "crypto wallet address (PII)", owasp: "LLM02:2025", redact: true},
	{rule: "us-ssn", re: regexp.MustCompile(`\b[0-9]{3}-[0-9]{2}-[0-9]{4}\b`), sev: sdkmodel.SeverityMedium, title: "US SSN (PII)", owasp: "LLM02:2025", redact: true},
	{rule: "es-nif", re: regexp.MustCompile(`\b[0-9]{8}[A-Za-z]\b`), sev: sdkmodel.SeverityMedium, title: "Spanish NIF/DNI (PII)", owasp: "LLM02:2025", redact: true},
	{rule: "es-nie", re: regexp.MustCompile(`\b[XYZxyz][0-9]{7}[A-Za-z]\b`), sev: sdkmodel.SeverityMedium, title: "Spanish NIE (PII)", owasp: "LLM02:2025", redact: true},
}

// ccCandidateRe finds candidate card numbers (13–19 digits, optional spaces/dashes)
// before the Luhn check rejects accidental long digit runs.
var ccCandidateRe = regexp.MustCompile(`\b(?:[0-9][ -]?){13,19}\b`)

// piiDetector is the PII/secrets guardrail. It is the module's authoritative
// minimal-data detector: it reports WHAT class of sensitive value appeared (so a
// finding/redaction can be produced) without ever surfacing the value itself
// (docs/SECURITY-HARDENING.md). It runs on every surface (input, output, tool_args).
type piiDetector struct{}

func newPIIDetector() Detector { return piiDetector{} }

func (piiDetector) Class() string { return classPII }

func (piiDetector) Inspect(in GuardrailInput) []Detection {
	out := scan(classPII, in.Text, secretShapes)
	// URL userinfo (postgres://user:pw@host) is shape-independent too, and is
	// reported before the key=value rule for the same reason it is scrubbed first:
	// it is the more precise account of the same bytes.
	if urlUserinfoRe.MatchString(in.Text) {
		out = append(out, Detection{
			Class: classPII, Rule: "url-userinfo", Severity: sdkmodel.SeverityHigh,
			Title: "credential in a URL authority exposed", Excerpt: "[redacted:url-userinfo]",
		}.tagged("LLM02:2025"))
	}
	// The key=value secret rule (api_key=…, password:…) is shape-independent.
	if keyValueSecretRe.MatchString(in.Text) {
		out = append(out, Detection{
			Class: classPII, Rule: "key-value-secret", Severity: sdkmodel.SeverityHigh,
			Title: "secret in key/value form exposed", Excerpt: "[redacted:key-value-secret]",
		}.tagged("LLM02:2025"))
	}
	out = append(out, scan(classPII, in.Text, piiShapes)...)
	// Credit cards: a candidate must pass the Luhn checksum to count (avoids
	// flagging any 16-digit id). The matched value is never echoed.
	for _, cand := range ccCandidateRe.FindAllString(in.Text, -1) {
		if luhnValid(cand) {
			out = append(out, Detection{
				Class: classPII, Rule: "credit-card", Severity: sdkmodel.SeverityHigh,
				Title: "credit card number (PII)", Excerpt: "[redacted:credit-card]",
			}.tagged("LLM02:2025"))
			break // one detection per inspection
		}
	}
	return out
}

// luhnValid reports whether the digits in s satisfy the Luhn checksum, ignoring
// spaces and dashes. A run with fewer than 13 digits is rejected.
func luhnValid(s string) bool {
	var digits []int
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits = append(digits, int(r-'0'))
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := digits[i]
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}
