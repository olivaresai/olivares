// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type withoutSessionPermissionVersion struct{ store.ExtensionRegistry }

func (r withoutSessionPermissionVersion) Register(d model.EntityDescriptor) error {
	fields := make([]model.FieldSpec, 0, len(d.Fields))
	for _, field := range d.Fields {
		if field.Name != colRBACSessionExact {
			fields = append(fields, field)
		}
	}
	d.Fields = fields
	return r.ExtensionRegistry.Register(d)
}

func TestSessionPermissionVersionAdditiveUpgrade(t *testing.T) {
	for _, kind := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(kind), func(t *testing.T) {
			cfg := store.Config{Engine: kind, DSN: filepath.Join(t.TempDir(), "upgrade.db"), Debug: true}
			if kind == store.EnginePostgres {
				pg := enginetest.IsolatedPostgresSplitOwner(t)
				cfg = store.Config{Engine: kind, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin, Debug: true}
			}
			m := New()
			old, err := engine.Open(t.Context(), cfg, func(reg store.ExtensionRegistry) error { return m.RegisterSchema(withoutSessionPermissionVersion{reg}) })
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = old.Close() })
			var tenant model.TenantID
			if err := old.System(t.Context(), func(sys store.SystemScope) error {
				if _, err := sys.EnsureSystemTenant(t.Context()); err != nil {
					return err
				}
				org, err := sys.CreateOrg(t.Context(), model.Org{Name: "upgrade", Slug: "upgrade", Status: model.StatusActive})
				tenant = org.TenantID
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := old.Mutate(t.Context(), tenant, func(sc store.Scope) error {
				for _, kind := range []model.Kind{customRoleKind, permGroupKind} {
					repo, err := sc.Ext(kind)
					if err != nil {
						return err
					}
					_, err = repo.Create(t.Context(), model.Record{colRBACName: "old", colRBACPerms: `["sessions:run:read"]`, colRBACCreatedBy: "test"})
					if err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}
			upgraded, err := engine.Open(t.Context(), cfg, m.RegisterSchema)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = upgraded.Close() })
			if err := upgraded.Mutate(t.Context(), tenant, func(sc store.Scope) error {
				for _, kind := range []model.Kind{customRoleKind, permGroupKind} {
					repo, err := sc.Ext(kind)
					if err != nil {
						return err
					}
					rows, err := listAll(t.Context(), repo)
					if err != nil {
						return err
					}
					if len(rows) != 1 {
						t.Fatalf("%s rows=%d", kind, len(rows))
					}
					if got := storedSessionPermissions(rows[0], colRBACPerms); len(got) != 2 {
						t.Fatalf("%s lost stored coverage: %v", kind, got)
					}
					rows[0][colRBACSessionExact] = true
					updated, err := repo.Update(t.Context(), rows[0])
					if err != nil {
						return err
					}
					if got := storedSessionPermissions(updated, colRBACPerms); len(got) != 1 {
						t.Fatalf("%s exact list widened: %v", kind, got)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			t.Log("stored role/group survived additive schema reopen; null preserves old coverage, true uses exact lists")
		})
	}
}
