// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// groupDepartments is the department surface a test wires to reach the group
// placement through HTTP. The Business service gates its add-on and then calls the
// same auth primitive, so the route's answers and refusal envelopes are the ones
// under test. The workspace move is the Business service's own transaction and is
// not reachable here.
func groupDepartments(o *api.Options) {
	o.Departments = authDepartments{o.Authenticator}
}

type authDepartments struct{ authr *auth.Authenticator }

func (d authDepartments) SetWorkspaceParent(context.Context, auth.Principal, model.TenantID, model.ID, model.ID) (model.Workspace, error) {
	return model.Workspace{}, errors.New("the workspace move is not under test here")
}

func (d authDepartments) SetGroupWorkspace(ctx context.Context, p auth.Principal, tenant model.TenantID, id, workspace model.ID) (model.UserGroup, error) {
	return d.authr.ConfigureGroupWorkspace(ctx, p, tenant, id, workspace)
}

// recordingDepartments answers a workspace move with the workspace it was given a
// parent for, and records the calls, so a test can tell a refusal before the
// service from one by it.
type recordingDepartments struct {
	authDepartments
	calls *[]model.ID
}

func (d recordingDepartments) SetWorkspaceParent(_ context.Context, _ auth.Principal, _ model.TenantID, id, parent model.ID) (model.Workspace, error) {
	*d.calls = append(*d.calls, id, parent)
	return model.Workspace{BaseFields: model.BaseFields{ID: id}, Name: "moved", Slug: "moved", ParentID: parent, Status: model.StatusActive}, nil
}

func errorCodeOf(r resp) string {
	e, _ := r.body["error"].(map[string]any)
	code, _ := e["code"].(string)
	return code
}

