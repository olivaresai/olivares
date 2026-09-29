// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The old module set is identical except for the new descriptor. No core
// migration is removed or rewritten to simulate this additive module upgrade.
type withoutStopGeneration struct{ store.ExtensionRegistry }

func (r withoutStopGeneration) Register(d model.EntityDescriptor) error {
	if d.Kind == killSwitchGenerationKind {
		return nil
	}
	return r.ExtensionRegistry.Register(d)
}

func TestKillSwitchGenerationAdditiveModuleUpgrade(t *testing.T) {
	for _, eng := range []store.Engine{store.EngineSQLite, store.EnginePostgres} {
		t.Run(string(eng), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			cfg := store.Config{Engine: eng, DSN: filepath.Join(t.TempDir(), "upgrade.db"), Debug: true}
			if eng == store.EnginePostgres {
				pg := enginetest.IsolatedPostgresSplitOwner(t)
				cfg = store.Config{Engine: eng, DSN: pg.App, OwnerDSN: pg.Owner, AdminDSN: pg.Admin, Debug: true}
			}
			m := New()
			old, err := engine.Open(ctx, cfg, func(reg store.ExtensionRegistry) error { return m.RegisterSchema(withoutStopGeneration{reg}) })
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = old.Close() })
			var tenant model.TenantID
			if err := old.System(ctx, func(sys store.SystemScope) error {
				if _, err := sys.EnsureSystemTenant(ctx); err != nil {
					return err
				}
				org, err := sys.CreateOrg(ctx, model.Org{Name: "upgrade", Slug: "upgrade", Status: model.StatusActive})
				tenant = model.TenantID(org.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := old.View(ctx, tenant, func(sc store.Scope) error {
				if _, err := sc.Ext(killSwitchGenerationKind); err == nil {
					t.Fatal("old fixture registered new descriptor")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}
			upgraded, err := engine.Open(ctx, cfg, m.RegisterSchema)
			if err != nil {
				t.Fatalf("additive reopen: %v", err)
			}
			t.Cleanup(func() { _ = upgraded.Close() })
			if got := stopSnapshot(t, upgraded, m, tenant); got.Generation() != 1 || got.State().Any() {
				t.Fatalf("upgraded domain = %+v", got)
			}
			if err := upgraded.Mutate(ctx, tenant, func(sc store.Scope) error {
				repo, err := sc.Ext(killSwitchGenerationKind)
				if err != nil {
					return err
				}
				// The actual UNIQUE tenant index must reject a second row, regardless of id.
				_, err = repo.Create(ctx, model.Record{colKSGeneration: int64(7)})
				return err
			}); err == nil {
				t.Fatal("singleton index admitted second tenant row")
			}
			if got := stopSnapshot(t, upgraded, m, tenant); got.Generation() != 1 {
				t.Fatal("rejected duplicate changed singleton")
			}
		})
	}
}
