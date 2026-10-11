// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// GET /v1/auth/effective-rights asks the live Authorizer, so the scoped grants and forbids
// this module projects reach it. These tests run it on the real REST path with the scoped
// engine mounted and compare each answer with what the enforced route then does for the
// same subject, so the read cannot drift from the 204 or the 403.

func rightsURL(subject, kind, id string) string {
	q := url.Values{"subject_type": {"user"}, "subject_id": {subject}, "kind": {kind}, "id": {id}}
	return "/v1/auth/effective-rights?" + q.Encode()
}

// rightsAt reads the rights of subject at the node as the administrator token: each right's
// state, and the path steps as "kind:ref".
func rightsAt(t *testing.T, h *harness, caller string, tenant model.TenantID, subject, kind, id string) (state map[string]string, path []string) {
	t.Helper()
	r := h.do("GET", rightsURL(subject, kind, id), caller, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("effective-rights = %d %s", r.code, r.raw)
	}
	state = map[string]string{}
	rights, _ := r.body["rights"].([]any)
	if len(rights) != len(auth.TrusteeRights()) {
		t.Fatalf("effective-rights lists %d rights, want %d: %s", len(rights), len(auth.TrusteeRights()), r.raw)
	}
	for _, it := range rights {
		m := it.(map[string]any)
		state[m["name"].(string)] = m["state"].(string)
	}
	steps, _ := r.body["path"].([]any)
	for _, it := range steps {
		m := it.(map[string]any)
		path = append(path, m["kind"].(string)+":"+m["ref"].(string))
	}
	return state, path
}

var writeRights = []string{"Write", "Create", "Erase", "Modify"}

func wantStates(t *testing.T, where string, state map[string]string, rights []string, want string) {
	t.Helper()
	for _, right := range rights {
		if state[right] != want {
			t.Errorf("%s: %s = %q, want %q", where, right, state[right], want)
		}
	}
}

// A grant scoped to one workspace shows only at nodes inside it, and each answer matches
// the enforced delete: held where the route answers 204, not held where it answers 403.
func TestEffectiveRightsFollowAWorkspaceScopedGrant(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	payments := h.createWorkspace(tenant, "payments")
	inPayments := h.createAgentIn(tenant, "pay-bot", payments)
	elsewhere := h.createAgentIn(tenant, "other-bot", "")
	uid, viewer := h.roleUser(admin, tenant, "viewer@acme.io", auth.RoleViewer)

	for _, a := range []model.Agent{inPayments, elsewhere} {
		state, _ := rightsAt(t, h, admin, tenant, uid, "agent", a.ID.String())
		wantStates(t, "before the grant at "+a.Name, state, writeRights, "not_held")
		wantStates(t, "before the grant at "+a.Name, state, []string{"Read"}, "held")
	}

	createGrant(t, h, admin, tenant, map[string]any{"subject_kind": "user", "subject_ref": uid, "role": auth.RoleEditor, "scope_tree": "workspace", "scope_ref": "payments"})

	stateIn, pathIn := rightsAt(t, h, admin, tenant, uid, "agent", inPayments.ID.String())
	stateOut, _ := rightsAt(t, h, admin, tenant, uid, "agent", elsewhere.ID.String())
	wantStates(t, "inside the grant's workspace", stateIn, writeRights, "held")
	wantStates(t, "outside the grant's workspace", stateOut, writeRights, "not_held")
	wantStates(t, "an editor grant", stateIn, []string{"Supervisor", "Access Control"}, "not_held")
	if want := []string{"workspace:payments", "agent:" + inPayments.ID.String()}; !reflect.DeepEqual(pathIn, want) {
		t.Errorf("path = %v, want %v", pathIn, want)
	}

	// The enforced route agrees, node by node.
	if r := h.do("DELETE", "/v1/agents/"+elsewhere.ID.String(), viewer, nil, hdr); r.code != http.StatusForbidden {
		t.Errorf("delete outside the workspace = %d %s, want 403 where the read says not held", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/agents/"+inPayments.ID.String(), viewer, nil, hdr); r.code != http.StatusNoContent {
		t.Errorf("delete inside the workspace = %d %s, want 204 where the read says held", r.code, r.raw)
	}
}

// A tenant-scope grant holds at every node and shows in whoami's set too: the two answers
// to "may this subject write agents" are the same fact.
func TestEffectiveRightsAgreeWithWhoamiForATenantScopedGrant(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "umbrella")

	payments := h.createWorkspace(tenant, "payments")
	inPayments := h.createAgentIn(tenant, "pay-bot", payments)
	elsewhere := h.createAgentIn(tenant, "other-bot", "")
	uid, viewer := h.roleUser(admin, tenant, "viewer@umbrella.io", auth.RoleViewer)

	if state, _ := rightsAt(t, h, admin, tenant, uid, "agent", elsewhere.ID.String()); state["Write"] != "not_held" {
		t.Fatal("precondition: a viewer must not hold Write by role")
	}
	if perms, _ := whoamiPerms(t, h, viewer, tenant); hasPerm(perms, "agent:write") {
		t.Fatal("precondition: whoami must not report agent:write for a viewer")
	}

	createGrant(t, h, admin, tenant, map[string]any{"subject_kind": "user", "subject_ref": uid, "role": auth.RoleEditor, "scope_tree": "tenant"})

	for _, a := range []model.Agent{inPayments, elsewhere} {
		state, _ := rightsAt(t, h, admin, tenant, uid, "agent", a.ID.String())
		wantStates(t, "a tenant-scoped editor grant at "+a.Name, state, writeRights, "held")
	}
	if perms, _ := whoamiPerms(t, h, viewer, tenant); !hasPerm(perms, "agent:write") {
		t.Errorf("whoami hides agent:write that the effective-rights read reports held; got %v", perms)
	}
}

