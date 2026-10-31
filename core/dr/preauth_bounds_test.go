// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package dr

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenCipherRejectsUnboundedCosts(t *testing.T) {
	for _, tc := range []struct {
		name         string
		time, memory uint32
		threads      uint8
	}{
		{"time", 4, 8, 1}, {"memory", 1, 64*1024 + 1, 1}, {"threads", 1, 8, 5},
		{"zero time", 0, 8, 1}, {"zero memory", 1, 0, 1}, {"zero threads", 1, 8, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("untrusted KDF parameters panicked: %v", r)
				}
			}()
			_, err := OpenCipher([]byte("fixture passphrase"), KDFParams{KDF: kdfArgon2id, AEAD: aeadAESGCM, Salt: strings.Repeat("01", 16), KeyLen: 32, Time: tc.time, Memory: tc.memory, Threads: tc.threads})
			if err == nil || !strings.Contains(err.Error(), "KDF cost") {
				t.Fatalf("want KDF cost refusal, got %v", err)
			}
		})
	}
}

func TestExtractBundleRejectsUnsafeManifestSnapshot(t *testing.T) {
	for _, digest := range []string{"", strings.Repeat("0", 64)} {
		t.Run("digest="+digest, func(t *testing.T) {
			for _, name := range []string{"", "..", "../outside", "/absolute", "../../../../dev/zero", "/dev/zero", "store/../outside"} {
				t.Run(name, func(t *testing.T) {
					m := Manifest{Format: ManifestFormatV1, Store: StoreSnapshot{Method: MethodVacuumInto, File: name, SHA256: digest}}
					if err := extractLegacyPathFixture(t, m); err == nil || !strings.Contains(err.Error(), "unsafe bundle entry") {
						t.Fatalf("unsafe manifest path accepted: %v", err)
					}
				})
			}
		})
	}
}

func TestExtractBundleRejectsUnsafeLegacyKeys(t *testing.T) {
	for _, tc := range []struct{ name, file string }{
		{"key", ""}, {"key", ".."}, {"key", "../outside"}, {"key", "/absolute"},
		{"key", "../../../../dev/zero"}, {"key", "/dev/zero"}, {"key", `keys\outside`},
		{"", "keys/key.enc"}, {".", "keys/key.enc"}, {"..", "keys/key.enc"},
		{"../escape.key", "keys/key.enc"}, {"/escape.key", "keys/key.enc"},
		{"nested/key", "keys/key.enc"}, {`..\escape.key`, "keys/key.enc"},
	} {
		t.Run(tc.name+":"+tc.file, func(t *testing.T) {
			m := Manifest{Format: ManifestFormatV1, Store: StoreSnapshot{Method: MethodVacuumInto, File: "store/snapshot"}, Keys: []KeyRef{{Name: tc.name, File: tc.file}}}
			if err := extractLegacyPathFixture(t, m); err == nil || !strings.Contains(err.Error(), "unsafe") {
				t.Fatalf("want unsafe legacy key refusal, got %v", err)
			}
		})
	}
}

func TestExtractBundlePreservesLegacyPathContracts(t *testing.T) {
	for _, store := range []StoreSnapshot{
		{Method: MethodVacuumInto, File: "store/snapshot"},
		{Method: MethodPITR, File: "s3://archive/base"},
		{Method: MethodPITR},
	} {
		t.Run(store.Method+":"+store.File, func(t *testing.T) {
			m := Manifest{Format: ManifestFormatV1, Store: store, Keys: []KeyRef{{Name: "audit-signing.key", File: "keys/audit-signing.key.enc"}}}
			if err := extractLegacyPathFixture(t, m); err != nil {
				t.Fatal(err)
			}
		})
	}
	// The legacy CLI hashes a claimed digest even for PITR. A fabricated digest
	// must not turn an external archive reference into a host-file read.
	m := Manifest{Format: ManifestFormatV1, Store: StoreSnapshot{Method: MethodPITR, File: "../../../../dev/zero", SHA256: strings.Repeat("0", 64)}}
	if err := extractLegacyPathFixture(t, m); err == nil {
		t.Fatal("PITR digest allowed a host-file read")
	}
}

// Use a real archive whose member names are safe: it is the manifest's later
// file reads and writes, rather than tar extraction paths, that need refusing.
func extractLegacyPathFixture(t *testing.T, m Manifest) error {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeTarBytes(tw, bundleManifestName, b); err != nil {
		t.Fatal(err)
	}
	if err := writeTarBytes(tw, bundleKEKName, []byte(`{"kdf":"raw","aead":"aes-256-gcm"}`)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err = ExtractBundle(bytes.NewReader(buf.Bytes()), t.TempDir())
	return err
}
