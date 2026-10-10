// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// TestDRBackupVerifyWithProviderAndModelsUnselected: a default install keeps the
// models module unselected, and a registered provider gives its availability
// writer something to record. The DR boots (manifest, verify, restore) must not
// start that writer: its event lands in the manifest tip but not in the bytes.
func TestDRBackupVerifyWithProviderAndModelsUnselected(t *testing.T) {
	prevVersion := version
	version = "26.900"
	t.Cleanup(func() { version = prevVersion })
	src := t.TempDir()
	ctx := context.Background()
	// The way serve boots: the default module profile, models dormant.
	eng, err := boot(ctx, bootConfig{DataDir: src, Engine: "sqlite", Version: "test", ApplyModuleProfile: true})
	if err != nil {
		t.Fatalf("seed boot: %v", err)
	}
	if eng.moduleProfile.Active("models") {
		_ = eng.Close()
		t.Fatal("precondition: models must be unselected on a default install")
	}
	var tid model.TenantID
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		o, e := sys.CreateOrg(ctx, model.Org{Name: "acme", Slug: "acme", Status: model.StatusActive})
		tid = o.TenantID
		return e
	}); err != nil {
		_ = eng.Close()
		t.Fatalf("create org: %v", err)
	}
	if _, err := eng.sessionsMod.CreateProviderRecord(ctx, tid, sessions.CreateProviderRecordInput{
		Kind: "ollama", DisplayName: "local", BaseURL: "http://127.0.0.1:1",
	}); err != nil {
		_ = eng.Close()
		t.Fatalf("register provider: %v", err)
	}
	if err := eng.signer.CheckpointAll(ctx, eng.store); err != nil {
		_ = eng.Close()
		t.Fatalf("checkpoint: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("close seed engine: %v", err)
	}

	pf := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(pf, []byte("a strong DR passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "estate.drbundle")
	if out, err := runDR("backup", "--data-dir", src, "--engine", "sqlite", "--out", bundle, "--passphrase-file", pf); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	out, err := runDR("verify", "--in", bundle, "--passphrase-file", pf)
	if err != nil || !strings.Contains(out, "DR drill PASSED") {
		t.Fatalf("verify: %v\n%s", err, out)
	}
	dst := t.TempDir()
	out, err = runDR("restore", "--in", bundle, "--data-dir", dst, "--engine", "sqlite", "--passphrase-file", pf, "--force")
	if err != nil || !strings.Contains(out, "restore verified") {
		t.Fatalf("restore: %v\n%s", err, out)
	}
}
