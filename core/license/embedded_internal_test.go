// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package license

import (
	"bytes"
	"testing"
)

// TestReleaseKeyResolution exercises the unexported ldflags seam directly, with no
// -tags release build needed: releaseKey() reads releasePublicKeyB64 LIVE, so
// setting the var here drives the exact decode path the linker's `-X` injection
// feeds at build time. It pins the contract relied on by DefaultPublicKey/KeyOrigin
// in a release build — including that a malformed value yields an error, NEVER a
// panic (a panic at package init would brick unrelated subcommands and boot).
func TestReleaseKeyResolution(t *testing.T) {
	saved := releasePublicKeyB64
	t.Cleanup(func() { releasePublicKeyB64 = saved })

	// Empty → no key, no error (a non-release build, or an un-injected one).
	releasePublicKeyB64 = ""
	if k, err := releaseKey(); k != nil || err != nil {
		t.Fatalf("empty: got (%v, %v), want (nil, nil)", k, err)
	}

	// A valid base64-std Ed25519 public key decodes back to the same bytes.
	pub, _, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	releasePublicKeyB64 = EncodeKey(pub)
	got, err := releaseKey()
	if err != nil {
		t.Fatalf("valid key: unexpected error %v", err)
	}
	if !bytes.Equal(got, pub) {
		t.Fatalf("valid key: decoded %x, want %x", got, pub)
	}

	// Surrounding whitespace is trimmed (ldflags values and file reads may carry a
	// trailing newline).
	releasePublicKeyB64 = "  " + EncodeKey(pub) + "\n"
	if got, err := releaseKey(); err != nil || !bytes.Equal(got, pub) {
		t.Fatalf("padded key: got (%x, %v), want the key and no error", got, err)
	}

	// Non-base64 → error, nil key (never a panic).
	releasePublicKeyB64 = "not valid base64 !!!"
	if k, err := releaseKey(); k != nil || err == nil {
		t.Fatalf("malformed b64: got (%v, %v), want (nil, error)", k, err)
	}

	// Valid base64 but the wrong length (e.g. a private key pasted by mistake) →
	// error, nil key.
	releasePublicKeyB64 = EncodeKey([]byte("too short for an ed25519 public key"))
	if k, err := releaseKey(); k != nil || err == nil {
		t.Fatalf("wrong length: got (%v, %v), want (nil, error)", k, err)
	}
}
