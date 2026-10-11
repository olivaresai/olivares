// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	coreengine "github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/store"
)

// An installation prepared by `db init --data-dir` must be backed up from the
// console without any DSN its owner never configured. pg_dump needs a role that
// bypasses row-level security, so db init saves one with the application and owner
// roles, and the engine started from that directory uses it.
func TestDBInitDataDirInstallBacksUpFromTheConsole(t *testing.T) {
	stampVersion(t)
	pgMaintenanceForTest(t, "ARCH_DB_INIT_CONSOLE_BACKUP")
	dir := filepath.Join(t.TempDir(), "install")
	if output, err := runDB(t, "init", "--superuser-dsn", "env:ARCH_DB_INIT_CONSOLE_BACKUP", "--data-dir", dir); err != nil {
		t.Fatalf("db init --data-dir: %v\n%s", err, output)
	}
	saved, err := quickstartPostgresConfig(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	adminFile := filepath.Join(dir, "postgres", "admin.dsn")
	if saved.AdminDSN != "file:"+adminFile {
		t.Fatalf("db init --data-dir saved admin DSN = %q, want file:%s", saved.AdminDSN, adminFile)
	}
	if info, err := os.Stat(adminFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the admin DSN must be a private file: %v", err)
	}
	raw, err := os.ReadFile(adminFile)
	if err != nil {
		t.Fatal(err)
	}
	posture, err := coreengine.ProbeRole(context.Background(), store.Config{Engine: store.EnginePostgres, DSN: strings.TrimSpace(string(raw))})
	if err != nil || !posture.Reachable || posture.Superuser || !posture.BypassRLS {
		t.Fatalf("the saved admin role must be reachable, NOSUPERUSER BYPASSRLS: %+v %v", posture, err)
	}

	// The same start `serve` makes from this directory, then one backup through
	// the console's own path.
	eng, err := boot(t.Context(), bootConfig{DataDir: dir, Version: version, Logger: discardLogger()})
	if err != nil {
		t.Fatalf("start from the db init directory: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if err := eng.api.RunStartupBackup(t.Context(), "console backup passphrase fixture", "db init install", "test"); err != nil {
		t.Fatalf("console backup on a db init --data-dir install: %v", err)
	}
}

// Only a superuser can create the BYPASSRLS admin role. A maintenance role that can
// create roles and databases but is not one (managed PostgreSQL) must not get an
// admin role it cannot create: its init keeps provisioning the other roles.
func TestMaintenanceCanCreateAdminNeedsASuperuser(t *testing.T) {
	super := pgMaintenanceForTest(t, "ARCH_DB_INIT_ADMIN_PROBE")
	superURL, err := url.Parse(super)
	if err != nil {
		t.Fatal(err)
	}
	if !maintenanceCanCreateAdmin(t.Context(), superURL) {
		t.Fatal("a superuser maintenance role can create the admin role")
	}
	conn, err := pgx.Connect(t.Context(), super)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	manager := fmt.Sprintf("t350_manager_%d", time.Now().UnixNano())
	if _, err := conn.Exec(t.Context(), "CREATE ROLE "+manager+" LOGIN PASSWORD 'manager-fixture' NOSUPERUSER NOBYPASSRLS CREATEROLE CREATEDB"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), "DROP ROLE "+manager) })
	managed := *superURL
	managed.User = url.UserPassword(manager, "manager-fixture")
	if maintenanceCanCreateAdmin(t.Context(), &managed) {
		t.Fatal("a maintenance role that is not a superuser cannot create the BYPASSRLS admin role")
	}
}

// An installation saved before db init provisioned an admin role still has none.
// Its console backup says so, and the command it names must run as written.
func TestConsoleBackupWithoutAnAdminRoleNamesARunnableRemedy(t *testing.T) {
	config := consoleDRConfig(bootConfig{DataDir: t.TempDir()}, store.EnginePostgres, nil, nil)
	_, err := config.PostgresSnapshot(t.Context(), filepath.Join(t.TempDir(), "dump"))
	if err == nil {
		t.Fatal("a PostgreSQL console backup without an admin DSN must be refused")
	}
	initCmd, _, findErr := newDBCmd().Find([]string{"init"})
	if findErr != nil {
		t.Fatal(findErr)
	}
	for _, name := range []string{"data-dir", "superuser-dsn", "admin-role", "admin-password-file"} {
		if !strings.Contains(err.Error(), "--"+name) {
			t.Errorf("the refusal does not name --%s: %v", name, err)
		}
		if initCmd.Flags().Lookup(name) == nil {
			t.Errorf("the refusal names --%s but `olivares db init` has no such flag", name)
		}
	}
}
