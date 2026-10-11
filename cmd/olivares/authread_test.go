// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/olivaresai/olivares/core/auth"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestOfflineSourceAndSecretReadsDoNotBootOrWrite(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "olivares.db")
	st, err := coreengine.Open(t.Context(), store.Config{Engine: store.EngineSQLite, DSN: dbPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	putRow(t, auth.NewSourceStore(st), model.SourceDef{
		Name: "config-reader", Kind: "claude-config", Tenant: planTenantA, Enabled: true,
	})
	if err := st.AuthMutate(t.Context(), func(as store.AuthScope) error {
		_, err := as.Secrets().Create(t.Context(), model.SecretEntry{
			Scope: auth.GlobalSecretScope, Name: "reader/credential", Hint: "opaque-hint",
			ValueSealed: "synthetic-ciphertext-not-for-output",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	files := offlineInstallationFiles(t, dir)
	for _, c := range offlineReadCommands {
		t.Run(c.name, func(t *testing.T) {
			out, err := runCLI(t, append(c.argv, "--data-dir", dir, "-o", "json")...)
			if err != nil {
				t.Fatalf("read existing core store without runtime keys: %v\n%s", err, out)
			}
			if !strings.Contains(out, c.want) || strings.Contains(out, "synthetic-ciphertext-not-for-output") {
				t.Errorf("unexpected read output: %s", out)
			}
			after, err := os.ReadFile(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Error("offline read changed database bytes")
			}
			if wal, err := os.ReadFile(dbPath + "-wal"); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			} else if len(wal) != 0 {
				t.Error("offline read created WAL content")
			}
			if got := offlineInstallationFiles(t, dir); !reflect.DeepEqual(files, got) {
				t.Errorf("offline read changed installation files: before=%v after=%v", files, got)
			}
		})
	}
}

var offlineReadCommands = []struct {
	name string
	argv []string
	want string
}{
	{"source list", []string{"sources", "ls"}, "config-reader"},
	{"source get", []string{"sources", "get", "config-reader"}, "config-reader"},
	{"secret list", []string{"secrets", "ls"}, "opaque-hint"},
	{"source plan", []string{"sources", "plan", "--name", "config-reader", "--enabled=false"}, "update"},
	{"source validate", []string{"sources", "validate"}, "config-reader"},
}

func TestOfflineAuthReaderRefusesMutations(t *testing.T) {
	dir := t.TempDir()
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(dir, "olivares.db")}
	st, err := coreengine.Open(t.Context(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	def := model.SourceDef{Name: "config-reader", Kind: "claude-config", Tenant: planTenantA, Enabled: true}
	putRow(t, auth.NewSourceStore(st), def)
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := coreengine.OpenAuthReader(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	err = reader.AuthView(t.Context(), func(as store.AuthScope) error {
		_, err := as.Sources().Create(t.Context(), model.SourceDef{Scope: auth.GlobalSourceScope, Name: "should-not-exist", Kind: "claude-config", Tenant: planTenantA})
		return err
	})
	if !errors.Is(err, store.ErrReadOnly) {
		t.Fatalf("AuthView mutation: %v", err)
	}
	sources := auth.NewSourceReader(reader)
	if err := sources.Delete(t.Context(), auth.Principal{}, auth.GlobalSourceScope, def.Name); !errors.Is(err, store.ErrReadOnly) {
		t.Fatalf("reader deletion: %v", err)
	}
	rows, err := sources.List(t.Context(), auth.GlobalSourceScope)
	if err != nil || len(rows) != 1 || rows[0].Name != def.Name {
		t.Fatalf("reader changed rows: %v %v", rows, err)
	}
	secrets := auth.NewSecretReader(reader)
	if secrets.SealerWired() {
		t.Fatal("metadata reader gained a sealer")
	}
	if _, err := secrets.Resolve(t.Context(), auth.GlobalSecretScope, "missing"); !errors.Is(err, auth.ErrNoSecretSealer) {
		t.Fatalf("reader opened a value: %v", err)
	}
}

func TestOfflineSourceAndSecretReadsPostgresReadOnly(t *testing.T) {
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	dir := t.TempDir()
	st, err := coreengine.Open(t.Context(), store.Config{Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	putRow(t, auth.NewSourceStore(st), model.SourceDef{Name: "config-reader", Kind: "claude-config", Tenant: planTenantA, Enabled: true})
	if err := st.AuthMutate(t.Context(), func(as store.AuthScope) error {
		_, err := as.Secrets().Create(t.Context(), model.SecretEntry{Scope: auth.GlobalSecretScope, Name: "reader/credential", Hint: "opaque-hint", ValueSealed: "synthetic-ciphertext-not-for-output"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	pgDir := filepath.Join(dir, "postgres")
	if err := os.Mkdir(pgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	maintenance, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatal(err)
	}
	defer maintenance.Close()
	for role, dsn := range map[string]string{"app": pg.App, "owner": pg.Owner} {
		if err := os.WriteFile(filepath.Join(pgDir, role+".dsn"), []byte(dsn+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal("fixture DSN malformed")
		}
		if _, err := maintenance.ExecContext(t.Context(), "ALTER ROLE "+pgx.Identifier{cfg.User}.Sanitize()+" IN DATABASE "+pgx.Identifier{cfg.Database}.Sanitize()+" SET default_transaction_read_only = on"); err != nil {
			t.Fatal(err)
		}
	}
	files := filesUnder(t, dir)
	for _, c := range offlineReadCommands {
		t.Run(c.name, func(t *testing.T) {
			out, err := runCLI(t, append(c.argv, "--data-dir", dir, "-o", "json")...)
			if err != nil {
				t.Fatalf("read-only PostgreSQL command: %v\n%s", err, out)
			}
			if !strings.Contains(out, c.want) || strings.Contains(out, "synthetic-ciphertext-not-for-output") {
				t.Errorf("unexpected read output: %s", out)
			}
			if got := filesUnder(t, dir); !reflect.DeepEqual(files, got) {
				t.Errorf("reader changed files: %v -> %v", files, got)
			}
		})
	}
	for role, dsn := range map[string]string{"app": pg.App, "owner": pg.Owner} {
		got, err := os.ReadFile(filepath.Join(pgDir, role+".dsn"))
		if err != nil || string(got) != dsn+"\n" {
			t.Errorf("reader changed %s credentials", role)
		}
	}
}

// SQLite may create empty WAL and shared-memory coordination files in mode=ro.
// Installation files and database/WAL contents are checked separately.
func offlineInstallationFiles(t *testing.T, dir string) []string {
	var files []string
	for _, name := range filesUnder(t, dir) {
		if name != "olivares.db-wal" && name != "olivares.db-shm" {
			files = append(files, name)
		}
	}
	return files
}

func TestOfflineReaderSeesCommittedWALWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "olivares.db")
	st, err := coreengine.Open(t.Context(), store.Config{Engine: store.EngineSQLite, DSN: dbPath}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	putRow(t, auth.NewSourceStore(st), model.SourceDef{Name: "wal-reader", Kind: "claude-config", Tenant: planTenantA, Enabled: true})
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	wal, err := os.ReadFile(dbPath + "-wal")
	if err != nil || len(wal) == 0 {
		t.Fatal("fixture must contain committed WAL data")
	}
	out, err := runCLI(t, "sources", "ls", "--data-dir", dir, "-o", "json")
	if err != nil || !strings.Contains(out, "wal-reader") {
		t.Fatalf("read committed WAL: %v\n%s", err, out)
	}
	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	afterWAL, err := os.ReadFile(dbPath + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !bytes.Equal(wal, afterWAL) {
		t.Fatal("offline reader changed database or committed WAL bytes")
	}
}

func TestOfflineReadersRefuseWithoutReturningAHandle(t *testing.T) {
	dir := t.TempDir()
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(dir, "missing.db")}
	reader, err := coreengine.OpenAuthReader(t.Context(), cfg)
	if reader != nil || !errors.Is(err, store.ErrNotFound) {
		t.Errorf("auth reader refusal must return no handle and ErrNotFound")
	}
	audit, err := coreengine.OpenAuditReader(t.Context(), cfg)
	if audit != nil || !errors.Is(err, store.ErrNotFound) {
		t.Errorf("published audit reader refusal must return no handle and ErrNotFound")
	}
	if got := filesUnder(t, dir); len(got) != 0 {
		t.Errorf("refused reader created files: %v", got)
	}
}