// A grant on a folder shows at the resources under it, with the folder on their path, and
// not at a resource outside it.
func TestEffectiveRightsFollowAFolderScopedGrant(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "stark")
	uid, _ := h.roleUser(admin, tenant, "viewer@stark.io", auth.RoleViewer)

	var root, child, outside model.Resource
	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		if root, err = sc.Resources().Create(ctx, model.Resource{Name: "finance", Kind: "folder", URI: "file:///finance"}); err != nil {
			return err
		}
		if child, err = sc.Resources().Create(ctx, model.Resource{Name: "q3", Kind: "doc", URI: "file:///finance/q3", ParentID: root.ID}); err != nil {
			return err
		}
		outside, err = sc.Resources().Create(ctx, model.Resource{Name: "notes", Kind: "doc", URI: "file:///notes"})
		return err
	}); err != nil {
		t.Fatalf("seed resources: %v", err)
	}
	createGrant(t, h, admin, tenant, map[string]any{"subject_kind": "user", "subject_ref": uid, "role": auth.RoleEditor, "scope_tree": "folder", "scope_ref": root.ID.String()})

	stateIn, pathIn := rightsAt(t, h, admin, tenant, uid, "resource", child.ID.String())
	stateOut, _ := rightsAt(t, h, admin, tenant, uid, "resource", outside.ID.String())
	wantStates(t, "under the granted folder", stateIn, writeRights, "held")
	wantStates(t, "outside the granted folder", stateOut, writeRights, "not_held")
	if want := []string{"workspace:" + model.DefaultWorkspaceSlug, "folder:" + root.ID.String(), "resource:" + child.ID.String()}; !reflect.DeepEqual(pathIn, want) {
		t.Errorf("path = %v, want %v", pathIn, want)
	}
}

// A grant on an agent group shows at the agents in it, with the group on their path. A
// session of such an agent is decided without the group, as the AuthZEN reads decide it
// (a session inherits its agent's groups only on a route that opts in), so its path lists
// none either: the path is what the decision used.
func TestEffectiveRightsFollowAnAgentGroupScopedGrant(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "wayne")
	member := h.createAgentIn(tenant, "in-group", "")
	loner := h.createAgentIn(tenant, "no-group", "")
	h.addAgentToGroup(tenant, member.ID, "builders", "")
	uid, _ := h.roleUser(admin, tenant, "viewer@wayne.io", auth.RoleViewer)

	var session model.Session
	ctx := context.Background()
	if err := h.st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var err error
		session, err = sc.Sessions().Create(ctx, model.Session{AgentID: member.ID})
		return err
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	createGrant(t, h, admin, tenant, map[string]any{"subject_kind": "user", "subject_ref": uid, "role": auth.RoleEditor, "scope_tree": "agent_group", "scope_ref": "builders"})

	stateIn, pathIn := rightsAt(t, h, admin, tenant, uid, "agent", member.ID.String())
	stateOut, _ := rightsAt(t, h, admin, tenant, uid, "agent", loner.ID.String())
	wantStates(t, "an agent in the group", stateIn, writeRights, "held")
	wantStates(t, "an agent outside the group", stateOut, writeRights, "not_held")
	if want := []string{"workspace:" + model.DefaultWorkspaceSlug, "agent_group:builders", "agent:" + member.ID.String()}; !reflect.DeepEqual(pathIn, want) {
		t.Errorf("agent path = %v, want %v", pathIn, want)
	}

	stateSession, pathSession := rightsAt(t, h, admin, tenant, uid, "session", session.ID.String())
	wantStates(t, "a session of an agent in the group", stateSession, writeRights, "not_held")
	if want := []string{"workspace:" + model.DefaultWorkspaceSlug, "session:" + session.ID.String()}; !reflect.DeepEqual(pathSession, want) {
		t.Errorf("session path = %v, want %v", pathSession, want)
	}
}

