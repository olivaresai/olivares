// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package redact is the minimal-data guard shared by the Olivares AI capture
// connectors. A cooperative-telemetry connector sees raw tool inputs and shell
// commands (a hook's tool_input, a Bash full_command) that can carry secrets or
// PII; the product persists only access edges, never payloads (docs/SECURITY-HARDENING.md). This
// package turns a raw, possibly sensitive string into a safe resource reference:
// it strips credentials from URLs and connection strings, scrubs known secret
// shapes (cloud keys, tokens, JWTs, private keys, key=value pairs), and offers a
// SHA-256 helper for de-duplicating a sensitive value without storing it.
//
// It is deliberately conservative and stdlib-only (no third-party dependency, no
// engine import), so it cannot itself become a data-exfiltration path. A scrub is
// allowed to over-redact (a false positive costs a less precise resource ref); it
// must never under-redact a value it was asked to clean.
package redact

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"
)

// Placeholder is the marker substituted for a redacted secret. It is bracketed
// and ASCII so a resource ref stays greppable and a test can assert "no raw
// secret survived" by checking the cleaned value both lacks the secret and (when
// something was redacted) contains this marker.
const Placeholder = "[REDACTED]"

// secretPattern is one labeled secret shape. label names the kind so a scrubbed
// string shows WHAT was removed (e.g. "[REDACTED:aws-access-key]") without
// revealing the value.
type secretPattern struct {
	label string
	re    *regexp.Regexp
}

// wholeMatchPatterns redact the entire match. They target high-precision secret
// shapes (a fixed prefix + charset) so false positives are rare; order does not
// matter because the shapes do not overlap in practice.
var wholeMatchPatterns = []secretPattern{
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
}

// keyValueRe matches a sensitive key followed by its value (in =, :, or
// key="value" form). Submatch 1 is the key+separator to preserve; submatch 2 is
// the value to redact, so the cleaned string keeps the structure
// ("api_key=[REDACTED]") while losing the secret. The value charset stops at
// whitespace and common delimiters so only the secret token is taken.
var keyValueRe = regexp.MustCompile(
	`(?i)((?:api[_-]?key|secret|token|password|passwd|pwd|access[_-]?key|auth|client[_-]?secret)["']?\s*[:=]\s*["']?)([^\s"'&;]{4,})`)

// Scrub returns s with every recognized secret replaced by a labeled
// placeholder, and reports whether anything was redacted. It applies the
// key=value rule first (so a value that also looks like a token is removed once,
// keeping its key) then the standalone shapes. It never returns the raw secret.
func Scrub(s string) (string, bool) {
	if s == "" {
		return "", false
	}
	redacted := false

	out := keyValueRe.ReplaceAllStringFunc(s, func(m string) string {
		sub := keyValueRe.FindStringSubmatch(m)
		if len(sub) != 3 {
			return m
		}
		redacted = true
		return sub[1] + Placeholder
	})

	for _, p := range wholeMatchPatterns {
		if p.re.MatchString(out) {
			redacted = true
			out = p.re.ReplaceAllString(out, "[REDACTED:"+p.label+"]")
		}
	}
	return out, redacted
}

// Clean returns s with secrets scrubbed, discarding the redaction flag. It is the
// single-return convenience for callers that only need the safe string.
func Clean(s string) string {
	out, _ := Scrub(s)
	return out
}

// ContainsSecret reports whether s contains a recognized secret shape. It is the
// detection half of Scrub, used to flag (not just clean) a value.
func ContainsSecret(s string) bool {
	if keyValueRe.MatchString(s) {
		return true
	}
	for _, p := range wholeMatchPatterns {
		if p.re.MatchString(s) {
			return true
		}
	}
	return false
}

// SanitizeURL reduces a URL to a non-sensitive resource reference: it drops the
// userinfo (basic-auth credentials), the query string and the fragment — the
// three places a token hides — keeping scheme, host and path. The remaining path
// is still scrubbed for embedded secrets.
//
// It is hardened against scheme-confusion: a value whose authority does not parse
// to a Host (e.g. "user:pass@host/x" with no scheme, or an opaque URI) but that
// carries a "userinfo@" authority is re-parsed with a forced authority so the
// credentials are still stripped. A value that is genuinely not URL-shaped is
// scrubbed as opaque text, so nothing ever leaks.
func SanitizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if cleaned, ok := stripURL(raw); ok {
		return cleaned
	}
	// Empty-host parse (no scheme, or opaque): if the authority carries a userinfo
	// ("...@..."), force an authority parse so the credentials are stripped.
	if strings.Contains(authority(raw), "@") {
		if cleaned, ok := stripURL("//" + raw); ok {
			return cleaned
		}
	}
	// Fallback: the value did not parse to a URL with a Host (a hostless or opaque
	// URI such as "s3:///logs?token=..."). Still drop a query/fragment that could
	// hide a credential before scrubbing the remainder for secret shapes. A clean
	// hostless URI (no "?"/"#") is left intact. Over-redaction is acceptable here;
	// under-redaction is not — the package must never leak a value it was asked to
	// clean (see the package doc).
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	return Clean(raw)
}

// stripURL parses raw as a URL with a real Host, drops its userinfo, query and
// fragment, scrubs the remainder, and reports success. It fails (ok=false) when
// raw has no Host so the caller can try a fallback.
func stripURL(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return Clean(u.String()), true
}

// authority returns the leading segment of s up to the first path, query or
// fragment delimiter — the part where a "userinfo@host" would live.
func authority(s string) string {
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		return s[:i]
	}
	return s
}

// SanitizeDSN reduces a database connection string to a non-sensitive reference.
// For URL-style DSNs (postgres://user:pass@host/db) it strips the password from
// the userinfo and the query (where libpq-style "password=" lives) while keeping
// host and database — the resource identity. A non-URL DSN (key/value or DSN
// fragments) is scrubbed for "password=" and known secret shapes.
func SanitizeDSN(raw string) string {
	raw = strings.TrimSpace(raw)
	if u, err := url.Parse(raw); err == nil && u.Host != "" && u.Scheme != "" {
		if u.User != nil {
			if name := u.User.Username(); name != "" {
				u.User = url.User(name)
			} else {
				u.User = nil
			}
		}
		u.RawQuery = ""
		u.Fragment = ""
		out, _ := Scrub(u.String())
		return out
	}
	out, _ := Scrub(raw)
	return out
}

// Hash returns the lowercase hex SHA-256 of s, a stable fingerprint used to
// de-duplicate or reference a sensitive value (a redacted detail, a full
// command) without persisting the value itself (docs/SECURITY-HARDENING.md).
func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
