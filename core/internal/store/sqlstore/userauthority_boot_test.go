// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestUserAuthorityFreshBootstrap(t *testing.T) {
	for _, profile := range []struct {
		name    string
		engine  store.Engine
		noAdmin bool
	}{{"sqlite", store.EngineSQLite, false}, {"postgres_admin", store.EnginePostgres, false}, {"postgres_no_admin", store.EnginePostgres, true}} {
		engine := profile.engine
		t.Run(profile.name, func(t *testing.T) {
			ctx := context.Background()
			cfg := store.Config{Engine: engine, DSN: filepath.Join(t.TempDir(), "user-authority.db")}
			var superDSN, database string
			if engine == store.EnginePostgres {
				pg := isolatedPGSplit(t)
				cfg.DSN, cfg.OwnerDSN, cfg.AdminDSN = pg.App, pg.Owner, pg.Admin
				superDSN, database = pg.Superuser, pg.Database
				if profile.noAdmin {
					cfg.AdminDSN = ""
				}
			}
			raw, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("fresh Open: %v", err)
			}
			defer raw.Close()
			s := raw.(*sqlStore)
			// The fresh first boot is exact about what it could prove. SQLite and the
			// AdminDSN attest an empty inventory awaiting SYSTEM genesis; without an
			// AdminDSN and before the DBA installs the closed routine the inventory is
			// unknown, which is a different fact from an attested empty estate and is
			// named as such, with no authority and no counts.
			firstBoot := store.DirectoryStatus{
				ControlMode: store.DirectoryControlStaged, WriterPosture: store.DirectoryWriterSQLiteCapability, ExpectedGeneration: 1,
				CoverageProtocol: coverageProtocolLegacy, InventoryAuthority: "sqlite", InventoryUnavailableReason: "system_bootstrap_pending",
			}
			if engine == store.EnginePostgres {
				firstBoot.WriterPosture, firstBoot.InventoryAuthority = store.DirectoryWriterSplitOwner, "admin_dsn"
				if profile.noAdmin {
					firstBoot.InventoryAuthority, firstBoot.InventoryUnavailableReason = "", "closed_routine_missing"
				}
			}
			directoryEpochTestWantStatus(t, raw, firstBoot)
			if err := raw.System(ctx, func(sys store.SystemScope) error { _, err := sys.EnsureSystemTenant(ctx); return err }); err != nil {
				t.Fatal(err)
			}
			var user model.User
			if err := raw.AuthMutate(ctx, func(as store.AuthScope) error {
				var err error
				user, err = as.Users().Create(ctx, model.User{Email: "f2a@example.test", Status: model.StatusActive})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := raw.AuthMutate(ctx, func(as store.AuthScope) error {
				user.DisplayName = "updated"
				_, err := as.Users().Update(ctx, user)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if engine == store.EnginePostgres {
				spec := store.PgProvisionSpec{Database: database, App: store.PgRole{Name: s.directoryGuardRoles.App.Role}, Owner: store.PgRole{Name: s.directoryGuardRoles.Owner.Role}, InstallDirectoryInventory: true}
				installed, err := ProvisionPostgres(ctx, superDSN, spec, true)
				if err != nil || !installed.DirectoryInventoryInstalled {
					t.Fatalf("install command seam: installed=%t err=%v", installed.DirectoryInventoryInstalled, err)
				}

				inv, err := readDirectoryInventory(ctx, s.db, s.dia, true, false)
				if err != nil || inv.System.ID != model.ID(model.SystemTenantID) {
					t.Fatalf("closed inventory SYSTEM: %+v err=%v", inv, err)
				}
			}
			tenantA := provisionTenant(t, raw, "f2a-first")
			tenantB := provisionTenant(t, raw, "f2a-second")
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}
			before, after, changed, err := OpenDirectoryWriterMaintenance(ctx, cfg, nil, 1)
			if err != nil {
				t.Fatalf("maintenance activation: %v", err)
			}
			if !changed || before.CoverageProtocol != coverageProtocolLegacy || after.CoverageProtocol != coverageProtocolTarget || after.ExpectedGeneration != 2 || !after.UserAuthorityCoverageComplete || after.InventoryOrgCount != 3 || after.InventoryBusinessOrgCount != 2 || after.InventoryEpochCount != 2 {
				t.Fatalf("transition before=%+v after=%+v changed=%t", before, after, changed)
			}
			reopened, err := Open(ctx, cfg, nil)
			if err != nil {
				t.Fatalf("target reopen: %v", err)
			}
			if h := F2AUserAuthorityVersionForTest(t, reopened, user.ID); h != 2 {
				t.Fatalf("maintenance reset retained H=%d want2", h)
			}
			status, _, err := reopened.(store.DirectoryStatuser).DirectoryStatus(ctx)
			if err != nil || status != after {
				t.Fatalf("fresh status=%+v want=%+v err=%v", status, after, err)
			}
			for _, tenant := range []model.TenantID{tenantA, tenantB} {
				if err := reopened.View(ctx, tenant, func(sc store.Scope) error {
					epoch, err := sc.(store.DirectorySnapshotReader).ReadDirectoryEpoch(ctx)
					if err == nil && epoch.Version != 2 {
						t.Errorf("activation G=%d want2", epoch.Version)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
			_, retry, changed, err := OpenDirectoryWriterMaintenance(ctx, cfg, nil, 1)
			if err != nil || changed || retry != after {
				t.Fatalf("exact retry=%+v changed=%t err=%v", retry, changed, err)
			}

		})
	}
}
