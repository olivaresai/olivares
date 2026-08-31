// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigHasInlineSecret(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"plain non-secret json", `{"name":"prod","enabled":true,"endpoint":"https://x.example"}`, false},
		{"file reference password", `{"password":"file:/run/secrets/db"}`, false},
		{"env reference client_secret", `{"client_secret":"env:OAUTH_SECRET"}`, false},
		{"store reference", `{"client_secret":"store:source/x/secret"}`, false},
		{"literal client_secret", `{"client_secret":"abc123literalvalue"}`, true},
		{"sk- provider key in any field", `{"any":"sk-ant-api03-AAAAAAAAAAAAAAAA"}`, true},
		{"sk- mid-word is not a key (disk-)", `{"name":"disk-encryption-at-rest-enabled"}`, false},
		{"sk- mid-word is not a key (task-)", `{"kind":"task-orchestration-pipeline-v2"}`, false},
		{"sk- mid-word is not a key (risk-)", `{"name":"risk-assessment-engine-default"}`, false},
		{"sk- mid-word is not a key (flask-)", `{"service":"flask-app-backend-service-name"}`, false},
		{"password-named flag with bool value", `{"reset_password_enabled":"true"}`, false},
		{"password-named flag with numeric value", `{"password_policy_min_length":"8"}`, false},
		{"aws access key value", `{"creds":{"id":"AKIAIOSFODNN7EXAMPLE"}}`, true},
		{"inline-credential dsn", `{"dsn":"postgres://u:realpass@h:5432/db"}`, true},
		{"literal secret_access_key", `{"secret_access_key":"wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"}`, true},
		{"password_file is a path not a secret", `{"password_file":"/run/secrets/db"}`, false},
		{"pem private key", "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----", true},
		{"pem certificate is public", "-----BEGIN CERTIFICATE-----\nabc\n-----END CERTIFICATE-----", false},
		{"empty password", `{"password":""}`, false},
		{"placeholder password", `{"password":"********"}`, false},
		{"nested array of secrets", `{"items":[{"password":"realsecretvalue"}]}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := configHasInlineSecret([]byte(c.in)); got != c.want {
				t.Errorf("configHasInlineSecret(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestReadOperatorConfigNotesUnsealedSecret checks the end-to-end accumulator: a
// referenced secret is NOT noted; a cleartext one IS, by path.
func TestReadOperatorConfigNotesUnsealedSecret(t *testing.T) {
	resetUnsealedSecretConfigs()
	dir := t.TempDir()

	referenced := filepath.Join(dir, "referenced.json")
	if err := os.WriteFile(referenced, []byte(`{"client_secret":"file:/run/secrets/oauth"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readOperatorConfig(referenced); err != nil {
		t.Fatal(err)
	}
	if got := drainUnsealedSecretConfigs(); len(got) != 0 {
		t.Fatalf("a referenced secret must not be noted, got %v", got)
	}

	cleartext := filepath.Join(dir, "s3.json")
	if err := os.WriteFile(cleartext, []byte(`{"secret_access_key":"wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readOperatorConfig(cleartext); err != nil {
		t.Fatal(err)
	}
	got := drainUnsealedSecretConfigs()
	if len(got) != 1 || got[0] != cleartext {
		t.Fatalf("want [%s] noted, got %v", cleartext, got)
	}
	// drain cleared it
	if again := drainUnsealedSecretConfigs(); len(again) != 0 {
		t.Errorf("drain should clear; got %v", again)
	}
}

// TestBootWarnsOnUnsealedSecretConfig is the end-to-end check: a real boot that
// loads a cleartext-secret operator config (via OLIVARES_NOTIFY_CONFIG) emits the
// advisory WARN naming the file — and never fails the boot.
func TestBootWarnsOnUnsealedSecretConfig(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "notify.json")
	if err := os.WriteFile(cfgPath, []byte(`[{"name":"cleartext-fixture","kind":"unknown-test-kind","config":{"secret_access_key":"wJalrXUtnFEMIK7MDENGbPxRfiCYEXAMPLEKEY"}}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLIVARES_NOTIFY_CONFIG", cfgPath)

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	eng, err := boot(context.Background(), bootConfig{DataDir: t.TempDir(), Engine: "sqlite", Version: "test", Logger: log})
	if err != nil {
		t.Fatalf("boot must not fail on a cleartext secret (advisory only): %v", err)
	}
	_ = eng.Close()

	out := buf.String()
	if !strings.Contains(out, "cleartext secret") || !strings.Contains(out, cfgPath) {
		t.Errorf("expected an unsealed-secret WARN naming %s, got logs:\n%s", cfgPath, out)
	}
}
