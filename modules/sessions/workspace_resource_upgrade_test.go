// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/olivaresai/olivares/core/engine"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type beforeWorkspaceResourceRegistry struct{ store.ExtensionRegistry }

func (r beforeWorkspaceResourceRegistry) Migrations(ns string, source fs.FS) error {
	if ns != Namespace {
		return r.ExtensionRegistry.Migrations(ns, source)
	}
	legacy := fstest.MapFS{}
	err := fs.WalkDir(source, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || strings.HasSuffix(path, "_workspace_resources.sql") {
			return nil
		}
		data, err := fs.ReadFile(source, path)
		if err == nil {
			legacy[path] = &fstest.MapFile{Data: data, Mode: 0644}
		}
		return err
	})
	if err != nil {
		return err
	}
	return r.ExtensionRegistry.Migrations(ns, legacy)
}
func TestWorkspaceResourceUpgradeSQLite(t *testing.T) {
	exerciseWorkspaceResourceUpgrade(t, store.Config{Engine: store.EngineSQLite, DSN: filepath.Join(t.TempDir(), "upgrade.db"), Debug: true})
}
func TestWorkspaceResourceUpgradePostgres(t *testing.T) {
	if !enginetest.PostgresAvailable(t) {
		t.Skip("PostgreSQL split-owner fixture unavailable")
	}
	pg := enginetest.IsolatedPostgresSplitOwner(t)
	exerciseWorkspaceResourceUpgrade(t, store.Config{Engine: store.EnginePostgres, DSN: pg.App, OwnerDSN: pg.Owner, Debug: true})
}
func exerciseWorkspaceResourceUpgrade(t *testing.T, cfg store.Config) {
	ctx := t.Context()
	m := New()
	st, err := engine.Open(ctx, cfg, func(reg store.ExtensionRegistry) error { return m.RegisterSchema(beforeWorkspaceResourceRegistry{reg}) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if st != nil {
			_ = st.Close()
		}
	})
	type legacyFolder struct {
		tenant model.TenantID
		id     model.ID
		ref    string
		root   string
	}
	var folders []legacyFolder
	for _, slug := range []string{"folder-upgrade", "other-folder-upgrade"} {
		folder := legacyFolder{ref: model.NewID().String(), root: t.TempDir()}
		if err := st.System(ctx, func(sys store.SystemScope) error {
			if _, err := sys.EnsureSystemTenant(ctx); err != nil {
				return err
			}
			org, err := sys.CreateOrg(ctx, model.Org{Name: slug, Slug: slug, Status: model.StatusActive})
			folder.tenant = org.TenantID
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := st.Mutate(ctx, folder.tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(workspaceKind)
			if err != nil {
				return err
			}
			rec, err := repo.Create(ctx, model.Record{colWsRef: folder.ref, colWsRootPath: folder.root, colWsMountMode: mountRW, colWsDLPMode: dlpOff, colWsState: wsActive})
			folder.id = model.ID(rec.String(model.ColID))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		folders = append(folders, folder)
	}
	for range 2 {
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}
		st, err = engine.Open(ctx, cfg, m.RegisterSchema)
		if err != nil {
			t.Fatal(err)
		}
		for _, folder := range folders {
			tenant, id, ref, root := folder.tenant, folder.id, folder.ref, folder.root
			if err := st.View(ctx, tenant, func(sc store.Scope) error {
				resource, err := sc.Resources().Get(ctx, id)
				if err != nil {
					return err
				}
				if resource.Kind != "folder" || resource.Path != "/"+id.String() || resource.URI != "workspace:"+ref {
					t.Fatalf("backfilled resource=%+v", resource)
				}
				repo, err := sc.Ext(workspaceKind)
				if err != nil {
					return err
				}
				row, err := repo.Get(ctx, id)
				if err == nil && (row.String(colWsRef) != ref || row.String(colWsRootPath) != root || row.Int(model.ColVersion) != 1) {
					t.Fatalf("registration rewritten: %v", row)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
			// A normal writer must still enroll after the migration's enrollment ended.
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				probe, err := sc.Resources().Create(ctx, model.Resource{Name: "writer probe", Kind: "folder"})
				if err != nil {
					return err
				}
				return sc.Resources().Delete(ctx, probe.ID)
			}); err != nil {
				t.Fatalf("lineage writer after upgrade: %v", err)
			}
		}
	}
}
