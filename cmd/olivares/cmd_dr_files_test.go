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
)

// registerFolder inserts a registered-folder row into the data dir's store, the row
// the sessions module writes when a person registers a folder.
func registerFolder(t *testing.T, dir, ref, root, mount string, backup *bool) {
	t.Helper()
	prepareCompositionTestBoot(t)
	defer prepareCompositionTestBoot(t)
	ctx := context.Background()
	eng, err := boot(ctx, bootConfig{DataDir: dir, Engine: "sqlite", Version: "test"})
	if err != nil {
		t.Fatalf("boot for folder row: %v", err)
	}
	defer func() { _ = eng.Close() }()
	var tenant model.TenantID
	if err := eng.store.System(ctx, func(sys store.SystemScope) error {
		orgs, err := sys.ListOrgs(ctx)
		if err == nil && len(orgs) > 0 {
			tenant = orgs[0].TenantID
		}
		return err
	}); err != nil || tenant.IsZero() {
		t.Fatalf("find the tenant: %v", err)
	}
	row := model.Record{
		"workspace_ref": ref, "root_path": root, "mount_mode": mount,
		"dlp_mode": "off", "state": "active", "name": ref,
	}
	if backup != nil {
		row["backup"] = *backup
	}
	if err := eng.store.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(model.Kind("sessions.workspace"))
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, row)
		return err
	}); err != nil {
		t.Fatalf("insert folder row: %v", err)
	}
}

// TestDRBackupCarriesFilesAndSignIns is issue 3029: a restore on another host must
// return the files of the registered folders and the tools' sign-ins, byte for byte
// with their modes; a folder with backup:false stays out and is listed; a folder
// registered read-only is copied without a write to its source.
func TestDRBackupCarriesFilesAndSignIns(t *testing.T) {
	prevVersion := version
	version = "1.900"
	t.Cleanup(func() { version = prevVersion })
	src := t.TempDir()
	seedDataDir(t, src)

	signIn := filepath.Join(src, toolLoginsDir, "acme", "claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(signIn), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(signIn, []byte(`{"token":"sign-in"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	work := t.TempDir()
	files := map[string]os.FileMode{"report.md": 0o640, "run.sh": 0o755, "sub/notes.txt": 0o600}
	for rel, mode := range files {
		p := filepath.Join(work, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("content of "+rel), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	scratch := t.TempDir()
	if err := os.WriteFile(filepath.Join(scratch, "tmp.bin"), []byte("purgeable"), 0o600); err != nil {
		t.Fatal(err)
	}
	shared := t.TempDir()
	sharedFile := filepath.Join(shared, "data.csv")
	if err := os.WriteFile(sharedFile, []byte("a,b\n1,2\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(shared, 0o755) })
	before, err := os.Stat(shared)
	if err != nil {
		t.Fatal(err)
	}

	off := false
	registerFolder(t, src, "work-folder", work, "rw", nil)
	registerFolder(t, src, "scratch-folder", scratch, "rw", &off)
	registerFolder(t, src, "shared-ro", shared, "ro", nil)

	pf := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(pf, []byte("a strong DR passphrase\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "estate.drbundle")
	if out, err := runDR(t, "backup", "--data-dir", src, "--engine", "sqlite", "--out", bundle, "--passphrase-file", pf); err != nil {
		t.Fatalf("backup: %v\n%s", err, out)
	}
	out, err := runDR(t, "inspect", "--in", bundle)
	if err != nil {
		t.Fatalf("inspect: %v\n%s", err, out)
	}
	if !strings.Contains(out, "not_included") || !strings.Contains(out, "scratch-folder") {
		t.Fatalf("the manifest does not list the folder left out:\n%s", out)
	}

	dst, froot := t.TempDir(), filepath.Join(t.TempDir(), "files")
	if out, err := runDR(t, "restore", "--in", bundle, "--data-dir", dst, "--engine", "sqlite", "--passphrase-file", pf, "--files-root", froot, "--force"); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}

	restoredShared := filepath.Join(froot, "folder-shared-ro")
	t.Cleanup(func() { _ = os.Chmod(restoredShared, 0o755) })
	if info, err := os.Stat(restoredShared); err != nil || info.Mode().Perm() != 0o555 {
		t.Fatalf("the read-only folder came back with mode %v: %v", info, err)
	}
	got, err := os.ReadFile(filepath.Join(dst, toolLoginsDir, "acme", "claude", ".credentials.json"))
	if err != nil || !bytes.Equal(got, []byte(`{"token":"sign-in"}`)) {
		t.Fatalf("the tool sign-in did not come back: %v %q", err, got)
	}
	for rel, mode := range files {
		p := filepath.Join(froot, "folder-work-folder", filepath.FromSlash(rel))
		body, err := os.ReadFile(p)
		if err != nil || string(body) != "content of "+rel {
			t.Fatalf("%s did not come back byte for byte: %v %q", rel, err, body)
		}
		if info, _ := os.Stat(p); info.Mode().Perm() != mode {
			t.Fatalf("%s mode = %v, want %v", rel, info.Mode().Perm(), mode)
		}
	}
	if _, err := os.Stat(filepath.Join(froot, "folder-scratch-folder")); err == nil {
		t.Fatal("a folder with backup:false was copied")
	}
	if body, err := os.ReadFile(filepath.Join(froot, "folder-shared-ro", "data.csv")); err != nil || string(body) != "a,b\n1,2\n" {
		t.Fatalf("the read-only folder did not come back: %v %q", err, body)
	}
	after, err := os.Stat(shared)
	if err != nil || !after.ModTime().Equal(before.ModTime()) || after.Mode() != before.Mode() {
		t.Fatalf("the backup touched the read-only source: %v", err)
	}
}
