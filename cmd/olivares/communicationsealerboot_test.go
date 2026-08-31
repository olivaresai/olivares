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

	"github.com/olivaresai/olivares/core/secure"
)

func TestCommunicationContentKeyringFileIsAnExactConfigKey(t *testing.T) {
	if envCommunicationContentKeyringFile != "OLIVARES_COMMUNICATION_CONTENT_KEYRING_FILE" {
		t.Fatalf("communication keyring env = %q", envCommunicationContentKeyringFile)
	}
	if mode := configEnvKeyMode(envCommunicationContentKeyringFile); mode != configKeyExact {
		t.Fatalf("%s registry mode = %v, want exact", envCommunicationContentKeyringFile, mode)
	}
}

func TestBootBindsCMEKCommunicationSealerButKeepsK3Off(t *testing.T) {
	startFakeKEKServer(t)
	ctx := context.Background()
	raw := communicationContentTestKeyring(t, "seal-v1", "digest-v1",
		communicationContentTestRoot{"seal-v1", communicationContentTestRootBytes(0x81)},
		communicationContentTestRoot{"digest-v1", communicationContentTestRootBytes(0x82)},
	)
	custody, err := loadKeyWrapConfig()
	if err != nil || custody == nil {
		t.Fatalf("load key-wrap config = %+v, %v", custody, err)
	}
	wrapper, err := custody.wrapper()
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := secure.Seal(ctx, wrapper, secure.PurposeOperatorConfig, raw)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "communication-keyring.sealed")
	if err := secure.WriteSealedFile(path, envelope); err != nil {
		t.Fatal(err)
	}
	t.Setenv(envCommunicationContentKeyringFile, path)

	eng, err := boot(ctx, bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test", NoIngest: true,
	})
	if err != nil {
		t.Fatalf("boot with communication content custody: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if eng.sessionsMod == nil {
		t.Fatal("boot did not construct sessions module")
	}

	readiness, err := eng.sessionsMod.EvaluateCommunicationReadiness(ctx)
	if err != nil {
		t.Fatalf("evaluate communication readiness: %v", err)
	}
	if !readiness.Components.SealerReady || readiness.Components.PumpReady || readiness.Effective {
		t.Fatalf("sealer-only composition readiness = %+v", readiness)
	}
	if eng.sessionsMod.CommunicationSessionCredentialsEnabled() {
		t.Fatal("binding the content sealer enabled communication-session credentials")
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
