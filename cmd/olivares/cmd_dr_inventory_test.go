// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/dr"
)

// TestDRBackupInventory pins the CONTRACT of what a DR bundle must contain, so a
// change that silently omits a captured artifact (or that stops capturing a whole
// class of signing key) fails the build. The rule (docs/DR-RUNBOOK.md §2, "minimal
// data"): the bundle carries EVERY *-signing.key in the data dir plus the store
// snapshot, and NOTHING else from the data dir (no TLS material, setup token or
// license — those come from the deployment).
func TestDRBackupInventory(t *testing.T) {
	src := t.TempDir()
	seedDataDir(t, src) // olivares.db + audit-signing.key + catalog-signing.key

	// A NEW signing-key class appears in the data dir. The backup must capture it too
	// (the glob is *-signing.key) — this is the "detects omissions" half: if capture
	// were narrowed to a fixed key list, this key would be missed and the test fails.
	writeSigningKey(t, filepath.Join(src, "custom-signing.key"))

	// Non-DR material that must NEVER be escrowed in a bundle (minimal data).
	for _, f := range []string{"tls.crt", "tls.key", "setup-token", "license.jwt"} {
		if err := os.WriteFile(filepath.Join(src, f), []byte("SECRET-not-for-DR"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	pf := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(pf, []byte("a strong DR passphrase"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "inv.drbundle")
	if out, err := runDR("backup", "--data-dir", src, "--out", bundle, "--passphrase-file", pf); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}

	// Extract and inventory the bundle contents.
	tmp := t.TempDir()
	f, err := os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := dr.ExtractBundle(f, tmp)
	_ = f.Close()
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	// 1) EVERY signing key in the data dir is captured — audit, catalog AND the new one.
	captured := map[string]bool{}
	for _, kr := range m.Keys {
		captured[kr.Name] = true
	}
	for _, want := range []string{"audit-signing.key", "catalog-signing.key", "custom-signing.key"} {
		if !captured[want] {
			t.Fatalf("backup OMITTED signing key %q — the bundle must carry every *-signing.key; captured=%v", want, captured)
		}
	}
	// The omission invariant: the bundle must carry EXACTLY the *-signing.key files
	// present in the data dir — no more, no fewer. If boot mints a new signing-key
	// class (this test discovered policy-signing.key alongside audit/catalog) it is
	// captured automatically; if capture were ever narrowed, count < files and this
	// fails.
	onDisk, err := filepath.Glob(filepath.Join(src, "*-signing.key"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Keys) != len(onDisk) {
		t.Fatalf("backup captured %d signing keys but the data dir has %d — an omission; captured=%v, on-disk=%v", len(m.Keys), len(onDisk), captured, onDisk)
	}

	// 2) The store snapshot is present (the other half of the DR set).
	if m.Store.File == "" || m.Store.SHA256 == "" {
		t.Fatalf("backup did not capture the store snapshot: %+v", m.Store)
	}
	if _, err := os.Stat(filepath.Join(tmp, filepath.FromSlash(m.Store.File))); err != nil {
		t.Fatalf("store snapshot missing from the bundle: %v", err)
	}

	// 3) Non-DR material is NEVER escrowed (minimal data).
	for _, forbidden := range []string{"tls.crt", "tls.key", "setup-token", "license.jwt"} {
		if captured[forbidden] {
			t.Fatalf("backup escrowed non-DR material %q — bundles must carry only signing keys + the store", forbidden)
		}
	}
}

// writeSigningKey writes a valid data-dir signing-key file (base64 Ed25519 private
// key + newline), the format sealSigningKeys/PubFingerprintFromSigningKey expect.
func writeSigningKey(t *testing.T, path string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
