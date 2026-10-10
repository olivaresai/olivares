// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

type drCensusFixture struct{ sdk.Module }

func (drCensusFixture) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: "dr-census-fixture"}
}

func (drCensusFixture) RegisterSchema(reg store.ExtensionRegistry) error {
	desc := censusFixtureDescriptor("fixture.dr_receipt", model.KindText, model.None("fixture receipt: dr_offline_census_test.go:1"))
	desc.AppendOnly = true
	return reg.Register(desc)
}

func TestDROfflineBackupAndVerifyDifferentModuleCensus(t *testing.T) {
	prepareCompositionTestBoot(t)
	previous := version
	version = "1.900"
	t.Cleanup(func() { version = previous })
	dir := t.TempDir()
	ctx := context.Background()
	b := &bootState{cfg: bootConfig{DataDir: dir, Engine: "sqlite", Version: version, NoIngest: true, Logger: discardLog()}}
	defer b.unwind()
	for _, phase := range []func(context.Context) error{b.configure, b.loadSigningCustody, b.buildRuntime} {
		if err := phase(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// Register an additional real module schema before store creation, exactly
	// as a fuller edition does. Never edit bootstrap receipts to manufacture it.
	if err := b.rt.AddModule(drCensusFixture{}, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := b.openStore(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.st.System(ctx, func(sc store.SystemScope) error {
		if _, err := sc.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		_, err := sc.CreateOrg(ctx, model.Org{Name: "Offline census", Slug: "offline-census", Status: model.StatusActive})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := b.signer.CheckpointAll(ctx, b.st); err != nil {
		t.Fatal(err)
	}
	b.unwind()
	if eng, err := drBoot(ctx, drFlags{dataDir: dir, engineKind: "sqlite"}); err == nil {
		_ = eng.Close()
		t.Fatal("runtime admission accepted a foreign module census")
	} else if !strings.Contains(err.Error(), "bootstrap history is not an exact compiled") {
		t.Fatalf("unexpected runtime refusal: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "olivares.db"))
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(key, bytes.Repeat([]byte{0x41}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "estate.drbundle")
	if out, err := runDR("backup", "--data-dir", dir, "--out", bundle, "--kek-key-file", key); err != nil {
		t.Fatalf("offline backup of fuller census: %v\n%s", err, out)
	}
	if out, err := runDR("verify", "--in", bundle, "--kek-key-file", key); err != nil {
		t.Fatalf("offline verify of fuller census: %v\n%s", err, out)
	}
	after, err := os.ReadFile(filepath.Join(dir, "olivares.db"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("offline backup/verify changed source database bytes")
	}
}

func TestDROfflineReaderNeverMintsMissingSigningKey(t *testing.T) {
	prepareCompositionTestBoot(t)
	dir := t.TempDir()
	seedDataDir(t, dir)
	key := filepath.Join(dir, "audit-signing.key")
	if err := os.Remove(key); err != nil {
		t.Fatal(err)
	}
	reader, err := drReadBoot(t.Context(), dir, filepath.Join(dir, "olivares.db"))
	if err == nil {
		_ = reader.Close()
		t.Fatal("offline DR accepted missing signing custody")
	}
	if _, err := os.Stat(key); !os.IsNotExist(err) {
		t.Fatalf("offline DR minted a signing key: %v", err)
	}
}
