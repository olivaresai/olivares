// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// fakeWrapper is an in-process KeyWrapper that emulates the property the real
// backends provide: a wrapped DEK only unwraps under the same KEK ("secret"),
// and — like AWS/GCP — under the same AAD. No real KMS crypto is needed to test
// the envelope logic; kmswrap has its own wire-level tests.
type fakeWrapper struct {
	provider string
	keyID    string
	secret   string // simulates WHICH KEK wrapped it (rewrap tests use two)
	fail     bool   // simulates a revoked/unreachable KEK
}

func canonAAD(aad map[string]string) string {
	keys := make([]string, 0, len(aad))
	for k := range aad {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + aad[k] + ";")
	}
	return b.String()
}

func (f *fakeWrapper) WrapKey(_ context.Context, pt []byte, aad map[string]string) ([]byte, error) {
	if f.fail {
		return nil, fmt.Errorf("kek unavailable")
	}
	return []byte(f.secret + "|" + canonAAD(aad) + "|" + string(pt)), nil
}

func (f *fakeWrapper) UnwrapKey(_ context.Context, ct []byte, aad map[string]string) ([]byte, error) {
	if f.fail {
		return nil, fmt.Errorf("kek unavailable")
	}
	want := f.secret + "|" + canonAAD(aad) + "|"
	if !strings.HasPrefix(string(ct), want) {
		return nil, fmt.Errorf("ciphertext was not wrapped under this KEK/context")
	}
	return []byte(strings.TrimPrefix(string(ct), want)), nil
}

func (f *fakeWrapper) KeyID() string    { return f.keyID }
func (f *fakeWrapper) Provider() string { return f.provider }

func testWrapper() *fakeWrapper {
	return &fakeWrapper{provider: "aws-kms", keyID: "arn:aws:kms:eu-west-1:111:key/abc", secret: "kek-1"}
}

func TestSealOpenRoundtrip(t *testing.T) {
	ctx := context.Background()
	w := testWrapper()
	payload := []byte(`{"some":"config"}`)
	e, err := Seal(ctx, w, PurposeOperatorConfig, payload)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if e.V != 1 || e.Provider != "aws-kms" || e.KeyID != w.keyID || e.Purpose != PurposeOperatorConfig {
		t.Fatalf("envelope metadata wrong: %+v", e)
	}
	if bytes.Contains(e.Ciphertext, payload) {
		t.Fatal("sealed envelope contains the plaintext")
	}
	got, err := e.Open(ctx, w, PurposeOperatorConfig)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("roundtrip mismatch: %q", got)
	}
}

