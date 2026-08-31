// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package recording

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// redactedValue replaces any URL-parameter value the recorder must not persist.
const redactedValue = "[REDACTED]"

// maxParamLen bounds a persisted URL-parameter value. Route parameters are
// identifiers by convention (model.ID, refs); anything longer is replaced by a
// one-way digest token so the frame stays joinable without disclosing content.
const maxParamLen = 128

// redactParam bounds and scrubs one URL-parameter value before it persists in a
// frame (minimal data, docs/SECURITY-HARDENING.md). Identifiers pass through; an email-shaped
// value (operator PII — keeps erasure away from frames), a credential-
// shaped value, or an overlong value never lands raw. Module-private by design:
// each AGPL module owns its redactor (the knowledge module's precedent).
func redactParam(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if looksSecret(v) || strings.ContainsRune(v, '@') {
		return redactedValue
	}
	if len(v) > maxParamLen {
		sum := sha256.Sum256([]byte(v))
		return "sha256:" + hex.EncodeToString(sum[:8])
	}
	return v
}

// looksSecret reports whether a value is credential-shaped: an inline
// key=value credential, a bearer/JWT/PEM fragment, or one of the engine's own
// opaque token prefixes. Detection-only; the caller substitutes.
func looksSecret(s string) bool {
	low := strings.ToLower(s)
	if i := strings.Index(low, "://"); i >= 0 {
		rest := low[i+3:]
		if at := strings.IndexByte(rest, '@'); at >= 0 && strings.IndexByte(rest[:at], ':') >= 0 {
			return true
		}
	}
	for _, kw := range []string{"token=", "secret=", "password=", "passwd=", "api_key=", "apikey=", "access_key=", "client_secret="} {
		if strings.Contains(low, kw) {
			return true
		}
	}
	// Substrings, not prefixes: a JWT/PEM/token embedded mid-value must not
	// escape redaction ("tok-eyJ..." is still a JWT).
	for _, p := range []string{"olvs_", "olvk_", "bearer ", "eyj", "-----begin"} {
		if strings.Contains(low, p) {
			return true
		}
	}
	return false
}

// redactParams applies redactParam to every URL parameter, dropping empties.
func redactParams(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = redactParam(v)
	}
	return out
}

// boundedQueryKeys joins sorted query-parameter NAMES (values are never
// captured), redacting each key — a bare query segment with no '=' lands its
// WHOLE token in key position (Go's url.Query), so a key can carry a secret or
// an email — and bounding the joined list so a hostile query string cannot
// bloat the immutable trail.
func boundedQueryKeys(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	const maxKeys, maxKeyLen, maxTotal = 32, 64, 512
	var b strings.Builder
	n := 0
	for _, k := range keys {
		if n >= maxKeys || b.Len() >= maxTotal {
			b.WriteString(",…")
			break
		}
		k = redactParam(k)
		if len(k) > maxKeyLen {
			k = k[:maxKeyLen]
		}
		if n > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		n++
	}
	return b.String()
}
