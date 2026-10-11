// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommunicationContentKeyringFileIsAnExactConfigKey(t *testing.T) {
	if envCommunicationContentKeyringFile != "OLIVARES_COMMUNICATION_CONTENT_KEYRING_FILE" {
		t.Fatalf("communication keyring env = %q", envCommunicationContentKeyringFile)
	}
	if mode := configEnvKeyMode(envCommunicationContentKeyringFile); mode != configKeyExact {
		t.Fatalf("%s registry mode = %v, want exact", envCommunicationContentKeyringFile, mode)
	}
}

func TestBootRejectsInvalidCommunicationKeyringBeforeStoreOpen(t *testing.T) {
	t.Setenv(envKeyWrap, "")
	dataDir := t.TempDir()
	path := filepath.Join(t.TempDir(), "invalid-communication-keyring.json")
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envCommunicationContentKeyringFile, path)

	eng, err := boot(context.Background(), bootConfig{
		DataDir: dataDir, Engine: "sqlite", Version: "test", NoIngest: true,
	})
	if eng != nil {
		_ = eng.Close()
		t.Fatal("invalid declared communication keyring returned an engine")
	}
	if !errors.Is(err, errCommunicationContentKeyring) ||
		!strings.Contains(err.Error(), envCommunicationContentKeyringFile) {
		t.Fatalf("invalid communication keyring error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "olivares.db")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("invalid communication keyring reached store open: %v", statErr)
	}
}

func TestBootTreatsWhitespaceCommunicationKeyringPathAsDeclared(t *testing.T) {
	t.Setenv(envKeyWrap, "")
	t.Setenv(envCommunicationContentKeyringFile, " \t ")
	dataDir := t.TempDir()

	eng, err := boot(context.Background(), bootConfig{
		DataDir: dataDir, Engine: "sqlite", Version: "test", NoIngest: true,
	})
	if eng != nil {
		_ = eng.Close()
		t.Fatal("whitespace communication keyring path returned an engine")
	}
	if !errors.Is(err, os.ErrNotExist) ||
		!strings.Contains(err.Error(), envCommunicationContentKeyringFile) {
		t.Fatalf("whitespace communication keyring error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dataDir, "olivares.db")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("whitespace communication keyring reached store open: %v", statErr)
	}
}