func TestOpenFailsClosed(t *testing.T) {
	ctx := context.Background()
	w := testWrapper()
	e, err := Seal(ctx, w, PurposeOperatorConfig, []byte("payload"))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Wrong expected purpose: refused before any KMS call.
	if _, err := e.Open(ctx, w, PurposeAuditSigningKey); err == nil {
		t.Fatal("Open accepted the wrong purpose")
	}
	// Provider mismatch: clear config error, not a KMS 4xx.
	other := &fakeWrapper{provider: "gcp-kms", keyID: "projects/p/...", secret: "kek-1"}
	if _, err := e.Open(ctx, other, PurposeOperatorConfig); err == nil || !strings.Contains(err.Error(), "wrapped by aws-kms") {
		t.Fatalf("Open with wrong provider: %v", err)
	}
	// Revoked KEK (the CMEK guarantee): unwrap fails => open fails.
	revoked := testWrapper()
	revoked.fail = true
	if _, err := e.Open(ctx, revoked, PurposeOperatorConfig); err == nil {
		t.Fatal("Open succeeded with a revoked KEK")
	}
	// Tampered ciphertext: GCM authentication fails.
	tampered := *e
	tampered.Ciphertext = append([]byte(nil), e.Ciphertext...)
	tampered.Ciphertext[0] ^= 0xff
	if _, err := tampered.Open(ctx, w, PurposeOperatorConfig); err == nil {
		t.Fatal("Open accepted tampered ciphertext")
	}
	// Purpose swapped in BOTH the field and the KEK context: the KEK unwrap may
	// pass on an AAD-less provider, but the local GCM AAD still refuses.
	swapped := *e
	swapped.Purpose = PurposeCatalogSigningKey
	swapped.Context = map[string]string{"olivares:purpose": PurposeCatalogSigningKey}
	// Use a wrapper that ignores AAD (Azure-like) so only the GCM binding is left.
	azureLike := &fakeWrapper{provider: "aws-kms", keyID: w.keyID, secret: "kek-1"}
	// Re-wrap the DEK under the swapped context so the fake unwrap passes.
	dekAndCtx := strings.TrimPrefix(string(e.WrappedDEK), "kek-1|"+canonAAD(wrapContext(PurposeOperatorConfig))+"|")
	swapped.WrappedDEK = []byte("kek-1|" + canonAAD(swapped.Context) + "|" + dekAndCtx)
	if _, err := swapped.Open(ctx, azureLike, PurposeCatalogSigningKey); err == nil {
		t.Fatal("Open accepted an envelope whose purpose was swapped end-to-end")
	}
	// Unsupported version.
	vbad := *e
	vbad.V = 2
	if _, err := vbad.Open(ctx, w, PurposeOperatorConfig); err == nil {
		t.Fatal("Open accepted an unknown envelope version")
	}
	// Wrong DEK size after unwrap.
	short := *e
	short.WrappedDEK = []byte("kek-1|" + canonAAD(e.Context) + "|tiny")
	if _, err := short.Open(ctx, w, PurposeOperatorConfig); err == nil {
		t.Fatal("Open accepted a non-32-byte DEK")
	}
}

func TestSealedSigningKeyRoundtrip(t *testing.T) {
	ctx := context.Background()
	w := testWrapper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	e, err := SealSigningKey(ctx, w, PurposeAuditSigningKey, priv, nil)
	if err != nil {
		t.Fatalf("SealSigningKey: %v", err)
	}
	if !bytes.Equal(e.PublicKey, priv.Public().(ed25519.PublicKey)) {
		t.Fatal("envelope did not record the public key")
	}
	got, err := e.OpenSigningKey(ctx, w, PurposeAuditSigningKey)
	if err != nil {
		t.Fatalf("OpenSigningKey: %v", err)
	}
	if !bytes.Equal(got, priv) {
		t.Fatal("signing key roundtrip mismatch")
	}
	// An inconsistent custody record (recorded pub != sealed key) is refused —
	// the GCM AAD binds the metadata, so the edit fails authentication.
	bad := *e
	bad.PublicKey = bytes.Repeat([]byte{1}, ed25519.PublicKeySize)
	if _, err := bad.OpenSigningKey(ctx, w, PurposeAuditSigningKey); err == nil {
		t.Fatal("OpenSigningKey accepted a mismatched recorded public key")
	}
}

// TestPriorPublicKeysAreAuthenticated is the disk-write-attacker drill: the
// rotation history feeds the default verifier's candidate set, so an attacker
// who can edit the envelope FILE (but not unwrap the KEK) must not be able to
// append a verification key of their own — the GCM AAD binding turns any edit
// of prior_public_keys (or public_key) into a decryption failure.
func TestPriorPublicKeysAreAuthenticated(t *testing.T) {
	ctx := context.Background()
	w := testWrapper()
	oldPub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	e, err := SealSigningKey(ctx, w, PurposeAuditSigningKey, priv, [][]byte{oldPub})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.OpenSigningKey(ctx, w, PurposeAuditSigningKey); err != nil {
		t.Fatalf("legitimate open: %v", err)
	}

	attackerPub, _, _ := ed25519.GenerateKey(rand.Reader)

	// Append a forged prior.
	forged := *e
	forged.PriorPublicKeys = append(append([][]byte(nil), e.PriorPublicKeys...), attackerPub)
	if _, err := forged.OpenSigningKey(ctx, w, PurposeAuditSigningKey); err == nil {
		t.Fatal("an appended forged prior key was accepted — the rotation history is not authenticated")
	}
	// Replace the legitimate prior.
	swapped := *e
	swapped.PriorPublicKeys = [][]byte{attackerPub}
	if _, err := swapped.OpenSigningKey(ctx, w, PurposeAuditSigningKey); err == nil {
		t.Fatal("a swapped prior key was accepted")
	}
	// Strip the history (hides a rotation from the verifier).
	stripped := *e
	stripped.PriorPublicKeys = nil
	if _, err := stripped.OpenSigningKey(ctx, w, PurposeAuditSigningKey); err == nil {
		t.Fatal("a stripped rotation history was accepted")
	}
	// Rewrap must preserve the binding.
	w2 := &fakeWrapper{provider: "gcp-kms", keyID: "projects/p/locations/l/keyRings/r/cryptoKeys/k", secret: "kek-2"}
	re, err := RewrapSealed(ctx, e, w, w2)
	if err != nil {
		t.Fatal(err)
	}
	reforged := *re
	reforged.PriorPublicKeys = append(append([][]byte(nil), re.PriorPublicKeys...), attackerPub)
	if _, err := reforged.OpenSigningKey(ctx, w2, PurposeAuditSigningKey); err == nil {
		t.Fatal("a forged prior survived a rewrap")
	}
}

