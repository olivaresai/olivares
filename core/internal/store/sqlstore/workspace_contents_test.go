// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"maps"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Synthetic module kinds for the contents read, one per unset semantics and one
// without lineage. They are not product descriptors.
var (
	wscDefaultItem = model.EntityDescriptor{
		Kind: "wsc.default_item", Table: "wsc_default_item",
		WorkspaceLineage: model.WorkspaceLineageSpec{
			Column: "workspace_id", Encoding: model.WorkspaceLineageID, Unset: model.WorkspaceUnsetMeansDefault,
		},
		Fields: []model.FieldSpec{{Name: "workspace_id", Kind: model.KindUUID, Nullable: true}},
	}
	wscHiddenItem = model.EntityDescriptor{
		Kind: "wsc.hidden_item", Table: "wsc_hidden_item",
		WorkspaceLineage: model.WorkspaceLineageSpec{
			Column: "ws_ref", Encoding: model.WorkspaceLineageID, Unset: model.WorkspaceUnsetHidden,
		},
		Fields: []model.FieldSpec{{Name: "ws_ref", Kind: model.KindText, Nullable: true}},
	}
	wscTenantItem = model.EntityDescriptor{
		Kind: "wsc.tenant_item", Table: "wsc_tenant_item",
		Fields: []model.FieldSpec{{Name: "label", Kind: model.KindText}},
	}
	// wscInternalItem declares lineage like wscDefaultItem and holds rows, but is
	// bookkeeping the module keeps for itself: the contents read must not list it.
	wscInternalItem = model.EntityDescriptor{
		Kind: "wsc.internal_item", Table: "wsc_internal_item", Internal: true,
		WorkspaceLineage: model.WorkspaceLineageSpec{
			Column: "workspace_id", Encoding: model.WorkspaceLineageID, Unset: model.WorkspaceUnsetMeansDefault,
		},
		Fields: []model.FieldSpec{{Name: "workspace_id", Kind: model.KindUUID, Nullable: true}},
	}
)

func registerWscItems(reg store.ExtensionRegistry) error {
	for _, d := range []model.EntityDescriptor{wscDefaultItem, wscHiddenItem, wscTenantItem, wscInternalItem} {
		if err := reg.Register(d); err != nil {
			return err
		}
	}
	return nil
}

