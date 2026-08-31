// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// THE POSTGRES RESTORE COULD MODIFY A LIVE ESTATE WITHOUT EXIGING OR SEALING
// ANYTHING, and the reason it was believed not to could only be found by running
// it (from the sol-max contrast of PR #588).
//
// The declaration guard classified a Postgres target as "clean" whenever the data
// dir held no local signing keys — true by design under BYOK/CMEK custody — so it
// never called decl.require and never sealed. That was defended with "a Postgres
// restore into a database that already holds the schema FAILS", which is a claim
// about pg_restore, and pg_restore does not behave that way: it is NOT atomic by
// default and the wrapper asked for neither --exit-on-error nor --single-transaction.
//
// MEASURED against PostgreSQL 16.14 with the shipped argv: rc 1, four errors, and
// the backup's rows INSERTED into both pre-existing tables of the live target.
// Exiting non-zero makes it LOUD, not non-destructive and not attributable.
//
// These tests run against a real server because that is the only place the
// property lives: no double reproduces pg_restore's partial-effect semantics, and
// a test that mocked the binary would have gone green over the defect.

// pgProbeDSN is the maintenance DSN the Postgres leg provisions its throwaway
// databases from. The variable name is the one the rest of the suite already
// gates on (core/internal/pgtest), so a run that configures Postgres configures
// this leg too; cmd/olivares cannot import that helper (it is core-internal), so
// the gate is repeated here rather than shared.
const pgProbeDSN = "OLIVARES_TEST_POSTGRES_SUPERUSER_DSN"

// pgFixture is a throwaway Postgres database, dropped when the test ends.
type pgFixture struct {
	dsn    string
	name   string
	binDir string
}

// newPGFixture creates an empty database for t and returns a handle that knows
// where the matching client binaries are. It SKIPS when no server is configured
// and FAILS when one is configured but unusable — a Postgres leg must never
// vanish quietly.
func newPGFixture(t *testing.T, label string) *pgFixture {
	t.Helper()
	super := strings.TrimSpace(os.Getenv(pgProbeDSN))
	if super == "" {
		t.Skipf("set %s to run the Postgres leg (throwaway databases are provisioned from it)", pgProbeDSN)
	}
	maint, err := sql.Open("pgx", super)
	if err != nil {
		t.Fatalf("open %s: %v", pgProbeDSN, err)
	}
	defer func() { _ = maint.Close() }()

	ctx := t.Context()
	var major int
	if err := maint.QueryRowContext(ctx, "SELECT current_setting('server_version_num')::int / 10000").Scan(&major); err != nil {
		t.Fatalf("probe server version over %s: %v", pgProbeDSN, err)
	}
	binDir := pgClientBinDir(t, major)

	name := fmt.Sprintf("s630_%s_%d", label, time.Now().UnixNano())
	if _, err := maint.ExecContext(ctx, `CREATE DATABASE "`+name+`"`); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		db, err := sql.Open("pgx", super)
		if err != nil {
			return
		}
		defer func() { _ = db.Close() }()
		_, _ = db.Exec(`DROP DATABASE IF EXISTS "` + name + `" WITH (FORCE)`)
	})
	return &pgFixture{dsn: replaceDBName(t, super, name), name: name, binDir: binDir}
}

// pgClientBinDir finds pg_dump/pg_restore of the SAME major as the server. A
// mismatched pg_dump refuses to dump a newer server at all, so a test that just
// took whatever was first on PATH would report a fixture failure as a defect.
func pgClientBinDir(t *testing.T, major int) string {
	t.Helper()
	if dir := strings.TrimSpace(os.Getenv("OLIVARES_TEST_PG_BINDIR")); dir != "" {
		return dir
	}
	candidates := []string{
		filepath.Join("/usr/lib/postgresql", strconv.Itoa(major), "bin"),
		filepath.Join("/usr/pgsql-"+strconv.Itoa(major), "bin"),
	}
	for _, dir := range candidates {
		if pgBinMajor(filepath.Join(dir, "pg_dump")) == major &&
			pgBinMajor(filepath.Join(dir, "pg_restore")) == major {
			return dir
		}
	}
	if pgBinMajor("pg_dump") == major && pgBinMajor("pg_restore") == major {
		return "" // both on PATH and matching
	}
	t.Skipf("no pg_dump/pg_restore of major %d found (set OLIVARES_TEST_PG_BINDIR); "+
		"a mismatched client cannot dump this server, so the leg would measure the fixture, not the code", major)
	return ""
}

