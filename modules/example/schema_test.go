// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package example_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/example"
	"github.com/olivaresai/olivares/sdk"
)

// The reference module declares a table but does not write observations to it.
// Here the composition root writes one fixture through the real scoped store.
func TestExampleSchemaRetainsScopedDataAcrossRestart(t *testing.T) {
	ctx := t.Context()
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "example.db")}
	var owner, other model.TenantID
	var id model.ID
	for _, dormant := range []bool{false, true, false} {
		rt := runtime.New(runtime.Options{Logger: quiet()})
		mod := example.New()
		var err error
		if dormant {
			err = rt.AddDormantModule(mod, sdk.Config{})
		} else {
			err = rt.AddModule(mod, sdk.Config{})
		}
		if err != nil {
			t.Fatal(err)
		}
		st, err := engine.Open(ctx, cfg, rt.RegisterSchema)
		if err != nil {
			t.Fatalf("open (dormant=%t): %v", dormant, err)
		}
		// Also close on an assertion failure; close before opening the next store.
		t.Cleanup(func() { _ = st.Close() })
		if id == "" {
			if err := st.System(ctx, func(sys store.SystemScope) error {
				if _, err := sys.EnsureSystemTenant(ctx); err != nil {
					return err
				}
				for _, slug := range []string{"example-owner", "example-other"} {
					org, err := sys.CreateOrg(ctx, model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
					if err != nil {
						return err
					}
					if slug == "example-owner" {
						owner = org.TenantID
					} else {
						other = org.TenantID
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := st.Mutate(ctx, owner, func(sc store.Scope) error {
				repo, err := sc.Ext("example.observation")
				if err != nil {
					return err
				}
				row, err := repo.Create(ctx, model.Record{"resource": "public.orders", "mode": "R"})
				if err == nil {
					id = model.ID(row["id"].(string))
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		if err := st.View(ctx, owner, func(sc store.Scope) error {
			repo, err := sc.Ext("example.observation")
			if err != nil {
				return err
			}
			row, err := repo.Get(ctx, id)
			if err != nil {
				return err
			}
			if row["resource"] != "public.orders" || row["mode"] != "R" {
				t.Fatalf("retained row (dormant=%t): %v", dormant, row)
			}
			_, err = repo.Create(ctx, model.Record{"resource": "refused", "mode": "R"})
			return err
		}); !errors.Is(err, store.ErrReadOnly) {
			t.Fatalf("read-only create (dormant=%t) = %v, want ErrReadOnly", dormant, err)
		}
		if err := st.View(ctx, other, func(sc store.Scope) error {
			repo, err := sc.Ext("example.observation")
			if err != nil {
				return err
			}
			_, err = repo.Get(ctx, id)
			return err
		}); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("other-tenant read (dormant=%t) = %v, want ErrNotFound", dormant, err)
		}
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
