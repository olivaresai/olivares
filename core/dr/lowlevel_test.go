// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"testing"

	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/model"

	_ "modernc.org/sqlite"
)

// tamperEvent simulates a DB-level attacker on the restored snapshot: it drops
// the immutability trigger and silently alters a row (mirrors the tamper
// tests). The hash chain must still detect it after restore.
func tamperEvent(t *testing.T, dbPath string, tenant model.TenantID) {
	t.Helper()
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer func() { _ = raw.Close() }()
	if _, err := raw.Exec("DROP TRIGGER audit_events_no_update"); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	if _, err := raw.Exec("UPDATE audit_events SET action = 'tampered' WHERE tenant_id = ? AND seq = 2", tenant.String()); err != nil {
		t.Fatalf("tamper: %v", err)
	}
}

func TestKeyCipherPassphraseRoundTrip(t *testing.T) {
	c, err := dr.NewPassphraseCipher([]byte("a strong passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("secret signing key material")
	sealed, err := c.Seal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, plain) {
		t.Fatal("plaintext leaked into ciphertext")
	}
	// Re-derive on the restore side from the recorded params.
	c2, err := dr.OpenCipher([]byte("a strong passphrase"), c.Params())
	if err != nil {
		t.Fatal(err)
	}
	got, err := c2.Open(sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("round trip mismatch")
	}
}

func TestKeyCipherWrongPassphraseFails(t *testing.T) {
	c, _ := dr.NewPassphraseCipher([]byte("right passphrase"))
	sealed, _ := c.Seal([]byte("key"))
	c2, err := dr.OpenCipher([]byte("WRONG passphrase"), c.Params())
	if err != nil {
		t.Fatalf("open cipher: %v", err)
	}
	if _, err := c2.Open(sealed); err == nil {
		t.Fatal("expected an authentication failure with the wrong passphrase")
	}
}

func TestKeyCipherRawKey(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	c, err := dr.NewRawKeyCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := c.Seal([]byte("kms-wrapped path"))
	c2, err := dr.OpenCipher(key, c.Params())
	if err != nil {
		t.Fatal(err)
	}
	got, err := c2.Open(sealed)
	if err != nil || string(got) != "kms-wrapped path" {
		t.Fatalf("raw key round trip failed: %v", err)
	}
	if _, err := dr.NewRawKeyCipher(key[:16]); err == nil {
		t.Fatal("expected a short-key rejection")
	}
}

func TestBundleRoundTrip(t *testing.T) {
	src := newEstate(t)
	tn := src.newTenant(t)
	src.appendN(t, tn, 2)
	b := makeBundle(t, src)
	dir, m, kek := extractBundle(t, b)
	if m.Format != dr.ManifestFormat {
		t.Fatalf("format = %q", m.Format)
	}
	if kek.KDF == "" {
		t.Fatalf("kek params not carried")
	}
	if len(m.Keys) != 1 || m.Keys[0].Role != dr.RoleAudit || m.Keys[0].PubSHA256 == "" {
		t.Fatalf("audit key ref not carried: %+v", m.Keys)
	}
	if m.Store.Method != dr.MethodVacuumInto || m.Store.SHA256 == "" {
		t.Fatalf("store meta wrong: %+v", m.Store)
	}
	_ = dir
}

func TestExtractBundleRejectsTraversal(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o600, Size: 3, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("bad"))
	_ = tw.Close()
	_ = gz.Close()
	if _, _, err := dr.ExtractBundle(&buf, t.TempDir()); err == nil {
		t.Fatal("expected a path-traversal rejection")
	}
}

func TestExtractBundleRejectsUnknownFormat(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte(`{"format":"bogus.v9"}`)
	_ = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gz.Close()
	if _, _, err := dr.ExtractBundle(&buf, t.TempDir()); err == nil {
		t.Fatal("expected an unknown-format rejection")
	}
}
