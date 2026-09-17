// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/store"
)

// openLoginCapabilityPGTopology opens a real store in the requested role topology.
func openLoginCapabilityPGTopology(pg pgtest.DSNs, split bool) (store.Store, error) {
	if split {
		return openLoginCapabilityPGAt(pg)
	}
	return Open(context.Background(), store.Config{Engine: store.EnginePostgres, DSN: pg.App, MaxConns: 4}, nil)
}

type loginCapabilityTopologyFixture struct {
	pg       pgtest.DSNs
	split    bool
	verifyDB *sql.DB
	roles    guardRoles
}

// newLoginCapabilityTopologyFixture migrates a fresh isolated database through a real Open,
// records one observation and returns the roles the verifier must accept.
func newLoginCapabilityTopologyFixture(t *testing.T, split bool) loginCapabilityTopologyFixture {
	t.Helper()
	f := loginCapabilityTopologyFixture{split: split}
	if split {
		f.pg = isolatedPGSplit(t)
	} else {
		f.pg = isolatedPG(t)
	}
	st, err := openLoginCapabilityPGTopology(f.pg, split)
	if err != nil {
		t.Fatalf("open the %s store: %v", map[bool]string{true: "split", false: "single-role"}[split], err)
	}
	observeLoginCapability(t, st, "artifact")
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	app := openCustodyPGPool(t, f.pg.App)
	f.roles = guardRoles{App: guardRoleFact{Known: true, Role: currentCustodyRole(t, app)}}
	f.verifyDB = app
	if split {
		owner := openCustodyPGPool(t, f.pg.Owner)
		f.roles.Owner = guardRoleFact{Known: true, Role: currentCustodyRole(t, owner)}
		f.roles.OwnerConfigured = true
		f.verifyDB = owner
	}
	return f
}

func (f loginCapabilityTopologyFixture) verify(t *testing.T) error {
	t.Helper()
	ctx := context.Background()
	tx, err := f.verifyDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck
	return verifyPostgresLoginCapabilityRelation(ctx, tx, f.roles)
}

// TestPGLoginCapabilityCleanSingleAndSplitObservationsVerify is the clean witness on every
// major: a normal observation in each topology verifies directly and reopens.
func TestPGLoginCapabilityCleanSingleAndSplitObservationsVerify(t *testing.T) {
	if !pgtest.Available(t) {
		t.Skipf("set %s (a superuser DSN) to run the login capability topology legs", pgtest.EnvSuperuserDSN)
	}
	for _, split := range []bool{false, true} {
		name := map[bool]string{true: "split", false: "single-role"}[split]
		t.Run(name, func(t *testing.T) {
			f := newLoginCapabilityTopologyFixture(t, split)
			var version int
			if err := f.verifyDB.QueryRowContext(context.Background(), `SELECT pg_catalog.current_setting('server_version_num')::pg_catalog.int4`).Scan(&version); err != nil {
				t.Fatalf("read the server version: %v", err)
			}
			if err := f.verify(t); err != nil {
				t.Fatalf("a clean %s observation failed the verifier on server %d: %v", name, version, err)
			}
			reopened, err := openLoginCapabilityPGTopology(f.pg, split)
			if err != nil {
				t.Fatalf("a clean %s database was refused at boot on server %d: %v", name, version, err)
			}
			_ = reopened.Close()
			t.Logf("LOGIN_CAPABILITY_CLEAN|topology=%s|server_version_num=%d", name, version)
		})
	}
}

