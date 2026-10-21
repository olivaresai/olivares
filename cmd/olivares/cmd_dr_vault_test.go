// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A personal vault item is a sealed secret like any other: `dr backup` carries the
// store key, so the item stored on the source is revealed, unchanged, by an engine
// booted from the restored estate.
func TestDRRestoreReopensAPersonalVaultItem(t *testing.T) {
	clearSealerKeyEnv(t)
	ctx := t.Context()
	src := t.TempDir()
	owner := auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID(), DisplayName: "vault owner"}
	name := "vault/" + owner.UserID.String() + "/bank"

	prepareCompositionTestBoot(t)
	eng, err := boot(ctx, bootConfig{DataDir: src, Engine: "sqlite", Version: "test"})
	if err != nil {
		t.Fatalf("source boot: %v", err)
	}
	var tenant model.TenantID
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		org, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		_ = eng.Close()
		t.Fatalf("create org: %v", err)
	}
	if _, err := eng.secretStore.Put(ctx, owner, tenant, name, "vault-dr-fixture-value", ""); err != nil {
		_ = eng.Close()
		t.Fatalf("put: %v", err)
	}
	if err := eng.signer.CheckpointAll(ctx, eng.store); err != nil {
		_ = eng.Close()
		t.Fatalf("checkpoint: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close source engine: %v", err)
	}

	pf := l3Passphrase(t)
	bundle := filepath.Join(t.TempDir(), "estate.drbundle")
	if out, err := runDR(t, "backup", "--data-dir", src, "--out", bundle, "--passphrase-file", pf); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	dst := filepath.Join(t.TempDir(), "restored")
	if out, err := runDR(t, "restore", "--in", bundle, "--data-dir", dst, "--engine", "sqlite", "--passphrase-file", pf); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}

	prepareCompositionTestBoot(t)
	restored, err := boot(ctx, bootConfig{DataDir: dst, Engine: "sqlite", Version: "test"})
	if err != nil {
		t.Fatalf("restored boot: %v", err)
	}
	defer func() { _ = restored.Close() }()
	got, err := restored.secretStore.Reveal(ctx, owner, tenant, name)
	if err != nil || string(got) != "vault-dr-fixture-value" {
		t.Fatalf("restored estate reveals %q, %v; want the stored value", got, err)
	}
}
