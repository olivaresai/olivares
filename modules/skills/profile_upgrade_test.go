// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"maps"
	"path/filepath"
	"testing"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/skills"
)

type beforeProfileTargetsRegistry struct{ store.ExtensionRegistry }

func (r beforeProfileTargetsRegistry) Register(d model.EntityDescriptor) error {
	if d.Kind == skills.AssignmentKind {
		for i := range d.Fields {
			if d.Fields[i].Name == "target_id" {
				d.Fields[i].Kind = model.KindUUID
			}
		}
	}
	return r.ExtensionRegistry.Register(d)
}

// Both engines already store UUIDs as TEXT. Reopening with a public-reference
// target column preserves every old pin and permits the new profile reference.
func TestSkillsProfileTargetUpgradePreservesExistingPins(t *testing.T) {
	cfg := store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "skills.db"), Debug: true}
	if enginetest.PostgresAvailable(t) {
		pg := enginetest.IsolatedPostgresSplitOwner(t)
		cfg.Engine, cfg.DSN, cfg.OwnerDSN = store.EnginePostgres, pg.App, pg.Owner
	}
	module := skills.New(skills.Options{})
	old, err := engine.Open(t.Context(), cfg, func(reg store.ExtensionRegistry) error {
		return module.RegisterSchema(beforeProfileTargetsRegistry{reg})
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Close() })
	tenant := model.TenantID("")
	if err := old.System(t.Context(), func(sc store.SystemScope) error {
		if _, err := sc.EnsureSystemTenant(t.Context()); err != nil {
			return err
		}
		org, err := sc.CreateOrg(t.Context(), model.Org{Name: "Profile upgrade", Slug: "profile-upgrade", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var pin model.Record
	if err := old.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(skills.AssignmentKind)
		if err != nil {
			return err
		}
		pin, err = repo.Create(t.Context(), model.Record{"target_kind": "workspace", "target_id": model.NewID().String(), "target_version": int64(1), "pack_id": model.NewID().String(), "revision_id": model.NewID().String(), "members": `["research"]`, "created_by": "system"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := engine.Open(t.Context(), cfg, module.RegisterSchema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })
	if err := upgraded.Mutate(t.Context(), tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(skills.AssignmentKind)
		if err != nil {
			return err
		}
		got, err := repo.Get(t.Context(), model.ID(pin.String(model.ColID)))
		if err != nil {
			return err
		}
		for key, want := range pin {
			if got[key] != want {
				t.Fatalf("legacy pin changed at %s: %v != %v", key, got[key], want)
			}
		}
		ref := "ppf_" + model.NewID().String()
		row := maps.Clone(pin)
		for _, key := range []string{model.ColID, model.ColTenantID, model.ColCreatedAt, model.ColUpdatedAt, model.ColVersion} {
			delete(row, key)
		}
		row["target_kind"], row["target_id"] = "profile", ref
		fresh, err := repo.Create(t.Context(), row)
		if err != nil {
			return err
		}
		if fresh.String("target_id") != ref {
			t.Fatalf("profile ref was rewritten: %v", fresh)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
