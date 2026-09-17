// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/internal/pgtest"
)

// logincapability_pg_dr_test.go — the DISASTER-RECOVERY half of the core v13 access
// boundary, which the rest of the v13 suite never asked about.
//
// `olivares dr backup` runs `pg_dump` on the ADMIN DSN and on no other, by contract:
// pg_dump keeps `row_security=off` and ABORTS as the NOBYPASSRLS application role under
// FORCE ROW LEVEL SECURITY (cmd/olivares/cmd_dr.go, the --admin-dsn flag help and
// backupPostgres). pg_dump's first act is to LOCK every relation of the schema IN ACCESS
// SHARE MODE in ONE statement, so a single unreadable relation does not degrade the dump —
// it aborts it. A backup role that cannot read one relation cannot back up ANY of them.
//
// That is why this leg asserts the WHOLE schema, not only v13's relation: the property the
// DR contract needs is "the admin read pool can read every relation the engine creates",
// and the shape of the defect it was born from was exactly one relation out of 306.
//
// It is a READ boundary, not a write one: the same cells prove that the admin role holds
// SELECT and NOTHING else, so admitting the backup role never admits a second writer.

// TestPGLoginCapabilityAdminReadPoolCanDumpInBothPostures is red on a tree where core v13's
// birth revoke strips the operator-provisioned admin SELECT, which is the state in which
// every Postgres backup of a v13 estate fails with
// `pg_dump: error: query failed: ERROR:  permission denied for table login_capability_observation`.
func TestPGLoginCapabilityAdminReadPoolCanDumpInBothPostures(t *testing.T) {
	if !pgtest.Available(t) {
		t.Skipf("set %s (a superuser DSN) to run the login capability DR legs", pgtest.EnvSuperuserDSN)
	}
	const rel = "public.login_capability_observation"
	for _, split := range []bool{false, true} {
		name := map[bool]string{true: "split", false: "single-role"}[split]
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newLoginCapabilityTopologyFixture(t, split)
			admin := openCustodyPGPool(t, f.pg.Admin)
			adminRole := currentCustodyRole(t, admin)

			// 1. The exact statement pg_dump issues, in the exact mode, inside the exact
			// kind of transaction pg_dump opens. Asserting `SELECT count(*)` instead would
			// measure a weaker privilege than the dump needs.
			tx, err := admin.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin the dump transaction as %q: %v", adminRole, err)
			}
			defer tx.Rollback() //nolint:errcheck // read-only probe, never committed
			if _, err := tx.ExecContext(ctx, "LOCK TABLE "+rel+" IN ACCESS SHARE MODE"); err != nil {
				t.Fatalf("the DR backup role %q cannot LOCK %s in the %s posture, so `olivares dr backup` cannot dump this estate at all: %v",
					adminRole, rel, name, err)
			}
			var observations int64
			if err := tx.QueryRowContext(ctx, "SELECT pg_catalog.count(*) FROM "+rel).Scan(&observations); err != nil {
				t.Fatalf("the DR backup role %q cannot read %s in the %s posture: %v", adminRole, rel, name, err)
			}
			if observations != 1 {
				t.Fatalf("the fixture's single observation read back as %d rows", observations)
			}

			// 2. NO relation of the schema may be unreadable to the backup role — the dump
			// is all-or-nothing, so "all but one" is the same failure as "none".
			var denied int64
			var deniedNames string
			if err := admin.QueryRowContext(ctx, `SELECT pg_catalog.count(*),
  COALESCE(pg_catalog.string_agg(c.relname::pg_catalog.text, ', ' ORDER BY c.relname), '')
FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind = 'r'
  AND NOT pg_catalog.has_table_privilege($1, c.oid, 'SELECT')`, adminRole).Scan(&denied, &deniedNames); err != nil {
				t.Fatalf("read the backup role's unreadable relations: %v", err)
			}
			if denied != 0 {
				t.Fatalf("the DR backup role %q cannot read %d relation(s) in the %s posture, so pg_dump aborts on the schema-wide LOCK: %s",
					adminRole, denied, name, deniedNames)
			}

			// 3. READ, and read only: the backup role is not a second writer of the
			// login-capability history, and cannot hand its read on.
			var write, columnWrite, grantable bool
			if err := admin.QueryRowContext(ctx, `SELECT
  pg_catalog.has_table_privilege($1, $2::pg_catalog.regclass, 'INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER'),
  pg_catalog.has_any_column_privilege($1, $2::pg_catalog.regclass, 'INSERT, UPDATE, REFERENCES'),
  pg_catalog.has_table_privilege($1, $2::pg_catalog.regclass, 'SELECT WITH GRANT OPTION')`, adminRole, rel).
				Scan(&write, &columnWrite, &grantable); err != nil {
				t.Fatalf("read the backup role's write privileges: %v", err)
			}
			if write || columnWrite || grantable {
				t.Fatalf("the backup role %q holds more than SELECT on %s: table write=%t column write=%t grantable=%t",
					adminRole, rel, write, columnWrite, grantable)
			}

			// 4. The verifier accepts the posture it just measured, and so does a real boot:
			// a fix that the verifier refuses is not a fix.
			if err := f.verify(t); err != nil {
				t.Fatalf("the verifier refused a database whose backup role holds exactly SELECT: %v", err)
			}
			reopened, err := openLoginCapabilityPGTopology(f.pg, split)
			if err != nil {
				t.Fatalf("the per-boot path refused a database whose backup role holds exactly SELECT: %v", err)
			}
			_ = reopened.Close()
			t.Logf("LOGIN_CAPABILITY_DR_READ|topology=%s|backup_role=%s|unreadable_relations=0", name, adminRole)
		})
	}
}
