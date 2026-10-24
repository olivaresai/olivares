// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The chunk size is tiny here so a handful of bytes spans several chunks. A frame
// is nonce (12) + chunk + tag (16).
const (
	testChunk = 16
	testFrame = 12 + testChunk + 16
)

func testCipher(t *testing.T, fill byte) *KeyCipher {
	t.Helper()
	key := bytes.Repeat([]byte{fill}, kekLen)
	c, err := NewRawKeyCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func sealBytes(t *testing.T, c *KeyCipher, plain []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	if err := c.sealStream(&out, bytes.NewReader(plain), testChunk); err != nil {
		t.Fatalf("seal: %v", err)
	}
	return out.Bytes()
}

func openBytes(c *KeyCipher, sealed []byte) ([]byte, error) {
	var out bytes.Buffer
	err := c.openStream(&out, bytes.NewReader(sealed))
	return out.Bytes(), err
}

func TestSnapshotStreamRoundTripAtChunkBoundaries(t *testing.T) {
	c := testCipher(t, 1)
	for _, size := range []int{0, 1, testChunk - 1, testChunk, testChunk + 1, 3 * testChunk, 3*testChunk + 7} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			plain := randomBytes(t, size)
			sealed := sealBytes(t, c, plain)
			if size > 0 && bytes.Contains(sealed, plain) {
				t.Fatal("plaintext leaked into the sealed stream")
			}
			got, err := openBytes(c, sealed)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			if !bytes.Equal(got, plain) {
				t.Fatalf("round trip differs: %d bytes in, %d out", len(plain), len(got))
			}
		})
	}
}

// TestSnapshotStreamSealsEveryChunkWithItsOwnNonce guards the one thing AES-GCM
// cannot survive: the same nonce twice under one key.
func TestSnapshotStreamSealsEveryChunkWithItsOwnNonce(t *testing.T) {
	c := testCipher(t, 1)
	sealed := sealBytes(t, c, randomBytes(t, 20*testChunk))
	seen := map[string]int{}
	for i := snapshotHeaderLen; i+12 <= len(sealed); i += testFrame {
		seen[string(sealed[i:i+12])]++
	}
	if len(seen) != 20 {
		t.Fatalf("want 20 distinct nonces, got %d", len(seen))
	}
}

func TestSnapshotStreamRefusesAnythingButTheSealedBytes(t *testing.T) {
	c := testCipher(t, 1)
	// 4 full chunks: the last is full and final, the hardest truncation case.
	plain := randomBytes(t, 4*testChunk)
	sealed := sealBytes(t, c, plain)
	frame := func(i int) []byte { return sealed[snapshotHeaderLen+i*testFrame : snapshotHeaderLen+(i+1)*testFrame] }
	join := func(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
	header := sealed[:snapshotHeaderLen]

	if got, err := openBytes(c, sealed); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("positive control failed: %v", err)
	}

	flip := func(at int) []byte {
		b := bytes.Clone(sealed)
		b[at] ^= 0x01
		return b
	}
	tooBig := bytes.Clone(sealed)
	binary.BigEndian.PutUint32(tooBig[len(snapshotMagic):], snapshotMaxChunkSize+1)
	zero := bytes.Clone(sealed)
	binary.BigEndian.PutUint32(zero[len(snapshotMagic):], 0)
	badMagic := bytes.Clone(sealed)
	badMagic[0] ^= 0x01
	otherChunk := bytes.Clone(sealed)
	binary.BigEndian.PutUint32(otherChunk[len(snapshotMagic):], testChunk*2)

	cases := []struct {
		name    string
		sealed  []byte
		kek     *KeyCipher
		wantErr string
	}{
		{"wrong KEK", sealed, testCipher(t, 2), "does not authenticate"},
		{"flipped nonce byte", flip(snapshotHeaderLen + 1), c, "does not authenticate"},
		{"flipped ciphertext byte", flip(snapshotHeaderLen + testFrame + 20), c, "does not authenticate"},
		{"flipped tag byte", flip(len(sealed) - 1), c, "does not authenticate"},
		{"two chunks swapped", join(header, frame(0), frame(2), frame(1), frame(3)), c, "does not authenticate"},
		{"first and last swapped", join(header, frame(3), frame(1), frame(2), frame(0)), c, "does not authenticate"},
		{"middle chunk dropped", join(header, frame(0), frame(2), frame(3)), c, "does not authenticate"},
		{"chunk duplicated", join(header, frame(0), frame(1), frame(1), frame(2), frame(3)), c, "does not authenticate"},
		{"truncated at a chunk boundary", join(header, frame(0), frame(1), frame(2)), c, "does not authenticate"},
		{"truncated mid chunk", sealed[:len(sealed)-5], c, "does not authenticate"},
		{"extended by a whole sealed chunk", join(sealed, frame(1)), c, "does not authenticate"},
		{"extended by trailing bytes", join(sealed, []byte("trailing")), c, "does not authenticate"},
		{"header only", header, c, "truncated"},
		{"empty", nil, c, "header"},
		{"bad magic", badMagic, c, "unknown header"},
		{"chunk size changed in header", otherChunk, c, "does not authenticate"},
		{"chunk size zero", zero, c, "out of range"},
		{"chunk size above the cap", tooBig, c, "out of range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := openBytes(tc.kek, tc.sealed)
			if err == nil {
				t.Fatal("a stream that is not exactly what was sealed opened")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not name %q", err, tc.wantErr)
			}
		})
	}
}

// TestOpenBundleSnapshotChecksThePlaintextTheManifestVouchesFor: the member
// authenticates, yet it is not the snapshot the keyed manifest describes (a
// producer that sealed the wrong file). The opened file must not survive that.
func TestOpenBundleSnapshotChecksThePlaintextTheManifestVouchesFor(t *testing.T) {
	c := testCipher(t, 1)
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "store"), 0o700); err != nil {
		t.Fatal(err)
	}
	plain := randomBytes(t, 3*testChunk+5)
	sum := sha256.Sum256(plain)
	if err := os.WriteFile(filepath.Join(work, "store", "x.db.enc"), sealBytes(t, c, plain), 0o600); err != nil {
		t.Fatal(err)
	}
	good := StoreSnapshot{Method: MethodVacuumInto, File: "store/x.db", Encryption: snapshotEncChunked,
		SizeBytes: int64(len(plain)), SHA256: hex.EncodeToString(sum[:])}

	for name, store := range map[string]StoreSnapshot{
		"wrong digest": {Method: good.Method, File: good.File, Encryption: good.Encryption, SizeBytes: good.SizeBytes, SHA256: strings.Repeat("0", 64)},
		"wrong size":   {Method: good.Method, File: good.File, Encryption: good.Encryption, SizeBytes: good.SizeBytes + 1, SHA256: good.SHA256},
	} {
		t.Run(name, func(t *testing.T) {
			err := openBundleSnapshot(work, &Manifest{Store: store}, c)
			if err == nil || !strings.Contains(err.Error(), "digest mismatch after opening") {
				t.Fatalf("a snapshot that is not the one the manifest describes was accepted: %v", err)
			}
			if _, statErr := os.Stat(filepath.Join(work, "store", "x.db")); statErr == nil {
				t.Fatal("the mismatching snapshot was left on disk")
			}
		})
	}

	if err := openBundleSnapshot(work, &Manifest{Store: good}, c); err != nil {
		t.Fatalf("positive control: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(work, "store", "x.db"))
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("opened snapshot differs from what was sealed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, "store", "x.db.enc")); err == nil {
		t.Fatal("the sealed copy was left beside the opened snapshot")
	}
}
