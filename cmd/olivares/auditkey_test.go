// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/secure"
)

// discardLog is defined in claude_inference_test.go (same test package).

func TestLoadAuditSigningKeyFromEnv(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	t.Setenv(envAuditKey, base64.StdEncoding.EncodeToString(priv))
	got, err := loadAuditSigningKey(t.TempDir(), discardLog())
	if err != nil || got.created {
		t.Fatalf("loadAuditSigningKey(env) = (created=%v, %v)", got.created, err)
	}
	if string(got.priv) != string(priv) {
		t.Fatal("the key from the environment was not returned verbatim")
	}
	if got.mode != custodyModeBYOKEnv {
		t.Fatalf("mode = %q, want %q", got.mode, custodyModeBYOKEnv)
	}
}

func TestLoadAuditSigningKeyFailsClosedOnMissingFile(t *testing.T) {
	// HA fail-closed: a configured-but-absent shared key must NEVER silently mint a
	// per-node key (which would fork the ledger at failover).
	t.Setenv(envAuditKeyFile, filepath.Join(t.TempDir(), "nope.key"))
	if _, err := loadAuditSigningKey(t.TempDir(), discardLog()); err == nil {
		t.Fatal("loadAuditSigningKey must fail closed when the configured key file is absent")
	}
}

func TestLoadAuditSigningKeyMintsLocallyByDefault(t *testing.T) {
	// No shared source configured: the single-node/dev path mints in the data dir.
	dir := t.TempDir()
	got, err := loadAuditSigningKey(dir, discardLog())
	if err != nil || !got.created {
		t.Fatalf("default loadAuditSigningKey = (created=%v, %v), want minted", got.created, err)
	}
	if got.mode != custodyModeMinted {
		t.Fatalf("mode = %q, want %q", got.mode, custodyModeMinted)
	}
	if got.createdAt.IsZero() {
		t.Fatal("newly minted key must retain its creation instant for custody metadata")
	}
}

func TestSigningKeyCustodyInfoUsesFullPublicKeyFingerprint(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, time.July, 24, 10, 0, 0, 0, time.UTC)
	key := loadedSigningKey{
		priv: privateKey, mode: custodyModeCMEK, kek: "aws-kms arn:example",
		priors: []ed25519.PublicKey{publicKey}, createdAt: createdAt,
	}
	info := key.custodyInfo("audit")
	sum := sha256.Sum256(publicKey)
	if info.PublicKey != base64.StdEncoding.EncodeToString(publicKey) {
		t.Fatalf("public key = %q, want base64 raw Ed25519 key", info.PublicKey)
	}
	if info.Fingerprint != hex.EncodeToString(sum[:]) || len(info.Fingerprint) != 64 {
		t.Fatalf("fingerprint = %q, want full SHA-256 hex", info.Fingerprint)
	}
	if info.Created != createdAt.Format(time.RFC3339) || info.PriorCount != 1 ||
		info.CustodyMode != custodyModeCMEK || info.KEK != "aws-kms arn:example" {
		t.Fatalf("custody metadata = %+v", info)
	}
}

func TestLoadCatalogSigningKeyLenientOnMissingFile(t *testing.T) {
	// Unlike the audit key, the catalog key is artifact-signing with an honest
	// per-node fallback — a configured-but-absent file mints rather than failing boot,
	// which is what lets the chart default the catalog Secret to the audit Secret
	// without forcing catalog-signing.key to be present in it.
	t.Setenv(envCatalogKeyFile, filepath.Join(t.TempDir(), "absent.key"))
	dir := t.TempDir()
	got, err := loadCatalogSigningKey(dir, discardLog())
	if err != nil {
		t.Fatalf("loadCatalogSigningKey must NOT fail on an absent file: %v", err)
	}
	if !got.created {
		t.Fatal("loadCatalogSigningKey should have minted a per-node key when the shared file was absent")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "catalog-signing.key")); statErr != nil {
		t.Fatal("the per-node catalog key was not written to the data dir")
	}
}

