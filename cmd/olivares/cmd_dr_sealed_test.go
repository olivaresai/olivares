// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/dr"
)

// rewriteBundle copies a bundle, passing every tar member through edit.
func rewriteBundle(t *testing.T, in, out string, edit func(name string, body []byte) []byte) {
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

// downgradeToLegacyBundle rewrites a bundle the way a pre-v2 engine wrote it:
// manifest v1, the snapshot in the clear under its own name, the same KEK and keys.
func downgradeToLegacyBundle(t *testing.T, bundle, out string, kekSecret []byte) {
	t.Helper()
	work := t.TempDir()
	m, kek, err := openAndCheckBundle(bundle, work)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := dr.OpenCipher(kekSecret, kek)
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
	input := dr.BundleInput{Manifest: m, KEK: kek, SnapshotPath: filepath.Join(work, m.Store.File), SealedKeys: sealedKeys}
	if err := dr.AuthenticateBundle(input, cipher); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := dr.WriteBundle(f, input); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func stampDRTestVersion(t *testing.T) {
	t.Helper()
	previous := version
	version = "1.900"
	t.Cleanup(func() { version = previous })
}

// TestDRBackupSealsTheSnapshotAndTamperingFailsBeforeAnyWrite is the operator's
// path: dr backup, read the file the way a thief would, then try to use a bundle
// whose sealed snapshot was altered.
func TestDRBackupSealsTheSnapshotAndTamperingFailsBeforeAnyWrite(t *testing.T) {
	stampDRTestVersion(t)
	src := t.TempDir()
	seedDataDir(t, src)
	kekFile := drPayloadTestKEK(t)
	bundle := filepath.Join(t.TempDir(), "estate.drbundle")
	if out, err := runDR(t, "backup", "--data-dir", src, "--engine", "sqlite", "--out", bundle, "--kek-key-file", kekFile); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}

	// What anyone holding the file sees: no member is a SQLite database.
	var storeMember string
	rewriteBundle(t, bundle, filepath.Join(t.TempDir(), "copy.drbundle"), func(name string, body []byte) []byte {
		if strings.HasPrefix(name, "store/") {
			storeMember = name
			if bytes.HasPrefix(body, []byte("SQLite format 3\x00")) || bytes.Contains(body, []byte("audit_events")) {
				t.Errorf("%s holds the database in the clear", name)
			}
		}
		return body
	})
	if storeMember != "store/olivares.db.enc" {
		t.Fatalf("snapshot member = %q", storeMember)
	}

	// inspect needs no KEK and states how the snapshot travels.
	if out, err := runDR(t, "inspect", "--in", bundle); err != nil || !strings.Contains(out, `"encryption": "aes-256-gcm-chunked-v1"`) {
		t.Fatalf("inspect: %v\n%s", err, out)
	}
	if out, err := runDR(t, "verify", "--in", bundle, "--kek-key-file", kekFile); err != nil || strings.Contains(out, "NOT encrypted") {
		t.Fatalf("a new bundle must verify without the old-format warning: %v\n%s", err, out)
	}

	tampered := filepath.Join(t.TempDir(), "tampered.drbundle")
	rewriteBundle(t, bundle, tampered, func(name string, body []byte) []byte {
		if name == storeMember {
			body[len(body)/2] ^= 0x01
		}
		return body
	})
	if out, err := runDR(t, "verify", "--in", tampered, "--kek-key-file", kekFile); err == nil || !strings.Contains(err.Error()+out, "digest mismatch") {
		t.Fatalf("verify of a tampered sealed snapshot: err=%v\n%s", err, out)
	}
	dst := t.TempDir()
	before := dirListing(t, dst)
	if out, err := runDR(t, "restore", "--in", tampered, "--data-dir", dst, "--engine", "sqlite", "--kek-key-file", kekFile, "--force"); err == nil || !strings.Contains(err.Error()+out, "digest mismatch") {
		t.Fatalf("restore of a tampered sealed snapshot: err=%v\n%s", err, out)
	}
	if after := dirListing(t, dst); strings.Join(after, " ") != strings.Join(before, " ") {
		t.Fatalf("a refused restore changed the data dir: before %v, after %v", before, after)
	}

	// Positive control: the same command and flags restore the untampered bundle.
	if out, err := runDR(t, "restore", "--in", bundle, "--data-dir", dst, "--engine", "sqlite", "--kek-key-file", kekFile, "--force"); err != nil || !strings.Contains(out, "restore verified") {
		t.Fatalf("restore of the untampered bundle: %v\n%s", err, out)
	}
}

// dirListing names every entry of dir, dot-files included.
func dirListing(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestDRLegacyBundleStillRestoresAndWarns: a bundle taken before the snapshot was
// sealed keeps verifying and restoring, and the operator is told to take a new one.
func TestDRLegacyBundleStillRestoresAndWarns(t *testing.T) {
	stampDRTestVersion(t)
	src := t.TempDir()
	seedDataDir(t, src)
	kekFile := drPayloadTestKEK(t)
	sealed := filepath.Join(t.TempDir(), "sealed.drbundle")
	if out, err := runDR(t, "backup", "--data-dir", src, "--engine", "sqlite", "--out", sealed, "--kek-key-file", kekFile); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	legacy := filepath.Join(t.TempDir(), "legacy.drbundle")
	downgradeToLegacyBundle(t, sealed, legacy, make([]byte, 32))

	if out, err := runDR(t, "verify", "--in", legacy, "--kek-key-file", kekFile); err != nil || !strings.Contains(out, "DR drill PASSED") || !strings.Contains(out, "NOT encrypted") {
		t.Fatalf("verify of an old-format bundle: %v\n%s", err, out)
	}
	dst := t.TempDir()
	out, err := runDR(t, "restore", "--in", legacy, "--data-dir", dst, "--engine", "sqlite", "--kek-key-file", kekFile, "--force")
	if err != nil || !strings.Contains(out, "restore verified") || !strings.Contains(out, "NOT encrypted") {
		t.Fatalf("restore of an old-format bundle: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dst, "olivares.db")); err != nil {
		t.Fatalf("old-format restore wrote no store: %v", err)
	}
}

// unsignedLegacyBundle rewrites a legacy bundle as a pre-v26.9 one: no inventory and no
// keyed authentication. flip alters one byte of the snapshot.
func unsignedLegacyBundle(t *testing.T, legacy, out string, flip bool) {
	t.Helper()
	rewriteBundle(t, legacy, out, func(name string, body []byte) []byte {
		switch name {
		case "manifest.json":
			var m map[string]any
			if err := json.Unmarshal(body, &m); err != nil {
				t.Fatal(err)
			}
			delete(m, "files")
			m["authentication"] = map[string]string{"algorithm": "", "value": ""}
			b, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			return b
		case "store/olivares.db":
			if flip {
				body[len(body)/2] ^= 0x01
			}
		}
		return body
	})
}

// TestDRUnsignedLegacyBundleKeepsItsSnapshotDigestCheck: with --allow-legacy-unsigned
// the digest in the manifest is the only integrity check a pre-v26.9 bundle has, so it
// still runs before anything is restored; and such a manifest cannot claim the sealed format.
func TestDRUnsignedLegacyBundleKeepsItsSnapshotDigestCheck(t *testing.T) {
	stampDRTestVersion(t)
	src := t.TempDir()
	seedDataDir(t, src)
	kekFile := drPayloadTestKEK(t)
	sealed := filepath.Join(t.TempDir(), "sealed.drbundle")
	if out, err := runDR(t, "backup", "--data-dir", src, "--engine", "sqlite", "--out", sealed, "--kek-key-file", kekFile); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	legacy := filepath.Join(t.TempDir(), "legacy.drbundle")
	downgradeToLegacyBundle(t, sealed, legacy, make([]byte, 32))

	good := filepath.Join(t.TempDir(), "unsigned.drbundle")
	unsignedLegacyBundle(t, legacy, good, false)
	if out, err := runDR(t, "verify", "--in", good, "--kek-key-file", kekFile, "--allow-legacy-unsigned"); err != nil || !strings.Contains(out, "DR drill PASSED") {
		t.Fatalf("positive control, an unsigned legacy bundle: %v\n%s", err, out)
	}
	bad := filepath.Join(t.TempDir(), "unsigned-altered.drbundle")
	unsignedLegacyBundle(t, legacy, bad, true)
	if out, err := runDR(t, "verify", "--in", bad, "--kek-key-file", kekFile, "--allow-legacy-unsigned"); err == nil || !strings.Contains(err.Error()+out, "bundle snapshot digest mismatch") {
		t.Fatalf("an altered unsigned legacy snapshot: err=%v\n%s", err, out)
	}

	// The same unsigned manifest claiming the sealed format is not a legacy bundle.
	claiming := filepath.Join(t.TempDir(), "unsigned-claiming.drbundle")
	rewriteBundle(t, good, claiming, func(name string, body []byte) []byte {
		if name != "manifest.json" {
			return body
		}
		return bytes.Replace(body, []byte(`"olivares.dr.manifest.v1"`), []byte(`"olivares.dr.manifest.v2"`), 1)
	})
	if out, err := runDR(t, "verify", "--in", claiming, "--kek-key-file", kekFile, "--allow-legacy-unsigned"); err == nil || !strings.Contains(err.Error()+out, "only accepts a pre-v2 bundle") {
		t.Fatalf("an unsigned manifest claiming the sealed format: err=%v\n%s", err, out)
	}
}

// TestOpenBundleOutputKeepsAnEarlierBundleUntilTheNewOneIsComplete: sealing writes a
// second copy of the snapshot, so a backup can now fail after --out was opened. That
// failure must not cost the previous good bundle of the same name.
func TestOpenBundleOutputKeepsAnEarlierBundleUntilTheNewOneIsComplete(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "estate.drbundle")
	if err := os.WriteFile(out, []byte("previous good bundle"), 0o640); err != nil {
		t.Fatal(err)
	}

	f, _, abort, err := openBundleOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("half a bund"); err != nil {
		t.Fatal(err)
	}
	abort()
	if got, err := os.ReadFile(out); err != nil || string(got) != "previous good bundle" {
		t.Fatalf("a failed backup changed the earlier bundle: %q err=%v", got, err)
	}
	if got := dirListing(t, dir); len(got) != 1 {
		t.Fatalf("a failed backup left %v", got)
	}

	f, commit, _, err := openBundleOutput(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("new bundle"); err != nil {
		t.Fatal(err)
	}
	if err := commit(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(out)
	if got, rerr := os.ReadFile(out); rerr != nil || string(got) != "new bundle" || err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("a completed backup: content %q, mode %v, err %v %v", got, info, rerr, err)
	}
	if got := dirListing(t, dir); len(got) != 1 {
		t.Fatalf("a completed backup left %v", got)
	}

	fresh := filepath.Join(dir, "fresh.drbundle")
	f, commit, _, err = openBundleOutput(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("first bundle"); err != nil {
		t.Fatal(err)
	}
	if err := commit(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(fresh); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("a new bundle must be private: %v %v", info, err)
	}

	// A device is written through, not replaced by a rename.
	f, commit, _, err = openBundleOutput(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("discarded"); err != nil {
		t.Fatal(err)
	}
	if err := commit(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(os.DevNull); err != nil || info.Mode()&os.ModeDevice == 0 {
		t.Fatalf("%s is no longer a device: %v %v", os.DevNull, info, err)
	}
}
