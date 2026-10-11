// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

func TestPostgresRestoreClosureReestablishesCustodyACL(t *testing.T) {
	for _, split := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "split"}[split], func(t *testing.T) {
			ctx := context.Background()
			var pg pgtest.DSNs
			if split {
				pg = isolatedPGSplit(t)
			} else {
				pg = isolatedPG(t)
			}
			cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App, AdminDSN: pg.Admin, MaxConns: 2}
			if split {
				cfg.OwnerDSN = pg.Owner
			}
			st, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			owner := openCustodyPGPool(t, pg.Owner)
			app := openCustodyPGPool(t, pg.App)
			admin := openCustodyPGPool(t, pg.Admin)
			roles := guardRoles{
				App:             guardRoleFact{Known: true, Role: currentCustodyRole(t, app)},
				Owner:           guardRoleFact{Known: true, Role: currentCustodyRole(t, owner)},
				OwnerConfigured: split,
			}
			dia, _ := dialect.New(store.EnginePostgres)
			if split {
				// A no-privileges import inherits the target owner's default DML grants.
				for _, table := range dialect.FinOpsCustodyControlTables() {
					mustExec(t, owner, "GRANT SELECT, INSERT, UPDATE, DELETE ON public."+table+" TO "+quoteIdent(roles.App.Role))
				}
				if err := verifyFinOpsCustodyControlOwnerACL(ctx, owner, dia, roles); !errors.Is(err, store.ErrAppendOnlyACLOpen) {
					t.Fatalf("restored default DML must fail the unchanged custody guard: %v", err)
				}
			}
			for _, table := range dialect.FinOpsCustodyControlTables() {
				mustExec(t, owner, "GRANT SELECT ON public."+table+" TO PUBLIC")
			}
			closureCfg := cfg
			closureCfg.AdminDSN = "not-a-connection-string"
			if split {
				t.Run("rollback", func(t *testing.T) {
					injected := errors.New("restore commit refused")
					postgresRestoreAuthorityCommitTestHook = func(tx *sql.Tx) error {
						if err := verifyFinOpsCustodyControlOwnerACL(ctx, tx, dia, roles); err != nil {
							t.Errorf("custody ACL still open before commit: %v", err)
						}
						return injected
					}
					t.Cleanup(func() { postgresRestoreAuthorityCommitTestHook = nil })
					err := RestorePostgresUserAuthorityPrivileges(ctx, closureCfg)
					postgresRestoreAuthorityCommitTestHook = nil
					if !errors.Is(err, injected) {
						t.Fatalf("lost precommit failure: %v", err)
					}
					if err := verifyFinOpsCustodyControlOwnerACL(ctx, owner, dia, roles); !errors.Is(err, store.ErrAppendOnlyACLOpen) {
						t.Fatalf("failed closure committed custody grants: %v", err)
					}
				})
			}
			t.Run("malformed_guard", func(t *testing.T) {
				table := dialect.FinOpsCustodyControlTables()[0]
				var trigger string
				if err := owner.QueryRowContext(ctx, "SELECT tgname FROM pg_trigger WHERE tgrelid=$1::regclass AND NOT tgisinternal ORDER BY tgname LIMIT 1", "public."+table).Scan(&trigger); err != nil {
					t.Fatal(err)
				}
				mustExec(t, owner, "ALTER TABLE public."+table+" DISABLE TRIGGER "+quoteIdent(trigger))
				if err := RestorePostgresUserAuthorityPrivileges(ctx, closureCfg); err == nil {
					t.Fatal("restore accepted disabled custody triggers")
				}
				var publicRead bool
				if err := owner.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_class c, LATERAL aclexplode(c.relacl) a WHERE c.oid=$1::regclass AND a.grantee=0 AND a.privilege_type='SELECT')", "public."+table).Scan(&publicRead); err != nil || !publicRead {
					t.Fatalf("failed restore did not roll back PUBLIC revoke: read=%v err=%v", publicRead, err)
				}
				mustExec(t, owner, "ALTER TABLE public."+table+" ENABLE ALWAYS TRIGGER "+quoteIdent(trigger))
			})
			for i := 0; i < 2; i++ {
				if err := RestorePostgresUserAuthorityPrivileges(ctx, closureCfg); err != nil {
					t.Fatalf("closure attempt %d: %v", i, err)
				}
				if err := verifyFinOpsCustodyControlOwnerACL(ctx, owner, dia, roles); err != nil {
					t.Fatalf("restored custody boundary: %v", err)
				}
			}
			if split {
				for _, table := range dialect.FinOpsCustodyControlTables() {
					if _, err := app.ExecContext(ctx, "DELETE FROM public."+table+" WHERE false"); err == nil || !strings.Contains(err.Error(), "permission denied") {
						t.Fatalf("application DELETE on %s must be denied by ACL: %v", table, err)
					}
				}
			}
			for _, table := range dialect.FinOpsCustodyControlTables() {
				var count int
				if err := admin.QueryRowContext(ctx, "SELECT count(*) FROM public."+table).Scan(&count); err != nil {
					t.Fatalf("backup administrator cannot read %s: %v", table, err)
				}
			}
			st, err = Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("boot recovered estate: %v", err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
