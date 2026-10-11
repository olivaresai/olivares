// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/internal/store/sqlstore"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Three synthetic module kinds: one declares workspace lineage (unset means the
// default workspace), one declares none, one declares lineage but is internal
// bookkeeping. They are not product descriptors.
const (
	contentsLinedKind    model.Kind = "wscapi.lined"
	contentsUnlinedKind  model.Kind = "wscapi.unlined"
	contentsInternalKind model.Kind = "wscapi.internal"
)

func contentsStore(t *testing.T) store.Store {
	t.Helper()
	st, err := sqlstore.Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: ":memory:"},
		func(reg store.ExtensionRegistry) error {
			if err := reg.Register(model.EntityDescriptor{
				Kind: contentsLinedKind, Table: "wscapi_lined",
				Fields: []model.FieldSpec{{Name: "workspace_id", Kind: model.KindUUID, Nullable: true}},
				WorkspaceLineage: model.WorkspaceLineageSpec{
					Column: "workspace_id", Encoding: model.WorkspaceLineageID, Unset: model.WorkspaceUnsetMeansDefault,
				},
			}); err != nil {
				return err
			}
			if err := reg.Register(model.EntityDescriptor{
				Kind: contentsInternalKind, Table: "wscapi_internal", Internal: true,
				Fields: []model.FieldSpec{{Name: "workspace_id", Kind: model.KindUUID, Nullable: true}},
				WorkspaceLineage: model.WorkspaceLineageSpec{
					Column: "workspace_id", Encoding: model.WorkspaceLineageID, Unset: model.WorkspaceUnsetMeansDefault,
				},
			}); err != nil {
				return err
			}
			return reg.Register(model.EntityDescriptor{
				Kind: contentsUnlinedKind, Table: "wscapi_unlined",
				Fields: []model.FieldSpec{{Name: "label", Kind: model.KindText}},
			})
		})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.System(context.Background(), func(sys store.SystemScope) error {
		_, err := sys.EnsureSystemTenant(context.Background())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return st
}

// GET /v1/workspaces/{id}/contents is the HTTP face of the one contents read:
// every kind that declares workspace lineage, module kinds included, counted
// through the confined scope, so rows without a workspace count in the default.
func TestWorkspaceContentsRoute(t *testing.T) {
	var census store.CompositionCensus
	h := newHarnessOptsFromStoreSource(t, harnessStoreSource{open: contentsStore}, func(o *api.Options) {
		// Production hands the registry in because the guards hide it.
		census = o.Store.(store.CompositionCensus)
		o.Store, o.Census = registryHidingStore{o.Store}, census
	})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "contents-route")
	h.elevate(admin)

	var defaultID string
	list := h.do("GET", "/v1/workspaces", admin, nil, tenantHdr(tenant))
	for _, item := range list.body["items"].([]any) {
		if ws := item.(map[string]any); ws["slug"] == model.DefaultWorkspaceSlug {
			defaultID = ws["id"].(string)
		}
	}
	named := h.do("POST", "/v1/workspaces", admin, map[string]any{"name": "Named", "slug": "named"}, tenantHdr(tenant))
	if named.code != http.StatusCreated {
		t.Fatalf("create workspace = %d %s", named.code, named.raw)
	}
	namedID := named.body["id"].(string)

	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		lined, err := sc.Ext(contentsLinedKind)
		if err != nil {
			return err
		}
		for _, ws := range []string{"", defaultID, namedID} {
			if _, err := sc.Agents().Create(ctx, model.Agent{Name: "a" + ws, Kind: "test", WorkspaceID: model.ID(ws)}); err != nil {
				return err
			}
			row := model.Record{}
			if ws != "" {
				row["workspace_id"] = ws
			}
			if _, err := lined.Create(ctx, row); err != nil {
				return err
			}
		}
		// The internal kind holds a row in the default workspace, the way a
		// module's per-workspace guard does from the moment the workspace exists.
		internal, err := sc.Ext(contentsInternalKind)
		if err != nil {
			return err
		}
		if _, err := internal.Create(ctx, model.Record{"workspace_id": defaultID}); err != nil {
			return err
		}
		unlined, err := sc.Ext(contentsUnlinedKind)
		if err != nil {
			return err
		}
		_, err = unlined.Create(ctx, model.Record{"label": "tenant-wide"})
		return err
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var wantKinds []string
	for _, d := range census.CensusDescriptors() {
		if d.WorkspaceLineage.Declared() && !d.Internal {
			wantKinds = append(wantKinds, string(d.Kind))
		}
	}
	// User groups sit outside the tenant scope, so they are no lineage kind; the
	// route adds their line beside the others.
	wantKinds = append(wantKinds, "core.user_group")
	for _, tc := range []struct {
		id          string
		agents, own int
	}{{defaultID, 2, 2}, {namedID, 1, 1}} {
		r := h.do("GET", "/v1/workspaces/"+tc.id+"/contents", admin, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("contents %s = %d %s", tc.id, r.code, r.raw)
		}
		if r.body["workspace_id"] != tc.id {
			t.Errorf("contents workspace_id = %v, want %s", r.body["workspace_id"], tc.id)
		}
		got := map[string]int{}
		var order []string
		for _, item := range r.body["kinds"].([]any) {
			k := item.(map[string]any)
			got[k["kind"].(string)] = int(k["count"].(float64))
			order = append(order, k["kind"].(string))
			if k["capped"] != false {
				t.Errorf("contents %s %s capped = %v, want false", tc.id, k["kind"], k["capped"])
			}
		}
		if len(order) != len(wantKinds) {
			t.Errorf("contents %s kinds = %v, want every lineage kind %v", tc.id, order, wantKinds)
		}
		for i := 1; i < len(order); i++ {
			if order[i-1] >= order[i] {
				t.Errorf("contents %s kinds not sorted: %v", tc.id, order)
			}
		}
		if _, ok := got[string(contentsInternalKind)]; ok {
			t.Errorf("contents %s lists %s, an internal kind", tc.id, contentsInternalKind)
		}
		if _, ok := got[string(contentsUnlinedKind)]; ok {
			t.Errorf("contents %s lists %s, which declares no lineage", tc.id, contentsUnlinedKind)
		}
		if got["core.agent"] != tc.agents || got[string(contentsLinedKind)] != tc.own {
			t.Errorf("contents %s = agents %d, %s %d; want %d and %d",
				tc.id, got["core.agent"], contentsLinedKind, got[string(contentsLinedKind)], tc.agents, tc.own)
		}
	}

	// Admin tier only: a viewer holds tenant:read but not every module read.
	if r := h.do("POST", "/v1/users", admin, map[string]any{
		"email": "contents-viewer@workspace.test", "password": "contents-route-test1", "tenant": tenant.String(),
		"role": auth.RoleViewer,
	}, nil); r.code != http.StatusCreated {
		t.Fatalf("create viewer = %d %s", r.code, r.raw)
	}
	viewer := h.login("contents-viewer@workspace.test", "contents-route-test1")
	if r := h.do("GET", "/v1/workspaces/"+defaultID+"/contents", viewer, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("viewer contents = %d %s, want 403", r.code, r.raw)
	}

	// A principal confined to the named workspace cannot learn the default exists.
	if r := h.do("POST", "/v1/users", admin, map[string]any{
		"email": "contents-confined@workspace.test", "password": "contents-route-test1", "tenant": tenant.String(),
		"role": auth.RoleAdmin, "workspace_id": namedID,
	}, nil); r.code != http.StatusCreated {
		t.Fatalf("create confined admin = %d %s", r.code, r.raw)
	}
	confined := h.login("contents-confined@workspace.test", "contents-route-test1")
	if r := h.do("GET", "/v1/workspaces/"+defaultID+"/contents", confined, nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Errorf("confined contents of another workspace = %d %s, want 404", r.code, r.raw)
	}
	if r := h.do("GET", "/v1/workspaces/"+namedID+"/contents", confined, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("confined contents of its own workspace = %d %s, want 200", r.code, r.raw)
	}
}

// Without the registry the route cannot know the module kinds: it says so with
// 501 instead of answering with the core kinds alone. Production's guards hide
// the store's registry, so only Options.Census provides it.
func TestWorkspaceContentsWithoutRegistry(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) { o.Store = registryHidingStore{o.Store} })
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "contents-no-registry")
	list := h.do("GET", "/v1/workspaces", admin, nil, tenantHdr(tenant))
	id := list.body["items"].([]any)[0].(map[string]any)["id"].(string)
	r := h.do("GET", "/v1/workspaces/"+id+"/contents", admin, nil, tenantHdr(tenant))
	if code, _ := r.body["error"].(map[string]any); r.code != http.StatusNotImplemented || code["code"] != "workspace_contents_unavailable" {
		t.Fatalf("contents without registry = %d %s, want 501 workspace_contents_unavailable", r.code, r.raw)
	}
}
