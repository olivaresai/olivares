// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestSetupTokenLifecycle(t *testing.T) {
	dir := t.TempDir()
	st := NewSetupToken(filepath.Join(dir, "setup.token"))

	if st.Exists() {
		t.Fatal("token should not exist initially")
	}
	tok, created, err := st.Ensure()
	if err != nil || !created || tok == "" {
		t.Fatalf("ensure = (%q,%v,%v)", tok, created, err)
	}
	// Re-ensure does not re-mint and does not reveal the token.
	tok2, created2, _ := st.Ensure()
	if created2 || tok2 != "" {
		t.Fatalf("re-ensure leaked/re-minted: (%q,%v)", tok2, created2)
	}
	if !st.Verify(tok) {
		t.Fatal("correct token did not verify")
	}
	if st.Verify("olst_wrong") {
		t.Fatal("wrong token verified")
	}
	if err := st.Consume(); err != nil {
		t.Fatal(err)
	}
	if st.Exists() || st.Verify(tok) {
		t.Fatal("token still active after consume")
	}
}

func TestReadSecretFailsClosedOnLoosePerms(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "key")
	if err := os.WriteFile(p, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecret(p); err == nil {
		t.Fatal("readSecret accepted a world-readable key file")
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecret(p); err != nil {
		t.Fatalf("readSecret rejected a 0600 file: %v", err)
	}
}

func TestLoadOrCreateSigningKey(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "audit.key")
	k1, created, err := LoadOrCreateSigningKey(p)
	if err != nil || !created {
		t.Fatalf("first load = (created=%v,%v)", created, err)
	}
	k2, created2, err := LoadOrCreateSigningKey(p)
	if err != nil || created2 {
		t.Fatalf("second load = (created=%v,%v)", created2, err)
	}
	if string(k1) != string(k2) {
		t.Fatal("signing key not stable across loads")
	}
}

func TestLoadSigningKeyFailsClosed(t *testing.T) {
	dir := t.TempDir()
	// A shared key that is ABSENT must fail closed — never mint a per-node key that
	// would fork the HA ledger. This is the whole point of the load-only path.
	if _, err := LoadSigningKey(filepath.Join(dir, "missing.key")); err == nil {
		t.Fatal("LoadSigningKey minted/accepted a missing key; it must fail closed")
	}

	// An existing, valid key mints once via LoadOrCreate, then loads identically via
	// the load-only path — proving a shared Secret resolves to the same key on every
	// replica.
	p := filepath.Join(dir, "audit.key")
	minted, _, err := LoadOrCreateSigningKey(p)
	if err != nil {
		t.Fatalf("seed key: %v", err)
	}
	loaded, err := LoadSigningKey(p)
	if err != nil {
		t.Fatalf("LoadSigningKey on a provisioned key: %v", err)
	}
	if string(loaded) != string(minted) {
		t.Fatal("LoadSigningKey returned a different key than was provisioned")
	}

	// A garbage value fails closed rather than yielding a malformed key.
	if _, err := DecodeSigningKey("not-base64-or-a-key"); err == nil {
		t.Fatal("DecodeSigningKey accepted an invalid key")
	}
	if k, err := DecodeSigningKey(base64.StdEncoding.EncodeToString(minted)); err != nil || string(k) != string(minted) {
		t.Fatalf("DecodeSigningKey round-trip failed: %v", err)
	}
}

func TestEnsureTLSCert(t *testing.T) {
	dir := t.TempDir()
	cert := filepath.Join(dir, "tls.crt")
	key := filepath.Join(dir, "tls.key")
	created, fp, err := EnsureTLSCert(cert, key)
	if err != nil || !created || fp == "" {
		t.Fatalf("first ensure = (created=%v, fp=%q, %v)", created, fp, err)
	}
	// Key file must be 0600.
	info, _ := os.Stat(key)
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("key perms too open: %o", info.Mode().Perm())
	}
	created2, fp2, err := EnsureTLSCert(cert, key)
	if err != nil || created2 || fp2 != fp {
		t.Fatalf("second ensure = (created=%v, fp=%q, %v)", created2, fp2, err)
	}
}
