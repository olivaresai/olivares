// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These cells need no server: they pin the argument list pg_restore receives, the
// credentials a scratch role is given, and the safety copy a forced replacement
// takes, which the real-server cells reach only through a whole round trip.

// fakePgRestore writes a stand-in pg_restore that records its argv, prints
// stderrText and fails.
func fakePgRestore(t *testing.T, stderrText string) (bin, argvFile string) {
	t.Helper()
	dir := t.TempDir()
	argvFile = filepath.Join(dir, "argv")
	bin = filepath.Join(dir, "pg_restore")
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argvFile + "'\nprintf '%s\\n' '" + stderrText + "' >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, argvFile
}

func TestRunPgRestoreIntoArgumentsAndLockHint(t *testing.T) {
	const lockMessage = "HINT: You might need to increase max_locks_per_transaction."
	for _, tc := range []struct {
		name     string
		replace  bool
		stderr   string
		want     []string
		deny     []string
		wantHint bool
	}{
		{"an empty target meets the dump as it is", false, "boom", []string{"--single-transaction"}, []string{"--clean", "--if-exists"}, false},
		{"a replacement drops what the dump replaces, in the same transaction", true, "boom", []string{"--single-transaction", "--clean", "--if-exists"}, nil, false},
		{"a replacement that ran out of locks names the setting", true, lockMessage, []string{"--clean"}, nil, true},
		{"the same message on an empty target is not given a replacement's remedy", false, lockMessage, nil, []string{"--clean"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, argvFile := fakePgRestore(t, tc.stderr)
			err := runPgRestoreInto(context.Background(), bin, "postgres://u:p@127.0.0.1:1/db", "dump.pgcustom", tc.replace)
			if err == nil {
				t.Fatal("a pg_restore that exits 1 was reported as success")
			}
			argv := string(readFileOrFatal(t, argvFile))
			for _, flag := range append([]string{"--no-owner", "--no-privileges"}, tc.want...) {
				if !strings.Contains(argv, flag+"\n") {
					t.Errorf("pg_restore was not given %s:\n%s", flag, argv)
				}
			}
			for _, flag := range tc.deny {
				if strings.Contains(argv, flag+"\n") {
					t.Errorf("pg_restore was given %s:\n%s", flag, argv)
				}
			}
			if got := strings.Contains(err.Error(), "raise max_locks_per_transaction"); got != tc.wantHint {
				t.Errorf("lock hint present = %v, want %v: %v", got, tc.wantHint, err)
			}
			if !strings.Contains(err.Error(), "nothing was dropped") {
				t.Errorf("the failure must say the transaction left everything in place: %v", err)
			}
		})
	}
}

// TestScratchRoleDSNInheritsOnlyTransportParameters is the cell for the override
// hole: libpq and pgx apply URL query parameters AFTER the userinfo and the path,
// so a maintenance DSN carrying user=, password= or dbname= would turn every
// scratch role into the DBA and aim pg_restore at the maintenance database.
func TestScratchRoleDSNInheritsOnlyTransportParameters(t *testing.T) {
	base, err := url.Parse("postgres://dba:dbapw@db.example:5433/postgres?sslmode=require&connect_timeout=5" +
		"&user=dba&password=dbapw&dbname=postgres&service=prod&servicefile=/etc/pg_service.conf&passfile=/root/.pgpass&options=-c%20role%3Ddba")
	if err != nil {
		t.Fatal(err)
	}
	got, err := url.Parse(scratchRoleDSN(base, url.UserPassword("olvdr_own_x", "scratchpw"), "olvdr_x"))
	if err != nil {
		t.Fatal(err)
	}
	if got.User.Username() != "olvdr_own_x" || got.Host != "db.example:5433" || got.Path != "/olvdr_x" {
		t.Fatalf("the scratch DSN must name the scratch role, the server and the scratch database: %s", got.Redacted())
	}
	q := got.Query()
	if q.Get("sslmode") != "require" || q.Get("connect_timeout") != "5" {
		t.Errorf("transport parameters must be inherited: %v", q)
	}
	for _, key := range []string{"user", "password", "dbname", "service", "servicefile", "passfile", "options"} {
		if _, ok := q[key]; ok {
			t.Errorf("the maintenance DSN's %s= must not reach a scratch role: %v", key, q)
		}
	}
}

