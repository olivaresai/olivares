// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// sealedTestBundle writes and extracts a bundle whose snapshot member is mutate
// applied to the real sealed stream. The inventory and the keyed manifest are
// computed over that member, so it passes every check before the chunks are opened:
// the case a producer holding the KEK, or a bug in one, could create.
func sealedTestBundle(t *testing.T, c *KeyCipher, plain []byte, mutate func([]byte) []byte) (string, *Manifest, KDFParams) {
	t.Helper()
	dir := t.TempDir()
	plainPath := filepath.Join(dir, "x.db")
	sealedPath := filepath.Join(dir, "x.db.sealed")
	if err := os.WriteFile(plainPath, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sealedPath, mutate(sealBytes(t, c, plain)), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(plain)
	m := &Manifest{Format: ManifestFormat, EngineKind: "sqlite", TipMatch: TipExact, Store: StoreSnapshot{
		Method: MethodVacuumInto, File: "store/x.db", SizeBytes: int64(len(plain)),
		SHA256: hex.EncodeToString(sum[:]), Encryption: snapshotEncChunked,
	}}
	in := BundleInput{Manifest: m, KEK: c.Params(), SnapshotPath: plainPath, SealedKeys: map[string][]byte{}, sealedSnapshot: sealedPath}
	if err := AuthenticateBundle(in, c); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteBundle(&buf, in); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	got, kek, err := ExtractBundle(&buf, work)
	if err != nil {
		t.Fatal(err)
	}
	return work, got, kek
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// TestVerifyBundleIntegrityRefusesAnAuthenticatedBundleWithABadChunk: the chunk
// authentication is a second line behind the inventory digest, and it is the one that
// names a reordered or truncated stream. A refusal leaves no plaintext behind.
func TestVerifyBundleIntegrityRefusesAnAuthenticatedBundleWithABadChunk(t *testing.T) {
	c := testCipher(t, 1)
	plain := randomBytes(t, 4*testChunk)
	frame := func(sealed []byte, i int) []byte {
		return sealed[snapshotHeaderLen+i*testFrame : snapshotHeaderLen+(i+1)*testFrame]
	}
	cases := []struct {
		name   string
		mutate func([]byte) []byte
		want   string
	}{
		{"unaltered", func(b []byte) []byte { return b }, ""},
		{"two chunks swapped", func(b []byte) []byte {
			return bytes.Join([][]byte{b[:snapshotHeaderLen], frame(b, 0), frame(b, 2), frame(b, 1), frame(b, 3)}, nil)
		}, "does not authenticate"},
		{"last chunk dropped", func(b []byte) []byte { return b[:len(b)-testFrame] }, "does not authenticate"},
		{"a chunk appended", func(b []byte) []byte { return append(bytes.Clone(b), frame(b, 1)...) }, "does not authenticate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			work, m, kek := sealedTestBundle(t, c, plain, tc.mutate)
			err := VerifyBundleIntegrity(work, m, kek, c, false)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("positive control: %v", err)
				}
				got, rerr := os.ReadFile(filepath.Join(work, "store", "x.db"))
				if rerr != nil || !bytes.Equal(got, plain) {
					t.Fatalf("opened snapshot differs from what was sealed: %v", rerr)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("a bundle with a bad chunk was accepted or refused for another reason: %v", err)
			}
			if got := dirNames(t, filepath.Join(work, "store")); len(got) != 1 || got[0] != "x.db.enc" {
				t.Fatalf("a refused bundle left %v in the scratch directory, want only the sealed member", got)
			}
		})
	}
}

// The format and the snapshot encryption of a keyed manifest must agree. Only a
// holder of the KEK can write one where they differ, so the manifest is re-keyed here.
func TestVerifyBundleIntegrityRefusesAKeyedManifestWhereFormatAndEncryptionDisagree(t *testing.T) {
	c := testCipher(t, 1)
	plain := randomBytes(t, 2*testChunk)
	for _, tc := range []struct {
		name string
		edit func(*Manifest)
		want string
	}{
		{"v2 naming an unsealed snapshot", func(m *Manifest) { m.Store.Encryption = "" }, "disagree"},
		{"v1 naming a sealed snapshot", func(m *Manifest) { m.Format = ManifestFormatV1 }, "disagree"},
		{"an encryption this reader does not know", func(m *Manifest) { m.Store.Encryption = "rot13" }, "unsupported snapshot encryption"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work, m, kek := sealedTestBundle(t, c, plain, func(b []byte) []byte { return b })
			tc.edit(m)
			value, err := manifestMAC(m, c.kek)
			if err != nil {
				t.Fatal(err)
			}
			m.Authentication.Value = value
			if err := VerifyBundleIntegrity(work, m, kek, c, false); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error naming %q", err, tc.want)
			}
		})
	}
}

// A PITR companion carries no snapshot bytes: nothing to seal, no store member, and
// no warning, in the current format.
func TestPITRCompanionBundleCarriesNoSnapshotAndStaysClean(t *testing.T) {
	c := testCipher(t, 1)
	m := &Manifest{Format: ManifestFormat, EngineKind: "postgres", TipMatch: TipAdvisory,
		Store: StoreSnapshot{Method: MethodPITR, File: "s3://archive/base"}}
	var buf bytes.Buffer
	if err := WriteAuthenticatedBundle(&buf, BundleInput{Manifest: m, KEK: c.Params(), SealedKeys: map[string][]byte{}}, c); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	got, kek, err := ExtractBundle(&buf, work)
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != ManifestFormat || got.Store.Encryption != "" {
		t.Fatalf("PITR manifest: format %q, encryption %q", got.Format, got.Store.Encryption)
	}
	if _, err := os.Stat(filepath.Join(work, "store")); err == nil {
		t.Fatal("a PITR companion has a store member")
	}
	if err := VerifyBundleIntegrity(work, got, kek, c, false); err != nil {
		t.Fatalf("PITR companion: %v", err)
	}
	if w := got.UnsealedSnapshotWarning(); w != "" {
		t.Fatalf("a PITR companion warned: %s", w)
	}
}