func TestRotateAndRewrap(t *testing.T) {
	ctx := context.Background()
	w1 := testWrapper()

	k1, e1, err := MintSealedSigningKey(ctx, w1, PurposeAuditSigningKey, nil)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	k2, e2, err := RotateSealedSigningKey(ctx, w1, w1, e1)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if bytes.Equal(k1, k2) {
		t.Fatal("rotation did not mint a new key")
	}
	if len(e2.PriorPublicKeys) != 1 || !bytes.Equal(e2.PriorPublicKeys[0], e1.PublicKey) {
		t.Fatalf("rotation lost the prior public key: %v", e2.PriorPublicKeys)
	}
	_, e3, err := RotateSealedSigningKey(ctx, w1, w1, e2)
	if err != nil {
		t.Fatalf("Rotate 2: %v", err)
	}
	if len(e3.PriorPublicKeys) != 2 || !bytes.Equal(e3.PriorPublicKeys[1], e2.PublicKey) {
		t.Fatalf("second rotation lost history: %v", e3.PriorPublicKeys)
	}

	// KEK rotation: rewrap under a NEW KEK, history intact, old KEK now useless.
	w2 := &fakeWrapper{provider: "gcp-kms", keyID: "projects/p/locations/l/keyRings/r/cryptoKeys/k", secret: "kek-2"}
	re, err := RewrapSealed(ctx, e3, w1, w2)
	if err != nil {
		t.Fatalf("Rewrap: %v", err)
	}
	if re.Provider != "gcp-kms" || len(re.PriorPublicKeys) != 2 || !bytes.Equal(re.PublicKey, e3.PublicKey) {
		t.Fatalf("rewrap lost custody metadata: %+v", re)
	}
	if _, err := re.OpenSigningKey(ctx, w2, PurposeAuditSigningKey); err != nil {
		t.Fatalf("open after rewrap: %v", err)
	}
	if _, err := re.OpenSigningKey(ctx, w1, PurposeAuditSigningKey); err == nil {
		t.Fatal("old KEK still opens the rewrapped envelope")
	}
}

