// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

func packageSnapshotFixture(t *testing.T) (string, string, store.Config) {
	t.Helper()
	dir := t.TempDir()
	request := filepath.Join(t.TempDir(), "upgrade-snapshot")
	if err := os.WriteFile(request, []byte("upgrade-snapshot.transaction1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(dir, "olivares.db")}
	packageSnapshotSQL(t, cfg.DSN, "CREATE TABLE upgrade_witness (value TEXT); INSERT INTO upgrade_witness VALUES ('before')")
	return dir, request, cfg
}

func packageSnapshotSQL(t *testing.T, dsn, query string) {
	t.Helper()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

func packageSnapshots(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "backups", "pre-upgrade", "*", "olivares.db"))
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestPackageUpgradeSnapshotBeforeBootMigrations(t *testing.T) {
	dir, request, cfg := packageSnapshotFixture(t)
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Engine: "sqlite", DSN: cfg.DSN,
		Version: "test", ServeMode: true, upgradeSnapshotRequest: request,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	files := packageSnapshots(t, dir)
	if len(files) != 1 {
		t.Fatalf("boot did not publish exactly one pre-migration snapshot: %v", files)
	}
	for _, f := range []string{files[0], cfg.DSN} {
		db, err := sql.Open("sqlite", f)
		if err != nil {
			t.Fatal(err)
		}
		var count int
		err = db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='orgs'").Scan(&count)
		_ = db.Close()
		if err != nil || (f == files[0] && count != 0) || (f == cfg.DSN && count != 1) {
			t.Fatalf("snapshot must precede real boot migration: %s orgs=%d err=%v", f, count, err)
		}
	}
}

func TestPackageUpgradeSnapshotRetryAndConcurrentBoots(t *testing.T) {
	dir, request, cfg := packageSnapshotFixture(t)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if err := snapshotPackageUpgrade(context.Background(), request, dir, cfg); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	files := packageSnapshots(t, dir)
	if len(files) != 1 {
		t.Fatalf("competing starts published %d snapshots", len(files))
	}
	before, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(files[0])
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot permissions: %v %v", st, err)
	}
	packageSnapshotSQL(t, cfg.DSN, "UPDATE upgrade_witness SET value='after'")
	if err := snapshotPackageUpgrade(context.Background(), request, dir, cfg); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(files[0])
	if string(before) != string(after) || len(packageSnapshots(t, dir)) != 1 {
		t.Fatal("retry overwrote the pre-migration snapshot")
	}
	if err := os.WriteFile(request, []byte("upgrade-snapshot.transaction2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := snapshotPackageUpgrade(context.Background(), request, dir, cfg); err != nil {
		t.Fatal(err)
	}
	if len(packageSnapshots(t, dir)) != 2 {
		t.Fatal("next package transaction must retain the old snapshot and capture a new one")
	}
}

func TestPackageUpgradeSnapshotFailureBlocksMigrations(t *testing.T) {
	dir, request, cfg := packageSnapshotFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "backups"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Engine: "sqlite", DSN: cfg.DSN,
		Version: "test", ServeMode: true, upgradeSnapshotRequest: request,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if eng != nil {
		_ = eng.Close()
	}
	if err == nil {
		t.Fatal("boot accepted a failed pre-migration snapshot")
	}
	db, err := sql.Open("sqlite", cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='orgs'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed snapshot allowed migration: orgs=%d err=%v", count, err)
	}
}

func TestPackageUpgradeSnapshotAbsentAndInvalidRequest(t *testing.T) {
	dir, request, cfg := packageSnapshotFixture(t)
	if err := snapshotPackageUpgrade(context.Background(), request+"-absent", dir, cfg); err != nil {
		t.Fatal(err)
	}
	if len(packageSnapshots(t, dir)) != 0 {
		t.Fatal("fresh install took an upgrade snapshot")
	}
	if err := os.WriteFile(request, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := snapshotPackageUpgrade(context.Background(), request, dir, cfg); err == nil {
		t.Fatal("empty request silently bypassed the snapshot")
	}
}

func TestPackageUpgradeSnapshotPostgres(t *testing.T) {
	source := newPGFixture(t, "pkgsnapshot")
	t.Setenv("PATH", source.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	source.exec(t, "CREATE TABLE package_upgrade_witness (value text); INSERT INTO package_upgrade_witness VALUES ('before')")
	dir, request, _ := packageSnapshotFixture(t)
	cfg := store.Config{Engine: store.EnginePostgres, DSN: source.dsn, AllowPrivilegedRole: true}
	if err := snapshotPackageUpgrade(t.Context(), request, dir, cfg); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "backups", "pre-upgrade", "*", "dump.pgcustom"))
	if err != nil || len(files) != 1 {
		t.Fatalf("Postgres snapshot: %v %v", files, err)
	}
	restored := newPGFixture(t, "pkgrestore")
	if err := runPgRestore(t.Context(), restored.bin("pg_restore"), restored.dsn, files[0]); err != nil {
		t.Fatal(err)
	}
	if restored.count(t, "SELECT count(*) FROM package_upgrade_witness WHERE value='before'") != 1 {
		t.Fatal("Postgres snapshot did not restore the pre-upgrade data")
	}
	// The package opt-in must not relax the existing DR CLI's own policy.
	if err := preflightPostgresDR(t.Context(), drFlags{engineKind: "postgres", dsn: source.dsn}, "backup"); err == nil {
		t.Fatal("package privilege option leaked into DR CLI")
	}
	// A new request with the admin pointed at another DB must leave no dump.
	cfg.AdminDSN = restored.dsn
	if err := os.WriteFile(request, []byte("upgrade-snapshot.wrongdatabase\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := snapshotPackageUpgrade(t.Context(), request, dir, cfg); err == nil || !strings.Contains(err.Error(), "reached database") {
		t.Fatalf("wrong admin database was not refused: %v", err)
	}
	files, _ = filepath.Glob(filepath.Join(dir, "backups", "pre-upgrade", "*", "dump.pgcustom"))
	if len(files) != 1 {
		t.Fatal("failed snapshot published a second dump")
	}
}

func TestPackageUpgradeSnapshotPostgresInvalidOutput(t *testing.T) {
	source := newPGFixture(t, "pkginvalidoutput")
	for _, missing := range []bool{true, false} {
		t.Run(fmt.Sprintf("missing=%t", missing), func(t *testing.T) {
			dir, request, _ := packageSnapshotFixture(t)
			binDir := t.TempDir()
			// Model a dump tool that exits successfully without usable output.
			body := "#!/bin/sh\nexit 0\n"
			if !missing {
				body = "#!/bin/sh\nwhile [ $# -gt 0 ]; do\n  if [ \"$1\" = --file ]; then : > \"$2\"; exit 0; fi\n  shift\ndone\nexit 1\n"
			}
			if err := os.WriteFile(filepath.Join(binDir, "pg_dump"), []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			cfg := store.Config{Engine: store.EnginePostgres, DSN: source.dsn, AllowPrivilegedRole: true}
			err := snapshotPackageUpgrade(t.Context(), request, dir, cfg)
			if missing {
				var pathErr *os.PathError
				if !errors.Is(err, os.ErrNotExist) || !errors.As(err, &pathErr) || pathErr.Op != "stat" {
					t.Fatalf("missing snapshot must preserve the filesystem cause: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "nonempty file") {
				t.Fatalf("empty snapshot must be rejected: %v", err)
			}
			entries, readErr := os.ReadDir(filepath.Join(dir, "backups", "pre-upgrade"))
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("invalid snapshot was published or staging was retained: %v %v", entries, readErr)
			}
		})
	}
}

func TestPackageUpgradeSnapshotPostgresSplitRoles(t *testing.T) {
	source := newPGSplitFixture(t, "pkgsnapshot", true)
	t.Setenv("PATH", source.binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir, request, _ := packageSnapshotFixture(t)
	source.seed(t, dir)
	cfg := store.Config{Engine: store.EnginePostgres, DSN: source.appDSN,
		OwnerDSN: source.ownerDSN, AdminDSN: source.adminDSN}
	if err := snapshotPackageUpgrade(t.Context(), request, dir, cfg); err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(dir, "backups", "pre-upgrade", "*", "dump.pgcustom"))
	if err != nil || len(files) != 1 {
		t.Fatalf("split-role Postgres snapshot: %v %v", files, err)
	}
	restored := newPGFixture(t, "pkgsplitrestore")
	if err := runPgRestore(t.Context(), restored.bin("pg_restore"), restored.dsn, files[0]); err != nil {
		t.Fatal(err)
	}
	// Boot also creates the default organization; compare the complete estate.
	want := source.superScalar(t, "SELECT count(*) FROM orgs")
	if got := restored.count(t, "SELECT count(*) FROM orgs"); got != want || got < 4 {
		t.Fatalf("snapshot tenant count under FORCE RLS: got %d, want %d (at least 4)", got, want)
	}
	if err := os.WriteFile(request, []byte("upgrade-snapshot.missingadmin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.AdminDSN = ""
	if err := snapshotPackageUpgrade(t.Context(), request, dir, cfg); err == nil || !strings.Contains(err.Error(), "--admin-dsn") {
		t.Fatalf("ordinary app role silently used as backup fallback: %v", err)
	}
}

// A retry may see a completed snapshot after any ancestor sync failed. It must
// repair durability before boot is allowed to open (and migrate) the source.
func TestPackageUpgradeSnapshotAncestorSyncFailure(t *testing.T) {
	for _, retry := range []bool{false, true} {
		for _, ancestor := range []string{"pre-upgrade", "backups", "data"} {
			t.Run(fmt.Sprintf("retry=%t/%s", retry, ancestor), func(t *testing.T) {
				dir, request, cfg := packageSnapshotFixture(t)
				if retry {
					if err := snapshotPackageUpgrade(t.Context(), request, dir, cfg); err != nil {
						t.Fatal(err)
					}
				}
				paths := map[string]string{
					"pre-upgrade": filepath.Join(dir, "backups", "pre-upgrade"),
					"backups":     filepath.Join(dir, "backups"),
					"data":        dir,
				}
				injected := errors.New("injected directory sync failure")
				err := snapshotPackageUpgradeWithSync(t.Context(), request, dir, cfg, func(path string) error {
					if path == paths[ancestor] {
						return injected
					}
					return syncDir(path)
				})
				if !errors.Is(err, injected) {
					t.Fatalf("snapshot accepted unsynced %s: %v", ancestor, err)
				}
				files := packageSnapshots(t, dir)
				if len(files) != 1 {
					t.Fatalf("expected published snapshot before ancestor sync: %v", files)
				}
				before, err := os.ReadFile(files[0])
				if err != nil {
					t.Fatal(err)
				}
				packageSnapshotSQL(t, cfg.DSN, "UPDATE upgrade_witness SET value='after'")
				if err := snapshotPackageUpgrade(t.Context(), request, dir, cfg); err != nil {
					t.Fatal(err)
				}
				after, err := os.ReadFile(files[0])
				if err != nil || string(before) != string(after) {
					t.Fatalf("durability retry replaced the original snapshot: %v", err)
				}
			})
		}
	}
}
