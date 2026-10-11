// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A row created without a workspace belongs to the tenant's default workspace
// (core/model/scoping.go). store.ConfineWorkspace says so with OpEqOrUnset; the
// workspace summary and the core agent-group list filtered workspace_id with a
// plain OpEq instead, so the default workspace's own summary and a principal
// confined to it missed every such row.
func TestDefaultWorkspaceHoldsRowsWithoutWorkspace(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "default-rows")
	h.elevate(admin)

	var defaultID string
	list := h.do("GET", "/v1/workspaces", admin, nil, tenantHdr(tenant))
	for _, item := range list.body["items"].([]any) {
		ws := item.(map[string]any)
		if ws["slug"] == model.DefaultWorkspaceSlug {
			defaultID = ws["id"].(string)
		}
	}
	if defaultID == "" {
		t.Fatalf("no default workspace in %s", list.raw)
	}
	named := h.do("POST", "/v1/workspaces", admin, map[string]any{"name": "Named", "slug": "named"}, tenantHdr(tenant))
	if named.code != http.StatusCreated {
		t.Fatalf("create workspace = %d %s", named.code, named.raw)
	}
	namedID := named.body["id"].(string)

	groups := make(map[string]string)
	for _, row := range []struct{ slug, workspace string }{
		{"unset", ""}, {"explicit-default", defaultID}, {"in-named", namedID},
	} {
		r := h.do("POST", "/v1/agents", admin, map[string]any{
			"name": row.slug, "kind": "test", "workspace_id": row.workspace,
		}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("create agent %s = %d %s", row.slug, r.code, r.raw)
		}
		r = h.do("POST", "/v1/agent-groups", admin, map[string]any{
			"name": row.slug, "slug": row.slug, "workspace_id": row.workspace,
		}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("create agent group %s = %d %s", row.slug, r.code, r.raw)
		}
		groups[row.slug] = r.body["id"].(string)
	}
	// Resources have no core create route; seed one without a workspace and one in
	// the named workspace through the store.
	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		for _, ws := range []string{"", namedID} {
			if _, err := sc.Resources().Create(ctx, model.Resource{
				Name: "res-" + ws, Kind: "folder", URI: "file:///res-" + ws, WorkspaceID: model.ID(ws),
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed resources: %v", err)
	}

	for _, tc := range []struct {
		id                  string
		want, wantResources int
	}{{defaultID, 2, 1}, {namedID, 1, 1}} {
		r := h.do("GET", "/v1/workspaces/"+tc.id+"/summary", admin, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("summary %s = %d %s", tc.id, r.code, r.raw)
		}
		for _, k := range []string{"agent_count", "group_count"} {
			if got := int(r.body[k].(float64)); got != tc.want {
				t.Errorf("summary %s %s = %d, want %d", tc.id, k, got, tc.want)
			}
		}
		if got := int(r.body["resource_count"].(float64)); got != tc.wantResources {
			t.Errorf("summary %s resource_count = %d, want %d", tc.id, got, tc.wantResources)
		}
	}

	r := h.do("POST", "/v1/users", admin, map[string]any{
		"email": "default-confined@workspace.test", "password": "supersecret1", "tenant": tenant.String(),
		"role": auth.RoleViewer, "workspace_id": defaultID,
	}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("create confined user = %d %s", r.code, r.raw)
	}
	confined := h.login("default-confined@workspace.test", "supersecret1")
	r = h.do("GET", "/v1/agent-groups", confined, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("confined agent-group list = %d %s", r.code, r.raw)
	}
	var got []string
	for _, item := range r.body["items"].([]any) {
		got = append(got, item.(map[string]any)["id"].(string))
	}
	want := []string{groups["unset"], groups["explicit-default"]}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("confined agent-group IDs = %v, want %v", got, want)
	}
}

// registryHidingStore stands for the service guards boot wraps the engine's store
// in: embedding the interface promotes only store.Store, so the optional
// capabilities, the registry (store.CompositionCensus) among them, are hidden
// exactly as in production.
type registryHidingStore struct{ store.Store }

// The summary's counts come from the workspace contents read with no module
// kind, so they need nothing the production store wrappers hide.
func TestWorkspaceSummaryBehindAStoreWrapper(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) { o.Store = registryHidingStore{o.Store} })
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "summary-wrapped")
	if r := h.do("POST", "/v1/agents", admin, map[string]any{"name": "a", "kind": "test"}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("create agent = %d %s", r.code, r.raw)
	}
	var defaultID string
	list := h.do("GET", "/v1/workspaces", admin, nil, tenantHdr(tenant))
	for _, item := range list.body["items"].([]any) {
		if ws := item.(map[string]any); ws["slug"] == model.DefaultWorkspaceSlug {
			defaultID = ws["id"].(string)
		}
	}
	r := h.do("GET", "/v1/workspaces/"+defaultID+"/summary", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("summary = %d %s", r.code, r.raw)
	}
	if got := int(r.body["agent_count"].(float64)); got != 1 {
		t.Fatalf("summary agent_count = %d, want 1", got)
	}
}
