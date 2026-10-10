// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/skills"
)

// Reproduce the deployed catalog descriptors, before directory lineage existed.
type legacySkillsCatalogSchema struct{ store.ExtensionRegistry }

func (r legacySkillsCatalogSchema) Register(d model.EntityDescriptor) error {
	if d.Kind == skills.PackKind || d.Kind == skills.RevisionKind {
		d.Fields = slices.DeleteFunc(slices.Clone(d.Fields), func(f model.FieldSpec) bool { return f.Name == "workspace_id" })
		d.WorkspaceLineage = model.WorkspaceLineageSpec{}
		d.WorkspaceConfinedReadOnly = false
	}
	return r.ExtensionRegistry.Register(d)
}

func TestSkillsCatalogDirectoryUpgradePreservesRowsAndConfinedReadOnly(t *testing.T) {
	engines := []store.Engine{store.EngineSQLite}
	if enginetest.PostgresAvailable(t) {
		engines = append(engines, store.EnginePostgres)
	}
	for _, backend := range engines {
		t.Run(string(backend), func(t *testing.T) {
			ctx := context.Background()
			cfg := store.Config{Engine: backend, DSN: filepath.Join(t.TempDir(), "upgrade.db")}
			if backend == store.EnginePostgres {
				pg := enginetest.IsolatedPostgresSplitOwner(t)
				cfg.DSN, cfg.OwnerDSN = pg.App, pg.Owner
				t.Log("catalog upgrade uses the PostgreSQL application role, with a separate migration owner")
			}
			module := skills.New(skills.Options{})
			old, err := engine.Open(ctx, cfg, func(reg store.ExtensionRegistry) error { return module.RegisterSchema(legacySkillsCatalogSchema{reg}) })
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = old.Close() })
			var tenant model.TenantID
			if err := old.System(ctx, func(sys store.SystemScope) error {
				if _, err := sys.EnsureSystemTenant(ctx); err != nil {
					return err
				}
				org, err := sys.CreateOrg(ctx, model.Org{Name: "Catalog", Slug: "catalog", Status: model.StatusActive})
				tenant = org.TenantID
				return err
			}); err != nil {
				t.Fatal(err)
			}
			packID, revisionID := model.NewID(), model.NewID()
			var defaultID model.ID
			payload := `{"pack_id":"` + packID.String() + `","number":1,"manifest_digest":"retained","members":[]}`
			if err := old.Mutate(ctx, tenant, func(sc store.Scope) error {
				def, err := sc.DefaultWorkspace(ctx)
				if err != nil {
					return err
				}
				defaultID = def.ID
				packs, err := sc.Ext(skills.PackKind)
				if err != nil {
					return err
				}
				if _, err = packs.CreateWithID(ctx, packID, model.Record{"name": "legacy", "state": "enabled", "latest_revision_id": revisionID.String(), "latest_revision": int64(1), "created_by": "system"}); err != nil {
					return err
				}
				revisions, err := sc.Ext(skills.RevisionKind)
				if err != nil {
					return err
				}
				_, err = revisions.CreateWithID(ctx, revisionID, model.Record{"pack_id": packID.String(), "payload": payload, "created_by": "system"})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}
			upgraded, err := engine.Open(ctx, cfg, module.RegisterSchema)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = upgraded.Close() })
			census, ok := upgraded.(store.CompositionCensus)
			if !ok {
				t.Fatal("store has no composition census")
			}
			if err := upgraded.View(ctx, tenant, func(sc store.Scope) error {
				contents, err := store.ReadWorkspaceContents(ctx, sc, defaultID, census.CensusDescriptors())
				if err != nil {
					return err
				}
				for _, kind := range []model.Kind{skills.PackKind, skills.RevisionKind} {
					if got := contents[kind].Count; got != 1 {
						t.Errorf("upgraded default workspace %s count=%d, want 1", kind, got)
					}
				}
				confined, err := store.ConfineWorkspace(ctx, sc, defaultID)
				if err != nil {
					return err
				}
				for _, entry := range []struct {
					kind model.Kind
					id   model.ID
				}{{skills.PackKind, packID}, {skills.RevisionKind, revisionID}} {
					repo, err := confined.Ext(entry.kind)
					if err != nil {
						return err
					}
					row, err := repo.Get(ctx, entry.id)
					if err != nil {
						return err
					}
					if row.Int(model.ColVersion) != 1 {
						t.Errorf("upgrade rewrote %s version", entry.kind)
					}
					if entry.kind == skills.RevisionKind && row.String("payload") != payload {
						t.Error("upgrade rewrote immutable revision payload")
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := upgraded.Mutate(ctx, tenant, func(sc store.Scope) error {
				confined, err := store.ConfineWorkspace(ctx, sc, defaultID)
				if err != nil {
					return err
				}
				for _, kind := range []model.Kind{skills.PackKind, skills.RevisionKind} {
					repo, err := confined.Ext(kind)
					if err != nil {
						return err
					}
					if _, err := repo.Create(ctx, model.Record{}); !errors.Is(err, store.ErrWorkspaceConfinement) {
						t.Errorf("confined %s create=%v, want confinement refusal", kind, err)
					}
					if _, err := repo.Update(ctx, model.Record{}); !errors.Is(err, store.ErrWorkspaceConfinement) {
						t.Errorf("confined %s update=%v, want confinement refusal", kind, err)
					}
					if err := repo.Delete(ctx, packID); !errors.Is(err, store.ErrWorkspaceConfinement) {
						t.Errorf("confined %s delete=%v, want confinement refusal", kind, err)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
