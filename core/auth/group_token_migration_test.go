// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/pgtest"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestGroupTokenBackfillOnUpgrade(t *testing.T) {
	for _, engine := range store.SupportedEngines() {
		t.Run(string(engine), func(t *testing.T) {
			ctx := context.Background()
			cfg := store.Config{Engine: engine, DSN: filepath.Join(t.TempDir(), "upgrade.db"), Debug: true}
			if engine == store.EnginePostgres {
				dsns := pgtest.Isolate(t, sqlstore.ProvisionPostgres, pgtest.SplitOwner)
				cfg.DSN, cfg.OwnerDSN, cfg.AdminDSN = dsns.App, dsns.Owner, dsns.Admin
			}
			st, err := sqlstore.Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			a := auth.NewAuthenticator(st, nil)
			super := mustSuperadmin(t, ctx, a)
			tenant := provisionTenant(t, st, "acme")
			uid, _ := mustMember(t, ctx, a, super, tenant, "group@acme.test", auth.RoleViewer)
			directID, direct := mustMember(t, ctx, a, super, tenant, "direct@acme.test", auth.RoleAdmin)
			group, err := a.SCIMCreateGroup(ctx, super, tenant, auth.SCIMGroupInput{
				DisplayName: "Admins", ExternalID: "admins", Members: []model.ID{uid, directID},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.ConfigureGroupRole(ctx, super, tenant, group.Group.ID, auth.RoleAdmin); err != nil {
				t.Fatal(err)
			}
			groupToken, groupRow, err := a.IssueToken(ctx, loginPrincipal(t, ctx, a, "group@acme.test"), auth.TokenSpec{
				Name: "legacy-group", BoundTenant: tenant, Role: auth.RoleAdmin,
			})
			if err != nil {
				t.Fatal(err)
			}
			lowerToken, lowerRow, err := a.IssueToken(ctx, loginPrincipal(t, ctx, a, "group@acme.test"), auth.TokenSpec{
				Name: "legacy-lower-role", BoundTenant: tenant, Role: auth.RoleEditor,
			})
			if err != nil {
				t.Fatal(err)
			}
			directToken, directRow, err := a.IssueToken(ctx, direct, auth.TokenSpec{
				Name: "legacy-direct", BoundTenant: tenant, Role: auth.RoleAdmin,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}

			driver, dsn := "sqlite", cfg.DSN
			if engine == store.EnginePostgres {
				driver, dsn = "pgx", cfg.OwnerDSN
			}
			db, err := sql.Open(driver, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, stmt := range []string{
				"ALTER TABLE api_tokens DROP COLUMN group_derived_role",
				"DELETE FROM schema_migrations_core WHERE version=29",
			} {
				if _, err := db.ExecContext(ctx, stmt); err != nil {
					t.Fatalf("%s: %v", stmt, err)
				}
			}

			reopened, err := sqlstore.Open(ctx, cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			a = auth.NewAuthenticator(reopened, nil)
			gotGroup, err := a.GetToken(ctx, groupRow.ID)
			if err != nil || !gotGroup.GroupDerivedRole {
				t.Fatalf("legacy group token provenance = %v, %v, want derived", gotGroup.GroupDerivedRole, err)
			}
			gotLower, err := a.GetToken(ctx, lowerRow.ID)
			if err != nil || !gotLower.GroupDerivedRole {
				t.Fatalf("legacy lower-role token provenance = %v, %v, want derived", gotLower.GroupDerivedRole, err)
			}
			gotDirect, err := a.GetToken(ctx, directRow.ID)
			if err != nil || gotDirect.GroupDerivedRole {
				t.Fatalf("legacy direct token provenance = %v, %v, want direct", gotDirect.GroupDerivedRole, err)
			}
			if err := a.SCIMDeleteGroup(ctx, super, tenant, group.Group.ID); err != nil {
				t.Fatal(err)
			}
			principal, err := a.Authenticate(ctx, groupToken)
			if err != nil {
				t.Fatal(err)
			}
			if role, ok := principal.RoleIn(tenant); !ok || role != auth.RoleViewer {
				t.Fatalf("upgraded group token role after group deletion = %q, %v, want viewer", role, ok)
			}
			principal, err = a.Authenticate(ctx, lowerToken)
			if err != nil {
				t.Fatal(err)
			}
			if role, ok := principal.RoleIn(tenant); !ok || role != auth.RoleViewer {
				t.Fatalf("upgraded lower-role token after group deletion = %q, %v, want viewer", role, ok)
			}
			principal, err = a.Authenticate(ctx, directToken)
			if err != nil {
				t.Fatal(err)
			}
			if role, ok := principal.RoleIn(tenant); !ok || role != auth.RoleAdmin {
				t.Fatalf("upgraded direct token role after group deletion = %q, %v, want admin", role, ok)
			}
		})
	}
}
