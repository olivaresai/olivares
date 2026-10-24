// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/dr"
)

// rewriteTestBundle copies a bundle, passing every tar member through edit.
func rewriteTestBundle(t *testing.T, in, out string, edit func(name string, body []byte) []byte) {
	t.Helper()
	src, err := os.Open(in)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	gz, err := gzip.NewReader(src)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		body = edit(hdr.Name, body)
		if err := tw.WriteHeader(&tar.Header{Name: hdr.Name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// oldFormatTestBundle rewrites a bundle the way a pre-v2 engine wrote it: manifest
// v1 and the snapshot in the clear, under the same KEK and keys.
func oldFormatTestBundle(t *testing.T, bundle string, cipher *dr.KeyCipher) string {
	t.Helper()
	work := t.TempDir()
	f, err := os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	m, kek, err := dr.ExtractBundle(f, work)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := dr.VerifyBundleIntegrity(work, m, kek, cipher, false); err != nil {
		t.Fatal(err)
	}
	sealedKeys := map[string][]byte{}
	for _, ref := range m.Keys {
		if sealedKeys[ref.File], err = os.ReadFile(filepath.Join(work, ref.File)); err != nil {
			t.Fatal(err)
		}
	}
	m.Format, m.Store.Encryption = dr.ManifestFormatV1, ""
	in := dr.BundleInput{Manifest: m, KEK: kek, SnapshotPath: filepath.Join(work, m.Store.File), SealedKeys: sealedKeys}
	if err := dr.AuthenticateBundle(in, cipher); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "old-format.drbundle")
	of, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := dr.WriteBundle(of, in); err != nil {
		_ = of.Close()
		t.Fatal(err)
	}
	if err := of.Close(); err != nil {
		t.Fatal(err)
	}
	return out
}

// The console restore reads both bundle formats and tells the operator when the
// snapshot of the bundle it just restored was not encrypted.
func TestConsoleRestoreWarnsOnlyForABundleWhoseSnapshotIsNotEncrypted(t *testing.T) {
	sealed, cipher := buildTestBundle(t)
	cases := []struct {
		name     string
		bundle   string
		wantWarn bool
	}{
		{"sealed", sealed, false},
		{"old format", oldFormatTestBundle(t, sealed, cipher), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			job, _, _, _ := runConsoleRestoreBundle(t, tc.bundle, false, false)
			if job.Status != drJobCompleted {
				t.Fatalf("restore job: status=%q phase=%q error=%q", job.Status, job.Phase, job.Error)
			}
			if got := strings.Contains(job.Notes, "NOT encrypted"); got != tc.wantWarn {
				t.Fatalf("old-format warning in the job notes = %v, want %v: %s", got, tc.wantWarn, job.Notes)
			}
		})
	}
}

// An altered sealed snapshot is refused while the bundle is still in the scratch
// directory: the live store and the signing key stay as they were.
func TestConsoleRestoreRefusesAnAlteredSealedSnapshotBeforeTouchingTheLiveStore(t *testing.T) {
	sealed, _ := buildTestBundle(t)
	altered := filepath.Join(t.TempDir(), "altered.drbundle")
	touched := false
	rewriteTestBundle(t, sealed, altered, func(name string, body []byte) []byte {
		if name == "store/olivares.db.enc" {
			touched = true
			body[len(body)/2] ^= 0x01
		}
		return body
	})
	if !touched {
		t.Fatal("the fixture bundle has no sealed snapshot member")
	}
	job, dataDir, live, _ := runConsoleRestoreBundle(t, altered, false, false)
	if job.Status != drJobFailed || !strings.Contains(job.Error, "verify bundle authentication") || !strings.Contains(job.Error, "digest mismatch") {
		t.Fatalf("an altered sealed snapshot: status=%q error=%q", job.Status, job.Error)
	}
	if !job.restartSafe {
		t.Fatal("a refusal in the scratch directory must be restart-safe")
	}
	key, err := os.ReadFile(filepath.Join(dataDir, "audit-signing.key"))
	if err != nil || string(key) != liveSigningKey {
		t.Fatalf("a refused restore replaced the signing key: %q err=%v", key, err)
	}
	var value string
	if err := live.QueryRow("SELECT value FROM marker").Scan(&value); err != nil || value != "live" {
		t.Fatalf("a refused restore changed the live store: %q err=%v", value, err)
	}
}
