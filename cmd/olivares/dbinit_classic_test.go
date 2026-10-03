// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Rule 1, WE DO NOT BREAK USERS (RM, 2026-10-02): RC10 routed `db init` through
// quickstart's provisioning, so 26.10.0's own invocation created olivares_<hex>,
// olivares_app_<hex> and a split owner, saved files under a data directory, a re-run
// made a second database, and a data directory holding SQLite or a named role without
// a password file was refused. These run 26.10.0's invocations literally.

func dbInitPasswordFile(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDBInitThe26100InvocationKeepsItsResult(t *testing.T) {
	maintenance := pgMaintenanceForTest(t, "FH_DBINIT_COMPAT_MAINTENANCE")
	ctx := context.Background()
	// 26.10.0 never read a data directory. The default one here already uses SQLite,
	// which RC10 refused ("already uses SQLite").
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "olivares.db"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OLIVARES_DATA_DIR", dataDir)
	args := []string{"init", "--superuser-dsn", "env:FH_DBINIT_COMPAT_MAINTENANCE",
		"--app-password-file", dbInitPasswordFile(t, "fh-dbinit-compat-app-password")}
	for run := 1; run <= 2; run++ { // the second run is the idempotent re-run
		out, err := runDB(t, args...)
		if err != nil {
			t.Fatalf("26.10.0 db init, run %d: %v\n%s", run, err, out)
		}
		for _, want := range []string{
			`provisioned database "olivares"`,
			"  app  : olivares_app — OK",
			"\nNext: store each password in a 0600 file and point serve at it",
			"`olivares setup` writes these files and the env file for you.\n",
		} {
			if !strings.Contains(out, want) {
				t.Fatalf("run %d output lacks %q:\n%s", run, want, out)
			}
		}
		if strings.Contains(out, "owner:") || strings.Contains(out, "Credentials saved") {
			t.Fatalf("run %d split the owner or saved credentials:\n%s", run, out)
		}
	}
	conn, err := pgx.Connect(ctx, maintenance)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var owner string
	if err := conn.QueryRow(ctx, "SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname = 'olivares'").Scan(&owner); err != nil || owner != "olivares_app" {
		t.Fatalf("database olivares owner = %q (%v), want the app role olivares_app", owner, err)
	}
	var generated, owners int
	if err := conn.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM pg_database WHERE datname LIKE 'olivares\_%'),
		(SELECT count(*) FROM pg_roles WHERE rolname LIKE 'olivares\_owner%' OR rolname LIKE 'olivares\_app\_%')`).Scan(&generated, &owners); err != nil {
		t.Fatal(err)
	}
	if generated != 0 || owners != 0 {
		t.Fatalf("generated databases %d, generated or owner roles %d: want none", generated, owners)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "postgres")); !os.IsNotExist(err) {
		t.Fatalf("db init wrote into the data directory: %v", err)
	}

	// An existing named role without a password file: 26.10.0 kept its password and
	// said so; RC10 refused ("requires --app-password-file").
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	named := "fh_compat_" + hex.EncodeToString(suffix[:])
	create := []string{"init", "--superuser-dsn", "env:FH_DBINIT_COMPAT_MAINTENANCE", "--app-role", named, "--database", named}
	if out, err := runDB(t, append(create, "--app-password-file", dbInitPasswordFile(t, "fh-dbinit-named-password"))...); err != nil {
		t.Fatalf("26.10.0 db init creating a named role: %v\n%s", err, out)
	}
	out, err := runDB(t, create...)
	if err != nil || !strings.Contains(out, "  app  : (password kept; not re-verified)") {
		t.Fatalf("26.10.0 db init naming an existing role without a password file: %v\n%s", err, out)
	}
}

// --print-sql shows the names its run uses: 26.10.0's names without --data-dir, the
// names given with it, and a refusal (never a placeholder) when a new data directory's
// names are still to be generated.
func TestDBInitPrintSQLShowsTheNamesItsRunUses(t *testing.T) {
	pw := dbInitPasswordFile(t, "fh-dbinit-preview-password")
	out, err := runDB(t, "init", "--print-sql", "--app-password-file", pw)
	if err != nil {
		t.Fatalf("26.10.0 db init --print-sql: %v\n%s", err, out)
	}
	if !strings.Contains(out, "CREATE ROLE olivares_app") || !strings.Contains(out, "CREATE DATABASE olivares OWNER olivares_app") || strings.Contains(out, "olivares_owner") {
		t.Fatalf("26.10.0 preview does not show olivares/olivares_app with the app role owning:\n%s", out)
	}
	if _, err := runDB(t, "init", "--print-sql", "--data-dir", t.TempDir(), "--app-password-file", pw); err == nil ||
		!strings.Contains(err.Error(), "generated when db init prepares it") {
		t.Fatalf("a new data directory's unnamed preview = %v, want the refusal", err)
	}
	out, err = runDB(t, "init", "--print-sql", "--data-dir", t.TempDir(), "--database", "fh_db", "--app-role", "fh_app",
		"--owner-role", "fh_owner", "--app-password-file", pw, "--owner-password-file", pw)
	if err != nil {
		t.Fatalf("named preview with --data-dir: %v\n%s", err, out)
	}
	for _, want := range []string{"CREATE ROLE fh_app", "CREATE ROLE fh_owner", "CREATE DATABASE fh_db OWNER fh_owner"} {
		if !strings.Contains(out, want) {
			t.Fatalf("named preview lacks %q:\n%s", want, out)
		}
	}
}
