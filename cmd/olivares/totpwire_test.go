// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"github.com/olivaresai/olivares/core/model"
	"os"
	"path/filepath"
	"testing"
)

func TestTOTPSeedSealerIsolationAndMalformedEnvelopes(t *testing.T) {
	ctx := context.Background()
	env := func(key string) func(string) string { return func(string) string { return key } }
	a, err := newTOTPSeedSealer(t.TempDir(), env(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))))
	if err != nil {
		t.Fatal(err)
	}
	b, err := newTOTPSeedSealer(t.TempDir(), env(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))))
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("synthetic seed")
	sealed, err := a.Seal(ctx, model.SystemTenantID, plain)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := a.Open(ctx, model.SystemTenantID, sealed)
	if err != nil || !bytes.Equal(opened, plain) {
		t.Fatal("own scope/key does not open")
	}
	if _, err = a.Open(ctx, model.TenantID(model.NewID()), sealed); err == nil {
		t.Fatal("foreign scope opened seed")
	}
	if _, err = b.Open(ctx, model.SystemTenantID, sealed); err == nil {
		t.Fatal("foreign key opened seed")
	}
	for _, bad := range []string{"v2:AA==", "v1:invalid!", "v1:", "v1:AA=="} {
		if _, err = a.Open(ctx, model.SystemTenantID, bad); err == nil {
			t.Fatal("malformed envelope opened")
		}
	}
}
func TestTOTPSeedSealerKeyCustody(t *testing.T) {
	dir := t.TempDir()
	empty := func(string) string { return "" }
	if _, err := newTOTPSeedSealer(dir, empty); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, totpSeedKeyFile)
	stat, err := os.Stat(p)
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("seed key is not private")
	}
	if err = os.Chmod(p, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = newTOTPSeedSealer(dir, empty); err == nil {
		t.Fatal("public key file accepted")
	}
	if _, err = newTOTPSeedSealer(t.TempDir(), func(string) string { return "bad" }); err == nil {
		t.Fatal("malformed environment key accepted")
	}
}

func TestTOTPSeedKeyConfigContract(t *testing.T) {
	if configEnvKeyMode(totpSeedKeyEnv) != configKeyExact {
		t.Error("TOTP HA seed key refused by strict config validation")
	}
	entries := effectiveConfigEntries([]string{totpSeedKeyEnv + "=synthetic-sensitive-value"}, func(string) string { return "synthetic-sensitive-value" })
	found := false
	for _, entry := range entries {
		if entry.Key == totpSeedKeyEnv {
			found = true
			if entry.Value != redactedConfigValue || !entry.Redacted {
				t.Error("TOTP seed key not redacted")
			}
		}
	}
	if !found {
		t.Error("TOTP seed key absent from effective config")
	}
}
