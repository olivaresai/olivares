// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Replay the registration before these nullable fields existed. The engine's
// additive extension reconciler owns their migration on both database engines.
type beforeWorkspaceSetupRegistry struct{ store.ExtensionRegistry }

func (r beforeWorkspaceSetupRegistry) Register(d model.EntityDescriptor) error {
	if d.Kind == workspaceKind {
		fields := make([]model.FieldSpec, 0, len(d.Fields))
		for _, f := range d.Fields {
			switch f.Name {
			case colWsSetupCommand, colWsSetupHash, colWsSetupActor, colWsSetupAt:
			default:
				fields = append(fields, f)
			}
		}
		d.Fields = fields
	}
	return r.ExtensionRegistry.Register(d)
}

func TestWorkspaceSetupUpgradeSQLite(t *testing.T) {
	exerciseWorkspaceSetupUpgrade(t, store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "upgrade.db"), Debug: true})
}
func TestWorkspaceSetupUpgradePostgres(t *testing.T) {
	if !enginetest.PostgresAvailable(t) {
		t.Skip("PostgreSQL split-owner fixture unavailable")
	}
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	exerciseWorkspaceSetupUpgrade(t, store.Config{Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, Debug: true})
}
func exerciseWorkspaceSetupUpgrade(t *testing.T, cfg store.Config) {
	t.Helper()
	ctx := t.Context()
	m := New()
	st, err := engine.Open(ctx, cfg, func(reg store.ExtensionRegistry) error { return m.RegisterSchema(beforeWorkspaceSetupRegistry{reg}) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if st != nil {
			_ = st.Close()
		}
	})
	var tenant model.TenantID
	if err := st.System(ctx, func(sys store.SystemScope) error {
		if _, err := sys.EnsureSystemTenant(ctx); err != nil {
			return err
		}
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Setup upgrade", Slug: "setup-upgrade", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))
	folder, err := m.createWorkspace(ctx, tenant, CreateWorkspaceParams{RootPath: t.TempDir(), Actor: "user:upgrade", ActorKind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	m = New()
	st, err = engine.Open(ctx, cfg, m.RegisterSchema)
	if err != nil {
		t.Fatalf("additive upgrade: %v", err)
	}
	m.UseData(api.NewModuleData(st))
	got, err := m.getWorkspace(ctx, tenant, folder.WorkspaceRef)
	if err != nil || got.RootPath != folder.RootPath || got.SetupCommand != "" || got.SetupApproval != nil {
		t.Fatalf("upgraded folder=%+v err=%v", got, err)
	}
	command := "npm ci "
	if _, err = m.configureWorkspaceSetup(ctx, tenant, folder.WorkspaceRef, &command, "", "user:upgrade", "user"); err != nil {
		t.Fatal(err)
	}
	hash := sha256Hex([]byte(command))
	approved, err := m.configureWorkspaceSetup(ctx, tenant, folder.WorkspaceRef, nil, hash, "user:upgrade", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	m = New()
	st, err = engine.Open(ctx, cfg, m.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	m.UseData(api.NewModuleData(st))
	got, err = m.getWorkspace(ctx, tenant, folder.WorkspaceRef)
	if err != nil || got.SetupCommand != command || got.SetupApproval == nil || *got.SetupApproval != *approved.SetupApproval {
		t.Fatalf("reopened approval=%+v err=%v", got, err)
	}
}
