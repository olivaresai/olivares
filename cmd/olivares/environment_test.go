// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootWarnsUnknownEnvironmentBeforeOpeningData(t *testing.T) {
	clearOlivaresEnv(t)
	const key, value = "OLIVARES_MISSPELLED_SETTING", "synthetic-private-value"
	t.Setenv(key, value)
	var out bytes.Buffer
	log := slog.New(slog.NewTextHandler(&out, nil))
	_, err := boot(context.Background(), bootConfig{
		ReadOnly: true, DataDir: filepath.Join(t.TempDir(), "absent"), Logger: log,
	})
	if err == nil {
		t.Fatal("expected refusal to open an absent read-only data directory")
	}
	if !strings.Contains(out.String(), key) || !strings.Contains(out.String(), "WARN") {
		t.Fatalf("boot did not warn about the unknown key: %s", out.String())
	}
	if strings.Contains(out.String(), value) {
		t.Fatal("boot warning leaked the unknown key's value")
	}
}

func TestEngineEnvironmentLoaderUsesEffectiveValue(t *testing.T) {
	const key = "OLIVARES_SANDBOX_RUNTIME_CONFIG"
	t.Setenv(key, "")
	path := filepath.Join(t.TempDir(), "sandbox.json")
	if err := os.WriteFile(path, []byte(`{"default":"gvisor"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	setActivationOverlayForTest(map[string]string{key: path})
	t.Cleanup(func() { setActivationOverlayForTest(nil) })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg, err := loadSandboxRuntimeConfig(log)
	if err != nil || cfg.Default != "gvisor" {
		t.Fatalf("loader ignored effective configuration: default=%q err=%v", cfg.Default, err)
	}
	t.Setenv(key, path+".missing")
	if _, err := loadSandboxRuntimeConfig(log); err == nil {
		t.Fatal("explicit missing path must override the overlay and fail")
	}
}

func TestConfigHelpIncludesEnvironmentReference(t *testing.T) {
	out, err := executeConfigCommand("--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range exactConfigEnvKeys {
		if !strings.Contains(out, key) {
			t.Errorf("config --help omitted %s", key)
		}
	}
}

func TestEngineEnvironmentPreservesProcessOnlyCustody(t *testing.T) {
	for _, key := range []string{envAuditKey, envAuditKeyFile, envAuditWrapped, envKeyWrap, envKeyCustody, envLedgerCustody, "OLIVARES_LEDGER_SIGNER"} {
		t.Setenv(key, "")
	}
	setActivationOverlayForTest(map[string]string{
		envAuditKey:              "synthetic-signing-material",
		envKeyWrap:               "invalid-wrap-backend",
		envKeyCustody:            "invalid-custody",
		envLedgerCustody:         "invalid-custody",
		"OLIVARES_LEDGER_SIGNER": "invalid-signer",
	})
	t.Cleanup(func() { setActivationOverlayForTest(nil) })
	if externalKeyCustodyConfigured() {
		t.Fatal("activation must not declare process-only key custody")
	}
	if cfg, err := describeConfiguredKEK(envKeyWrap); cfg != "none" || err != nil {
		t.Fatalf("activation changed process-only key wrapping: %v, %v", cfg, err)
	}
	if cfg, err := loadCustodyAssertions(); cfg.auditKey != "" || cfg.ledger != "" || err != nil {
		t.Fatalf("activation changed process-only custody assertions: %v, %v", cfg, err)
	}
	if key, err := buildCheckpointKey(slog.New(slog.NewTextHandler(io.Discard, nil))); key != nil || err != nil {
		t.Fatalf("activation changed process-only ledger signer: %v, %v", key, err)
	}
}
