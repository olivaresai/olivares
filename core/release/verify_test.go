// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package release

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

// signManifest builds a checksums.txt for the given files and an Ed25519
// detached signature over its exact bytes, the way the release pipeline does.
func signManifest(t *testing.T, priv ed25519.PrivateKey, files map[string][]byte) (checksums, sig []byte) {
	t.Helper()
	var b strings.Builder
	for name, data := range files {
		sum := sha256.Sum256(data)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	checksums = []byte(b.String())
	raw := ed25519.Sign(priv, checksums)
	sig = []byte(base64.StdEncoding.EncodeToString(raw))
	return checksums, sig
}

func TestVerifyHappyPath(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	artifact := []byte("the enterprise binary bytes")
	name := "olivares-enterprise-linux-amd64.tar.gz"
	checksums, sig := signManifest(t, priv, map[string][]byte{
		name:           artifact,
		"other.tar.gz": []byte("unrelated"),
	})
	if err := Verify(artifact, checksums, sig, name, pub); err != nil {
		t.Fatalf("Verify happy path: %v", err)
	}
	// A raw (non-base64) 64-byte signature must also verify.
	rawSig := ed25519.Sign(priv, checksums)
	if err := Verify(artifact, checksums, rawSig, name, pub); err != nil {
		t.Fatalf("Verify with raw signature: %v", err)
	}
}

func TestVerifyTamperedArtifactAborts(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	name := "app.tar.gz"
	checksums, sig := signManifest(t, priv, map[string][]byte{name: []byte("good bytes")})
	// The signature still verifies (manifest untouched) but the bytes changed:
	// the SHA-256 binding must catch it.
	if err := Verify([]byte("EVIL bytes"), checksums, sig, name, pub); err == nil {
		t.Fatal("tampered artifact must abort")
	} else if !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("want checksum mismatch, got %v", err)
	}
}

func TestVerifyTamperedManifestAborts(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	name := "app.tar.gz"
	artifact := []byte("good bytes")
	checksums, sig := signManifest(t, priv, map[string][]byte{name: artifact})
	// Flip a byte in the manifest AFTER signing: the signature must fail.
	tampered := append([]byte{}, checksums...)
	tampered[0] ^= 0xFF
	if err := Verify(artifact, tampered, sig, name, pub); err != ErrBadSignature {
		t.Fatalf("tampered manifest: want ErrBadSignature, got %v", err)
	}
}

func TestVerifyWrongKeyAborts(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	attackerPub, _, _ := ed25519.GenerateKey(nil) // a different key
	name := "app.tar.gz"
	artifact := []byte("bytes")
	checksums, sig := signManifest(t, priv, map[string][]byte{name: artifact})
	if err := Verify(artifact, checksums, sig, name, attackerPub); err != ErrBadSignature {
		t.Fatalf("wrong key: want ErrBadSignature, got %v", err)
	}
}

func TestVerifyNoKeyFailsClosed(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(nil)
	name := "app.tar.gz"
	artifact := []byte("bytes")
	checksums, sig := signManifest(t, priv, map[string][]byte{name: artifact})
	if err := Verify(artifact, checksums, sig, name, nil); err != ErrNoKey {
		t.Fatalf("no key: want ErrNoKey (fail-closed), got %v", err)
	}
}

func TestVerifyArtifactNotInManifest(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	checksums, sig := signManifest(t, priv, map[string][]byte{"present.tar.gz": []byte("x")})
	if err := Verify([]byte("x"), checksums, sig, "absent.tar.gz", pub); err == nil ||
		!strings.Contains(err.Error(), "not listed") {
		t.Fatalf("absent artifact: want ErrNotInManifest, got %v", err)
	}
}

func TestParseChecksumsRejectsGarbage(t *testing.T) {
	for _, bad := range []string{
		"",                                 // no artifacts
		"not-a-hex-digest  file",           // bad digest
		"abc  file",                        // too-short digest
		strings.Repeat("z", 64) + "  file", // non-hex
	} {
		if _, err := ParseChecksums([]byte(bad)); err == nil {
			t.Fatalf("ParseChecksums(%q) should error", bad)
		}
	}
	// Both text (two-space) and binary (space-star) forms parse.
	sum := strings.Repeat("a", 64)
	m, err := ParseChecksums([]byte(sum + "  text.bin\n" + sum + " *binary.bin\n# comment\n"))
	if err != nil {
		t.Fatalf("ParseChecksums valid: %v", err)
	}
	if m["text.bin"] != sum || m["binary.bin"] != sum {
		t.Fatalf("parsed = %+v", m)
	}

	// A DUPLICATE filename is an error, not last-wins. Silently taking the last entry
	// lets an appended line redefine a digest an earlier reader (a human eyeballing
	// the file, a first-wins parser) already resolved differently: one document, two
	// answers about the same bytes.
	dup := sum + "  same.bin\n" + strings.Repeat("b", 64) + "  same.bin\n"
	got, err := ParseChecksums([]byte(dup))
	if err == nil {
		t.Fatalf("a duplicated filename must be an error, got %+v", got)
	}
	if !strings.Contains(err.Error(), "same.bin") || !strings.Contains(err.Error(), "two digests") {
		t.Errorf("the error must name the file and the conflict, got: %v", err)
	}
	// The same filename with the SAME digest is a duplicate too: a checksums.txt we
	// cannot fully account for is not one to trust bytes against.
	if _, err := ParseChecksums([]byte(sum + "  same.bin\n" + sum + "  same.bin\n")); err == nil {
		t.Error("an exactly repeated line must also be refused")
	}
}

func TestKeyOriginAndDecode(t *testing.T) {
	// No embedded key by default in the test binary.
	if KeyOrigin() != "none" || KeyConfigured() {
		t.Fatalf("default build must embed no OTA key, got origin=%s", KeyOrigin())
	}
	pub, _, _ := ed25519.GenerateKey(nil)
	b64 := base64.StdEncoding.EncodeToString(pub)
	got, err := DecodePublicKey(b64)
	if err != nil || string(got) != string(pub) {
		t.Fatalf("DecodePublicKey round-trip: %v", err)
	}
	if _, err := DecodePublicKey("!!!not base64!!!"); err == nil {
		t.Fatal("DecodePublicKey must reject non-base64")
	}
	if _, err := DecodePublicKey(base64.StdEncoding.EncodeToString([]byte("too short"))); err == nil {
		t.Fatal("DecodePublicKey must reject wrong-length keys")
	}
}
