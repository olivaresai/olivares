// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"

	jose "github.com/go-jose/go-jose/v4"
)

// cardIssuerKeys mints a dedicated Ed25519 card-issuance keypair and returns the private
// signing JWK plus a public trust anchor JWKS (as an operator would configure it).
func cardIssuerKeys(t *testing.T, kid string) (jose.JSONWebKey, *jose.JSONWebKeySet) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519: %v", err)
	}
	privJWK := jose.JSONWebKey{Key: priv, KeyID: kid, Algorithm: string(jose.EdDSA)}
	pubJWK := jose.JSONWebKey{Key: pub, KeyID: kid, Algorithm: string(jose.EdDSA)}
	return privJWK, &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{pubJWK}}
}

// A card richer than the AgentCard struct: iconUrl, documentationUrl,
// defaultInputModes/defaultOutputModes and per-skill tags/description are NOT all
// modeled by the typed struct, so a typed re-marshal would drop them. Signing the raw
// bytes MUST preserve and bind them (review finding F1).
const richCardJSON = `{
  "name": "olivares-governed-agent",
  "description": "An agent whose card Olivares signs.",
  "version": "1.0.0",
  "iconUrl": "https://agent.example/icon.png",
  "documentationUrl": "https://agent.example/docs",
  "defaultInputModes": ["text/plain","application/json"],
  "defaultOutputModes": ["text/plain"],
  "supportedInterfaces": [{"url":"https://agent.example/a2a","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
  "skills": [{"id":"s1","name":"search","description":"web search","tags":["read","net"]}]
}`

func verifyServed(t *testing.T, served []byte, anchor *jose.JSONWebKeySet) (trustLevel, string) {
	t.Helper()
	rc, err := parseCard(served)
	if err != nil {
		t.Fatalf("parseCard: %v", err)
	}
	return verifyCard(context.Background(), rc, anchor, nil)
}

// TestSignCardJSON_RichCardRoundTripsAndPreservesFields is the F1 regression: a card
// with fields the typed struct does not model is signed and SERVED with those fields
// intact, and verifies as trustVerified — the signature binds the full card, not a
// stripped projection.
func TestSignCardJSON_RichCardRoundTripsAndPreservesFields(t *testing.T) {
	privJWK, anchor := cardIssuerKeys(t, "olivares-card-key-1")
	signer, err := NewJOSECardSigner(privJWK)
	if err != nil {
		t.Fatalf("NewJOSECardSigner: %v", err)
	}
	signed, err := SignCardJSON([]byte(richCardJSON), signer)
	if err != nil {
		t.Fatalf("SignCardJSON: %v", err)
	}
	// The served signed card STILL carries every rich field (no lossy projection).
	var served map[string]any
	if err := json.Unmarshal(signed, &served); err != nil {
		t.Fatalf("signed card not JSON: %v", err)
	}
	for _, k := range []string{"iconUrl", "documentationUrl", "defaultInputModes", "defaultOutputModes"} {
		if _, ok := served[k]; !ok {
			t.Errorf("signed card dropped field %q (lossy projection)", k)
		}
	}
	if sigs, _ := served["signatures"].([]any); len(sigs) != 1 {
		t.Fatalf("want exactly 1 signature, got %v", served["signatures"])
	}
	// And the full card verifies against the trust anchor.
	if level, detail := verifyServed(t, signed, anchor); level != trustVerified {
		t.Fatalf("rich card did not verify: level=%q detail=%q", level, detail)
	}
}

// TestSignCardJSON_PreservesExistingSignatures proves a second issuer's signature is
// appended, not clobbered.
func TestSignCardJSON_PreservesExistingSignatures(t *testing.T) {
	priv1, _ := cardIssuerKeys(t, "key-1")
	priv2, anchor2 := cardIssuerKeys(t, "key-2")
	s1, _ := NewJOSECardSigner(priv1)
	s2, _ := NewJOSECardSigner(priv2)

	once, err := SignCardJSON([]byte(richCardJSON), s1)
	if err != nil {
		t.Fatalf("first sign: %v", err)
	}
	twice, err := SignCardJSON(once, s2)
	if err != nil {
		t.Fatalf("second sign: %v", err)
	}
	var served map[string]any
	_ = json.Unmarshal(twice, &served)
	if sigs, _ := served["signatures"].([]any); len(sigs) != 2 {
		t.Fatalf("want 2 signatures after two issuers, got %v", served["signatures"])
	}
	// The second issuer's signature still verifies against its own anchor.
	if level, detail := verifyServed(t, twice, anchor2); level != trustVerified {
		t.Fatalf("second issuer signature did not verify: %q %q", level, detail)
	}
}

// TestSignCardJSON_TamperBreaksSignature proves the signature binds the card content:
// mutating a signed card after issuance makes it fail verification.
func TestSignCardJSON_TamperBreaksSignature(t *testing.T) {
	privJWK, anchor := cardIssuerKeys(t, "key-1")
	signer, _ := NewJOSECardSigner(privJWK)
	signed, err := SignCardJSON([]byte(richCardJSON), signer)
	if err != nil {
		t.Fatalf("SignCardJSON: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(signed, &m)
	m["name"] = "impersonated-agent" // tamper AFTER signing
	tampered, _ := json.Marshal(m)
	if level, _ := verifyServed(t, tampered, anchor); level == trustVerified {
		t.Fatal("a tampered card must NOT verify as trustVerified")
	}
}

// TestNewJOSECardSigner_RejectsUnsafeKeys proves the fail-closed construction: a
// symmetric/"none" algorithm or a public-only key is refused.
func TestNewJOSECardSigner_RejectsUnsafeKeys(t *testing.T) {
	if _, err := NewJOSECardSigner(jose.JSONWebKey{Key: []byte("shared-secret-32-bytes-long!!!!!"), Algorithm: "HS256"}); err == nil {
		t.Error("a symmetric (HS256) key must be rejected for card signing")
	}
	_, anchor := cardIssuerKeys(t, "key-1")
	if _, err := NewJOSECardSigner(anchor.Keys[0]); err == nil {
		t.Error("a public-only key must be rejected for card signing")
	}
	if _, err := NewJOSECardSigner(jose.JSONWebKey{Key: []byte("x"), Algorithm: "none"}); err == nil {
		t.Error(`the "none" algorithm must be rejected`)
	}
}

// TestSignCardJSON_FailClosed proves nil signer and non-object input are hard errors.
func TestSignCardJSON_FailClosed(t *testing.T) {
	if _, err := SignCardJSON([]byte(richCardJSON), nil); err == nil {
		t.Error("SignCardJSON with a nil signer must error")
	}
	privJWK, _ := cardIssuerKeys(t, "key-1")
	signer, _ := NewJOSECardSigner(privJWK)
	if _, err := SignCardJSON([]byte(`["not","an","object"]`), signer); err == nil {
		t.Error("SignCardJSON on a non-object card must error")
	}
}