// pgBinMajor reports a client binary's major version, or 0 if it cannot be run.
func pgBinMajor(bin string) int {
	out, err := exec.Command(bin, "--version").Output() // #nosec G204 -- test-only version probe of a known client name
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(out))
	if len(fields) < 3 {
		return 0
	}
	major, err := strconv.Atoi(strings.SplitN(fields[2], ".", 2)[0])
	if err != nil {
		return 0
	}
	return major
}

// bin resolves a client binary inside the fixture's matched bin dir.
func (f *pgFixture) bin(name string) string {
	if f.binDir == "" {
		return name
	}
	return filepath.Join(f.binDir, name)
}

// exec runs SQL against the fixture database.
func (f *pgFixture) exec(t *testing.T, stmts string) {
	t.Helper()
	db, err := sql.Open("pgx", f.dsn)
	if err != nil {
		t.Fatalf("open %s: %v", f.name, err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), stmts); err != nil {
		t.Fatalf("exec on %s: %v", f.name, err)
	}
}

// canExec runs SQL that MAY be unavailable on this server (an extension the build
// does not carry), reporting whether it worked instead of failing the test. It
// exists so a cell that needs postgres_fdw can SKIP with a reason rather than
// report the server's packaging as a defect in the code.
func (f *pgFixture) canExec(t *testing.T, stmts string) bool {
	t.Helper()
	db, err := sql.Open("pgx", f.dsn)
	if err != nil {
		t.Fatalf("open %s: %v", f.name, err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), stmts); err != nil {
		t.Logf("optional DDL unavailable on this server: %v", err)
		return false
	}
	return true
}

