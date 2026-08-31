// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// webhookSecretPrefix is the documented prefix on a CMA webhook signing secret (a 32-byte
// secret shown once at endpoint creation). The bytes after it are base64-encoded.
const webhookSecretPrefix = "whsec_"

// webhookSigHeader is the request header carrying the HMAC of a signed CMA webhook.
const webhookSigHeader = "X-Webhook-Signature"

// deriveWebhookKey turns a whsec_-prefixed signing secret into the HMAC key. Per the
// Standard-Webhooks scheme the bytes after the prefix are base64; the key is those decoded
// bytes. If the remainder is not valid base64 (a non-standard secret), the raw remainder
// bytes are used so a valid round-trip is still possible — over-acceptance of the key
// encoding never weakens the signature check itself (a wrong key simply never verifies).
func deriveWebhookKey(secret string) []byte {
	rem := strings.TrimPrefix(strings.TrimSpace(secret), webhookSecretPrefix)
	if rem == "" {
		return nil
	}
	if b, err := base64.StdEncoding.DecodeString(rem); err == nil && len(b) > 0 {
		return b
	}
	if b, err := base64.RawStdEncoding.DecodeString(rem); err == nil && len(b) > 0 {
		return b
	}
	return []byte(rem)
}

// computeWebhookMAC is the raw HMAC-SHA256 of body keyed by key. The connector signs the
// raw request body (the strongest public evidence for the CMA scheme: "generate a signature
// from the request body"). The exact signed-string framing is beta-gated; if Anthropic
// finalizes a `{id}.{timestamp}.{body}` construction, only this function changes.
func computeWebhookMAC(key, body []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	return mac.Sum(nil)
}

// verifyWebhookSignature reports whether headerVal authenticates body under key. It is
// FAIL-CLOSED: an empty key or header is false (an unsigned payload is never trusted). The
// X-Webhook-Signature value may be a space-separated list (key rotation) of tokens, each
// optionally prefixed with a scheme id (e.g. "v1,<sig>" Standard-Webhooks, or "sha256=<sig>");
// each candidate signature is decoded as base64 (std/raw) or hex and compared in constant
// time (hmac.Equal). Any one match accepts.
func verifyWebhookSignature(key, body []byte, headerVal string) bool {
	if len(key) == 0 || strings.TrimSpace(headerVal) == "" {
		return false
	}
	want := computeWebhookMAC(key, body)
	for _, tok := range strings.Fields(headerVal) {
		sig := tok
		// Strip a scheme prefix: "v1,<sig>" (Standard Webhooks) or "sha256=<sig>".
		if i := strings.IndexByte(sig, ','); i >= 0 {
			sig = sig[i+1:]
		}
		sig = strings.TrimPrefix(sig, "sha256=")
		if got, err := base64.StdEncoding.DecodeString(sig); err == nil && hmac.Equal(got, want) {
			return true
		}
		if got, err := base64.RawStdEncoding.DecodeString(sig); err == nil && hmac.Equal(got, want) {
			return true
		}
		if got, err := hex.DecodeString(sig); err == nil && hmac.Equal(got, want) {
			return true
		}
	}
	return false
}

// signWebhookBody is the test/helper inverse: it produces the base64 "v1,<sig>" header value
// a conformant transmitter would send for body under the given whsec_ secret. It lets the
// receiver's HMAC round-trip be proven without a live (beta-gated) endpoint.
func signWebhookBody(secret string, body []byte) string {
	mac := computeWebhookMAC(deriveWebhookKey(secret), body)
	return "v1," + base64.StdEncoding.EncodeToString(mac)
}
