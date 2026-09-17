// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/store"
)

// TestPostgresRestoreClosureReestablishesLoginCapabilityACL is the RESTORE half of the same
// contract, and it is a second defect of the same family that the first one hid: until
// `dr backup` could produce a dump at all, nothing in this tree had ever booted a RESTORED
// v13 estate.
//
// `dr restore` runs `pg_restore --no-privileges` (cmd/olivares/cmd_dr.go runPgDump/runPgRestore),
// so no grant of the SOURCE travels. Every restored relation arrives with the DESTINATION's
// default privileges instead — and in the owner/app split those are
// `ALTER DEFAULT PRIVILEGES FOR ROLE <owner> … GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES
// TO <app>`, which is strictly more than v13's contract allows the application role and carries
// none of the three column grants it requires. The boot that `dr restore` performs to prove
// continuity then refuses the estate it has just written.
//
// The catalog perturbation below is that exact end state, isolated the same way
// TestPostgresRestoreUserAuthorityClosure isolates the stripped function ACL: it costs no dump,
// and TestDRPostgresRoundTripAcrossBothPostures still exercises the real one.
func TestPostgresRestoreClosureReestablishesLoginCapabilityACL(t *testing.T) {
	if !pgtest.Available(t) {
		t.Skipf("set %s (a superuser DSN) to run the login capability restore legs", pgtest.EnvSuperuserDSN)
	}
	const rel = "public.login_capability_observation"
	for _, split := range []bool{false, true} {
		name := map[bool]string{true: "split", false: "single-role"}[split]
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			f := newLoginCapabilityTopologyFixture(t, split)
			cfg := store.Config{Engine: store.EnginePostgres, DSN: f.pg.App, MaxConns: 2}
			if split {
				cfg.OwnerDSN = f.pg.Owner
			}
			owner := openCustodyPGPool(t, f.pg.Owner)
			admin := openCustodyPGPool(t, f.pg.Admin)
			adminRole := currentCustodyRole(t, admin)

			if split {
				// The destination's default privileges, exactly: table-wide DML for the
				// application role and not one column grant.
				for _, stmt := range []string{
					"REVOKE UPDATE (last_observed_at, last_artifact_version, observation_count) ON " + rel + " FROM " + quoteIdent(f.roles.App.Role),
					"REVOKE ALL PRIVILEGES ON " + rel + " FROM " + quoteIdent(f.roles.App.Role),
					"GRANT SELECT, INSERT, UPDATE, DELETE ON " + rel + " TO " + quoteIdent(f.roles.App.Role),
				} {
					if _, err := owner.ExecContext(ctx, stmt); err != nil {
						t.Fatalf("reproduce the restored ACL %q: %v", stmt, err)
					}
				}
				// RED witness: this is the state a restore leaves, and it is refused.
				if err := f.verify(t); err == nil {
					t.Fatal("the verifier accepted a restored relation carrying the destination's default DML")
				} else if !strings.Contains(err.Error(), "DELETE") {
					t.Fatalf("the verifier refused the restored ACL for the wrong reason: %v", err)
				}
			}

			// AdminDSN is deliberately unusable: the closure's authority is owner+app only.
			closureCfg := cfg
			closureCfg.AdminDSN = "not-a-connection-string"
			if err := RestorePostgresUserAuthorityPrivileges(ctx, closureCfg); err != nil {
				t.Fatalf("the restore closure failed: %v", err)
			}
			if err := f.verify(t); err != nil {
				t.Fatalf("the verifier refused the relation the restore closure just established: %v", err)
			}
			// Idempotent: a second closure is the same fact, not a second one.
			if err := RestorePostgresUserAuthorityPrivileges(ctx, closureCfg); err != nil {
				t.Fatalf("the restore closure is not idempotent: %v", err)
			}
			if err := f.verify(t); err != nil {
				t.Fatalf("the second closure changed the boundary: %v", err)
			}
			// The closure re-establishes the WRITE boundary without taking the backup
			// role's read away — a restored estate must be backup-able immediately.
			var adminReads, adminWrites bool
			if err := admin.QueryRowContext(ctx, `SELECT
  pg_catalog.has_table_privilege($1, $2::pg_catalog.regclass, 'SELECT'),
  pg_catalog.has_table_privilege($1, $2::pg_catalog.regclass, 'INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER')`, adminRole, rel).
				Scan(&adminReads, &adminWrites); err != nil {
				t.Fatalf("read the backup role's posture after the closure: %v", err)
			}
			if !adminReads || adminWrites {
				t.Fatalf("after the restore closure the backup role %q reads=%t writes=%t, want read-only", adminRole, adminReads, adminWrites)
			}
			reopened, err := openLoginCapabilityPGTopology(f.pg, split)
			if err != nil {
				t.Fatalf("the per-boot path refused the restored estate: %v", err)
			}
			_ = reopened.Close()
		})
	}
}
