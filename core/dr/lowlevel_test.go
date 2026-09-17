// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
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
	cipher, err := dr.OpenCipher([]byte(testPass), kek)
	if err != nil {
		t.Fatal(err)
	}
	if err := dr.VerifyBundleIntegrity(dir, m, kek, cipher, false); err != nil {
		t.Fatalf("authenticated bundle did not verify: %v", err)
	}
	if len(m.Files) != 3 || m.Authentication.Algorithm == "" || m.Authentication.Value == "" {
		t.Fatalf("bundle lacks the authenticated per-file inventory: files=%d auth=%+v", len(m.Files), m.Authentication)
	}
	_ = dir
}

func TestBundleIntegrityRejectsAlteredDigestAndPayload(t *testing.T) {
	src := newEstate(t)
	tn := src.newTenant(t)
	src.appendN(t, tn, 2)
	b := makeBundle(t, src)
	dir, m, kek := extractBundle(t, b)
	cipher, err := dr.OpenCipher([]byte(testPass), kek)
	if err != nil {
		t.Fatal(err)
	}
	if err := dr.VerifyBundleIntegrity(dir, m, kek, cipher, false); err != nil {
		t.Fatalf("positive control: %v", err)
	}

	original := m.Files[0].SHA256
	m.Files[0].SHA256 = strings.Repeat("0", 64)
	if err := dr.VerifyBundleIntegrity(dir, m, kek, cipher, false); err == nil || !strings.Contains(err.Error(), "authentication mismatch") {
		t.Fatalf("altered manifest digest was not rejected by its keyed authentication: %v", err)
	}
	m.Files[0].SHA256 = original

	snapshot := filepath.Join(dir, m.Store.File)
	f, err := os.OpenFile(snapshot, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("altered")); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := dr.VerifyBundleIntegrity(dir, m, kek, cipher, false); err == nil || !strings.Contains(err.Error(), "payload digest mismatch") {
		t.Fatalf("altered payload was not rejected: %v", err)
	}
}

func TestBundleIntegrityRequiresExplicitLegacyException(t *testing.T) {
	cipher, err := dr.NewRawKeyCipher(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	m := &dr.Manifest{Format: dr.ManifestFormat}
	if err := dr.VerifyBundleIntegrity(t.TempDir(), m, cipher.Params(), cipher, false); err == nil || !strings.Contains(err.Error(), "unsigned") {
		t.Fatalf("unsigned legacy bundle was not refused: %v", err)
	}
	if err := dr.VerifyBundleIntegrity(t.TempDir(), m, cipher.Params(), cipher, true); err != nil {
		t.Fatalf("explicit legacy exception did not work: %v", err)
	}
}

func TestWriteBundleRequiresAuthenticatedManifest(t *testing.T) {
	var out bytes.Buffer
	err := dr.WriteBundle(&out, dr.BundleInput{Manifest: &dr.Manifest{Format: dr.ManifestFormat}})
	if err == nil || !strings.Contains(err.Error(), "requires an authenticated manifest") {
		t.Fatalf("low-level serializer accepted an unauthenticated manifest: %v", err)
	}
}

func TestExtractBundleRejectsTraversal(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o600, Size: 3, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("bad"))
	_ = tw.Close()
	_ = gz.Close()
	if _, _, err := dr.ExtractBundle(&buf, t.TempDir()); err == nil || !strings.Contains(err.Error(), `unsafe bundle entry "../escape"`) {
		t.Fatalf("expected the path guard itself to reject traversal, got %v", err)
	}
}

func TestExtractBundleRejectsAbsolutePath(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "/absolute", Mode: 0o600, Size: 3, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("bad"))
	_ = tw.Close()
	_ = gz.Close()
	if _, _, err := dr.ExtractBundle(&buf, t.TempDir()); err == nil || !strings.Contains(err.Error(), `unsafe bundle entry "/absolute"`) {
		t.Fatalf("expected the path guard itself to reject an absolute path, got %v", err)
	}
}

func TestExtractBundleRejectsBackslashAlias(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: `store\olivares.db`, Mode: 0o600, Size: 3, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("bad"))
	_ = tw.Close()
	_ = gz.Close()
	if _, _, err := dr.ExtractBundle(&buf, t.TempDir()); err == nil || !strings.Contains(err.Error(), "unsafe bundle entry") {
		t.Fatalf("expected a backslash alias to be rejected, got %v", err)
	}
}

func TestExtractBundleRejectsLinkEntry(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "store/olivares.db", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
	_ = tw.Close()
	_ = gz.Close()
	if _, _, err := dr.ExtractBundle(&buf, t.TempDir()); err == nil || !strings.Contains(err.Error(), "is not a regular file") {
		t.Fatalf("expected a link entry to be rejected, got %v", err)
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
