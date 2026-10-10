// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// A tenant-wide caller may narrow a core list with ?workspace_id=. Naming the
// default workspace must return what the default workspace holds, the rows
// created without a workspace included (core/model/scoping.go), exactly as the
// confined scope answers a principal confined to it. A plain OpEq on the column
// answered the explicit rows only. A workspace that does not exist keeps the
// empty page it always answered, and a non-default one keeps its own rows.
func TestCoreListsWorkspaceFilterOfATenantWideCaller(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "list-ws-filter")
	h.elevate(admin)

	var defaultID string
	list := h.do("GET", "/v1/workspaces", admin, nil, tenantHdr(tenant))
	for _, item := range list.body["items"].([]any) {
		if ws := item.(map[string]any); ws["slug"] == model.DefaultWorkspaceSlug {
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

	agents, groups := map[string]string{}, map[string]string{}
	for _, row := range []struct{ slug, workspace string }{
		{"unset", ""}, {"explicit-default", defaultID}, {"in-named", namedID},
	} {
		r := h.do("POST", "/v1/agents", admin, map[string]any{
			"name": row.slug, "kind": "test", "workspace_id": row.workspace,
		}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("create agent %s = %d %s", row.slug, r.code, r.raw)
		}
		agents[row.slug] = r.body["id"].(string)
		r = h.do("POST", "/v1/agent-groups", admin, map[string]any{
			"name": row.slug, "slug": row.slug, "workspace_id": row.workspace,
		}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("create agent group %s = %d %s", row.slug, r.code, r.raw)
		}
		groups[row.slug] = r.body["id"].(string)
	}

	ids := func(path string) []string {
		t.Helper()
		r := h.do("GET", path, admin, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, r.code, r.raw)
		}
		var got []string
		for _, item := range r.body["items"].([]any) {
			got = append(got, item.(map[string]any)["id"].(string))
		}
		slices.Sort(got)
		return got
	}
	sorted := func(v ...string) []string { slices.Sort(v); return v }

	for _, tc := range []struct {
		name, query string
		agents      []string
		groups      []string
	}{
		{"default workspace", "?workspace_id=" + defaultID,
			sorted(agents["unset"], agents["explicit-default"]), sorted(groups["unset"], groups["explicit-default"])},
		{"named workspace", "?workspace_id=" + namedID,
			sorted(agents["in-named"]), sorted(groups["in-named"])},
		{"unparseable workspace", "?workspace_id=no-such-workspace", nil, nil},
		{"absent workspace", "?workspace_id=" + model.NewID().String(), nil, nil},
		{"no filter", "",
			sorted(agents["unset"], agents["explicit-default"], agents["in-named"]),
			sorted(groups["unset"], groups["explicit-default"], groups["in-named"])},
	} {
		if got := ids("/v1/agents" + tc.query); !slices.Equal(got, tc.agents) {
			t.Errorf("%s: GET /v1/agents%s = %v, want %v", tc.name, tc.query, got, tc.agents)
		}
		if got := ids("/v1/agent-groups" + tc.query); !slices.Equal(got, tc.groups) {
			t.Errorf("%s: GET /v1/agent-groups%s = %v, want %v", tc.name, tc.query, got, tc.groups)
		}
	}

	// A caller confined to a workspace keeps its own rows whatever workspace the
	// query names: the caller's confinement is never retargeted by ?workspace_id=.
	if r := h.do("POST", "/v1/users", admin, map[string]any{
		"email": "named-confined@workspace.test", "password": "list-ws-filter-test1", "tenant": tenant.String(),
		"role": auth.RoleViewer, "workspace_id": namedID,
	}, nil); r.code != http.StatusCreated {
		t.Fatalf("create confined user = %d %s", r.code, r.raw)
	}
	confined := h.login("named-confined@workspace.test", "list-ws-filter-test1")
	for _, query := range []string{"", "?workspace_id=" + defaultID, "?workspace_id=" + namedID} {
		for path, want := range map[string][]string{
			"/v1/agents":       {agents["in-named"]},
			"/v1/agent-groups": {groups["in-named"]},
		} {
			r := h.do("GET", path+query, confined, nil, tenantHdr(tenant))
			if r.code != http.StatusOK {
				t.Fatalf("confined GET %s%s = %d %s", path, query, r.code, r.raw)
			}
			var got []string
			for _, item := range r.body["items"].([]any) {
				got = append(got, item.(map[string]any)["id"].(string))
			}
			if !slices.Equal(got, want) {
				t.Errorf("confined GET %s%s = %v, want %v", path, query, got, want)
			}
		}
	}
}