// TestPGLoginCapabilityRefusesEffectiveMaintain covers PostgreSQL 17's pg_maintain predefined
// role, which confers MAINTAIN (including LOCK TABLE) on every relation without any relation
// ACL entry. R5 §3 forbids another operational role access: a non-superuser user role holding
// effective MAINTAIN must be refused by the verifier and by the actual per-boot Open.
func TestPGLoginCapabilityRefusesEffectiveMaintain(t *testing.T) {
	if !pgtest.Available(t) {
		t.Skipf("set %s (a superuser DSN) to run the login capability MAINTAIN legs", pgtest.EnvSuperuserDSN)
	}
	for _, tc := range []struct {
		name    string
		split   bool
		grantee string // "third" or "app"
	}{
		{"split third user inherits pg_maintain", true, "third"},
		{"split application role inherits pg_maintain", true, "app"},
		{"single-role third user inherits pg_maintain", false, "third"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newLoginCapabilityTopologyFixture(t, tc.split)
			su := openCustodyPGPool(t, loginCapabilitySuperuserOnDatabase(t, f.pg))
			var version int
			if err := su.QueryRowContext(ctx, `SELECT pg_catalog.current_setting('server_version_num')::pg_catalog.int4`).Scan(&version); err != nil {
				t.Fatalf("read the server version: %v", err)
			}
			if version < 170000 {
				t.Skipf("server_version_num=%d has no pg_maintain predefined role or MAINTAIN privilege", version)
			}
			if err := f.verify(t); err != nil {
				t.Fatalf("green witness: the clean relation failed its verifier before the grant: %v", err)
			}
			grantee := f.roles.App.Role
			if tc.grantee == "third" {
				grantee = fmt.Sprintf("r107_maint_%d", time.Now().UnixNano())
				if _, err := su.ExecContext(ctx, "CREATE ROLE "+quoteIdent(grantee)+" NOLOGIN INHERIT"); err != nil {
					t.Fatalf("create the third role: %v", err)
				}
			}
			// pg_maintain membership is CLUSTER-GLOBAL: undo it before the pools close.
			t.Cleanup(func() {
				stmt := "REVOKE pg_maintain FROM " + quoteIdent(grantee)
				if tc.grantee == "third" {
					stmt = "DROP ROLE IF EXISTS " + quoteIdent(grantee)
				}
				if _, err := su.ExecContext(context.Background(), stmt); err != nil {
					t.Errorf("cleanup %q: %v", stmt, err)
				}
			})
			if _, err := su.ExecContext(ctx, "GRANT pg_maintain TO "+quoteIdent(grantee)); err != nil {
				t.Fatalf("grant pg_maintain: %v", err)
			}
			var effective, explicit bool
			if err := su.QueryRowContext(ctx, `SELECT
  pg_catalog.has_table_privilege($1, 'public.login_capability_observation'::pg_catalog.regclass, 'MAINTAIN'),
  EXISTS (SELECT 1 FROM pg_catalog.pg_class c CROSS JOIN LATERAL pg_catalog.aclexplode(c.relacl) a
          JOIN pg_catalog.pg_roles g ON g.oid = a.grantee
          WHERE c.oid = 'public.login_capability_observation'::pg_catalog.regclass AND g.rolname = $1
            AND a.privilege_type = 'MAINTAIN')`, grantee).
				Scan(&effective, &explicit); err != nil {
				t.Fatalf("read the effective MAINTAIN witness: %v", err)
			}
			if !effective || explicit {
				t.Fatalf("witness: %q effective MAINTAIN=%t via explicit relation ACL=%t, want effective without any ACL entry", grantee, effective, explicit)
			}
			if err := f.verify(t); err == nil {
				t.Errorf("RED: the verifier admitted effective MAINTAIN held by %q (server %d)", grantee, version)
			} else if !strings.Contains(err.Error(), grantee) {
				t.Errorf("the verifier refused, but not for %q: %v", grantee, err)
			} else {
				t.Logf("verifier refused: %v", err)
			}
			reopened, err := openLoginCapabilityPGTopology(f.pg, tc.split)
			if err == nil {
				_ = reopened.Close()
				t.Errorf("RED: the per-boot path admitted effective MAINTAIN held by %q (server %d)", grantee, version)
			} else if strings.Contains(err.Error(), "core v13 login capability") {
				t.Logf("per-boot refused by v13: %v", err)
			} else {
				t.Logf("per-boot refused by an earlier guard: %v", err)
			}
		})
	}
}
