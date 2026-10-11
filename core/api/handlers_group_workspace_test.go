// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// groupRow returns the GET /v1/groups row of one group.
func groupRow(t *testing.T, h *harness, token string, tenant model.TenantID, id model.ID) map[string]any {
	t.Helper()
	res := h.do(http.MethodGet, "/v1/groups", token, nil, tenantHdr(tenant))
	if res.code != http.StatusOK {
		t.Fatalf("list groups = %d %s", res.code, res.raw)
	}
	for _, row := range res.body["groups"].([]any) {
		if g := row.(map[string]any); g["id"] == id.String() {
			return g
		}
	}
	t.Fatalf("group %s is not listed: %s", id, res.raw)
	return nil
}

// PUT /v1/groups/{id}/workspace places a user group in a workspace of its
// organization (or, with "", takes it out), and the group list and the
// workspace's contents show it. Membership is not touched.
func TestGroupWorkspaceRoute(t *testing.T) {
	h := newHarnessOpts(t, func(o *api.Options) {
		o.Census = o.Store.(store.CompositionCensus)
		groupDepartments(o)
	})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "group-workspace")
	other := h.createOrg(admin, "group-workspace-other")
	h.elevate(admin)
	ctx := context.Background()
	actor := h.principalOf(admin)

	mk := func(tn model.TenantID, slug string) string {
		r := h.do("POST", "/v1/workspaces", admin, map[string]any{"name": slug, "slug": slug}, tenantHdr(tn))
		if r.code != http.StatusCreated {
			t.Fatalf("create workspace %s = %d %s", slug, r.code, r.raw)
		}
		return r.body["id"].(string)
	}
	sales, ops, foreign := mk(tenant, "sales"), mk(tenant, "ops"), mk(other, "sales")
	g, err := h.authr.SCIMCreateGroup(ctx, actor, tenant, auth.SCIMGroupInput{DisplayName: "Sales staff", ExternalID: "ss"})
	if err != nil {
		t.Fatal(err)
	}
	free, err := h.authr.SCIMCreateGroup(ctx, actor, tenant, auth.SCIMGroupInput{DisplayName: "Everyone", ExternalID: "all"})
	if err != nil {
		t.Fatal(err)
	}
	put := func(token, ws string) resp {
		return h.do(http.MethodPut, "/v1/groups/"+g.Group.ID.String()+"/workspace", token, map[string]any{"workspace_id": ws}, tenantHdr(tenant))
	}

	if res := put("", sales); res.code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated place = %d %s", res.code, res.raw)
	}
	if row := groupRow(t, h, admin, tenant, g.Group.ID); row["workspace_id"] != "" {
		t.Fatalf("a new group lists workspace_id %v, want empty (tenant-wide)", row["workspace_id"])
	}
	res := put(admin, sales)
	if res.code != http.StatusOK || res.body["workspace_id"] != sales || res.body["id"] != g.Group.ID.String() {
		t.Fatalf("place = %d %s", res.code, res.raw)
	}
	if row := groupRow(t, h, admin, tenant, g.Group.ID); row["workspace_id"] != sales {
		t.Errorf("list shows workspace_id %v, want %s", row["workspace_id"], sales)
	}
	if row := groupRow(t, h, admin, tenant, free.Group.ID); row["workspace_id"] != "" {
		t.Errorf("an untouched group lists workspace_id %v, want empty", row["workspace_id"])
	}

	// The workspace contents list it, in the place it was put and nowhere else.
	kindCount := func(ws, kind string) int {
		t.Helper()
		r := h.do("GET", "/v1/workspaces/"+ws+"/contents", admin, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("contents %s = %d %s", ws, r.code, r.raw)
		}
		for _, item := range r.body["kinds"].([]any) {
			if k := item.(map[string]any); k["kind"] == kind {
				return int(k["count"].(float64))
			}
		}
		t.Fatalf("contents %s lists no %s line: %s", ws, kind, r.raw)
		return -1
	}
	userGroups := func(ws string) int { return kindCount(ws, "core.user_group") }
	if got := userGroups(sales); got != 1 {
		t.Errorf("sales lists %d user groups, want 1", got)
	}
	if got := userGroups(ops); got != 0 {
		t.Errorf("ops lists %d user groups, want 0", got)
	}
	if res := put(admin, ops); res.code != http.StatusOK {
		t.Fatalf("move = %d %s", res.code, res.raw)
	}
	if userGroups(sales) != 0 || userGroups(ops) != 1 {
		t.Errorf("after the move sales/ops list %d/%d user groups, want 0/1", userGroups(sales), userGroups(ops))
	}

	// Agent groups reuse their existing place, update and roster paths.
	ag := h.do("POST", "/v1/agent-groups", admin, map[string]any{
		"name": "Sales bots", "slug": "sales-bots", "workspace_id": sales,
	}, tenantHdr(tenant))
	if ag.code != http.StatusCreated {
		t.Fatalf("create agent group = %d %s", ag.code, ag.raw)
	}
	agID := ag.body["id"].(string)
	agent := h.do("POST", "/v1/agents", admin, map[string]any{"name": "bot", "kind": "claude-code"}, tenantHdr(tenant))
	if agent.code != http.StatusCreated {
		t.Fatalf("create agent = %d %s", agent.code, agent.raw)
	}
	agentID := agent.body["id"].(string)
	if r := h.do("PUT", "/v1/agent-groups/"+agID+"/members/"+agentID, admin, nil, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("add agent group member = %d %s", r.code, r.raw)
	}
	if kindCount(sales, "core.agent_group") != 1 || kindCount(ops, "core.agent_group") != 0 {
		t.Fatal("agent group is not counted in its workspace")
	}
	if r := h.do("PATCH", "/v1/agent-groups/"+agID, admin, map[string]any{"workspace_id": ops}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("move agent group = %d %s", r.code, r.raw)
	}
	if kindCount(sales, "core.agent_group") != 0 || kindCount(ops, "core.agent_group") != 1 {
		t.Fatal("agent group counts did not follow its move")
	}
	if r := h.do("GET", "/v1/agent-groups/"+agID+"/members", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK || len(r.body["items"].([]any)) != 1 || r.body["items"].([]any)[0].(map[string]any)["agent_id"] != agentID {
		t.Fatalf("move changed the agent roster = %d %s", r.code, r.raw)
	}

	// Refusals change nothing: another organization's workspace and a missing
	// one are 404, a body that is not a JSON object is 400.
	for name, ws := range map[string]string{"foreign": foreign, "unknown": model.NewID().String()} {
		if res := put(admin, ws); res.code != http.StatusNotFound {
			t.Errorf("%s workspace = %d %s, want 404", name, res.code, res.raw)
		}
	}
	if res := h.do(http.MethodPut, "/v1/groups/"+g.Group.ID.String()+"/workspace", admin, "not json", tenantHdr(tenant)); res.code != http.StatusBadRequest {
		t.Errorf("a non-object JSON body = %d %s, want 400", res.code, res.raw)
	}
	if row := groupRow(t, h, admin, tenant, g.Group.ID); row["workspace_id"] != ops {
		t.Errorf("after the refusals the place is %v, want %s", row["workspace_id"], ops)
	}
	for name, body := range map[string]any{
		"missing": map[string]any{}, "null-field": map[string]any{"workspace_id": nil}, "null-body": json.RawMessage("null"),
	} {
		if r := h.do(http.MethodPut, "/v1/groups/"+g.Group.ID.String()+"/workspace", admin, body, tenantHdr(tenant)); r.code != http.StatusBadRequest {
			t.Errorf("%s placement input = %d %s, want 400", name, r.code, r.raw)
		}
		if row := groupRow(t, h, admin, tenant, g.Group.ID); row["workspace_id"] != ops {
			t.Errorf("%s placement input changed the place to %v, want %s", name, row["workspace_id"], ops)
		}
	}

	// A viewer cannot write it, and a principal confined to a workspace can
	// neither write it nor read another workspace's id off the list.
	if r := h.do("POST", "/v1/users", admin, map[string]any{
		"email": "viewer@group-workspace.test", "password": "group-workspace-test1", "tenant": tenant.String(), "role": auth.RoleViewer,
	}, nil); r.code != http.StatusCreated {
		t.Fatalf("create viewer = %d %s", r.code, r.raw)
	}
	if res := put(h.login("viewer@group-workspace.test", "group-workspace-test1"), sales); res.code != http.StatusForbidden {
		t.Errorf("viewer place = %d %s, want 403", res.code, res.raw)
	}
	if r := h.do("POST", "/v1/users", admin, map[string]any{
		"email": "confined@group-workspace.test", "password": "group-workspace-test1", "tenant": tenant.String(),
		"role": auth.RoleAdmin, "workspace_id": sales,
	}, nil); r.code != http.StatusCreated {
		t.Fatalf("create confined admin = %d %s", r.code, r.raw)
	}
	confined := h.login("confined@group-workspace.test", "group-workspace-test1")
	if res := put(confined, sales); res.code != http.StatusForbidden {
		t.Errorf("confined place = %d %s, want 403", res.code, res.raw)
	}
	if row := groupRow(t, h, confined, tenant, g.Group.ID); row["workspace_id"] != "" {
		t.Errorf("confined group list exposes another workspace: %v", row["workspace_id"])
	}

	if res := put(admin, ""); res.code != http.StatusOK || res.body["workspace_id"] != "" {
		t.Fatalf("clear place = %d %s", res.code, res.raw)
	}
	if userGroups(sales) != 0 || userGroups(ops) != 0 {
		t.Fatal("cleared user group is still counted in a workspace")
	}

}

func TestGroupWorkspaceOpenAPI(t *testing.T) {
	h := newHarness(t)
	doc := decodeDoc(t, rawGet(h, "/openapi.json", "", nil))
	paths := doc["paths"].(map[string]any)
	path, ok := paths["/v1/groups/{id}/workspace"].(map[string]any)
	if !ok || path["put"] == nil {
		t.Fatal("OpenAPI omits the user group placement route")
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	props := schemas["DirectoryGroup"].(map[string]any)["properties"].(map[string]any)
	if props["workspace_id"] == nil {
		t.Fatal("OpenAPI omits the user group workspace_id")
	}
}
