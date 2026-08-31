// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sigbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

// bombBundle builds a raw tar.gz (NOT a valid signed bundle) with the given entries — a
// gzip-bomb shape used to prove Read fails closed BEFORE the signature check (F9).
func bombBundle(t *testing.T, entries []struct {
	name string
	size int
}) []byte {
	t.Helper()
	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	tw := tar.NewWriter(gw)
	zero := make([]byte, 1<<16)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Typeflag: tar.TypeReg, Size: int64(e.size), Mode: 0o600}); err != nil {
			t.Fatal(err)
		}
		for remaining := e.size; remaining > 0; {
			n := remaining
			if n > len(zero) {
				n = len(zero)
			}
			if _, err := tw.Write(zero[:n]); err != nil {
				t.Fatal(err)
			}
			remaining -= n
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return gzBuf.Bytes()
}

// TestReadEntryCountBombRejectedBeforeVerify: a bundle of millions of tiny entries must be
// rejected on the entry-count ceiling BEFORE any signature work — never buffered unboundedly.
func TestReadEntryCountBombRejectedBeforeVerify(t *testing.T) {
	entries := make([]struct {
		name string
		size int
	}, maxBundleEntries+50)
	for i := range entries {
		entries[i].name = "e" + itoa(i) + ".bin"
		entries[i].size = 1
	}
	bomb := bombBundle(t, entries)

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, err := Read(bytes.NewReader(bomb), TagUpdateManifest, pub, time.Now().UTC())
	if err == nil {
		t.Fatal("a bundle over the entry-count ceiling must be rejected (F9)")
	}
	if !strings.Contains(err.Error(), "entries") {
		t.Errorf("error = %v, want the entry-count ceiling", err)
	}
}

// TestReadByteBombRejectedBeforeVerify: a bundle whose decompressed payload exceeds the total
// ceiling must be rejected with a bounded read, never OOMing the importer pre-verification.
func TestReadByteBombRejectedBeforeVerify(t *testing.T) {
	bomb := bombBundle(t, []struct {
		name string
		size int
	}{{name: "big.bin", size: maxBundleBytes + (1 << 20)}}) // 1 MiB over the ceiling

	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	_, err := Read(bytes.NewReader(bomb), TagUpdateManifest, pub, time.Now().UTC())
	if err == nil {
		t.Fatal("a bundle over the total-decompressed ceiling must be rejected (F9)")
	}
	// The bounded reader surfaces the overflow as a truncated read (EOF at the ceiling) or an
	// oversize-tar error — either way the payload is NEVER fully buffered pre-verification.
	msg := err.Error()
	if !(strings.Contains(msg, "tar") || strings.Contains(msg, "entries") ||
		strings.Contains(msg, "EOF") || strings.Contains(msg, "read entry")) {
		t.Errorf("error = %v, want a bounded read failure", err)
	}
}

// itoa avoids strconv import churn in this small test.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