func TestSealedFileRoundtripAndPerms(t *testing.T) {
	ctx := context.Background()
	w := testWrapper()
	dir := t.TempDir()
	path := filepath.Join(dir, "audit-signing.key.sealed")

	priv, e, err := MintSealedSigningKey(ctx, w, PurposeAuditSigningKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteSealedFile(path, e); err != nil {
		t.Fatalf("WriteSealedFile: %v", err)
	}
	got, ge, err := LoadSealedSigningKey(ctx, path, w, PurposeAuditSigningKey)
	if err != nil {
		t.Fatalf("LoadSealedSigningKey: %v", err)
	}
	if !bytes.Equal(got, priv) || !bytes.Equal(ge.PublicKey, e.PublicKey) {
		t.Fatal("sealed file roundtrip mismatch")
	}

	// World-readable envelope refused (defense in depth).
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadSealedSigningKey(ctx, path, w, PurposeAuditSigningKey); err == nil {
		t.Fatal("LoadSealedSigningKey read a world-readable envelope")
	}

	// Missing envelope fails closed — never mints.
	if _, _, err := LoadSealedSigningKey(ctx, filepath.Join(dir, "absent"), w, PurposeAuditSigningKey); err == nil {
		t.Fatal("LoadSealedSigningKey invented a key for a missing envelope")
	}
}

func TestIsSealedEnvelopeSniff(t *testing.T) {
	if IsSealedEnvelope([]byte(`{"sources":[{"kind":"vault"}]}`)) {
		t.Fatal("plain operator config sniffed as sealed")
	}
	if IsSealedEnvelope([]byte("AAAA base64 key line\n")) {
		t.Fatal("plain key file sniffed as sealed")
	}
	ctx := context.Background()
	e, err := Seal(ctx, testWrapper(), PurposeOperatorConfig, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "e")
	if err := WriteSealedFile(path, e); err != nil {
		t.Fatal(err)
	}
	enc, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !IsSealedEnvelope(enc) {
		t.Fatal("sealed envelope not sniffed")
	}
	if _, err := DecodeSealedEnvelope(enc); err != nil {
		t.Fatalf("DecodeSealedEnvelope: %v", err)
	}
	if _, err := DecodeSealedEnvelope([]byte("not json")); err == nil {
		t.Fatal("DecodeSealedEnvelope accepted non-envelope input")
	}
}

// TestRotateRefusesTamperedHistory closes finding F-02 of the adversarial
// contrast run against this work on 2026-08-06 (HIGH).
//
// The envelope makes a specific promise about an attacker who can WRITE the
// envelope file: PublicKey and PriorPublicKeys are bound into the GCM AAD, so an
// injected verification key is a decryption failure rather than a forged
// candidate (envelope.go, SealedEnvelope doc). Open keeps that promise and
// RewrapSealed keeps it, because both rebuild the AAD.
//
// RotateSealedSigningKey did not. It copied PriorPublicKeys, PublicKey and
// Purpose straight out of the decoded JSON and sealed them into a FRESH, valid
// envelope — laundering an attacker's edit into authenticated custody metadata
// that the next boot then trusts as a verifier candidate. The edit is only
// detectable while nobody rotates; the documented ceremony erased it.
//
// Rotation now authenticates the old envelope before carrying anything forward.
func TestRotateRefusesTamperedHistory(t *testing.T) {
	ctx := context.Background()
	w := testWrapper()

	_, gen1, err := MintSealedSigningKey(ctx, w, PurposeAuditSigningKey, nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	_, gen2, err := RotateSealedSigningKey(ctx, w, w, gen1)
	if err != nil {
		t.Fatalf("honest rotation must keep working: %v", err)
	}
	if len(gen2.PriorPublicKeys) != 1 {
		t.Fatalf("honest rotation lost the history: %+v", gen2.PriorPublicKeys)
	}

	// An attacker with write access to the envelope file appends a key of their
	// own to the rotation history. On disk this is just JSON.
	attackerPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate attacker key: %v", err)
	}
	tampered := *gen2
	tampered.PriorPublicKeys = append(append([][]byte{}, gen2.PriorPublicKeys...), []byte(attackerPub))

	// Control: the edit is detectable TODAY, on the read path. If this ever stops
	// failing, the AAD binding itself is broken and the test below proves nothing.
	if _, err := tampered.Open(ctx, w, PurposeAuditSigningKey); err == nil {
		t.Fatal("CONTROL FAILED: a tampered rotation history opened cleanly, so the GCM AAD binding is not doing its job at all")
	}

	// The ceremony must not launder it into a fresh, authenticated envelope.
	_, gen3, err := RotateSealedSigningKey(ctx, w, w, &tampered)
	if err == nil {
		for _, p := range gen3.PriorPublicKeys {
			if bytes.Equal(p, attackerPub) {
				t.Fatal("`keys rotate` authenticated an attacker-injected verification key: an edit that failed on the read path became trusted custody metadata after the documented ceremony")
			}
		}
		t.Fatal("rotation accepted an envelope whose custody metadata does not authenticate")
	}
}
