// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/models"
)

// recordingBudgetGate captures the BudgetDims the routing chain hands the FinOps seam, so
// a test can prove the budget tie-in (the acting session_ref crosses the seam so an
// identity-scoped Claude budget can be resolved). It allows by default.
type recordingBudgetGate struct {
	last models.BudgetDims
	deny bool
	// denyWhenSession denies ONLY when an identity-bearing SessionRef is present — a stand-in
	// for an identity-scoped budget, to prove the session_ref actually drives the
	// decision rather than just being plumbed.
	denyWhenSession bool
	action          string
	budgetR         string
}

func (g *recordingBudgetGate) Check(_ context.Context, _ model.TenantID, dims models.BudgetDims) (models.BudgetDecision, error) {
	g.last = dims
	if g.deny || (g.denyWhenSession && dims.SessionRef != "") {
		action := g.action
		if action == "" {
			action = "block"
		}
		return models.BudgetDecision{Allowed: false, Action: action, BudgetRef: g.budgetR, Reason: "capped"}, nil
	}
	return models.BudgetDecision{Allowed: true}, nil
}

func createModelGroup(t *testing.T, h *harness, tok string, tenant model.TenantID, body map[string]any) resp {
	t.Helper()
	return h.do("POST", "/v1/m/models/model-groups", tok, body, tenantHdr(tenant))
}

func createModelAccess(t *testing.T, h *harness, tok string, tenant model.TenantID, body map[string]any) resp {
	t.Helper()
	return h.do("POST", "/v1/m/models/model-access", tok, body, tenantHdr(tenant))
}

// --- model-group CRUD --------------------------------------------------------