// A forbid turns a right the role grants into "not_held", and the enforced route agrees.
// The engine's own denial is typed evidence, so it is "not_held" and never "unknown".
func TestEffectiveRightsSeeAForbid(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	payments := h.createWorkspace(tenant, "payments")
	inPayments := h.createAgentIn(tenant, "pay-bot", payments)
	elsewhere := h.createAgentIn(tenant, "other-bot", "")
	uid, editor := h.roleUser(admin, tenant, "editor@acme.io", auth.RoleEditor)

	state, _ := rightsAt(t, h, admin, tenant, uid, "agent", inPayments.ID.String())
	wantStates(t, "before the forbid", state, writeRights, "held")

	h.publishGrant(admin, tenant, `forbid(principal in Role::"editor", action == Action::"agent:write", resource) when { resource in Workspace::"payments" };`)

	stateIn, _ := rightsAt(t, h, admin, tenant, uid, "agent", inPayments.ID.String())
	stateOut, _ := rightsAt(t, h, admin, tenant, uid, "agent", elsewhere.ID.String())
	wantStates(t, "inside the forbidden workspace", stateIn, writeRights, "not_held")
	wantStates(t, "outside the forbidden workspace", stateOut, writeRights, "held")
	if r := h.do("DELETE", "/v1/agents/"+inPayments.ID.String(), editor, nil, hdr); r.code != http.StatusForbidden {
		t.Errorf("delete inside the forbidden workspace = %d %s, want 403 where the read says not held", r.code, r.raw)
	}
}

// The caller must be able to read the node itself: a forbid over a workspace applies to the
// administrator asking, who is told no more than an ordinary read of the node would tell.
func TestEffectiveRightsRefuseANodeTheCallerCannotRead(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	payments := h.createWorkspace(tenant, "payments")
	inPayments := h.createAgentIn(tenant, "pay-bot", payments)
	elsewhere := h.createAgentIn(tenant, "other-bot", "")
	uid, _ := h.roleUser(admin, tenant, "viewer@acme.io", auth.RoleViewer)
	_, tenantAdmin := h.roleUser(admin, tenant, "ops@acme.io", auth.RoleAdmin)

	for _, a := range []model.Agent{inPayments, elsewhere} {
		if r := h.do("GET", rightsURL(uid, "agent", a.ID.String()), tenantAdmin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Fatalf("control: a tenant admin reads the rights at %s = %d %s", a.Name, r.code, r.raw)
		}
	}
	h.publishGrant(admin, tenant, `forbid(principal in Role::"admin", action == Action::"agent:read", resource) when { resource in Workspace::"payments" };`)

	if r := h.do("GET", rightsURL(uid, "agent", inPayments.ID.String()), tenantAdmin, nil, tenantHdr(tenant)); r.code != http.StatusNotFound {
		t.Errorf("a node the caller is forbidden to read = %d %s, want 404", r.code, r.raw)
	}
	if r := h.do("GET", rightsURL(uid, "agent", elsewhere.ID.String()), tenantAdmin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("a node outside the forbidden workspace = %d %s, want 200", r.code, r.raw)
	}
}

// With the scoped engine mounted, a workspace-confined administrator is still refused: the
// answer spans the whole tenant. The same read by an unconfined administrator is the control.
func TestEffectiveRightsRefuseAConfinedAdminUnderTheScopedEngine(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "wayne")
	payments := h.createWorkspace(tenant, "payments")
	agent := h.createAgentIn(tenant, "pay-bot", payments)
	uid, _ := h.roleUser(admin, tenant, "viewer@wayne.io", auth.RoleViewer)
	_, unconfined := h.roleUser(admin, tenant, "ops@wayne.io", auth.RoleAdmin)

	if r := h.do("POST", "/v1/users", admin, map[string]any{
		"email": "confined@wayne.io", "password": "confinedpass1", "tenant": tenant.String(),
		"role": auth.RoleAdmin, "workspace_id": payments.String(),
	}, nil); r.code != http.StatusCreated {
		t.Fatalf("create confined admin = %d %s", r.code, r.raw)
	}
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "confined@wayne.io", "password": "confinedpass1"}, nil)
	if lr.code != http.StatusOK {
		t.Fatalf("confined login = %d %s", lr.code, lr.raw)
	}
	confined := lr.body["token"].(string)

	target := rightsURL(uid, "agent", agent.ID.String())
	if r := h.do("GET", target, unconfined, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("control: an unconfined admin = %d %s, want 200", r.code, r.raw)
	}
	if r := h.do("GET", target, confined, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("confined admin = %d %s, want 403", r.code, r.raw)
	}
}
