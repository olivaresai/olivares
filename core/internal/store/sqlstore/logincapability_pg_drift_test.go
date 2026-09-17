// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/store"
)

func openLoginCapabilityPGAt(pg pgtest.DSNs) (store.Store, error) {
	return Open(context.Background(), store.Config{
		Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin, MaxConns: 4,
	}, nil)
}

func loginCapabilitySuperuserOnDatabase(t *testing.T, pg pgtest.DSNs) string {
	t.Helper()
	u, err := url.Parse(pg.Superuser)
	if err != nil {
		t.Fatalf("parse the superuser DSN: %v", err)
	}
	u.Path = "/" + pg.Database
	return u.String()
}

// TestPGLoginCapabilityVerifierRefusesDriftAtVerifierAndBoot is the correction-1 red/green
// control set for Root's INITIAL-FINDINGS: whole CHECK semantics, PUBLIC column ACLs, exact
// ownership and the other-operational-role boundary. Every drift must be refused both by the
// verifier itself and by the actual per-boot path of a real Open on the same database.
func TestPGLoginCapabilityVerifierRefusesDriftAtVerifierAndBoot(t *testing.T) {
	if !pgtest.Available(t) {
		t.Skipf("set %s (a superuser DSN) to run the login capability drift controls", pgtest.EnvSuperuserDSN)
	}
	const rel = "public.login_capability_observation"
	drifts := []struct {
		name  string
		stmts func(app, owner, admin, unique string) []string
	}{
		{"count check weakened to non-negative", func(_, _, _, _ string) []string {
			return []string{
				"ALTER TABLE " + rel + " DROP CONSTRAINT login_capability_observation_count_check",
				"ALTER TABLE " + rel + " ADD CONSTRAINT login_capability_observation_count_check CHECK (observation_count >= 0)",
			}
		}},
		{"key check widened with OR true", func(_, _, _, _ string) []string {
			return []string{
				"ALTER TABLE " + rel + " DROP CONSTRAINT login_capability_observation_key_check",
				"ALTER TABLE " + rel + " ADD CONSTRAINT login_capability_observation_key_check CHECK (capability_key = 'global/default/login-enforcement' OR true)",
			}
		}},
		{"PUBLIC column SELECT", func(_, _, _, _ string) []string {
			return []string{"GRANT SELECT (first_observed_at) ON " + rel + " TO PUBLIC"}
		}},
		{"relation owned by a third role", func(_, _, _, unique string) []string {
			return []string{
				"CREATE ROLE " + quoteIdent("r105_owner_"+unique) + " NOLOGIN",
				"ALTER TABLE " + rel + " OWNER TO " + quoteIdent("r105_owner_"+unique),
			}
		}},
		// The admin read pool's PLAIN SELECT is the posture `olivares db init` provisions and
		// the one `olivares dr backup`'s pg_dump needs; it is asserted as a GREEN witness by
		// TestPGLoginCapabilityAdminReadPoolCanDumpInBothPostures. What must still be refused
		// is that role holding anything MORE than the read — a write, or the authority to hand
		// the read on. Both directions are drifts here, so admitting the read never quietly
		// admitted a second writer.
		{"operational admin role INSERT", func(_, _, admin, _ string) []string {
			return []string{"GRANT INSERT ON " + rel + " TO " + quoteIdent(admin)}
		}},
		{"operational admin role DELETE", func(_, _, admin, _ string) []string {
			return []string{"GRANT DELETE ON " + rel + " TO " + quoteIdent(admin)}
		}},
		{"operational admin role column UPDATE", func(_, _, admin, _ string) []string {
			return []string{"GRANT UPDATE (observation_count) ON " + rel + " TO " + quoteIdent(admin)}
		}},
		{"operational admin role SELECT WITH GRANT OPTION", func(_, _, admin, _ string) []string {
			return []string{"GRANT SELECT ON " + rel + " TO " + quoteIdent(admin) + " WITH GRANT OPTION"}
		}},
		{"role inheriting the application role", func(app, _, _, unique string) []string {
			return []string{
				"CREATE ROLE " + quoteIdent("r105_member_"+unique) + " NOLOGIN INHERIT",
				"GRANT " + quoteIdent(app) + " TO " + quoteIdent("r105_member_"+unique),
			}
		}},
	}
	for _, drift := range drifts {
		t.Run(drift.name, func(t *testing.T) {
			ctx := context.Background()
			pg := isolatedPGSplit(t)
			st, err := openLoginCapabilityPGAt(pg)
			if err != nil {
				t.Fatalf("open the split store: %v", err)
			}
			observeLoginCapability(t, st, "artifact")
			if err := st.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			owner := openCustodyPGPool(t, pg.Owner)
			app := openCustodyPGPool(t, pg.App)
			admin := openCustodyPGPool(t, pg.Admin)
			su := openCustodyPGPool(t, loginCapabilitySuperuserOnDatabase(t, pg))
			roles := guardRoles{
				App:             guardRoleFact{Known: true, Role: currentCustodyRole(t, app)},
				Owner:           guardRoleFact{Known: true, Role: currentCustodyRole(t, owner)},
				OwnerConfigured: true,
			}
			verify := func() error {
				tx, err := owner.BeginTx(ctx, nil)
				if err != nil {
					t.Fatalf("begin: %v", err)
				}
				defer tx.Rollback() //nolint:errcheck
				return verifyPostgresLoginCapabilityRelation(ctx, tx, roles)
			}
			if err := verify(); err != nil {
				t.Fatalf("green witness: the migrated relation failed its verifier before drift: %v", err)
			}
			unique := fmt.Sprintf("%d", time.Now().UnixNano())
			// Roles are CLUSTER-GLOBAL: a leftover member of the application role makes every
			// later database's pre-existing authority ACL guard refuse (measured in this
			// correction). Undo the global part of each drift before the pools close.
			t.Cleanup(func() {
				for _, stmt := range []string{
					"ALTER TABLE " + rel + " OWNER TO " + quoteIdent(roles.Owner.Role),
					"DROP ROLE IF EXISTS " + quoteIdent("r105_owner_"+unique),
					"DROP ROLE IF EXISTS " + quoteIdent("r105_member_"+unique),
				} {
					if _, err := su.ExecContext(context.Background(), stmt); err != nil {
						t.Errorf("drift cleanup %q: %v", stmt, err)
					}
				}
			})
			for _, stmt := range drift.stmts(roles.App.Role, roles.Owner.Role, currentCustodyRole(t, admin), unique) {
				if _, err := su.ExecContext(ctx, stmt); err != nil {
					t.Fatalf("apply drift %q: %v", stmt, err)
				}
			}
			if err := verify(); err == nil {
				t.Errorf("RED: the verifier admitted drift %q", drift.name)
			} else {
				t.Logf("verifier refused: %v", err)
			}
			reopened, err := openLoginCapabilityPGAt(pg)
			if err == nil {
				_ = reopened.Close()
				t.Errorf("RED: the per-boot path admitted drift %q", drift.name)
			} else if !strings.Contains(err.Error(), "core v13 login capability") {
				// Measured in the red run: an earlier pre-existing boot guard (the owner
				// effective-privilege preflight, the authority-function ACL check) already
				// refuses these two drifts before v13 is reached. The boot still refuses.
				if drift.name == "relation owned by a third role" || drift.name == "role inheriting the application role" {
					t.Logf("per-boot refused by an earlier pre-existing guard: %v", err)
				} else {
					t.Errorf("the per-boot refusal is not the v13 verifier's: %v", err)
				}
			} else {
				t.Logf("per-boot refused: %v", err)
			}
		})
	}
}
