// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package opgate

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

func factualTestKeyset() Keyset {
	var keys []SelectedKey
	for i, p := range []KeyPurpose{KeyAudit, KeyCatalog, KeyPolicy} {
		private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{byte(i + 1)}, 32))
		public := private.Public().(ed25519.PublicKey)
		f, _ := FingerprintPublicKey(public)
		keys = append(keys, SelectedKey{p, CustodyLocal, f})
	}
	k, err := NewKeyset(keys)
	if err != nil {
		panic(err)
	}
	return k
}
func TestClosedRecordJSON(t *testing.T) {
	a := storeAnchor(t)
	lease, ok, err := TryAcquire(ModeExclusive, a)
	if err != nil || !ok {
		t.Fatal(err)
	}
	defer lease.Release()
	rec := helperRecord(a, StateComplete)
	rec.Keyset = factualTestKeyset()
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var nullKeysRecord map[string]json.RawMessage
	var nullKeysSet map[string]json.RawMessage
	if err := json.Unmarshal(raw, &nullKeysRecord); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(nullKeysRecord["keyset"], &nullKeysSet); err != nil {
		t.Fatal(err)
	}
	nullKeysSet["keys"] = json.RawMessage("null")
	nullKeysRecord["keyset"], err = json.Marshal(nullKeysSet)
	if err != nil {
		t.Fatal(err)
	}
	nullKeysRaw, err := json.Marshal(nullKeysRecord)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		raw   []byte
		valid bool
	}{
		{"exact", raw, true}, {"whitespace", append(append([]byte{}, raw...), []byte(" \t\r\n")...), true},
		{"second_object", append(append([]byte{}, raw...), []byte(`{"state":"pending"}`)...), false},
		{"second_scalar", append(append([]byte{}, raw...), []byte(` null`)...), false},
		{"garbage", append(append([]byte{}, raw...), byte('x')), false},
		{"top_duplicate", bytes.Replace(raw, []byte(`"format":1`), []byte(`"format":1,"format":1`), 1), false},
		{"escaped_duplicate", bytes.Replace(raw, []byte(`"format":1`), []byte(`"format":1,"\u0066ormat":1`), 1), false},
		{"nested_duplicate", bytes.Replace(raw, []byte(`"engine":"sqlite"`), []byte(`"engine":"sqlite","engine":"sqlite"`), 1), false},
		{"key_duplicate", bytes.Replace(raw, []byte(`"purpose":"audit"`), []byte(`"purpose":"audit","purpose":"audit"`), 1), false},
		{"unknown", bytes.Replace(raw, []byte(`"format":1`), []byte(`"format":1,"unknown":false`), 1), false},
		{"case_alias", bytes.Replace(raw, []byte(`"format":1`), []byte(`"Format":1`), 1), false},
		{"future_format", bytes.Replace(raw, []byte(`"format":1`), []byte(`"format":2`), 1), false},
		{"absent_enrolled", bytes.Replace(raw, []byte(`"enrolled":true,`), nil, 1), false},
		{"false_enrolled", bytes.Replace(raw, []byte(`"enrolled":true`), []byte(`"enrolled":false`), 1), false},
		{"null_enrolled", bytes.Replace(raw, []byte(`"enrolled":true`), []byte(`"enrolled":null`), 1), false},
		{"absent_destination_field", bytes.Replace(raw, []byte(`"database":"",`), nil, 1), false},
		{"null_destination_field", bytes.Replace(raw, []byte(`"database":""`), []byte(`"database":null`), 1), false},
		{"unknown_nested_field", bytes.Replace(raw, []byte(`"engine":"sqlite"`), []byte(`"engine":"sqlite","extra":0`), 1), false},
		{"null_journal", bytes.Replace(raw, []byte(`"format":1`), []byte(`"format":1,"journal_path":null`), 1), false},
		{"null_keyset_keys", nullKeysRaw, false},
		{"missing_keyset", bytes.Replace(raw, []byte(`"keyset":`), []byte(`"missing_keyset":`), 1), false},
		{"future_keyset_format", bytes.Replace(raw, []byte(`"keyset":{"format":1`), []byte(`"keyset":{"format":2`), 1), false},
		{"noncomplete_with_keyset", bytes.Replace(raw, []byte(`"state":"complete"`), []byte(`"state":"pending"`), 1), false},
		{"null", []byte(`null`), false}, {"array", []byte(`[]`), false}, {"incomplete", []byte(`{}`), false},
		{"truncated", raw[:len(raw)-1], false}, {"oversized", bytes.Repeat([]byte(" "), maxRecordBytes+1), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(a.RecordPath(), tc.raw, 0600); err != nil {
				t.Fatal(err)
			}
			_, present, err := lease.Read(a)
			if !present || (err == nil) != tc.valid {
				t.Fatalf("present=%t err=%v valid=%t", present, err, tc.valid)
			}
		})
	}
}
func TestRecordFileRefusalsAreBoundedAndPreserveLock(t *testing.T) {
	for _, kind := range []string{"absent", "regular", "symlink", "fifo", "directory", "unreadable"} {
		t.Run(kind, func(t *testing.T) {
			a := storeAnchor(t)
			lease, ok, err := TryAcquire(ModeExclusive, a)
			if err != nil || !ok {
				t.Fatal(err)
			}
			defer lease.Release()
			lock, err := os.Stat(a.LockPath())
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "regular":
				if err := lease.Commit(a, helperRecord(a, StatePending)); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				err = os.Symlink(a.LockPath(), a.RecordPath())
			case "fifo":
				err = syscall.Mkfifo(a.RecordPath(), 0600)
			case "directory":
				err = os.Mkdir(a.RecordPath(), 0700)
			case "unreadable":
				err = os.WriteFile(a.RecordPath(), []byte("{}"), 0000)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.Lstat(a.RecordPath())
			done := make(chan struct{})
			var present bool
			var readErr error
			go func() { _, present, readErr = lease.Read(a); close(done) }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("record read blocked")
			}
			if kind == "absent" {
				if present || readErr != nil {
					t.Fatalf("absence: %t %v", present, readErr)
				}
			} else if kind == "regular" {
				if !present || readErr != nil {
					t.Fatal(readErr)
				}
			} else if !present || readErr == nil {
				t.Fatalf("refusal: %t %v", present, readErr)
			}
			after, _ := os.Lstat(a.RecordPath())
			if before != nil && (after == nil || !os.SameFile(before, after) || before.Mode() != after.Mode()) {
				t.Fatal("record object mutated")
			}
			current, _ := os.Stat(a.LockPath())
			if !os.SameFile(lock, current) {
				t.Fatal("lock inode replaced")
			}
		})
	}
}
func TestTypedKeysetCanonicalContract(t *testing.T) {
	k := factualTestKeyset()
	typed, err := k.Typed()
	if err != nil {
		t.Fatal(err)
	}
	// Independent bytes: no production encoder or marshal layout in the oracle.
	var entries []string
	for i, p := range []string{"audit", "catalog", "policy"} {
		public := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{byte(i + 1)}, 32)).Public().(ed25519.PublicKey)
		fingerprint := sha256.Sum256(public)
		entries = append(entries, `{"purpose":"`+p+`","source":"local","public_sha256":"`+hex.EncodeToString(fingerprint[:])+`"}`)
	}
	want := sha256.Sum256([]byte("olivares.dr.keyset.v1\n[" + strings.Join(entries, ",") + "]"))
	if typed.Digest() != KeysetDigest(want) {
		t.Fatal("canonical commitment differs from independent bytes")
	}
	for _, mode := range []string{"minted", "byok-env", "byok-file", "cmek"} {
		t.Run(mode, func(t *testing.T) {
			source, err := CustodySourceForLoader(mode)
			if err != nil {
				t.Fatal(err)
			}
			keys := typed.Keys()
			keys[0].Source = source
			wire, err := NewKeyset(keys[:])
			if err != nil {
				t.Fatal(err)
			}
			if _, err := wire.Typed(); err != nil {
				t.Fatal(err)
			}
			wantEntries := append([]string{}, entries...)
			wantEntries[0] = strings.Replace(wantEntries[0], `"source":"local"`, `"source":"`+string(source)+`"`, 1)
			want := sha256.Sum256([]byte("olivares.dr.keyset.v1\n[" + strings.Join(wantEntries, ",") + "]"))
			if wire.SHA256 != hex.EncodeToString(want[:]) {
				t.Fatal("source-specific commitment differs from independent bytes")
			}
		})
	}
	if _, err := CustodySourceForLoader("created-this-boot"); err == nil {
		t.Fatal("accepted unsupported custody loader mode")
	}
	for _, size := range []int{0, 31, 33, 64} {
		if _, err := FingerprintPublicKey(make([]byte, size)); err == nil {
			t.Fatalf("accepted public key size %d", size)
		}
	}
	mutations := []struct {
		name string
		fn   func(*Keyset)
	}{
		{"format", func(k *Keyset) { k.Format = 2 }}, {"digest", func(k *Keyset) { k.SHA256 = strings.Repeat("a", 64) }},
		{"short_digest", func(k *Keyset) { k.SHA256 = "ab" }}, {"uppercase", func(k *Keyset) { k.SHA256 = strings.ToUpper(k.SHA256) }},
		{"unknown_purpose", func(k *Keyset) { k.Keys[2].Purpose = "checkpoint" }}, {"duplicate", func(k *Keyset) { k.Keys[1] = k.Keys[0] }},
		{"missing", func(k *Keyset) { k.Keys = k.Keys[:2] }}, {"extra", func(k *Keyset) { k.Keys = append(k.Keys, k.Keys[0]) }},
		{"unknown_source", func(k *Keyset) { k.Keys[0].Source = "byok" }}, {"short_fingerprint", func(k *Keyset) { k.Keys[0].PublicSHA256 = "abcd" }},
		{"source_commitment", func(k *Keyset) { k.Keys[0].Source = "byok-env" }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			bad := k
			bad.Keys = append([]KeyFingerprint{}, k.Keys...)
			tc.fn(&bad)
			if _, err := bad.Typed(); err == nil {
				t.Fatal("malformed set accepted")
			}
		})
	}
}
