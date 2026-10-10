// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	inventoryAdminEmail    = "admin@example.com"
	inventoryAdminPassword = "correct horse battery staple"
)

func TestDRPostgresInventoryRestore(t *testing.T) {
	stampVersion(t)
	src := newPGSplitFixture(t, "inventory_src", true)
	dataDir := t.TempDir()
	src.seed(t, dataDir)
	super := strings.TrimSpace(os.Getenv(pgProbeDSN))
	if _, err := coreengine.ProvisionPostgres(t.Context(), super, store.PgProvisionSpec{Database: src.db, App: store.PgRole{Name: src.appRole}, Owner: store.PgRole{Name: src.ownerRole}, InstallDirectoryInventory: true}, true); err != nil {
		t.Fatal(err)
	}
	// The estate the console journey backs up has a first administrator.
	eng, err := boot(t.Context(), bootConfig{DataDir: dataDir, Engine: "postgres", DSN: src.appDSN, OwnerDSN: src.ownerDSN, AdminDSN: src.adminDSN, Version: "test"})
	if err != nil {
		t.Fatalf("source boot with the inventory installed: %v", err)
	}
	var tenant model.TenantID
	if err := eng.store.System(t.Context(), func(sys store.SystemScope) error {
		org, e := sys.CreateOrg(t.Context(), model.Org{Name: "First", Slug: "first", Status: model.StatusActive})
		tenant = org.TenantID
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := eng.authr.BootstrapSuperadminOwning(t.Context(), inventoryAdminEmail, inventoryAdminPassword, tenant); err != nil {
		t.Fatalf("first administrator: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "inventory.drbundle")
	pf := passphraseFile(t)
	args := append([]string{"backup", "--data-dir", dataDir}, src.drArgs()...)
	args = append(args, "--pg-dump", src.bin("pg_dump"), "--out", bundle, "--passphrase-file", pf)
	if out, err := runDR(args...); err != nil {
		t.Fatalf("backup: %v %s", err, out)
	}
	for _, name := range []string{"missing", "inline", "wrong_database", "non_dba", "valid"} {
		t.Run(name, func(t *testing.T) {
			dst := newPGSplitFixture(t, "inventory_dst", true)
			dir := t.TempDir()
			args := append([]string{"restore", "--data-dir", dir}, dst.drArgs()...)
			args = append(args, "--pg-restore", dst.bin("pg_restore"), "--in", bundle, "--passphrase-file", pf)
			switch name {
			case "inline":
				args = append(args, "--superuser-dsn", "postgres://test:test@127.0.0.1/test")
			case "wrong_database":
				t.Setenv("DR_TEST_DBA", roleDSNKeepUser(t, super, src.db))
				args = append(args, "--superuser-dsn", "env:DR_TEST_DBA")
			case "non_dba":
				t.Setenv("DR_TEST_DBA", dst.ownerDSN)
				args = append(args, "--superuser-dsn", "env:DR_TEST_DBA")
			case "valid":
				t.Setenv("DR_TEST_DBA", roleDSNKeepUser(t, super, dst.db))
				args = append(args, "--superuser-dsn", "env:DR_TEST_DBA")
			}
			out, err := runDR(args...)
			if name == "valid" {
				if err != nil {
					t.Fatalf("native restore: %v %s", err, out)
				}
				if n := dst.superScalar(t, `SELECT count(*) FROM pg_proc WHERE oid='public.olivares_directory_inventory_v1()'::regprocedure AND pg_get_userbyid(proowner)='olivares_directory_inventory_owner' AND proacl IS NOT NULL AND NOT EXISTS(SELECT 1 FROM aclexplode(proacl) a WHERE a.grantee=0) AND has_function_privilege($1,oid,'EXECUTE')`, dst.appRole); n != 1 {
					t.Fatal("inventory authority not restored")
				}
				// No admin DSN: a db init --data-dir estate reads every tenant through the inventory.
				restored, err := boot(t.Context(), bootConfig{DataDir: dir, Engine: "postgres", DSN: dst.appDSN, OwnerDSN: dst.ownerDSN, Version: "test"})
				if err != nil {
					t.Fatalf("boot the restored estate: %v", err)
				}
				defer func() { _ = restored.Close() }()
				if _, _, err := restored.authr.Login(t.Context(), inventoryAdminEmail, inventoryAdminPassword, "127.0.0.1"); err != nil {
					t.Fatalf("the original administrator cannot sign in to the restored estate: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid DBA input succeeded")
			}
			if n := dst.superScalar(t, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p')`); n != 0 {
				t.Fatal("authority refusal followed import")
			}
			files, e := os.ReadDir(dir)
			if e != nil || len(files) != 0 {
				t.Fatalf("authority refusal changed custody: entries=%d err=%v", len(files), e)
			}
		})
	}
}