// The Community build wires no department service: placing a department and filing
// a group in a workspace answer 501 departments_unavailable to the owner and to an
// administrator alike (the seam answers before any role check), and change nothing.
// A tree, a nesting edge and a place stored before stay readable, and a confined
// caller still sees no parent in its reads. Group nesting is published Community
// API (since v26.8.0): it keeps working with no department service wired.
func TestDepartmentsAreBusiness(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "departments-community")
	h.elevate(admin)
	ctx := context.Background()
	actor, err := h.authr.Authenticate(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(slug string) string {
		t.Helper()
		r := h.do(http.MethodPost, "/v1/workspaces", admin, map[string]any{"name": slug, "slug": slug}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("create workspace %s = %d %s", slug, r.code, r.raw)
		}
		return r.body["id"].(string)
	}
	sales, emea, apac := mk("sales"), mk("emea"), mk("apac")
	groups := map[string]model.UserGroup{}
	for _, name := range []string{"child", "parent"} {
		g, err := h.authr.SCIMCreateGroup(ctx, actor, tenant, auth.SCIMGroupInput{DisplayName: name, ExternalID: name})
		if err != nil {
			t.Fatal(err)
		}
		groups[name] = g.Group
	}
	// What a Business install stored, written below the HTTP surface.
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, err := sc.Workspaces().SetParent(ctx, model.ID(emea), model.ID(sales))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.AuthMutate(ctx, func(as store.AuthScope) error {
		child, err := as.Groups().Get(ctx, groups["child"].ID)
		if err != nil {
			return err
		}
		child.ParentGroupID, child.WorkspaceID = groups["parent"].ID, model.ID(sales)
		_, err = as.Groups().Update(ctx, child)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	child := groups["child"].ID.String()
	for _, c := range []struct{ path, field, value string }{
		{"/v1/workspaces/" + emea + "/parent", "parent_id", apac},
		{"/v1/workspaces/" + emea + "/parent", "parent_id", ""},
		{"/v1/groups/" + child + "/workspace", "workspace_id", apac},
	} {
		r := h.do(http.MethodPut, c.path, admin, map[string]any{c.field: c.value}, tenantHdr(tenant))
		if r.code != http.StatusNotImplemented || errorCodeOf(r) != "departments_unavailable" {
			t.Errorf("PUT %s %s=%q = %d %s, want 501 departments_unavailable", c.path, c.field, c.value, r.code, r.raw)
		}
	}

	if r := h.do(http.MethodGet, "/v1/workspaces/"+emea, admin, nil, tenantHdr(tenant)); r.code != http.StatusOK || r.body["parent_id"] != sales {
		t.Errorf("stored department read = %d %s, want parent_id %s", r.code, r.raw, sales)
	}
	list := h.do(http.MethodGet, "/v1/groups", admin, nil, tenantHdr(tenant))
	if list.code != http.StatusOK {
		t.Fatalf("list groups = %d %s", list.code, list.raw)
	}
	found := false
	for _, row := range list.body["groups"].([]any) {
		g := row.(map[string]any)
		if g["id"] == child {
			found = true
			if g["parent_group_id"] != groups["parent"].ID.String() || g["workspace_id"] != sales {
				t.Errorf("stored nesting and place read = %v, want parent %s in %s", g, groups["parent"].ID, sales)
			}
		}
	}
	if !found {
		t.Fatalf("the group list omits the nested group: %s", list.raw)
	}

	in := map[string]any{"email": "confined@departments.test", "password": "departments-test1", "tenant": tenant.String(), "role": auth.RoleOwner, "workspace_id": emea}
	if r := h.do(http.MethodPost, "/v1/users", admin, in, nil); r.code != http.StatusCreated {
		t.Fatalf("create confined owner = %d %s", r.code, r.raw)
	}
	confined := h.login("confined@departments.test", "departments-test1")
	if r := h.do(http.MethodGet, "/v1/workspaces/"+emea, confined, nil, tenantHdr(tenant)); r.code != http.StatusOK || r.body["parent_id"] != nil {
		t.Errorf("confined read = %d %s, want the parent hidden", r.code, r.raw)
	}
	if r := h.do(http.MethodGet, "/v1/workspaces", confined, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("confined list = %d %s", r.code, r.raw)
	} else {
		for _, item := range r.body["items"].([]any) {
			if row := item.(map[string]any); row["parent_id"] != nil {
				t.Errorf("a confined caller's list shows parent_id %v for %v", row["parent_id"], row["id"])
			}
		}
	}

	in = map[string]any{"email": "admin@departments.test", "password": "departments-test1", "tenant": tenant.String(), "role": auth.RoleAdmin}
	if r := h.do(http.MethodPost, "/v1/users", admin, in, nil); r.code != http.StatusCreated {
		t.Fatalf("create admin = %d %s", r.code, r.raw)
	}
	tenantAdmin := h.login("admin@departments.test", "departments-test1")
	if r := h.do(http.MethodPut, "/v1/workspaces/"+emea+"/parent", tenantAdmin, map[string]any{"parent_id": ""}, tenantHdr(tenant)); r.code != http.StatusNotImplemented || errorCodeOf(r) != "departments_unavailable" {
		t.Errorf("administrator set-parent = %d %s, want 501 departments_unavailable", r.code, r.raw)
	}
	if r := h.do(http.MethodPut, "/v1/groups/"+child+"/parent", "", map[string]any{"parent_id": ""}, tenantHdr(tenant)); r.code != http.StatusUnauthorized {
		t.Errorf("unauthenticated nesting = %d %s, want 401", r.code, r.raw)
	}
	for _, parent := range []string{"", groups["parent"].ID.String()} {
		r := h.do(http.MethodPut, "/v1/groups/"+child+"/parent", admin, map[string]any{"parent_id": parent}, tenantHdr(tenant))
		if r.code != http.StatusOK || r.body["parent_group_id"] != parent {
			t.Errorf("Community nesting parent_id=%q = %d %s, want 200 with that parent", parent, r.code, r.raw)
		}
	}
}

// With the Business service wired, the workspace move is still refused before the
// service runs to an administrator, a viewer, a confined owner, a malformed body and
// a session without the step-up. The owner's move reaches the service once, and the
// answer is the workspace the service returned.
func TestWorkspaceParentGuardsRunBeforeTheService(t *testing.T) {
	var calls []model.ID
	h := newHarnessOpts(t, func(o *api.Options) {
		o.Departments = recordingDepartments{authDepartments{o.Authenticator}, &calls}
	})
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "workspace-parent-guards")
	h.elevate(admin)
	mk := func(slug string) string {
		t.Helper()
		r := h.do(http.MethodPost, "/v1/workspaces", admin, map[string]any{"name": slug, "slug": slug}, tenantHdr(tenant))
		if r.code != http.StatusCreated {
			t.Fatalf("create workspace %s = %d %s", slug, r.code, r.raw)
		}
		return r.body["id"].(string)
	}
	sales, emea := mk("sales"), mk("emea")
	put := func(token string, body any) resp {
		return h.do(http.MethodPut, "/v1/workspaces/"+emea+"/parent", token, body, tenantHdr(tenant))
	}
	user := func(email, role, workspace string) string {
		t.Helper()
		in := map[string]any{"email": email, "password": "workspace-parent-test1", "tenant": tenant.String(), "role": role}
		if workspace != "" {
			in["workspace_id"] = workspace
		}
		if r := h.do(http.MethodPost, "/v1/users", admin, in, nil); r.code != http.StatusCreated {
			t.Fatalf("create %s = %d %s", email, r.code, r.raw)
		}
		return h.login(email, "workspace-parent-test1")
	}

	if r := put("", map[string]any{"parent_id": sales}); r.code != http.StatusUnauthorized {
		t.Errorf("unauthenticated set-parent = %d %s, want 401", r.code, r.raw)
	}
	for name, token := range map[string]string{
		"admin":  user("admin@workspace-parent.test", auth.RoleAdmin, ""),
		"viewer": user("viewer@workspace-parent.test", auth.RoleViewer, ""),
	} {
		if r := put(token, map[string]any{"parent_id": ""}); r.code != http.StatusForbidden {
			t.Errorf("%s set-parent = %d %s, want 403", name, r.code, r.raw)
		}
	}
	confined := user("confined@workspace-parent.test", auth.RoleOwner, emea)
	if r := put(confined, map[string]any{"parent_id": ""}); r.code != http.StatusForbidden || errorCodeOf(r) != "forbidden" {
		t.Errorf("confined owner set-parent = %d %s, want 403 forbidden", r.code, r.raw)
	}
	for name, body := range map[string]any{
		"missing": map[string]any{}, "null-field": map[string]any{"parent_id": nil},
		"null-body": json.RawMessage("null"), "not-json": "not json",
	} {
		if r := put(admin, body); r.code != http.StatusBadRequest {
			t.Errorf("%s input = %d %s, want 400", name, r.code, r.raw)
		}
	}
	h.requirePasskeyStepUp()
	fresh := h.login("root@x.io", "supersecret1") // a new session, at AAL1
	if r := put(fresh, map[string]any{"parent_id": sales}); r.code != http.StatusForbidden || errorCodeOf(r) != "step_up_required" {
		t.Errorf("set-parent at AAL1 = %d %s, want 403 step_up_required", r.code, r.raw)
	}
	if len(calls) != 0 {
		t.Fatalf("refused moves reached the service: %v", calls)
	}

	r := put(admin, map[string]any{"parent_id": sales})
	if r.code != http.StatusOK || r.body["id"] != emea || r.body["parent_id"] != sales {
		t.Fatalf("owner set-parent = %d %s", r.code, r.raw)
	}
	if len(calls) != 2 || calls[0].String() != emea || calls[1].String() != sales {
		t.Errorf("service calls = %v, want [%s %s]", calls, emea, sales)
	}
}

func TestWorkspaceParentOpenAPI(t *testing.T) {
	h := newHarness(t)
	doc := decodeDoc(t, rawGet(h, "/openapi.json", "", nil))
	path, ok := doc["paths"].(map[string]any)["/v1/workspaces/{id}/parent"].(map[string]any)
	if !ok || path["put"] == nil {
		t.Fatal("OpenAPI omits the workspace set-parent route")
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	if schemas["Workspace"].(map[string]any)["properties"].(map[string]any)["parent_id"] == nil {
		t.Fatal("OpenAPI omits the workspace parent_id")
	}
}