// count runs a scalar count query against the fixture database.
func (f *pgFixture) count(t *testing.T, query string) int {
	t.Helper()
	db, err := sql.Open("pgx", f.dsn)
	if err != nil {
		t.Fatalf("open %s: %v", f.name, err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRowContext(t.Context(), query).Scan(&n); err != nil {
		t.Fatalf("count on %s: %v", f.name, err)
	}
	return n
}

// replaceDBName rewrites the database component of a libpq URL.
func replaceDBName(t *testing.T, dsn, name string) string {
	t.Helper()
	cut := strings.LastIndex(dsn, "/")
	if cut < 0 {
		t.Fatalf("%s is not a libpq URL: %q", pgProbeDSN, dsn)
	}
	rest := dsn[cut+1:]
	query := ""
	if q := strings.Index(rest, "?"); q >= 0 {
		query = rest[q:]
	}
	return dsn[:cut+1] + name + query
}

// estateDump builds a custom-format dump of a small two-table estate and returns
// its path, plus the fixture the dump was taken from.
func estateDump(t *testing.T) string {
	t.Helper()
	src := newPGFixture(t, "src")
	src.exec(t, `
CREATE TABLE orgs (id text PRIMARY KEY, name text NOT NULL);
CREATE TABLE audit_events (id text PRIMARY KEY, tenant text NOT NULL, seq bigint NOT NULL, action text);
INSERT INTO orgs VALUES ('org-backup-1','from the BACKUP'),('org-backup-2','from the BACKUP');
INSERT INTO audit_events VALUES ('ev-b1','t1',1,'backup.event'),('ev-b2','t1',2,'backup.event');`)

	dump := filepath.Join(t.TempDir(), "dump.pgcustom")
	cmd := exec.Command(src.bin("pg_dump"), // #nosec G204 -- test-only, both operands are fixture-owned
		"--format=custom", "--no-owner", "--no-privileges", "--file", dump, "--dbname", src.dsn)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pg_dump: %v\n%s", err, out)
	}
	return dump
}

// TestPgRestoreIntoALiveDatabaseLeavesItUNCHANGEDWhenItFails is the measured
// defect. The target already holds the dump's schema plus a row created AFTER the
// backup — the live estate. pg_restore must fail (the objects collide) and it must
// leave NOTHING behind: no backup row, and the live row intact.
//
// Before --single-transaction this test fails on the row counts, not on the error:
// the command returned an error AND had already inserted the backup's rows.
//
// THE FIXTURE IS SHAPED TO DISCRIMINATE, and the first version was not. It gave the
// live target BOTH of the dump's tables, so the very first CREATE TABLE conflicted
// and the run stopped there — which proves --exit-on-error and says nothing about
// the transaction. An external contrast pointed that out: the semantic mutant
// (--single-transaction replaced by --exit-on-error) survived while the literal
// mutant died.
//
// So the live target holds ONLY `orgs`. pg_restore walks the TOC with audit_events
// FIRST (measured: entry 216 before 215), so audit_events is created with no
// conflict and `orgs` conflicts after it. With --exit-on-error alone the run exits
// 1 and LEAVES audit_events behind — measured on PostgreSQL 16.14. With
// --single-transaction it is rolled back, which is what the absent-table assertion
// below is for.
func TestPgRestoreIntoALiveDatabaseLeavesItUNCHANGEDWhenItFails(t *testing.T) {
	dump := estateDump(t)
	live := newPGFixture(t, "live")
	live.exec(t, `
CREATE TABLE orgs (id text PRIMARY KEY, name text NOT NULL);
INSERT INTO orgs VALUES ('org-live-9','LIVE, created AFTER the backup');`)

	err := runPgRestore(context.Background(), live.bin("pg_restore"), live.dsn, dump)
	if err == nil {
		t.Fatal("pg_restore over a live estate reported success")
	}

	// The discriminator: a table the dump creates BEFORE it hits the conflict. Only
	// a rollback removes it; stopping at the first error does not.
	if n := live.count(t, `SELECT count(*) FROM pg_catalog.pg_class c
	  JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
	  WHERE n.nspname = 'public' AND c.relname = 'audit_events'`); n != 0 {
		t.Fatal("NOT TRANSACTIONAL: a table the dump created before the conflict survived the failure — " +
			"this is --exit-on-error behavior, not a rolled-back single transaction")
	}
	if n := live.count(t, `SELECT count(*) FROM orgs WHERE id LIKE 'org-backup-%'`); n != 0 {
		t.Fatalf("PARTIAL RESTORE: %d row(s) from the BACKUP landed in the live estate although the command failed", n)
	}
	if n := live.count(t, `SELECT count(*) FROM orgs WHERE id = 'org-live-9'`); n != 1 {
		t.Fatalf("the live row was disturbed by a failed restore: got %d, want 1", n)
	}
}

// TestPgRestoreStillRestoresIntoAnEmptyTarget is the other half of the atomicity
// decision: making a failed restore leave nothing must not make a GOOD restore
// leave nothing either. This is the documented Postgres path (an empty target).
func TestPgRestoreStillRestoresIntoAnEmptyTarget(t *testing.T) {
	dump := estateDump(t)
	fresh := newPGFixture(t, "fresh")

	if err := runPgRestore(context.Background(), fresh.bin("pg_restore"), fresh.dsn, dump); err != nil {
		t.Fatalf("restore into an EMPTY target failed: %v", err)
	}
	if n := fresh.count(t, `SELECT count(*) FROM orgs`); n != 2 {
		t.Fatalf("orgs restored = %d, want 2", n)
	}
	if n := fresh.count(t, `SELECT count(*) FROM audit_events`); n != 2 {
		t.Fatalf("audit_events restored = %d, want 2", n)
	}
}

// TestDRRestoreOverALivePostgresRefusesWithoutADeclaration is the classifier
// defect. The declaration was decided from the FILESYSTEM alone, so a Postgres
// estate whose keys are externally custodied (BYOK/CMEK — an empty data dir by
// design) read as "clean target" and the command asked for nothing.
//
// The refusal must land BEFORE the bundle is opened, so the bundle here is
// deliberately not a valid one: if the command gets far enough to complain about
// the bundle, the guard did not fire.
func TestDRRestoreOverALivePostgresRefusesWithoutADeclaration(t *testing.T) {
	live := newPGFixture(t, "guard")
	live.exec(t, `CREATE TABLE orgs (id text PRIMARY KEY, name text NOT NULL);
INSERT INTO orgs VALUES ('org-live-9','LIVE');`)

	emptyDataDir := t.TempDir() // externally custodied keys: nothing on disk
	bundle := filepath.Join(t.TempDir(), "not-a-bundle.drbundle")
	if err := os.WriteFile(bundle, []byte("not a bundle"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runDR("restore", "--engine", "postgres", "--dsn", live.dsn,
		"--data-dir", emptyDataDir, "--in", bundle)
	if err == nil {
		t.Fatalf("a Postgres restore over a LIVE database was accepted with no declaration:\n%s", out)
	}
	if !strings.Contains(err.Error(), "--operator") || !strings.Contains(err.Error(), "--reason") {
		t.Fatalf("the refusal must name both missing flags, got: %v", err)
	}
}

// TestDRRestoreIntoAnEmptyPostgresNeedsNoDeclaration keeps the guard from
// becoming "always require": a restore into an EMPTY database destroys nothing,
// and the documented Postgres path must stay usable without the flags. The
// command still fails on the invalid bundle — that is the point: it got PAST the
// declaration guard.
func TestDRRestoreIntoAnEmptyPostgresNeedsNoDeclaration(t *testing.T) {
	fresh := newPGFixture(t, "clean")

	bundle := filepath.Join(t.TempDir(), "not-a-bundle.drbundle")
	if err := os.WriteFile(bundle, []byte("not a bundle"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := runDR("restore", "--engine", "postgres", "--dsn", fresh.dsn,
		"--data-dir", t.TempDir(), "--in", bundle)
	if err == nil {
		t.Fatal("an invalid bundle was accepted")
	}
	if strings.Contains(err.Error(), "--operator") {
		t.Fatalf("an EMPTY Postgres target demanded a declaration it does not need: %v", err)
	}
}

// TestDRRestoreOverAPostgresHoldingSTATEThatIsNotATableStillRefuses is the
// external contrast's B-02, and it is the same defect class as the one this whole
// pass exists for, one level down: the FIRST version of the occupancy probe listed
// five relkinds it thought were "state" (r,p,m,v,S) and therefore answered CLEAN
// over everything else. Measured by the contrast on PostgreSQL 16.14, a database
// holding a schema, a function, an extension, a FOREIGN TABLE and a large object
// counted zero.
//
// A foreign table is the sharpest of those — `relkind='f'`, a real table to every
// query that touches it, whose data lives elsewhere — but the shape of the mistake
// is what matters: an ENUMERATION of what counts as state, written by someone
// thinking about the estate's own tables, silently defines everything it forgot as
// emptiness. The cells here are the ones that are cheap to build without an
// extension, so they run everywhere the rest of this leg runs.
func TestDRRestoreOverAPostgresHoldingSTATEThatIsNotATableStillRefuses(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "not-a-bundle.drbundle")
	if err := os.WriteFile(bundle, []byte("not a bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, ddl string
	}{
		{"a schema of its own", `CREATE SCHEMA tenant_data;`},
		{"a function and nothing else", `CREATE FUNCTION s630_f() RETURNS int LANGUAGE sql AS 'SELECT 1';`},
		{"a sequence and nothing else", `CREATE SEQUENCE s630_seq;`},
		{"a view over nothing", `CREATE VIEW s630_v AS SELECT 1 AS one;`},
		// THE CELL THE MUTATION BATTERY DEMANDED. The four above all survive a mutant
		// that puts the old relkind enumeration back — 'S' and 'v' were on that list,
		// and a schema and a function are caught by other terms — so without this one
		// the fix for the contrast's actual finding was untested and the mutant lived.
		// A foreign table is relkind 'f', which the enumeration did not list.
		{"a FOREIGN TABLE — relkind 'f', the one the enumeration forgot", `
CREATE EXTENSION IF NOT EXISTS postgres_fdw;
CREATE SERVER s630_srv FOREIGN DATA WRAPPER postgres_fdw OPTIONS (host 'localhost');
CREATE FOREIGN TABLE s630_ft (id int) SERVER s630_srv OPTIONS (table_name 'elsewhere');`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := newPGFixture(t, "state")
			if strings.Contains(tc.ddl, "postgres_fdw") && !target.canExec(t, tc.ddl) {
				t.Skip("postgres_fdw is not installable on this server; the relkind 'f' cell cannot run here")
			}
			if !strings.Contains(tc.ddl, "postgres_fdw") {
				target.exec(t, tc.ddl)
			}
			_, err := runDR("restore", "--engine", "postgres", "--dsn", target.dsn,
				"--data-dir", t.TempDir(), "--in", bundle)
			if err == nil {
				t.Fatal("an invalid bundle was accepted")
			}
			if !strings.Contains(err.Error(), "--operator") {
				t.Fatalf("a target holding %s was classified as EMPTY: %v", tc.name, err)
			}
		})
	}
}

// TestDRRestoreOverAnUnREADABLEPostgresRefusesWithoutADeclaration is the
// fail-closed half. "I could not look" is not "it is clean": a target the command
// cannot reach may well hold an estate, and the classifier that decided from the
// filesystem answered "clean" for every one of them.
func TestDRRestoreOverAnUnREADABLEPostgresRefusesWithoutADeclaration(t *testing.T) {
	if strings.TrimSpace(os.Getenv(pgProbeDSN)) == "" {
		t.Skipf("set %s to run the Postgres leg", pgProbeDSN)
	}
	bundle := filepath.Join(t.TempDir(), "not-a-bundle.drbundle")
	if err := os.WriteFile(bundle, []byte("not a bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A DSN that resolves to nothing: the port is closed on purpose.
	dead := "postgres://olivares:olivares@127.0.0.1:1/olivares?sslmode=disable&connect_timeout=2"

	_, err := runDR("restore", "--engine", "postgres", "--dsn", dead,
		"--data-dir", t.TempDir(), "--in", bundle)
	if err == nil {
		t.Fatal("a restore against an unreachable Postgres target was accepted")
	}
	if !strings.Contains(err.Error(), "--operator") {
		t.Fatalf("an UNREADABLE target must fail closed and demand the declaration, got: %v", err)
	}
}
