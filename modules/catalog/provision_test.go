// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package catalog

import (
	"crypto/ed25519"
	"testing"
)

// TestWithSigningKeyProvisioning proves the construction-time signing-key provisioning
// (the on-by-default boot path, wire.go: catalog.New(WithSigningKey(...))): a valid key
// makes the module sign and PIN (expectedFingerprint becomes the trust anchor), while a
// nil or malformed key is ignored — the honest unsigned/unpinned posture of a node with
// no key. It never duplicates the audit key (a separate, independent signer).
func TestWithSigningKeyProvisioning(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)

	m := New(WithSigningKey(priv))
	if m.signingKey() == nil {
		t.Fatal("WithSigningKey(valid) must provision the signer (entries would ship unsigned)")
	}
	if got, want := m.expectedFingerprint(), keyFingerprint(pub); got != want {
		t.Fatalf("expectedFingerprint = %q, want %q (the configured key is the verify trust anchor)", got, want)
	}

	// A nil key is ignored — a node with no key keeps the unsigned/unpinned posture.
	if u := New(WithSigningKey(nil)); u.signingKey() != nil || u.expectedFingerprint() != "" {
		t.Error("WithSigningKey(nil) must leave the module unsigned (the unpinned default is preserved)")
	}
	// A malformed (short) key is ignored, never half-provisioned.
	if s := New(WithSigningKey(ed25519.PrivateKey{1, 2, 3})); s.signingKey() != nil {
		t.Error("WithSigningKey(short) must be ignored, never partially provisioned")
	}
}
