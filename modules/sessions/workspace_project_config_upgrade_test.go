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

// Replay the registration from before these nullable fields existed. The
// engine's additive extension reconciler owns their migration on both engines.
type beforeProjectConfigRegistry struct{ store.ExtensionRegistry }

func (r beforeProjectConfigRegistry) Register(d model.EntityDescriptor) error {
	if d.Kind == workspaceKind {
		fields := make([]model.FieldSpec, 0, len(d.Fields))
		for _, f := range d.Fields {
			switch f.Name {
			case colWsProjectConfigHash, colWsProjectConfigActor, colWsProjectConfigAt:
			default:
				fields = append(fields, f)
			}
		}
		d.Fields = fields
	}
	return r.ExtensionRegistry.Register(d)
}

func TestProjectConfigApprovalUpgradeSQLite(t *testing.T) {
	exerciseProjectConfigUpgrade(t, store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "upgrade.db"), Debug: true})
}

func TestProjectConfigApprovalUpgradePostgres(t *testing.T) {
	if !enginetest.PostgresAvailable(t) {
		t.Skip("PostgreSQL split-owner fixture unavailable")
	}
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	exerciseProjectConfigUpgrade(t, store.Config{Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, Debug: true})
}

// A folder registered before the approval columns existed opens after the
// upgrade with no approval, takes one, and keeps it across a restart.
func exerciseProjectConfigUpgrade(t *testing.T, cfg store.Config) {
	t.Helper()
	ctx := t.Context()
	m := New()
	st, err := engine.Open(ctx, cfg, func(reg store.ExtensionRegistry) error { return m.RegisterSchema(beforeProjectConfigRegistry{reg}) })
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
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Project config upgrade", Slug: "project-config-upgrade", Status: model.StatusActive})
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
	rec, err := m.loadWorkspaceRec(ctx, tenant, folder.WorkspaceRef)
	if err != nil || storedProjectConfigApproval(rec) != nil {
		t.Fatalf("upgraded folder approval = %+v err = %v", storedProjectConfigApproval(rec), err)
	}
	hash := sha256Hex([]byte("reviewed"))
	approved, err := m.approveProjectConfig(ctx, tenant, folder.WorkspaceRef, hash, "user:upgrade", "user")
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
	rec, err = m.loadWorkspaceRec(ctx, tenant, folder.WorkspaceRef)
	got := storedProjectConfigApproval(rec)
	if err != nil || got == nil || *got != approved || !got.covers(hash) {
		t.Fatalf("reopened approval = %+v want %+v err = %v", got, approved, err)
	}
}
