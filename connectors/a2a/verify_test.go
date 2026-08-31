// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import (
	"encoding/json"
	"testing"
)

// verifyBytes parses card bytes and verifies them against a trust anchor (no jku).
func verifyBytes(t *testing.T, cardBytes, trustJWKS []byte) trustLevel {
	t.Helper()
	rc, err := parseCard(cardBytes)
	if err != nil {
		t.Fatalf("parse card: %v", err)
	}
	anchor, err := parseJWKS(trustJWKS)
	if err != nil {
		t.Fatalf("parse jwks: %v", err)
	}
	lvl, _ := verifyCard(t.Context(), rc, anchor, nil)
	return lvl
}

func TestVerifyCardValidSignature(t *testing.T) {
	priv, jwks := keypair(t, "k1")
	card := signedCardBytes(t, priv, "k1", baseCard("researcher"))
	if lvl := verifyBytes(t, card, jwks); lvl != trustVerified {
		t.Fatalf("a validly signed card should be trustVerified, got %q", lvl)
	}
}

func TestVerifyCardTamperedIsUnverified(t *testing.T) {
	priv, jwks := keypair(t, "k1")
	card := signedCardBytes(t, priv, "k1", baseCard("researcher"))

	// Tamper with a signed field AFTER signing — the canonical payload no longer
	// matches the signature, so verification must fail (never silently trusted).
	var obj map[string]any
	if err := json.Unmarshal(card, &obj); err != nil {
		t.Fatal(err)
	}
	obj["description"] = "TAMPERED"
	tampered, _ := json.Marshal(obj)

	if lvl := verifyBytes(t, tampered, jwks); lvl != trustUnverified {
		t.Fatalf("a tampered card must be trustUnverified, got %q", lvl)
	}
}

func TestVerifyCardWrongKeyIsUnverified(t *testing.T) {
	priv, _ := keypair(t, "k1")
	_, otherJWKS := keypair(t, "k1") // different key, same kid
	card := signedCardBytes(t, priv, "k1", baseCard("researcher"))
	if lvl := verifyBytes(t, card, otherJWKS); lvl != trustUnverified {
		t.Fatalf("a card signed by a different key must be trustUnverified, got %q", lvl)
	}
}

func TestVerifyCardUnsigned(t *testing.T) {
	_, jwks := keypair(t, "k1")
	unsigned, _ := json.Marshal(baseCard("researcher")) // no signatures field
	if lvl := verifyBytes(t, unsigned, jwks); lvl != trustUnsigned {
		t.Fatalf("an unsigned card should be trustUnsigned, got %q", lvl)
	}
}

func TestVerifyCardSignedButNoAnchorIsUnverified(t *testing.T) {
	// A signature present but no operator trust anchor and no jku-fetch → identity
	// not established → UNTRUSTED (not silently accepted).
	priv, _ := keypair(t, "k1")
	card := signedCardBytes(t, priv, "k1", baseCard("researcher"))
	if lvl := verifyBytes(t, card, nil); lvl != trustUnverified {
		t.Fatalf("signed card with no trust anchor should be trustUnverified, got %q", lvl)
	}
}