func TestSafetyDumpPostgresNeedsAnAdminRoleAndLeavesNothingOnFailure(t *testing.T) {
	prev := pgDumpRunner
	t.Cleanup(func() { pgDumpRunner = prev })
	dir := t.TempDir()

	t.Run("no admin role: refused before anything is written", func(t *testing.T) {
		pgDumpRunner = func(context.Context, string, string, string) error {
			t.Fatal("pg_dump ran without an admin role")
			return nil
		}
		_, err := safetyDumpPostgres(context.Background(), drFlags{}, "pg_dump", dir, "20261025-000000")
		if err == nil || !strings.Contains(err.Error(), "--admin-dsn") || !strings.Contains(err.Error(), "nothing was changed") {
			t.Fatalf("the refusal must name --admin-dsn: %v", err)
		}
	})

	t.Run("a copy is a 0600 file named for the restore", func(t *testing.T) {
		pgDumpRunner = func(_ context.Context, _, dsn, out string) error {
			if dsn != "postgres://admin@h/db" {
				t.Errorf("the safety copy must be dumped through the admin role, got %q", dsn)
			}
			return os.WriteFile(out, []byte("dump"), 0o644)
		}
		out, err := safetyDumpPostgres(context.Background(), drFlags{adminDSN: "postgres://admin@h/db"}, "pg_dump", dir, "20261025-000000")
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(out) != "database.pgcustom.pre-restore-20261025-000000" {
			t.Errorf("unexpected name %s", out)
		}
		if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("the copy must be readable by its owner only: %v %v", info, err)
		}
	})

	t.Run("an existing copy is never overwritten", func(t *testing.T) {
		existing := t.TempDir()
		if err := os.WriteFile(filepath.Join(existing, "database.pgcustom.pre-restore-20261025-000002"), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		pgDumpRunner = func(context.Context, string, string, string) error {
			t.Fatal("pg_dump ran over an existing safety copy")
			return nil
		}
		_, err := safetyDumpPostgres(context.Background(), drFlags{adminDSN: "postgres://admin@h/db"}, "pg_dump", existing, "20261025-000002")
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("an existing safety copy must be refused: %v", err)
		}
	})

	t.Run("the dump is written under a .partial name and the final name appears only on success", func(t *testing.T) {
		seen := t.TempDir()
		pgDumpRunner = func(_ context.Context, _, _, out string) error {
			if !strings.HasSuffix(out, ".partial") {
				t.Errorf("pg_dump must write to a .partial name, got %s", out)
			}
			if _, err := os.Lstat(strings.TrimSuffix(out, ".partial")); err == nil {
				t.Errorf("the final name exists while pg_dump is still writing")
			}
			if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o600 {
				t.Errorf("the file must exist with mode 0600 before pg_dump writes: %v %v", info, err)
			}
			return os.WriteFile(out, []byte("dump"), 0o600)
		}
		out, err := safetyDumpPostgres(context.Background(), drFlags{adminDSN: "postgres://admin@h/db"}, "pg_dump", seen, "20261025-000003")
		if err != nil {
			t.Fatal(err)
		}
		if left, _ := filepath.Glob(filepath.Join(seen, "*.partial")); len(left) != 0 || filepath.Base(out) != "database.pgcustom.pre-restore-20261025-000003" {
			t.Errorf("unexpected files after success: %v %s", left, out)
		}
	})

	t.Run("a failed dump leaves no file and says nothing was changed", func(t *testing.T) {
		failDir := t.TempDir()
		pgDumpRunner = func(_ context.Context, _, _, out string) error {
			_ = os.WriteFile(out, []byte("partial"), 0o600)
			return os.ErrPermission
		}
		_, err := safetyDumpPostgres(context.Background(), drFlags{adminDSN: "postgres://admin@h/db"}, "pg_dump", failDir, "20261025-000001")
		if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
			t.Fatalf("a failed safety copy must stop the replacement: %v", err)
		}
		if left, _ := filepath.Glob(filepath.Join(failDir, "*")); len(left) != 0 {
			t.Errorf("a failed safety copy left files behind: %v", left)
		}
	})
}

func TestCheckScratchBaseRefusesWhatTheScratchRolesWouldNotInherit(t *testing.T) {
	for _, tc := range []struct {
		dsn, wantErr string
	}{
		{"postgres://dba:pw@db.example:5432/postgres?sslmode=require&connect_timeout=5", ""},
		{"postgres://dba:pw@db.example/postgres", ""},
		{"postgres:///postgres?host=/var/run/postgresql", "must name the server in its host part"},
		{"postgres://db.example/postgres?target_session_attrs=read-write", `query parameter "target_session_attrs"`},
		{"postgres://db.example/postgres?channel_binding=require", `query parameter "channel_binding"`},
		{"postgres://db.example/postgres?user=other", `query parameter "user"`},
	} {
		u, err := url.Parse(tc.dsn)
		if err != nil {
			t.Fatal(err)
		}
		err = checkScratchBase(u)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected refusal: %v", tc.dsn, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: want a refusal containing %q, got %v", tc.dsn, tc.wantErr, err)
		}
	}
}

func TestScratchRoleDSNKeepsASpaceInACertificatePath(t *testing.T) {
	base, err := url.Parse("postgres://dba@db.example/postgres?sslrootcert=%2Fetc%2Fmy%20certs%2Fca.pem")
	if err != nil {
		t.Fatal(err)
	}
	dsn := scratchRoleDSN(base, url.UserPassword("r", "p"), "d")
	if !strings.Contains(dsn, "sslrootcert=%2Fetc%2Fmy%20certs%2Fca.pem") || strings.Contains(dsn, "+") {
		t.Fatalf("a space must stay %%20 for libpq's decoder: %s", dsn)
	}
}
