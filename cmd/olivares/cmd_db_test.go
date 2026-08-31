// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// runDB executes `olivares db <args>` with captured output.
func runDB(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newDBCmd()
	var buf, errOut bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

func TestDBCheckSQLite(t *testing.T) {
	t.Parallel()
	dsn := filepath.Join(t.TempDir(), "check.db")
	out, err := runDB(t, "check", "--engine", "sqlite", "--dsn", dsn)
	if err != nil {
		t.Fatalf("db check: %v\n%s", err, out)
	}
	if !strings.Contains(out, "RLS-safe") {
		t.Errorf("expected an RLS-safe verdict for sqlite, got:\n%s", out)
	}
	if !strings.HasPrefix(out, "DSN") || !strings.Contains(out, "--dsn") {
		t.Errorf("db check text table changed:\n%s", out)
	}

	out, err = runDB(t, "check", "--engine", "sqlite", "--dsn", dsn, "--format", "json")
	if err != nil {
		t.Fatalf("db check json: %v\n%s", err, out)
	}
	var results []dbCheckResult
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("db check JSON is invalid: %v\n%s", err, out)
	}
	if len(results) != 1 || results[0].DSN != "--dsn" || !results[0].Accepted {
		t.Fatalf("db check JSON result = %#v", results)
	}
}

func TestDBCheckNeedsADSN(t *testing.T) {
	t.Parallel()
	if _, err := runDB(t, "check", "--engine", "postgres"); err == nil {
		t.Fatal("db check with no DSN should error")
	}
}

func TestDBInitPrintSQLSingleRole(t *testing.T) {
	t.Parallel()
	out, err := runDB(t, "init", "--print-sql", "--app-role", "olivares_app", "--database", "olivares")
	if err != nil {
		t.Fatalf("db init --print-sql: %v\n%s", err, out)
	}
	for _, want := range []string{"CREATE ROLE olivares_app", "'********'", "CREATE DATABASE olivares OWNER olivares_app"} {
		if !strings.Contains(out, want) {
			t.Errorf("print-sql missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ALTER DEFAULT PRIVILEGES") {
		t.Error("single-role print-sql should not include split grants")
	}
}

func TestDBInitPrintSQLSplit(t *testing.T) {
	t.Parallel()
	out, err := runDB(t, "init", "--print-sql", "--owner-role", "olivares_owner", "--admin-role", "olivares_admin")
	if err != nil {
		t.Fatalf("db init --print-sql split: %v\n%s", err, out)
	}
	for _, want := range []string{
		"CREATE ROLE olivares_owner",
		"ALTER DEFAULT PRIVILEGES FOR ROLE olivares_owner",
		"CREATE ROLE olivares_admin",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("split print-sql missing %q in:\n%s", want, out)
		}
	}
}

func TestDBInitPrintSQLRejectsBadRole(t *testing.T) {
	t.Parallel()
	if _, err := runDB(t, "init", "--print-sql", "--app-role", "bad role"); err == nil {
		t.Fatal("db init --print-sql with a bad role name should error")
	}
}

func TestDBInitNeedsSuperuserDSN(t *testing.T) {
	t.Parallel()
	// Without --print-sql and without --superuser-dsn, init must refuse rather than
	// silently do nothing.
	if _, err := runDB(t, "init", "--app-role", "olivares_app"); err == nil {
		t.Fatal("db init without --superuser-dsn (and no --print-sql) should error")
	}
}

func TestCheckVerdict(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		posture store.RolePosture
		admin   bool
		wantOK  bool
	}{
		{"app rls-safe", store.RolePosture{Engine: store.EnginePostgres, Reachable: true, Role: "olivares_app"}, false, true},
		{"app superuser refused", store.RolePosture{Engine: store.EnginePostgres, Reachable: true, Role: "postgres", Superuser: true}, false, false},
		{"app bypassrls refused", store.RolePosture{Engine: store.EnginePostgres, Reachable: true, BypassRLS: true}, false, false},
		{"app unreachable", store.RolePosture{Engine: store.EnginePostgres, Reachable: false, Err: "boom"}, false, false},
		{"admin bypassrls ok", store.RolePosture{Engine: store.EnginePostgres, Reachable: true, Role: "olivares_admin", BypassRLS: true}, true, true},
		{"admin not bypassrls refused", store.RolePosture{Engine: store.EnginePostgres, Reachable: true, Role: "olivares_app"}, true, false},
		{"admin superuser refused", store.RolePosture{Engine: store.EnginePostgres, Reachable: true, Superuser: true, BypassRLS: true}, true, false},
		{"sqlite reachable ok", store.RolePosture{Engine: store.EngineSQLite, Reachable: true}, false, true},
		// This case used to read "sqlite always ok" with Reachable left unset, which
		// encoded a real defect: a SQLite DSN that could not be opened is not
		// "RLS-safe by construction", it is unknown — and --strict passed on a
		// database nobody could connect to.
		{"sqlite unreachable refused", store.RolePosture{Engine: store.EngineSQLite, Err: "boom"}, false, false},
		// The preflight must mirror the boot guards EXACTLY: stricter is as wrong as
		// laxer, because the command is believed. Open refuses the app and owner pools
		// under session_replication_role=replica (every ordinary trigger stops firing)
		// but does NOT check the admin pool, which is read-only and cross-tenant.
		{"app replica refused", store.RolePosture{
			Engine: store.EnginePostgres, Reachable: true, Role: "olivares_app",
			ReplicationRole: "replica",
		}, false, false},
		{"app local accepted", store.RolePosture{
			Engine: store.EnginePostgres, Reachable: true, Role: "olivares_app",
			ReplicationRole: "local",
		}, false, true},
		{"admin replica still accepted", store.RolePosture{
			Engine: store.EnginePostgres, Reachable: true, Role: "olivares_admin",
			BypassRLS: true, ReplicationRole: "replica",
		}, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, ok := checkVerdict(c.posture, c.admin)
			if ok != c.wantOK {
				t.Errorf("checkVerdict ok=%v, want %v", ok, c.wantOK)
			}
		})
	}
}
