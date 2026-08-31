// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

// a realistic whsec_ secret: the bytes after the prefix are base64.
const testSecret = "whsec_" + "c2VjcmV0LWtleS0zMi1ieXRlcy1sb25nLWVub3VnaCE=" // base64("secret-key-32-bytes-long-enough!")

func TestVerifyWebhookSignatureRoundTrip(t *testing.T) {
	body := []byte(`{"type":"event","id":"event_1","data":{"type":"session.status_idled","id":"sesn_1"}}`)
	key := deriveWebhookKey(testSecret)
	if len(key) == 0 {
		t.Fatal("derived key is empty")
	}
	header := signWebhookBody(testSecret, body)
	if !verifyWebhookSignature(key, body, header) {
		t.Fatalf("a correctly signed body must verify; header=%q", header)
	}
}

func TestVerifyWebhookSignatureRejectsTamperedBody(t *testing.T) {
	body := []byte(`{"type":"event","id":"event_1"}`)
	key := deriveWebhookKey(testSecret)
	header := signWebhookBody(testSecret, body)
	tampered := []byte(`{"type":"event","id":"event_HIJACK"}`)
	if verifyWebhookSignature(key, tampered, header) {
		t.Fatal("a tampered body must NOT verify against the original signature")
	}
}

func TestVerifyWebhookSignatureRejectsWrongKey(t *testing.T) {
	body := []byte(`{"a":"b"}`)
	header := signWebhookBody(testSecret, body)
	wrong := deriveWebhookKey("whsec_" + base64.StdEncoding.EncodeToString([]byte("a-different-32-byte-secret-key!!!")))
	if verifyWebhookSignature(wrong, body, header) {
		t.Fatal("a signature under a different key must NOT verify (fail closed)")
	}
}

func TestVerifyWebhookSignatureFailClosedOnEmpty(t *testing.T) {
	body := []byte(`{}`)
	key := deriveWebhookKey(testSecret)
	if verifyWebhookSignature(nil, body, signWebhookBody(testSecret, body)) {
		t.Error("an empty key must never verify")
	}
	if verifyWebhookSignature(key, body, "") {
		t.Error("an empty signature header must never verify (unsigned payload not trusted)")
	}
	if verifyWebhookSignature(key, body, "   ") {
		t.Error("a blank signature header must never verify")
	}
}

// TestVerifyWebhookSignatureAcceptsAlternateEncodings proves the verifier tolerates the
// documented framings: bare base64, "v1,<base64>" (Standard Webhooks), "sha256=<hex>", and a
// space-separated list (key rotation) where only one entry matches.
func TestVerifyWebhookSignatureAcceptsAlternateEncodings(t *testing.T) {
	body := []byte(`payload-bytes`)
	key := deriveWebhookKey(testSecret)
	mac := hmac.New(sha256.New, key)
	mac.Write(body)
	sum := mac.Sum(nil)
	b64 := base64.StdEncoding.EncodeToString(sum)
	hx := hex.EncodeToString(sum)

	cases := map[string]string{
		"bare base64":       b64,
		"v1 prefixed":       "v1," + b64,
		"sha256 hex prefix": "sha256=" + hx,
		"rotation list":     "v1,AAAA " + "v1," + b64, // first entry bogus, second valid
	}
	for name, header := range cases {
		if !verifyWebhookSignature(key, body, header) {
			t.Errorf("%s: header %q should verify", name, header)
		}
	}
}

func TestDeriveWebhookKeyRequiresPrefix(t *testing.T) {
	if k := deriveWebhookKey("whsec_"); k != nil {
		t.Error("an empty secret body yields no key")
	}
	if k := deriveWebhookKey(""); k != nil {
		t.Error("an empty secret yields no key")
	}
	// A non-base64 remainder falls back to raw bytes (a valid key, just not base64).
	if k := deriveWebhookKey("whsec_not-base64-!!!"); len(k) == 0 {
		t.Error("a non-base64 secret body should fall back to raw bytes, not an empty key")
	}
}
