// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/dr"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"

	_ "modernc.org/sqlite"
)

// sealMarker is a string only the store snapshot contains: an organization name
// is personal data that the bundle must not carry in the clear.
const sealMarker = "sealmarker-acme-9f3c1e"

// bundleMembers returns every tar member of a bundle by name, read straight from
// the file with no Olivares code in between: what anyone with the file sees.
func bundleMembers(t *testing.T, bundlePath string) map[string][]byte {
	t.Helper()
	f, err := os.Open(bundlePath)
	if err != nil {
		t.Fatalf("open bundle: %v", err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gunzip bundle: %v", err)
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("read bundle: %v", err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read member %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = body
	}
}

func createNamedOrg(t *testing.T, e *estate, slug string) {
	t.Helper()
	if err := e.st.System(context.Background(), func(sys store.SystemScope) error {
		_, err := sys.CreateOrg(context.Background(), model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
		return err
	}); err != nil {
		t.Fatalf("create org %s: %v", slug, err)
	}
}

// TestBundleSnapshotIsNotReadableWithoutKEK is the reproducer for the clear-text
// snapshot: untar a bundle and the accounts, policies and audit ledger were in
// the file. Nothing in the bundle except the manifest may be readable without the KEK.
func TestBundleSnapshotIsNotReadableWithoutKEK(t *testing.T) {
	src := newEstate(t)
	createNamedOrg(t, src, sealMarker)

	// Positive control: the marker really is in an unsealed snapshot, so finding
	// it absent from the bundle means something.
	plain := src.snapshotInto(t, filepath.Join(t.TempDir(), "plain.db"))
	raw, err := os.ReadFile(plain)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(sealMarker)) {
		t.Fatal("positive control failed: the plain snapshot does not contain the marker")
	}

	b := makeBundle(t, src)
	members := bundleMembers(t, b.path)

	var storeMembers []string
	for name, body := range members {
		if strings.HasPrefix(name, "store/") {
			storeMembers = append(storeMembers, name)
			if len(body) == 0 {
				t.Fatalf("store member %s is empty", name)
			}
		}
		if name == "manifest.json" {
			continue // the manifest is not secret and is meant to be readable
		}
		if bytes.Contains(body, []byte(sealMarker)) {
			t.Errorf("bundle member %s carries the organization name in the clear", name)
		}
		if bytes.HasPrefix(body, []byte("SQLite format 3\x00")) {
			t.Errorf("bundle member %s is a readable SQLite database", name)
		}
	}
	sort.Strings(storeMembers)
	if len(storeMembers) != 1 {
		t.Fatalf("want exactly one store/ member, got %v", storeMembers)
	}

	// The exit criterion as an operator would run it: untar, then ask sqlite.
	dir := t.TempDir()
	extracted := filepath.Join(dir, "snapshot")
	if err := os.WriteFile(extracted, members[storeMembers[0]], 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", extracted)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&n); err == nil {
		t.Fatalf("the extracted store member opened as a database with %d objects, without the KEK", n)
	}
}

// storeEntries lists the files under dir/store, which is where a half-opened
// snapshot or a temporary copy would show.
func storeEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatalf("read store dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// restoreAndVerify is the restore side of a bundle that was opened into dir: decrypt
// the audit key, open the snapshot as a store and run the continuity verification.
func restoreAndVerify(t *testing.T, dir string, m *dr.Manifest, kek dr.KDFParams) *dr.RestoreReport {
	t.Helper()
	cipher, err := dr.OpenCipher([]byte(testPass), kek)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(filepath.Join(dir, m.Keys[0].File))
	if err != nil {
		t.Fatal(err)
	}
	keyBytes, err := cipher.Open(blob)
	if err != nil {
		t.Fatal(err)
	}
	rest := openRestored(t, filepath.Join(dir, m.Store.File), decodeKeyFile(t, keyBytes))
	rep, err := dr.RestoreVerify(context.Background(), rest.st, m, rest.pub(), rest.cpVerifier(t))
	if err != nil {
		t.Fatalf("restore verify: %v", err)
	}
	return rep
}

func TestSealedBundleOpensWithTheKEK(t *testing.T) {
	src := newEstate(t)
	createNamedOrg(t, src, sealMarker)
	b := makeBundle(t, src)

	if b.manifest.Format != dr.ManifestFormat || b.manifest.Store.Encryption == "" {
		t.Fatalf("a new bundle must be the sealed format: %q, encryption %q", b.manifest.Format, b.manifest.Store.Encryption)
	}
	dir, m, kek := extractBundleRaw(t, b)
	if got := storeEntries(t, dir); len(got) != 1 || got[0] != "olivares.db.enc" {
		t.Fatalf("without the KEK only the sealed member exists, got %v", got)
	}
	if w := m.UnsealedSnapshotWarning(); w != "" {
		t.Fatalf("a sealed bundle warned: %s", w)
	}
	inventoried := map[string]bool{}
	for _, f := range m.Files {
		inventoried[f.Path] = true
	}
	if !inventoried["store/olivares.db.enc"] || inventoried["store/olivares.db"] {
		t.Fatalf("the inventory must bind the sealed member and not the plaintext name, got %v", inventoried)
	}

	cipher, err := dr.OpenCipher([]byte(testPass), kek)
	if err != nil {
		t.Fatal(err)
	}
	if err := dr.VerifyBundleIntegrity(dir, m, kek, cipher, false); err != nil {
		t.Fatalf("verify with the KEK: %v", err)
	}
	if got := storeEntries(t, dir); len(got) != 1 || got[0] != "olivares.db" {
		t.Fatalf("after opening only the snapshot remains, got %v", got)
	}
	opened, err := os.ReadFile(filepath.Join(dir, m.Store.File))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(opened, []byte(sealMarker)) {
		t.Fatal("the opened snapshot does not hold the data that was backed up")
	}
	if rep := restoreAndVerify(t, dir, m, kek); !rep.OK {
		t.Fatalf("a sealed bundle did not restore to a safe ledger: %v", rep.Problems)
	}
}

func TestSealedBundleRefusesWrongKEKOrAlteredMemberBeforeAnyPlaintext(t *testing.T) {
	src := newEstate(t)
	createNamedOrg(t, src, sealMarker)
	b := makeBundle(t, src)

	t.Run("wrong KEK", func(t *testing.T) {
		dir, m, kek := extractBundleRaw(t, b)
		wrong, err := dr.NewPassphraseCipher([]byte("not the backup passphrase"))
		if err != nil {
			t.Fatal(err)
		}
		if err := dr.VerifyBundleIntegrity(dir, m, kek, wrong, false); err == nil || !strings.Contains(err.Error(), "authentication mismatch") {
			t.Fatalf("a wrong KEK was not refused: %v", err)
		}
		if got := storeEntries(t, dir); len(got) != 1 || got[0] != "olivares.db.enc" {
			t.Fatalf("a refused bundle left plaintext behind: %v", got)
		}
	})

	t.Run("altered sealed member", func(t *testing.T) {
		dir, m, kek := extractBundleRaw(t, b)
		sealed := filepath.Join(dir, m.Store.File+".enc")
		body, err := os.ReadFile(sealed)
		if err != nil {
			t.Fatal(err)
		}
		body[len(body)/2] ^= 0x01
		if err := os.WriteFile(sealed, body, 0o600); err != nil {
			t.Fatal(err)
		}
		cipher, err := dr.OpenCipher([]byte(testPass), kek)
		if err != nil {
			t.Fatal(err)
		}
		if err := dr.VerifyBundleIntegrity(dir, m, kek, cipher, false); err == nil || !strings.Contains(err.Error(), "payload digest mismatch") {
			t.Fatalf("an altered sealed member was not refused: %v", err)
		}
		if got := storeEntries(t, dir); len(got) != 1 || got[0] != "olivares.db.enc" {
			t.Fatalf("a refused bundle left plaintext behind: %v", got)
		}
	})

	t.Run("sealed member missing", func(t *testing.T) {
		dir, m, kek := extractBundleRaw(t, b)
		if err := os.Remove(filepath.Join(dir, m.Store.File+".enc")); err != nil {
			t.Fatal(err)
		}
		cipher, err := dr.OpenCipher([]byte(testPass), kek)
		if err != nil {
			t.Fatal(err)
		}
		if err := dr.VerifyBundleIntegrity(dir, m, kek, cipher, false); err == nil || !strings.Contains(err.Error(), "read DR payload") {
			t.Fatalf("a bundle without its snapshot was not refused: %v", err)
		}
	})
}

// TestLegacyUnsealedBundleStillRestoresWithAWarning: a bundle taken before the
// snapshot was sealed must keep restoring (we do not break users), and say so.
func TestLegacyUnsealedBundleStillRestoresWithAWarning(t *testing.T) {
	src := newEstate(t)
	tn := src.newTenant(t)
	src.appendN(t, tn, 3)
	b := makeBundleFormat(t, src, true)

	// The fixture really is the old shape: snapshot in the clear under its own name.
	members := bundleMembers(t, b.path)
	if !bytes.HasPrefix(members["store/olivares.db"], []byte("SQLite format 3\x00")) {
		t.Fatal("the legacy fixture does not carry a clear snapshot")
	}

	dir, m, kek := extractBundleRaw(t, b)
	if m.Format != dr.ManifestFormatV1 || m.Store.Encryption != "" {
		t.Fatalf("not the old format: %q, encryption %q", m.Format, m.Store.Encryption)
	}
	cipher, err := dr.OpenCipher([]byte(testPass), kek)
	if err != nil {
		t.Fatal(err)
	}
	if err := dr.VerifyBundleIntegrity(dir, m, kek, cipher, false); err != nil {
		t.Fatalf("an old-format bundle no longer verifies: %v", err)
	}
	if w := m.UnsealedSnapshotWarning(); !strings.Contains(w, "NOT encrypted") {
		t.Fatalf("an old-format bundle did not warn that its snapshot is not encrypted: %q", w)
	}
	if rep := restoreAndVerify(t, dir, m, kek); !rep.OK {
		t.Fatalf("an old-format bundle did not restore to a safe ledger: %v", rep.Problems)
	}

	if w := (&dr.Manifest{Store: dr.StoreSnapshot{Method: dr.MethodPITR}}).UnsealedSnapshotWarning(); w != "" {
		t.Fatalf("a PITR companion carries no snapshot yet warned: %s", w)
	}
}

// TestWriteBundleRefusesPlaintextUnderASealedManifest: the manifest says the member
// is sealed, so shipping the plaintext under that name must not be possible.
func TestWriteBundleRefusesPlaintextUnderASealedManifest(t *testing.T) {
	src := newEstate(t)
	b := makeBundle(t, src)
	plain := src.snapshotInto(t, filepath.Join(t.TempDir(), "plain.db"))

	err := dr.WriteBundle(io.Discard, dr.BundleInput{Manifest: b.manifest, SnapshotPath: plain})
	if err == nil || !strings.Contains(err.Error(), "only the plaintext was given") {
		t.Fatalf("a sealed manifest was written with a plaintext snapshot: %v", err)
	}

	sum, size, err := dr.FileSHA256(plain)
	if err != nil {
		t.Fatal(err)
	}
	b.manifest.Store.SHA256, b.manifest.Store.SizeBytes = sum, size
	cipher, err := dr.NewRawKeyCipher(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	err = dr.AuthenticateBundle(dr.BundleInput{
		Manifest: b.manifest, KEK: cipher.Params(), SnapshotPath: plain,
		SealedKeys: map[string][]byte{b.manifest.Keys[0].File: []byte("blob")},
	}, cipher)
	if err == nil || !strings.Contains(err.Error(), "no sealed member") {
		t.Fatalf("a sealed manifest was authenticated without a sealed member: %v", err)
	}
}

// TestLegacyBundleWithAnAlteredSnapshotIsStillRefused: the digest of a signed pre-v2
// bundle's snapshot used to be checked by the callers before the manifest was
// authenticated; it is now checked by VerifyBundleIntegrity, after. An altered snapshot
// must still be refused, before anything is restored.
func TestLegacyBundleWithAnAlteredSnapshotIsStillRefused(t *testing.T) {
	src := newEstate(t)
	b := makeBundleFormat(t, src, true)
	dir, m, kek := extractBundleRaw(t, b)
	cipher, err := dr.OpenCipher([]byte(testPass), kek)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(dir, m.Store.File)
	f, err := os.OpenFile(snapshot, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("altered"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := dr.VerifyBundleIntegrity(dir, m, kek, cipher, false); err == nil || !strings.Contains(err.Error(), "payload digest mismatch for store/olivares.db") {
		t.Fatalf("an altered pre-v2 snapshot was not refused by the authenticated inventory: %v", err)
	}
}
