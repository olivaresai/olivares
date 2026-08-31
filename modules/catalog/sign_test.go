// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

// args bundles the canonical-preimage fields for a test entry.
type args struct {
	name, kind, slug, ver, sum, owner string
	spec                              map[string]any
}

func (a args) hash(t *testing.T) (hexStr string, raw []byte) {
	t.Helper()
	h, err := contentHash(a.name, a.kind, a.slug, a.ver, a.sum, a.owner, a.spec)
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h), h
}

func (a args) verify(storedHashHex, sig, pub, expectedFP string) verifyResult {
	return verify(storedHashHex, sig, pub, expectedFP, a.name, a.kind, a.slug, a.ver, a.sum, a.owner, a.spec)
}

// TestVerifyPinnedTrustAnchor proves the configured-key trust anchor: with a
// catalog key configured (expectedFP set), only a signature from THAT key over the
// matching hash verifies — a downgrade (stripped signature) and a substitution
// (signed by another key) are both rejected.
func TestVerifyPinnedTrustAnchor(t *testing.T) {
	a := args{"GitHub MCP", "mcp", "github", "1.0.0", "summary", "owner", map[string]any{"transport": "stdio", "endpoint": "npx x"}}
	hashHex, raw := a.hash(t)

	pubA, privA, _ := ed25519.GenerateKey(nil)
	fpA := keyFingerprint(pubA)
	sigA := base64.StdEncoding.EncodeToString(ed25519.Sign(privA, raw))
	pubAB64 := base64.StdEncoding.EncodeToString(pubA)

	// Pristine, signed by the configured key → verified.
	if r := a.verify(hashHex, sigA, pubAB64, fpA); !r.Verified || !r.HashOK || !r.SignatureOK {
		t.Errorf("pristine pinned: %+v", r)
	}

	// Downgrade: no signature while a key is configured → NOT verified.
	if r := a.verify(hashHex, "", "", fpA); r.Verified {
		t.Errorf("downgrade (stripped signature) verified, must not: %+v", r)
	}

	// Substitution: a valid signature under a DIFFERENT key → NOT verified, even
	// though the signature itself checks out against its carried key.
	pubB, privB, _ := ed25519.GenerateKey(nil)
	sigB := base64.StdEncoding.EncodeToString(ed25519.Sign(privB, raw))
	pubBB64 := base64.StdEncoding.EncodeToString(pubB)
	r := a.verify(hashHex, sigB, pubBB64, fpA)
	if r.Verified {
		t.Errorf("substitution (signed by another key) verified, must not: %+v", r)
	}
	if !r.SignatureOK {
		t.Errorf("the substituted signature is valid over its carried key, signature_ok should be true: %+v", r)
	}
	if r.SignedByFP == fpA {
		t.Error("substituted key fingerprint should differ from the configured key")
	}
}

// TestVerifyUnpinned proves the no-key posture: integrity rests on the content
// hash; an unsigned entry verifies by hash, a tampered one does not.
func TestVerifyUnpinned(t *testing.T) {
	a := args{"GitHub MCP", "mcp", "github", "1.0.0", "summary", "owner", map[string]any{"k": "v"}}
	hashHex, _ := a.hash(t)

	if r := a.verify(hashHex, "", "", ""); !r.Verified || !r.HashOK || r.Signed {
		t.Errorf("unpinned unsigned pristine: %+v", r)
	}
	// Tamper the spec without recomputing the stored hash → hash mismatch.
	tampered := args{a.name, a.kind, a.slug, a.ver, a.sum, a.owner, map[string]any{"k": "evil"}}
	if r := tampered.verify(hashHex, "", "", ""); r.Verified || r.HashOK {
		t.Errorf("tampered unsigned verified, must not: %+v", r)
	}
}

// TestContentHashDeterminism pins the integrity-critical hashing contract: nil and
// empty specs hash identically, map key order is irrelevant, and the display name
// is covered (a name change changes the hash).
func TestContentHashDeterminism(t *testing.T) {
	h1, _ := contentHash("n", "k", "s", "1.0.0", "sum", "o", nil)
	h2, _ := contentHash("n", "k", "s", "1.0.0", "sum", "o", map[string]any{})
	if !bytes.Equal(h1, h2) {
		t.Error("nil spec and empty spec must hash identically")
	}

	ha, _ := contentHash("n", "k", "s", "1.0.0", "sum", "o", map[string]any{"x": 1.0, "y": 2.0})
	hb, _ := contentHash("n", "k", "s", "1.0.0", "sum", "o", map[string]any{"y": 2.0, "x": 1.0})
	if !bytes.Equal(ha, hb) {
		t.Error("content hash must be independent of map key order")
	}

	hn, _ := contentHash("OTHER", "k", "s", "1.0.0", "sum", "o", map[string]any{"x": 1.0, "y": 2.0})
	if bytes.Equal(ha, hn) {
		t.Error("the display name must be covered by the content hash (a name change must change the hash)")
	}
}