func TestModelGroupCRUD(t *testing.T) {
	h := newHarness(t, models.New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// Create — hybrid membership: explicit refs + a family selector.
	r := createModelGroup(t, h, admin, tenant, map[string]any{
		"name": "frontier", "member_refs": []string{"claude-opus-4-8"}, "family_selectors": []string{"claude-sonnet"},
		"description": "frontier set",
	})
	if r.code != http.StatusCreated {
		t.Fatalf("create model-group = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	// A group with NO members and NO selectors is rejected.
	if r := createModelGroup(t, h, admin, tenant, map[string]any{"name": "empty"}); r.code != http.StatusBadRequest {
		t.Errorf("empty model-group = %d, want 400", r.code)
	}
	// Duplicate name ⇒ 409.
	if r := createModelGroup(t, h, admin, tenant, map[string]any{"name": "frontier", "member_refs": []string{"x"}}); r.code != http.StatusConflict {
		t.Errorf("duplicate group name = %d, want 409", r.code)
	}
	// List.
	if r := h.do("GET", "/v1/m/models/model-groups", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("list groups = %d %s", r.code, r.raw)
	}
	// Update: name is immutable (forced to the stored value); members can change.
	r = h.do("PUT", "/v1/m/models/model-groups/"+id, admin, map[string]any{
		"name": "renamed", "member_refs": []string{"claude-opus-4-8", "claude-haiku-4-5"},
	}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("update group = %d %s", r.code, r.raw)
	}
	if r.body["name"] != "frontier" {
		t.Errorf("group name must be immutable on update, got %v", r.body["name"])
	}
	// Delete.
	if r := h.do("DELETE", "/v1/m/models/model-groups/"+id, admin, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Errorf("delete group = %d %s", r.code, r.raw)
	}
}

// --- model-access CRUD + validation + permission tier ------------------------

func TestModelAccessCRUDAndValidation(t *testing.T) {
	h := newHarness(t, models.New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	// A target_kind=model_group grant naming a non-existent group is rejected.
	if r := createModelAccess(t, h, admin, tenant, map[string]any{
		"subject_kind": "role", "subject_ref": "viewer", "target_kind": "model_group", "target_ref": "ghost",
	}); r.code != http.StatusBadRequest {
		t.Errorf("grant to missing group = %d, want 400 (%s)", r.code, r.raw)
	}
	// Create the group, then the grant resolves.
	if r := createModelGroup(t, h, admin, tenant, map[string]any{"name": "frontier", "member_refs": []string{"claude-opus-4-8"}}); r.code != http.StatusCreated {
		t.Fatalf("create group = %d %s", r.code, r.raw)
	}
	r := createModelAccess(t, h, admin, tenant, map[string]any{
		"subject_kind": "role", "subject_ref": "viewer", "target_kind": "model_group", "target_ref": "frontier",
		"surfaces": []string{"direct"},
	})
	if r.code != http.StatusCreated {
		t.Fatalf("create model-access = %d %s", r.code, r.raw)
	}
	id := r.body["id"].(string)

	// Validation: bad subject_kind, bad target_kind, bad surface.
	for _, bad := range []map[string]any{
		{"subject_kind": "team", "subject_ref": "x", "target_kind": "model", "target_ref": "claude-opus-4-8"},
		{"subject_kind": "user", "subject_ref": "x", "target_kind": "deployment", "target_ref": "y"},
		{"subject_kind": "user", "subject_ref": "x", "target_kind": "model", "target_ref": "claude-opus-4-8", "surfaces": []string{"sky-net"}},
		{"subject_kind": "user", "target_kind": "model", "target_ref": "claude-opus-4-8"}, // missing subject_ref
	} {
		if r := createModelAccess(t, h, admin, tenant, bad); r.code != http.StatusBadRequest {
			t.Errorf("invalid grant %v = %d, want 400", bad, r.code)
		}
	}
	// Duplicate (subject, target, workspace) ⇒ 409.
	if r := createModelAccess(t, h, admin, tenant, map[string]any{
		"subject_kind": "role", "subject_ref": "viewer", "target_kind": "model_group", "target_ref": "frontier",
	}); r.code != http.StatusConflict {
		t.Errorf("duplicate grant = %d, want 409", r.code)
	}
	// List + delete.
	if r := h.do("GET", "/v1/m/models/model-access?subject_ref=viewer", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("list model-access = %d %s", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/m/models/model-access/"+id, admin, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Errorf("delete model-access = %d %s", r.code, r.raw)
	}
}

// TestModelAccessAuthoringIsAdminTier: authoring a model-access grant is admin-tier — an
// editor (who CAN write a routing policy) cannot widen who may use a model.
func TestModelAccessAuthoringIsAdminTier(t *testing.T) {
	h := newHarness(t, models.New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "editor@x.io", "editor")

	r := createModelAccess(t, h, editor, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": "u1", "target_kind": "model", "target_ref": "claude-opus-4-8",
	})
	if r.code != http.StatusForbidden {
		t.Fatalf("editor authoring a model-access grant = %d, want 403 (admin-tier)", r.code)
	}
}

func TestModelGovernanceAudits(t *testing.T) {
	h := newHarness(t, models.New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	gr := createModelGroup(t, h, admin, tenant, map[string]any{"name": "g", "member_refs": []string{"claude-opus-4-8"}})
	if gr.code != http.StatusCreated {
		t.Fatalf("create group = %d %s", gr.code, gr.raw)
	}
	gid := gr.body["id"].(string)
	ga := createModelAccess(t, h, admin, tenant, map[string]any{
		"subject_kind": "role", "subject_ref": "viewer", "target_kind": "model_group", "target_ref": "g",
	})
	if ga.code != http.StatusCreated {
		t.Fatalf("create grant = %d %s", ga.code, ga.raw)
	}
	aid := ga.body["id"].(string)
	// Exercise update + delete on both entities (the grant first, so the group is no
	// longer referenced and may be deleted).
	h.do("PUT", "/v1/m/models/model-groups/"+gid, admin, map[string]any{"name": "g", "member_refs": []string{"claude-opus-4-8", "claude-haiku-4-5"}}, tenantHdr(tenant))
	h.do("PUT", "/v1/m/models/model-access/"+aid, admin, map[string]any{"subject_kind": "role", "subject_ref": "editor", "target_kind": "model_group", "target_ref": "g"}, tenantHdr(tenant))
	h.do("DELETE", "/v1/m/models/model-access/"+aid, admin, nil, tenantHdr(tenant))
	h.do("DELETE", "/v1/m/models/model-groups/"+gid, admin, nil, tenantHdr(tenant))

	// Every mutation self-audits to the real principal (not the SYSTEM actor).
	got := h.auditActions(tenant)
	want := map[string]bool{
		"models.model_group.create": false, "models.model_group.update": false, "models.model_group.delete": false,
		"models.model_access.create": false, "models.model_access.update": false, "models.model_access.delete": false,
	}
	for _, a := range got {
		if _, ok := want[a]; ok {
			want[a] = true
		}
	}
	for action, seen := range want {
		if !seen {
			t.Errorf("missing self-audit for %q (got %v)", action, got)
		}
	}
}

// TestModelGroupDeleteRefusedWhileReferenced: a model-group cannot be deleted while a grant
// targets it (symmetric with create-time validation) — else the grant would silently
// confine its subjects to an empty set (deny-all).
func TestModelGroupDeleteRefusedWhileReferenced(t *testing.T) {
	h := newHarness(t, models.New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	gid := createModelGroup(t, h, admin, tenant, map[string]any{"name": "frontier", "member_refs": []string{"claude-opus-4-8"}}).body["id"].(string)
	aid := createModelAccess(t, h, admin, tenant, map[string]any{
		"subject_kind": "role", "subject_ref": "viewer", "target_kind": "model_group", "target_ref": "frontier",
	}).body["id"].(string)
	if r := h.do("DELETE", "/v1/m/models/model-groups/"+gid, admin, nil, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Fatalf("delete referenced group = %d, want 409 %s", r.code, r.raw)
	}
	h.do("DELETE", "/v1/m/models/model-access/"+aid, admin, nil, tenantHdr(tenant))
	if r := h.do("DELETE", "/v1/m/models/model-groups/"+gid, admin, nil, tenantHdr(tenant)); r.code != http.StatusNoContent {
		t.Fatalf("delete now-unreferenced group = %d, want 204 %s", r.code, r.raw)
	}
}

// TestModelAccessWorkspaceRefValidated: a workspace_ref that names no workspace is rejected
// (typo ⇒ silently-dead grant); the reserved "default" slug is always valid.
func TestModelAccessWorkspaceRefValidated(t *testing.T) {
	h := newHarness(t, models.New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	if r := createModelAccess(t, h, admin, tenant, map[string]any{
		"subject_kind": "role", "subject_ref": "viewer", "target_kind": "model", "target_ref": "claude-opus-4-8", "workspace_ref": "ghost",
	}); r.code != http.StatusBadRequest {
		t.Fatalf("unknown workspace_ref = %d, want 400 %s", r.code, r.raw)
	}
	// Seed the workspace directly (the create-workspace API is AAL3-gated, out of scope here).
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		_, e := sc.Workspaces().Create(context.Background(), model.Workspace{Name: "Payments", Slug: "payments", Status: model.StatusActive})
		return e
	}); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if r := createModelAccess(t, h, admin, tenant, map[string]any{
		"subject_kind": "role", "subject_ref": "viewer", "target_kind": "model", "target_ref": "claude-opus-4-8", "workspace_ref": "payments",
	}); r.code != http.StatusCreated {
		t.Fatalf("existing workspace_ref = %d, want 201 %s", r.code, r.raw)
	}
	if r := createModelAccess(t, h, admin, tenant, map[string]any{
		"subject_kind": "role", "subject_ref": "viewer", "target_kind": "model", "target_ref": "claude-sonnet-4-6", "workspace_ref": "default",
	}); r.code != http.StatusCreated {
		t.Fatalf("reserved default workspace_ref = %d, want 201 %s", r.code, r.raw)
	}
}

// TestExecuteModelAccessUserSubject: a user-subject grant confines THAT user end-to-end.
func TestExecuteModelAccessUserSubject(t *testing.T) {
	ex := &stubExecutor{res: models.ExecuteResult{Text: "ok"}}
	h := newHarness(t, models.New(models.WithExecutor(ex)))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	// A non-superadmin user with role admin (admin-tier needed to execute); capture its id.
	ru := h.do("POST", "/v1/users", admin, map[string]any{"email": "u@x.io", "password": "memberpass1"}, nil)
	if ru.code != http.StatusCreated {
		t.Fatalf("create user = %d %s", ru.code, ru.raw)
	}
	uid := ru.body["id"].(string)
	if r := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": "admin"}, nil); r.code != http.StatusCreated {
		t.Fatalf("membership = %d %s", r.code, r.raw)
	}
	utok := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "u@x.io", "password": "memberpass1"}, nil).body["token"].(string)
	seedModel(t, h, tenant, "anthropic", "claude-opus-4-8")
	policy := createRoutingPolicy(t, h, admin, tenant)

	// Grant THIS user (by id) access only to sonnet ⇒ executing opus is denied.
	if r := createModelAccess(t, h, admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": uid, "target_kind": "model", "target_ref": "claude-sonnet-4-6",
	}); r.code != http.StatusCreated {
		t.Fatalf("author user grant = %d %s", r.code, r.raw)
	}
	if r := execAs(t, h, utok, tenant, policy, ""); r.code != http.StatusForbidden {
		t.Fatalf("user-subject governed to sonnet, executing opus = %d, want 403 %s", r.code, r.raw)
	}
}

// TestExecuteBudgetIdentityDeny proves the session_ref actually DRIVES an identity-scoped
// budget decision (not just plumbing): the gate denies only when a session_ref is present.
func TestExecuteBudgetIdentityDeny(t *testing.T) {
	bg := &recordingBudgetGate{denyWhenSession: true}
	ex := &stubExecutor{res: models.ExecuteResult{Text: "ok"}}
	h := newHarness(t, models.New(models.WithExecutor(ex), models.WithBudgetGate(bg)))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedModel(t, h, tenant, "anthropic", "claude-opus-4-8")
	policy := createRoutingPolicy(t, h, admin, tenant)
	exec := "/v1/m/models/routing-policies/" + policy + "/execute"
	// WITH session_ref ⇒ the identity budget resolves and blocks (402).
	if r := h.do("POST", exec, admin, map[string]any{"input": "hi", "session_ref": "sess-x"}, tenantHdr(tenant)); r.code != http.StatusPaymentRequired {
		t.Errorf("identity budget with session = %d, want 402 %s", r.code, r.raw)
	}
	// WITHOUT session_ref ⇒ no identity to bind ⇒ the identity budget is inert (200).
	if r := h.do("POST", exec, admin, map[string]any{"input": "hi"}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("no session ⇒ identity budget inert = %d, want 200 %s", r.code, r.raw)
	}
}

// --- end-to-end enforcement on the execute (selection) path ------------------

// execAs runs the routing policy as tok with an optional surface, returning the status.
func execAs(t *testing.T, h *harness, tok string, tenant model.TenantID, policyID, surface string) resp {
	t.Helper()
	body := map[string]any{"input": "hello", "session_ref": "sess-1"}
	if surface != "" {
		body["surface"] = surface
	}
	return h.do("POST", "/v1/m/models/routing-policies/"+policyID+"/execute", tok, body, tenantHdr(tenant))
}

func TestExecuteModelAccessEnforced(t *testing.T) {
	ex := &stubExecutor{res: models.ExecuteResult{Text: "ok"}}
	h := newHarness(t, models.New(models.WithExecutor(ex)))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	mgr := h.roleToken(admin, tenant, "mgr@x.io", "admin") // non-superadmin, can execute (admin-tier)
	seedModel(t, h, tenant, "anthropic", "claude-opus-4-8")
	policy := createRoutingPolicy(t, h, admin, tenant)

	// 1) Ungoverned (no grants): the governed admin-role user executes successfully.
	if r := execAs(t, h, mgr, tenant, policy, ""); r.code != http.StatusOK {
		t.Fatalf("ungoverned execute = %d, want 200 (opt-in: no grants ⇒ not governed) %s", r.code, r.raw)
	}

	// 2) Author a grant confining role:admin to a DIFFERENT model ⇒ opus is now denied.
	if r := createModelAccess(t, h, admin, tenant, map[string]any{
		"subject_kind": "role", "subject_ref": "admin", "target_kind": "model", "target_ref": "claude-sonnet-4-6",
	}); r.code != http.StatusCreated {
		t.Fatalf("author grant = %d %s", r.code, r.raw)
	}
	r := execAs(t, h, mgr, tenant, policy, "")
	if r.code != http.StatusForbidden {
		t.Fatalf("governed user executing an ungranted model = %d, want 403 %s", r.code, r.raw)
	}
	if ex.calls != 1 {
		t.Errorf("the executor must NOT be reached on a model-access denial (calls=%d, want 1 from step 1)", ex.calls)
	}
}

func TestExecuteModelAccessGrantedViaGroupAndSurface(t *testing.T) {
	ex := &stubExecutor{res: models.ExecuteResult{Text: "ok"}}
	h := newHarness(t, models.New(models.WithExecutor(ex)))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	mgr := h.roleToken(admin, tenant, "mgr@x.io", "admin")
	seedModel(t, h, tenant, "anthropic", "claude-opus-4-8")
	policy := createRoutingPolicy(t, h, admin, tenant)

	// A model-group grant concedes the set; the grant is api(direct)-only.
	if r := createModelGroup(t, h, admin, tenant, map[string]any{"name": "frontier", "member_refs": []string{"claude-opus-4-8"}}); r.code != http.StatusCreated {
		t.Fatalf("create group = %d %s", r.code, r.raw)
	}
	if r := createModelAccess(t, h, admin, tenant, map[string]any{
		"subject_kind": "role", "subject_ref": "admin", "target_kind": "model_group", "target_ref": "frontier",
		"surfaces": []string{"direct"},
	}); r.code != http.StatusCreated {
		t.Fatalf("author grant = %d %s", r.code, r.raw)
	}

	// Granted via the group, default/direct surface ⇒ allowed.
	if r := execAs(t, h, mgr, tenant, policy, ""); r.code != http.StatusOK {
		t.Errorf("group-granted model (no surface) = %d, want 200 %s", r.code, r.raw)
	}
	if r := execAs(t, h, mgr, tenant, policy, "direct"); r.code != http.StatusOK {
		t.Errorf("group-granted model on direct = %d, want 200 %s", r.code, r.raw)
	}
	// The grant is api-only ⇒ a Bedrock request is denied at selection.
	if r := execAs(t, h, mgr, tenant, policy, "bedrock-mantle"); r.code != http.StatusForbidden {
		t.Errorf("group-granted model on Bedrock (api-only grant) = %d, want 403 %s", r.code, r.raw)
	}
}

// TestExecuteBudgetTieInSessionRef proves the budget tie-in: the acting session_ref
// crosses the FinOps seam so an identity-scoped Claude budget can be resolved/enforced.
func TestExecuteBudgetTieInSessionRef(t *testing.T) {
	bg := &recordingBudgetGate{}
	ex := &stubExecutor{res: models.ExecuteResult{Text: "ok"}}
	h := newHarness(t, models.New(models.WithExecutor(ex), models.WithBudgetGate(bg)))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedModel(t, h, tenant, "anthropic", "claude-opus-4-8")
	policy := createRoutingPolicy(t, h, admin, tenant)

	if r := execAs(t, h, admin, tenant, policy, ""); r.code != http.StatusOK {
		t.Fatalf("execute = %d %s", r.code, r.raw)
	}
	if bg.last.SessionRef != "sess-1" {
		t.Errorf("budget gate saw session_ref %q, want sess-1 (identity-budget tie-in)", bg.last.SessionRef)
	}
	if bg.last.ModelRef != "claude-opus-4-8" {
		t.Errorf("budget gate saw model %q, want claude-opus-4-8", bg.last.ModelRef)
	}

	// And a capped budget denies the spend (defense-in-depth, 402 block).
	bg.deny, bg.action = true, "block"
	if r := execAs(t, h, admin, tenant, policy, ""); r.code != http.StatusPaymentRequired {
		t.Errorf("capped budget execute = %d, want 402", r.code)
	}
}
