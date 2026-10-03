// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package toolinstall

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/openpgp" //nolint:staticcheck
)

// Claude Code 2.1.286's manifest and its detached signature, as published on
// downloads.claude.ai (recorded 2026-10-01), verify in process under the
// embedded release key: a node with no gpg binary installs the same release.
func TestNativeOpenPGPVerifiesTheOfficialClaudeManifest(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "claude-manifest-2.1.286.json"))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(filepath.Join("testdata", "claude-manifest-2.1.286.sig.txt"))
	if err != nil {
		t.Fatal(err)
	}
	v := NativeOpenPGPVerifier{}
	rep, err := v.Verify(context.Background(), []byte(claudeReleaseKey), ClaudeReleaseKeyFingerprint, sig, data)
	if err != nil {
		t.Fatalf("official manifest: %v", err)
	}
	if rep.PrimaryFingerprint != ClaudeReleaseKeyFingerprint || rep.Verifier != "openpgp/go-x-crypto" || rep.Created.IsZero() {
		t.Fatalf("report = %+v", rep)
	}
	tampered := append(bytes.Clone(data), ' ')
	if _, err := v.Verify(context.Background(), []byte(claudeReleaseKey), ClaudeReleaseKeyFingerprint, sig, tampered); KindOf(err) != KindSignatureInvalid {
		t.Fatalf("tampered manifest = %v, want signature invalid", err)
	}
	if _, err := v.Verify(context.Background(), []byte(claudeReleaseKey), "0000000000000000000000000000000000000000", sig, data); KindOf(err) != KindVerificationUnavailable {
		t.Fatalf("wrong pin = %v, want verification unavailable", err)
	}
	raw := rawSignature(sig)
	for _, tc := range []struct {
		name      string
		signature []byte
	}{
		{"binary", raw},
		{"binary_trailing_whitespace", append(bytes.Clone(raw), []byte(" \t\r\n")...)},
		{"armored_trailing_whitespace", append(bytes.Clone(sig), []byte(" \t\r\n")...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := v.Verify(context.Background(), []byte(claudeReleaseKey), ClaudeReleaseKeyFingerprint, tc.signature, data); err != nil {
				t.Fatalf("single valid detached signature = %v", err)
			}
		})
	}
}

func TestNativeOpenPGPRefusesASignatureByAnotherKey(t *testing.T) {
	other, err := openpgp.NewEntity("someone else", "", "other@example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"version":"9.9.9"}`)
	var sig bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&sig, other, bytes.NewReader(data), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := (NativeOpenPGPVerifier{}).Verify(context.Background(), []byte(claudeReleaseKey), ClaudeReleaseKeyFingerprint, sig.Bytes(), data); KindOf(err) != KindSignatureInvalid {
		t.Fatalf("other key = %v, want signature invalid", err)
	}
}

func TestNativeOpenPGPRefusesMultipleSignatures(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "claude-manifest-2.1.286.json"))
	if err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(filepath.Join("testdata", "claude-manifest-2.1.286.sig.txt"))
	if err != nil {
		t.Fatal(err)
	}
	raw := rawSignature(sig)
	for _, tc := range []struct {
		name      string
		signature []byte
	}{
		{"binary", append(bytes.Clone(raw), raw...)},
		{"armored", append(bytes.Clone(sig), sig...)},
		{"armored_then_binary", append(bytes.Clone(sig), raw...)},
		{"binary_then_armored", append(append(bytes.Clone(raw), '\n'), sig...)},
		{"binary_after_bad_armor_preamble", append(append(append([]byte("-----BEGIN PGP SIGNATURE-----\n"), raw...), '\n'), sig...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := (NativeOpenPGPVerifier{}).Verify(context.Background(), []byte(claudeReleaseKey), ClaudeReleaseKeyFingerprint, tc.signature, data); KindOf(err) != KindSignatureInvalid {
				t.Fatalf("two valid detached signatures = %v, want signature invalid", err)
			}
		})
	}
}