// TestSealSnapshotFileAtTheProductionChunkSize runs the file helpers with the size
// the engine really uses, over more than two chunks.
func TestSealSnapshotFileAtTheProductionChunkSize(t *testing.T) {
	if snapshotChunkSize > snapshotMaxChunkSize {
		t.Fatalf("the writer's chunk size %d exceeds what the reader accepts (%d)", snapshotChunkSize, snapshotMaxChunkSize)
	}
	c := testCipher(t, 1)
	dir := t.TempDir()
	plain := randomBytes(t, 2*snapshotChunkSize+1)
	src := filepath.Join(dir, "snap.db")
	if err := os.WriteFile(src, plain, 0o600); err != nil {
		t.Fatal(err)
	}
	sealed, err := sealSnapshotFile(c, src)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(snapshotHeaderLen + len(plain) + 3*28); info.Size() != want {
		t.Fatalf("sealed size = %d, want %d (3 chunks of nonce+tag)", info.Size(), want)
	}
	dst := filepath.Join(dir, "opened.db")
	size, sum, err := openSnapshotFile(c, sealed, dst)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(plain)
	got, rerr := os.ReadFile(dst)
	if rerr != nil || !bytes.Equal(got, plain) || size != int64(len(plain)) || sum != hex.EncodeToString(want[:]) {
		t.Fatalf("round trip: size %d, sum %s, read err %v", size, sum, rerr)
	}
}

// A sealing that fails must not leave a sealed copy beside the snapshot, and a
// bundle that fails its pre-write check must not have written a byte.
func TestSealingFailureLeavesNoSealedCopyAndWritesNothing(t *testing.T) {
	c := testCipher(t, 1)

	parent := t.TempDir()
	if err := os.Mkdir(filepath.Join(parent, "snap"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := sealSnapshotFile(c, filepath.Join(parent, "snap")); err == nil {
		t.Fatal("sealing a directory succeeded")
	}
	if got := dirNames(t, parent); len(got) != 1 || got[0] != "snap" {
		t.Fatalf("a failed sealing left %v", got)
	}

	dir := t.TempDir()
	plainPath := filepath.Join(dir, "x.db")
	if err := os.WriteFile(plainPath, randomBytes(t, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &Manifest{Format: ManifestFormat, EngineKind: "sqlite", TipMatch: TipExact, Store: StoreSnapshot{
		Method: MethodVacuumInto, File: "store/x.db", SizeBytes: 100, SHA256: strings.Repeat("0", 64),
	}}
	var out bytes.Buffer
	err := WriteAuthenticatedBundle(&out, BundleInput{Manifest: m, KEK: c.Params(), SnapshotPath: plainPath, SealedKeys: map[string][]byte{}}, c)
	if err == nil || !strings.Contains(err.Error(), "snapshot bytes differ from manifest") {
		t.Fatalf("a snapshot that differs from its manifest was bundled: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("a refused bundle wrote %d bytes", out.Len())
	}
	if got := dirNames(t, dir); len(got) != 1 || got[0] != "x.db" {
		t.Fatalf("a refused bundle left %v beside the snapshot", got)
	}
}

// TestSnapshotSealUsesAFreshNonceForEverySeal guards GCM's one hard rule across
// backups: the same chunk of the same snapshot sealed twice under one KEK must not
// repeat a nonce.
func TestSnapshotSealUsesAFreshNonceForEverySeal(t *testing.T) {
	c := testCipher(t, 1)
	plain := randomBytes(t, 3*testChunk)
	first, second := sealBytes(t, c, plain), sealBytes(t, c, plain)
	n0 := first[snapshotHeaderLen : snapshotHeaderLen+12]
	n1 := second[snapshotHeaderLen : snapshotHeaderLen+12]
	if bytes.Equal(n0, n1) {
		t.Fatal("two seals of the same snapshot used the same nonce for chunk 0")
	}
	if bytes.Equal(first[snapshotHeaderLen+12:snapshotHeaderLen+testFrame], second[snapshotHeaderLen+12:snapshotHeaderLen+testFrame]) {
		t.Fatal("two seals of the same chunk produced the same ciphertext")
	}
}

// TestV1ManifestEncodingIsUnchanged pins the JSON of a v1 manifest. The keyed
// authentication is an HMAC over this encoding, so a bundle taken before the
// snapshot was sealed verifies only while a manifest without encryption marshals to
// exactly the bytes the old struct produced.
func TestV1ManifestEncodingIsUnchanged(t *testing.T) {
	m := Manifest{
		Format: ManifestFormatV1, CreatedAt: "2026-01-02T03:04:05Z", EngineKind: "sqlite", Version: "1.0",
		Store:    StoreSnapshot{Method: MethodVacuumInto, File: "store/olivares.db", SizeBytes: 5, SHA256: "ab"},
		TipMatch: TipExact,
	}
	got, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"format":"olivares.dr.manifest.v1","created_at":"2026-01-02T03:04:05Z","engine":"sqlite","engine_version":"1.0",` +
		`"store":{"method":"sqlite-vacuum-into","file":"store/olivares.db","size_bytes":5,"sha256":"ab"},` +
		`"tenants":null,"keys":null,"tip_match":"exact","authentication":{"algorithm":"","value":""}}`
	if string(got) != want {
		t.Fatalf("a v1 manifest no longer marshals as it did:\n got %s\nwant %s", got, want)
	}
}