func TestLoadPolicySigningKeyMirrorsCatalogSemantics(t *testing.T) {
	// the policy-artifact key follows the catalog key exactly — minted by
	// default, LENIENT on a configured-but-absent shared file (artifact signing
	// with an honest fallback, never ledger integrity).
	dir := t.TempDir()
	got, err := loadPolicySigningKey(dir, discardLog())
	if err != nil || !got.created || got.mode != custodyModeMinted {
		t.Fatalf("default loadPolicySigningKey = (created=%v, mode=%q, %v), want minted", got.created, got.mode, err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "policy-signing.key")); statErr != nil {
		t.Fatal("the per-node policy key was not written to the data dir")
	}
	// A second boot reuses the same key (pinned fingerprints must hold).
	again, err := loadPolicySigningKey(dir, discardLog())
	if err != nil || again.created {
		t.Fatalf("second load = (created=%v, %v), want reuse", again.created, err)
	}
	if !again.priv.Equal(got.priv) {
		t.Fatal("the policy key must be stable across boots")
	}
	// Lenient on a configured-but-absent shared file.
	t.Setenv(envPolicyKeyFile, filepath.Join(t.TempDir(), "absent.key"))
	lenient, err := loadPolicySigningKey(t.TempDir(), discardLog())
	if err != nil || !lenient.created {
		t.Fatalf("absent shared file must mint per-node, got (created=%v, %v)", lenient.created, err)
	}
}

// sealTestKey writes a sealed audit-key envelope under a fake AWS KEK served by
// kekServer (custody_test.go) and returns the minted key.
func sealTestKey(t *testing.T, path, purpose string, priors [][]byte) ed25519.PrivateKey {
	t.Helper()
	cfg, err := loadKeyWrapConfig()
	if err != nil || cfg == nil {
		t.Fatalf("loadKeyWrapConfig: cfg=%v err=%v", cfg, err)
	}
	w, err := cfg.wrapper()
	if err != nil {
		t.Fatal(err)
	}
	priv, env, err := secure.MintSealedSigningKey(context.Background(), w, purpose, priors)
	if err != nil {
		t.Fatal(err)
	}
	if err := secure.WriteSealedFile(path, env); err != nil {
		t.Fatal(err)
	}
	return priv
}

func TestLoadAuditSigningKeyCMEK(t *testing.T) {
	srv := startFakeKEKServer(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "audit-signing.key.sealed")

	oldPub, _, _ := ed25519.GenerateKey(rand.Reader)
	priv := sealTestKey(t, path, secure.PurposeAuditSigningKey, [][]byte{oldPub})
	t.Setenv(envAuditWrapped, path)

	got, err := loadAuditSigningKey(dir, discardLog())
	if err != nil {
		t.Fatalf("loadAuditSigningKey(CMEK): %v", err)
	}
	if string(got.priv) != string(priv) {
		t.Fatal("unwrapped key mismatch")
	}
	if got.mode != custodyModeCMEK || got.created {
		t.Fatalf("mode=%q created=%v, want cmek/false", got.mode, got.created)
	}
	if got.createdAt.IsZero() {
		t.Fatal("CMEK envelope creation instant was discarded")
	}
	if len(got.priors) != 1 || string(got.priors[0]) != string(oldPub) {
		t.Fatalf("rotation history not surfaced: %v", got.priors)
	}

	// Revoked KEK (the server refuses): boot must fail closed, never mint.
	srv.revoked = true
	if _, err := loadAuditSigningKey(dir, discardLog()); err == nil {
		t.Fatal("loadAuditSigningKey must fail closed when the KEK refuses to unwrap")
	}
	srv.revoked = false

	// Ambiguous custody (envelope + BYOK env) is an error, not a precedence guess.
	t.Setenv(envAuditKey, base64.StdEncoding.EncodeToString(priv))
	if _, err := loadAuditSigningKey(dir, discardLog()); err == nil {
		t.Fatal("ambiguous custody sources must be refused")
	}
	t.Setenv(envAuditKey, "")

	// A sealed envelope without a configured KEK cannot be opened.
	t.Setenv(envKeyWrap, "")
	if _, err := loadAuditSigningKey(dir, discardLog()); err == nil {
		t.Fatal("a sealed envelope without OLIVARES_KEY_WRAP must fail closed")
	}
}

func TestLoadCatalogSigningKeyCMEKFailsClosed(t *testing.T) {
	// The catalog key is lenient for an ABSENT plain shared file, but a
	// sealed envelope is an explicit custody opt-in: unwrap failures fail the boot.
	srv := startFakeKEKServer(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog-signing.key.sealed")
	sealTestKey(t, path, secure.PurposeCatalogSigningKey, nil)
	t.Setenv(envCatalogWrapped, path)

	got, err := loadCatalogSigningKey(dir, discardLog())
	if err != nil || got.mode != custodyModeCMEK {
		t.Fatalf("loadCatalogSigningKey(CMEK) = (%q, %v)", got.mode, err)
	}

	srv.revoked = true
	if _, err := loadCatalogSigningKey(dir, discardLog()); err == nil {
		t.Fatal("catalog CMEK unwrap failure must fail the boot, not mint a plaintext key")
	}
}