// One read of what a workspace holds: every kind of the registry that declares
// workspace lineage, core and module, counted through ConfineWorkspace. A row
// without a workspace counts in the default workspace when its kind says so, and
// a kind without lineage is not part of any workspace.
func TestReadWorkspaceContents(t *testing.T) {
	st := openSQLiteTest(t, registerWscItems)
	ctx := context.Background()
	tenant := provisionTenant(t, st, "wsc-contents")
	descriptors := st.(store.CompositionCensus).CensusDescriptors()

	var def, named model.Workspace
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		if def, err = sc.DefaultWorkspace(ctx); err != nil {
			return err
		}
		if named, err = sc.Workspaces().Create(ctx, model.Workspace{
			Name: "Named", Slug: "named", Status: model.StatusActive,
		}); err != nil {
			return err
		}
		for i, ws := range []model.ID{"", def.ID, named.ID} {
			slug := []string{"unset", "default", "named"}[i]
			a, err := sc.Agents().Create(ctx, model.Agent{Name: slug, Kind: "test", WorkspaceID: ws})
			if err != nil {
				return err
			}
			if _, err := sc.Sessions().Create(ctx, model.Session{ExternalID: slug, AgentID: a.ID, WorkspaceID: ws}); err != nil {
				return err
			}
			if _, err := sc.AgentGroups().Create(ctx, model.AgentGroup{Name: slug, Slug: slug, WorkspaceID: ws}); err != nil {
				return err
			}
			if _, err := sc.Resources().Create(ctx, model.Resource{
				Name: slug, Kind: "folder", URI: "file:///" + slug, WorkspaceID: ws,
			}); err != nil {
				return err
			}
			for kind, column := range map[model.Kind]string{
				wscDefaultItem.Kind: "workspace_id", wscHiddenItem.Kind: "ws_ref", wscInternalItem.Kind: "workspace_id",
			} {
				repo, err := sc.Ext(kind)
				if err != nil {
					return err
				}
				row := model.Record{}
				if ws != "" {
					row[column] = ws.String()
				}
				if _, err := repo.Create(ctx, row); err != nil {
					return err
				}
			}
		}
		tenantRepo, err := sc.Ext(wscTenantItem.Kind)
		if err != nil {
			return err
		}
		_, err = tenantRepo.Create(ctx, model.Record{"label": "tenant-wide"})
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Another tenant's rows without a workspace: the unset-means-default
	// predicate must never reach them.
	other := provisionTenant(t, st, "wsc-other")
	if err := st.Mutate(ctx, other, func(sc store.Scope) error {
		if _, err := sc.Agents().Create(ctx, model.Agent{Name: "other", Kind: "test"}); err != nil {
			return err
		}
		repo, err := sc.Ext(wscDefaultItem.Kind)
		if err != nil {
			return err
		}
		_, err = repo.Create(ctx, model.Record{})
		return err
	}); err != nil {
		t.Fatalf("seed other tenant: %v", err)
	}

	for _, tc := range []struct {
		name string
		ws   model.ID
		want map[model.Kind]store.KindCount
	}{
		{"default", def.ID, map[model.Kind]store.KindCount{
			"core.agent": {Count: 2}, "core.session": {Count: 2}, "core.resource": {Count: 2},
			"core.agent_group": {Count: 2}, wscDefaultItem.Kind: {Count: 2}, wscHiddenItem.Kind: {Count: 1},
		}},
		{"named", named.ID, map[model.Kind]store.KindCount{
			"core.agent": {Count: 1}, "core.session": {Count: 1}, "core.resource": {Count: 1},
			"core.agent_group": {Count: 1}, wscDefaultItem.Kind: {Count: 1}, wscHiddenItem.Kind: {Count: 1},
		}},
	} {
		var got map[model.Kind]store.KindCount
		if err := st.View(ctx, tenant, func(sc store.Scope) error {
			var err error
			got, err = store.ReadWorkspaceContents(ctx, sc, tc.ws, descriptors)
			return err
		}); err != nil {
			t.Fatalf("%s contents: %v", tc.name, err)
		}
		for kind, want := range tc.want {
			if c, ok := got[kind]; !ok || c != want {
				t.Errorf("%s contents %s = %+v (present %v), want %+v", tc.name, kind, c, ok, want)
			}
		}
		// Exactly the registry's lineage kinds that are not internal bookkeeping,
		// and nothing else.
		for _, d := range descriptors {
			want := d.WorkspaceLineage.Declared() && !d.Internal
			if _, ok := got[d.Kind]; ok != want {
				t.Errorf("%s contents lists %s = %v, want %v (declares lineage, not internal)", tc.name, d.Kind, ok, want)
			}
		}
	}

	// The internal kind's rows exist and stay readable through the confined
	// scope: only the contents read leaves the kind out.
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		confined, err := store.ConfineWorkspace(ctx, sc, def.ID)
		if err != nil {
			return err
		}
		repo, err := confined.Ext(wscInternalItem.Kind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{Limit: 10})
		if err != nil {
			return err
		}
		if len(rows) != 2 {
			t.Errorf("internal kind rows in the default workspace = %d, want 2", len(rows))
		}
		return nil
	}); err != nil {
		t.Fatalf("read internal rows: %v", err)
	}

	// Without descriptors the read counts the four core kinds only: what the
	// workspace summary publishes.
	var core map[model.Kind]store.KindCount
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var err error
		core, err = store.ReadWorkspaceContents(ctx, sc, def.ID, nil)
		return err
	}); err != nil {
		t.Fatalf("core contents: %v", err)
	}
	wantCore := map[model.Kind]store.KindCount{
		"core.agent": {Count: 2}, "core.session": {Count: 2}, "core.resource": {Count: 2}, "core.agent_group": {Count: 2},
	}
	if !maps.Equal(core, wantCore) {
		t.Errorf("core contents = %v, want %v", core, wantCore)
	}

	// A core kind that declares lineage but has no typed reader is an error, not
	// a zero count.
	err := st.View(ctx, tenant, func(sc store.Scope) error {
		_, err := store.ReadWorkspaceContents(ctx, sc, def.ID, []model.EntityDescriptor{{
			Kind: "core.unread", WorkspaceLineage: wscDefaultItem.WorkspaceLineage,
		}})
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "no reader for core kind core.unread") {
		t.Fatalf("contents of an unread core kind = %v, want a no-reader error", err)
	}
}
