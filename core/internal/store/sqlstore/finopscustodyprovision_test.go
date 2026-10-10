// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// Browser module selection re-execs quickstart with its original --postgres
// bootstrap reference. Provisioning an existing estate must preserve custody's
// SELECT-only application boundary before the boot verifier runs again.
func TestFinOpsCustodyPostgresReprovisionPreservesRestartAndData(t *testing.T) {
	for _, route := range []string{"executor", "rendered_sql"} {
		t.Run(route, func(t *testing.T) {
			ctx := context.Background()
			dsns := isolatedPGSplit(t)
			cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, OwnerDSN: dsns.Owner, MaxConns: 2}
			st, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			dia := st.(*sqlStore).dia
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			owner := openCustodyPGPool(t, dsns.Owner)
			app := openCustodyPGPool(t, dsns.App)
			appConfig, err := pgx.ParseConfig(dsns.App)
			if err != nil {
				t.Fatal(err)
			}
			ownerConfig, err := pgx.ParseConfig(dsns.Owner)
			if err != nil {
				t.Fatal(err)
			}
			roles := guardRoles{App: guardRoleFact{Known: true, Role: appConfig.User}, Owner: guardRoleFact{Known: true, Role: ownerConfig.User}, OwnerConfigured: true}
			enrollThrough(t, ctx, owner, dia, custodyInstanceA)
			if _, err := owner.ExecContext(ctx, "CREATE TABLE public.custody_reprovision_mutable (value INTEGER)"); err != nil {
				t.Fatal(err)
			}
			rows := func() string {
				var snapshot strings.Builder
				for _, table := range dialect.FinOpsCustodyControlTables() {
					var value string
					if err := owner.QueryRowContext(ctx, "SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text)::text, '[]') FROM public."+table+" r").Scan(&value); err != nil {
						t.Fatal(err)
					}
					snapshot.WriteString(value)
				}
				return snapshot.String()
			}
			before := rows()
			spec := store.PgProvisionSpec{Database: dsns.Database, SSLMode: "disable",
				App:   store.PgRole{Name: appConfig.User, Password: appConfig.Password},
				Owner: store.PgRole{Name: ownerConfig.User, Password: ownerConfig.Password}}
			if route == "executor" {
				if _, err := ProvisionPostgres(ctx, dsns.Superuser, spec, true); err != nil {
					t.Fatal(err)
				}
			} else {
				steps, err := RenderProvisionSQL(spec)
				if err != nil {
					t.Fatal(err)
				}
				var dml string
				for _, step := range steps {
					if strings.Contains(step.SQL, "GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES") {
						dml = step.SQL
					}
				}
				if dml == "" {
					t.Fatal("rendered provision recipe lost its DML transaction")
				}
				if _, err := owner.ExecContext(ctx, dml); err != nil {
					t.Fatal(err)
				}
			}
			if after := rows(); after != before {
				t.Fatal("reprovision changed custody data")
			}
			if _, err := app.ExecContext(ctx, "INSERT INTO public.custody_reprovision_mutable VALUES (1)"); err != nil {
				t.Fatalf("ordinary application DML lost: %v", err)
			}
			if err := verifyFinOpsCustodyControlOwnerACL(ctx, owner, dia, roles); err != nil {
				t.Fatalf("reprovision widened custody ACL before restart: %v", err)
			}
			reopened, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("restart after reprovision refused: %v", err)
			}
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
			if after := rows(); after != before {
				t.Fatal("restart changed enrolled custody data")
			}
			// Reprovisioning is explicit grant establishment. Ordinary boots still
			// refuse subsequent drift without repairing it or changing data.
			if _, err := owner.ExecContext(ctx, "GRANT UPDATE ON public.control_custody_enrollment TO "+quoteIdent(appConfig.User)); err != nil {
				t.Fatal(err)
			}
			drift := finOpsCustodyDurableCatalog(t, owner, store.EnginePostgres)
			bad, err := Open(ctx, cfg, nil)
			if bad != nil {
				_ = bad.Close()
			}
			if !errors.Is(err, store.ErrAppendOnlyACLOpen) {
				t.Fatalf("ordinary restart accepted ACL drift: %v", err)
			}
			if finOpsCustodyDurableCatalog(t, owner, store.EnginePostgres) != drift || rows() != before {
				t.Fatal("ordinary restart repaired ACL drift or changed custody data")
			}
		})
	}
}
