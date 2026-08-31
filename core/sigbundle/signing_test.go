// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sigbundle

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	return pub, priv
}

// TestTagsAreDisjoint is the domain-separation guarantee: no registered tag may be a
// prefix of another, and every tag must end in '\n'. If two tags collided, a signature
// minted for one could verify as the other — the exact cross-protocol forgery the
// registry exists to prevent.
func TestTagsAreDisjoint(t *testing.T) {
	if a, b := TagPrefixCollision(Tags); a != "" || b != "" {
		t.Fatalf("domain tags collide: %q is a prefix of %q", a, b)
	}
	seen := map[string]bool{}
	for _, tag := range Tags {
		if !strings.HasSuffix(tag, "\n") {
			t.Errorf("tag %q does not end in a newline", tag)
		}
		if seen[tag] {
			t.Errorf("tag %q is registered twice", tag)
		}
		seen[tag] = true
	}
}

// TestSignVerifyRoundTrip: a signature verifies under its own tag and key.
func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv := testKey(t)
	payload := []byte(`{"hello":"world"}`)
	for _, tag := range Tags {
		sig := Sign(tag, payload, priv)
		if err := Verify(tag, payload, sig, pub); err != nil {
			t.Errorf("Verify under %q: %v", tag, err)
		}
	}
}

// TestCrossTagForgeryRejected is the load-bearing security test: a signature made under
// one domain tag must NOT verify under another, even with the same key and payload.
func TestCrossTagForgeryRejected(t *testing.T) {
	pub, priv := testKey(t)
	payload := []byte("same bytes, different intent")
	sig := Sign(TagUpdateManifest, payload, priv)

	if err := Verify(TagDDILBundle, payload, sig, pub); err != ErrBadSignature {
		t.Fatalf("an update-manifest signature verified as a DDIL bundle (err=%v) — domain separation broken", err)
	}
	if err := Verify(TagSecurityAdvisories, payload, sig, pub); err != ErrBadSignature {
		t.Fatalf("an update-manifest signature verified as an advisories feed (err=%v)", err)
	}
}

// TestVerifyFailClosed: a nil/short key never yields "verified".
func TestVerifyFailClosed(t *testing.T) {
	_, priv := testKey(t)
	payload := []byte("x")
	sig := Sign(TagDDILBundle, payload, priv)

	if err := Verify(TagDDILBundle, payload, sig, nil); err != ErrNoKey {
		t.Errorf("nil key: err=%v, want ErrNoKey", err)
	}
	if err := Verify(TagDDILBundle, payload, sig, ed25519.PublicKey{1, 2, 3}); err != ErrNoKey {
		t.Errorf("short key: err=%v, want ErrNoKey", err)
	}
}

// TestVerifyTamperedPayload: flipping any payload byte fails the signature.
func TestVerifyTamperedPayload(t *testing.T) {
	pub, priv := testKey(t)
	payload := []byte("authentic payload")
	sig := Sign(TagSecurityAdvisories, payload, priv)

	tampered := append([]byte(nil), payload...)
	tampered[0] ^= 0xff
	if err := Verify(TagSecurityAdvisories, tampered, sig, pub); err != ErrBadSignature {
		t.Fatalf("tampered payload verified: err=%v", err)
	}
}

// TestVerifyWrongKey: a signature from another key fails.
func TestVerifyWrongKey(t *testing.T) {
	_, priv := testKey(t)
	otherPub, _ := testKey(t)
	payload := []byte("y")
	sig := Sign(TagUpdateManifest, payload, priv)
	if err := Verify(TagUpdateManifest, payload, sig, otherPub); err != ErrBadSignature {
		t.Fatalf("signature verified against the wrong key: err=%v", err)
	}
}

// TestUnregisteredTag: SigningInput panics, Verify returns ErrUnknownTag.
func TestUnregisteredTag(t *testing.T) {
	pub, _ := testKey(t)
	if err := Verify("olivares.not-a-real-tag.v1\n", []byte("x"), make([]byte, ed25519.SignatureSize), pub); err != ErrUnknownTag {
		t.Errorf("Verify with unregistered tag: err=%v, want ErrUnknownTag", err)
	}
	defer func() {
		if recover() == nil {
			t.Errorf("SigningInput with an unregistered tag did not panic")
		}
	}()
	_ = SigningInput("olivares.not-a-real-tag.v1\n", []byte("x"))
}

// TestSigningInputShape: exactly tag||payload, nothing more.
func TestSigningInputShape(t *testing.T) {
	payload := []byte("PAYLOAD")
	got := SigningInput(TagDDILBundle, payload)
	want := append([]byte(TagDDILBundle), payload...)
	if string(got) != string(want) {
		t.Fatalf("SigningInput = %q, want %q", got, want)
	}
}
