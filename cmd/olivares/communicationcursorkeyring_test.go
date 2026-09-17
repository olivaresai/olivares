// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions"
)

func cursorKeyringDocument(currentKID string, entries ...communicationCursorKeyringEntry) []byte {
	raw, err := json.Marshal(communicationCursorKeyringDocument{
		Format: communicationCursorKeyringFormat, CurrentKID: currentKID, Keys: entries,
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func cursorKeyringEntry(kid string, fill byte, retiredAt time.Time) communicationCursorKeyringEntry {
	entry := communicationCursorKeyringEntry{
		KID: kid, KeyBase64: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32)),
	}
	if !retiredAt.IsZero() {
		entry.RetiredAt = retiredAt.UTC().Format(time.RFC3339)
	}
	return entry
}

func writeCursorKeyringFile(t *testing.T, raw []byte, perm os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cursor-keyring.json")
	if err := os.WriteFile(path, raw, perm); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCommunicationCursorKeyringEnvKeysAreExact(t *testing.T) {
	for _, key := range []string{envCommunicationCursorKeyringFile, envCommunicationActivation} {
		if mode := configEnvKeyMode(key); mode != configKeyExact {
			t.Fatalf("%s registry mode = %v, want exact", key, mode)
		}
	}
}

func TestLoadCommunicationCursorKeyringRetainsRotatedKeysWithinTheWindow(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	recent := now.Add(-time.Minute)
	stale := now.Add(-sessions.CommunicationCursorTokenRetentionWindow - time.Minute)
	path := writeCursorKeyringFile(t, cursorKeyringDocument("k2",
		cursorKeyringEntry("k2", 0x21, time.Time{}),
		cursorKeyringEntry("k1", 0x22, recent),
		cursorKeyringEntry("k0", 0x23, stale),
	), 0o600)
	keyring, status, err := loadCommunicationCursorKeyring(context.Background(), path, nil, now)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if keyring == nil || status.SigningKID != "k2" || strings.Join(status.VerificationKIDs, ",") != "k1,k2" ||
		status.DroppedRetired != 1 {
		t.Fatalf("status = %+v", status)
	}
	m := sessions.New()
	m.UseCommunicationCursorTokenKeyring(keyring)
	if !m.CommunicationCursorTokenKeyringBound() {
		t.Fatal("loaded keyring did not bind")
	}
	if got, gotStatus, err := loadCommunicationCursorKeyring(context.Background(), "", nil, now); got != nil || err != nil ||
		gotStatus.SigningKID != "" {
		t.Fatalf("empty path = %v %+v %v, want unbound and no error", got, gotStatus, err)
	}
}

func TestLoadCommunicationCursorKeyringRefusesUnsafeOrMalformedCustody(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	cases := map[string]struct {
		raw  []byte
		perm os.FileMode
	}{
		"current retired": {cursorKeyringDocument("k1", cursorKeyringEntry("k1", 0x31, now.Add(-time.Second))), 0o600},
		"missing current": {cursorKeyringDocument("k9", cursorKeyringEntry("k1", 0x31, time.Time{})), 0o600},
		"unknown member":  {[]byte(`{"format":"` + communicationCursorKeyringFormat + `","current_kid":"k1","keys":[{"kid":"k1","key_base64":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)) + `","extra":1}]}`), 0o600},
		"wrong format":    {[]byte(`{"format":"other","current_kid":"k1","keys":[]}`), 0o600},
		"short key":       {[]byte(`{"format":"` + communicationCursorKeyringFormat + `","current_kid":"k1","keys":[{"kid":"k1","key_base64":"c2hvcnQ="}]}`), 0o600},
		"world readable":  {cursorKeyringDocument("k1", cursorKeyringEntry("k1", 0x31, time.Time{})), 0o644},
		"bad retired_at":  {[]byte(`{"format":"` + communicationCursorKeyringFormat + `","current_kid":"k1","keys":[{"kid":"k1","key_base64":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)) + `"},{"kid":"k0","key_base64":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)) + `","retired_at":"yesterday"}]}`), 0o600},
		"not json at all": {[]byte("nope"), 0o600},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeCursorKeyringFile(t, tc.raw, tc.perm)
			keyring, _, err := loadCommunicationCursorKeyring(context.Background(), path, nil, now)
			if err == nil || keyring != nil {
				t.Fatalf("%s: accepted (%v)", name, err)
			}
		})
	}
}
