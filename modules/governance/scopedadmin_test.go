// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// rbac POSTs to /v1/m/governance/rbac/<path> with the tenant header.
func (h *harness) rbac(method, path, token string, tenant model.TenantID, body any) resp {
	return h.do(method, "/v1/m/governance/rbac/"+path, token, body, tenantHdr(tenant))
}

// principalOf authenticates a session/token string into a Principal (for engine-level
// Scoped() assertions that need the real UserID parent).
func (h *harness) principalOf(token string) auth.Principal {
	h.t.Helper()
	p, err := h.authr.Authenticate(context.Background(), token)
	if err != nil {
		h.t.Fatalf("authenticate: %v", err)
	}
	return p
}

// --- e2e: a workspace-admin grant authored via REST enforces on the real path -------

func TestScopedAdminWorkspaceAdminE2E(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin() // the first user is a superadmin (root)
	tenant := h.createOrg(admin, "acme")
	hdr := tenantHdr(tenant)

	payments := h.createWorkspace(tenant, "payments")
	inPayments := h.createAgentIn(tenant, "pay-bot", payments)
	inDefault := h.createAgentIn(tenant, "default-bot", model.ID(""))
	uid, viewer := h.roleUser(admin, tenant, "v@acme.io", auth.RoleViewer)

	// Baseline: a viewer cannot delete (agent:write) any agent.
	if r := h.do("DELETE", "/v1/agents/"+inPayments.ID.String(), viewer, nil, hdr); r.code != http.StatusForbidden {
		t.Fatalf("baseline viewer delete must be 403, got %d %s", r.code, r.raw)
	}

	// The superadmin makes the viewer a workspace-admin of payments (subject=user).
	r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": uid, "role": "admin",
		"scope_tree": "workspace", "scope_ref": "payments",
	})
	if r.code != http.StatusCreated {
		t.Fatalf("create scoped-admin grant = %d %s", r.code, r.raw)
	}

	// Now the viewer CAN delete an agent in payments...
	if r := h.do("DELETE", "/v1/agents/"+inPayments.ID.String(), viewer, nil, hdr); r.code != http.StatusNoContent {
		t.Errorf("workspace-admin must delete in its workspace, got %d %s", r.code, r.raw)
	}
	// ...but NOT an agent in the default workspace.
	if r := h.do("DELETE", "/v1/agents/"+inDefault.ID.String(), viewer, nil, hdr); r.code != http.StatusForbidden {
		t.Errorf("workspace-admin must not reach another workspace, got %d %s", r.code, r.raw)
	}

	// Every delegation is audited.
	if !contains(h.auditActions(tenant), "governance.rbac.grant") {
		t.Error("the scoped-admin grant must be audited (governance.rbac.grant)")
	}
}

// --- a custom role + permission-group confers EXACTLY its bundle ---------------------

func TestScopedAdminCustomRoleExactBundle(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "bundleco")

	uid, wtok := h.roleUser(admin, tenant, "w@bundleco.io", auth.RoleViewer)

	if r := h.rbac("POST", "permission-groups", admin, tenant, map[string]any{
		"name": "billing", "permissions": []string{"cost:read"},
	}); r.code != http.StatusCreated {
		t.Fatalf("create permission-group = %d %s", r.code, r.raw)
	}
	if r := h.rbac("POST", "roles", admin, tenant, map[string]any{
		"name": "auditor", "permissions": []string{"finding:read"}, "groups": []string{"billing"},
	}); r.code != http.StatusCreated {
		t.Fatalf("create custom role = %d %s", r.code, r.raw)
	}
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": uid, "role": "auditor", "role_custom": true, "scope_tree": "tenant",
	}); r.code != http.StatusCreated {
		t.Fatalf("assign custom role = %d %s", r.code, r.raw)
	}

	p := h.principalOf(wtok)
	dummy := model.ID("01HZZZ") // tenant-scope grant matches any resource; id is incidental
	if sd := h.scoped(tenant, p, "finding:read", dummy); sd.Effect != auth.EffectGrant {
		t.Errorf("finding:read (direct perm) must be GRANTED, got %v (%s)", sd.Effect, sd.Reason)
	}
	if sd := h.scoped(tenant, p, "cost:read", dummy); sd.Effect != auth.EffectGrant {
		t.Errorf("cost:read (from the included group) must be GRANTED, got %v (%s)", sd.Effect, sd.Reason)
	}
	if sd := h.scoped(tenant, p, "agent:write", dummy); sd.Effect != auth.EffectAbstain {
		t.Errorf("agent:write (outside the bundle) must ABSTAIN, got %v (%s)", sd.Effect, sd.Reason)
	}
}

// --- the per-scope ceiling: a tenant admin cannot delegate above its own role --------

func TestScopedAdminCeilingTenantAdmin(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "ceilco")
	h.createWorkspace(tenant, "w")
	_, tadmin := h.roleUser(admin, tenant, "ta@ceilco.io", auth.RoleAdmin)
	vUID, _ := h.roleUser(admin, tenant, "v@ceilco.io", auth.RoleViewer)

	// A tenant admin (read+write) MAY delegate an editor within a workspace.
	if r := h.rbac("POST", "grants", tadmin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": vUID, "role": "editor", "scope_tree": "workspace", "scope_ref": "w",
	}); r.code != http.StatusCreated {
		t.Fatalf("tenant admin must delegate editor@workspace, got %d %s", r.code, r.raw)
	}
	// ...but NOT an owner (which confers the resource admin verb the actor lacks).
	if r := h.rbac("POST", "grants", tadmin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": vUID, "role": "owner", "scope_tree": "workspace", "scope_ref": "w",
	}); r.code != http.StatusForbidden {
		t.Errorf("tenant admin must NOT delegate owner (ceiling), got %d %s", r.code, r.raw)
	}
}

// --- scoped-admin SUB-delegation, bounded to its scope (incl. agent-group containment)

func TestScopedAdminSubDelegation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "subco")

	wA := h.createWorkspace(tenant, "wa")
	wB := h.createWorkspace(tenant, "wb")
	agentA := h.createAgentIn(tenant, "a", wA)
	agentB := h.createAgentIn(tenant, "b", wB)
	h.addAgentToGroup(tenant, agentA.ID, "bots-a", wA)
	h.addAgentToGroup(tenant, agentB.ID, "bots-b", wB)

	uUID, uTok := h.roleUser(admin, tenant, "u@subco.io", auth.RoleViewer)
	vUID, _ := h.roleUser(admin, tenant, "v@subco.io", auth.RoleViewer)

	// The superadmin makes U a workspace-admin of wa (admin-capable ⇒ may sub-delegate).
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": uUID, "role": "admin", "scope_tree": "workspace", "scope_ref": "wa",
	}); r.code != http.StatusCreated {
		t.Fatalf("seed workspace-admin = %d %s", r.code, r.raw)
	}

	// U may sub-delegate an editor within wa.
	if r := h.rbac("POST", "grants", uTok, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": vUID, "role": "editor", "scope_tree": "workspace", "scope_ref": "wa",
	}); r.code != http.StatusCreated {
		t.Errorf("scoped-admin must sub-delegate editor@wa, got %d %s", r.code, r.raw)
	}
	// U may NOT delegate in a different workspace.
	if r := h.rbac("POST", "grants", uTok, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": vUID, "role": "editor", "scope_tree": "workspace", "scope_ref": "wb",
	}); r.code != http.StatusForbidden {
		t.Errorf("scoped-admin must NOT delegate outside its workspace, got %d %s", r.code, r.raw)
	}
	// U may NOT grant owner even inside wa (perm ceiling).
	if r := h.rbac("POST", "grants", uTok, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": vUID, "role": "owner", "scope_tree": "workspace", "scope_ref": "wa",
	}); r.code != http.StatusForbidden {
		t.Errorf("scoped-admin must NOT grant owner (perm ceiling), got %d %s", r.code, r.raw)
	}
	// Agent-group containment: U MAY delegate to a group whose workspace is wa...
	if r := h.rbac("POST", "grants", uTok, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": vUID, "role": "editor", "scope_tree": "agent_group", "scope_ref": "bots-a",
	}); r.code != http.StatusCreated {
		t.Errorf("scoped-admin must delegate to a group inside its workspace, got %d %s", r.code, r.raw)
	}
	// ...but NOT to a group whose workspace is wb.
	if r := h.rbac("POST", "grants", uTok, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": vUID, "role": "editor", "scope_tree": "agent_group", "scope_ref": "bots-b",
	}); r.code != http.StatusForbidden {
		t.Errorf("scoped-admin must NOT delegate to a group outside its workspace, got %d %s", r.code, r.raw)
	}
	_ = wB
}

// --- the endpoint RBAC gate: a non-admin cannot reach the write API ------------------

func TestScopedAdminEndpointGate(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "gateco")
	vUID, viewer := h.roleUser(admin, tenant, "v@gateco.io", auth.RoleViewer)

	// A viewer holds governance:rbac:read (verb tier) → the catalog is readable.
	if r := h.rbac("GET", "catalog", viewer, tenant, nil); r.code != http.StatusOK {
		t.Errorf("viewer must read the catalog, got %d %s", r.code, r.raw)
	}
	// ...but not governance:rbac:admin → it cannot author a grant (403 at the gate).
	if r := h.rbac("POST", "grants", viewer, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": vUID, "role": "editor", "scope_tree": "tenant",
	}); r.code != http.StatusForbidden {
		t.Errorf("viewer must not reach the grant API, got %d %s", r.code, r.raw)
	}
}

// --- validation negatives (deny-closed) ---------------------------------------------

func TestScopedAdminValidationNegatives(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "valco")
	uUID, _ := h.roleUser(admin, tenant, "u@valco.io", auth.RoleViewer)

	// Unknown workspace slug.
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": uUID, "role": "editor", "scope_tree": "workspace", "scope_ref": "ghost",
	}); r.code != http.StatusBadRequest {
		t.Errorf("unknown workspace must be 400, got %d %s", r.code, r.raw)
	}
	// Unknown custom role.
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": uUID, "role": "ghostrole", "role_custom": true, "scope_tree": "tenant",
	}); r.code != http.StatusBadRequest {
		t.Errorf("unknown custom role must be 400, got %d %s", r.code, r.raw)
	}
	// A tenant scope must carry no ref.
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": uUID, "role": "editor", "scope_tree": "tenant", "scope_ref": "x",
	}); r.code != http.StatusBadRequest {
		t.Errorf("tenant scope with a ref must be 400, got %d %s", r.code, r.raw)
	}
	// A role subject must name a built-in role.
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "role", "subject_ref": "notarole", "role": "editor", "scope_tree": "tenant",
	}); r.code != http.StatusBadRequest {
		t.Errorf("bad role subject must be 400, got %d %s", r.code, r.raw)
	}
	// A non-catalog permission in a custom role.
	if r := h.rbac("POST", "roles", admin, tenant, map[string]any{
		"name": "bad", "permissions": []string{"agent:delete"},
	}); r.code != http.StatusBadRequest {
		t.Errorf("non-catalog perm must be 400, got %d %s", r.code, r.raw)
	}
	// A grant whose role confers no permissions.
	if r := h.rbac("POST", "roles", admin, tenant, map[string]any{
		"name": "empty", "permissions": []string{},
	}); r.code != http.StatusCreated {
		t.Fatalf("empty role create = %d %s", r.code, r.raw)
	}
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": uUID, "role": "empty", "role_custom": true, "scope_tree": "tenant",
	}); r.code != http.StatusBadRequest {
		t.Errorf("grant conferring no permissions must be 400, got %d %s", r.code, r.raw)
	}
}

// --- a definition still in use cannot be deleted ------------------------------------

func TestScopedAdminDeleteRoleInUse(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "delco")
	uUID, _ := h.roleUser(admin, tenant, "u@delco.io", auth.RoleViewer)

	if r := h.rbac("POST", "roles", admin, tenant, map[string]any{
		"name": "reader", "permissions": []string{"agent:read"},
	}); r.code != http.StatusCreated {
		t.Fatalf("create role = %d %s", r.code, r.raw)
	}
	gr := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": uUID, "role": "reader", "role_custom": true, "scope_tree": "tenant",
	})
	if gr.code != http.StatusCreated {
		t.Fatalf("assign role = %d %s", gr.code, gr.raw)
	}
	grantID, _ := gr.body["id"].(string)

	// In use → 409.
	if r := h.rbac("DELETE", "roles/reader", admin, tenant, nil); r.code != http.StatusConflict {
		t.Errorf("deleting a role in use must be 409, got %d %s", r.code, r.raw)
	}
	// Revoke, then delete succeeds.
	if r := h.rbac("DELETE", "grants/"+grantID, admin, tenant, nil); r.code != http.StatusNoContent {
		t.Fatalf("revoke grant = %d %s", r.code, r.raw)
	}
	if r := h.rbac("DELETE", "roles/reader", admin, tenant, nil); r.code != http.StatusNoContent {
		t.Errorf("deleting an unused role must be 204, got %d %s", r.code, r.raw)
	}
	if !contains(h.auditActions(tenant), "governance.rbac.revoke") {
		t.Error("revoke must be audited (governance.rbac.revoke)")
	}
}

// --- the catalog endpoint feeds the role editor --------------------------------

func TestScopedAdminCatalog(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "catco")

	r := h.rbac("GET", "catalog", admin, tenant, nil)
	if r.code != http.StatusOK {
		t.Fatalf("catalog = %d %s", r.code, r.raw)
	}
	kinds, _ := r.body["kinds"].([]any)
	verbs, _ := r.body["verbs"].([]any)
	if !anyEq(kinds, "agent") || !anyEq(kinds, "model") {
		t.Errorf("catalog kinds must include agent/model, got %v", kinds)
	}
	if !anyEq(verbs, "admin") || !anyEq(verbs, "read") {
		t.Errorf("catalog verbs must include read/admin, got %v", verbs)
	}
}

// --- the managed projection composes (unions) with the free-form Cedar surface ------

func TestScopedAdminMergesWithFreeForm(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "mergeco")
	hdr := tenantHdr(tenant)

	alpha := h.createWorkspace(tenant, "alpha")
	beta := h.createWorkspace(tenant, "beta")
	aAlpha := h.createAgentIn(tenant, "a", alpha)
	aBeta := h.createAgentIn(tenant, "b", beta)
	aDefault := h.createAgentIn(tenant, "d", model.ID(""))
	vUID, viewer := h.roleUser(admin, tenant, "v@mergeco.io", auth.RoleViewer)

	// Free-form surface: viewers may write in alpha.
	h.publishGrant(admin, tenant, `permit(principal in Role::"viewer", action == Action::"agent:write", resource) when { resource in Workspace::"alpha" };`)
	// Managed surface: this user is a workspace-admin of beta.
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": vUID, "role": "admin", "scope_tree": "workspace", "scope_ref": "beta",
	}); r.code != http.StatusCreated {
		t.Fatalf("managed grant = %d %s", r.code, r.raw)
	}

	// Both surfaces enforce simultaneously (the union): alpha via free-form, beta via managed.
	if r := h.do("DELETE", "/v1/agents/"+aAlpha.ID.String(), viewer, nil, hdr); r.code != http.StatusNoContent {
		t.Errorf("free-form grant (alpha) must still apply after a managed publish, got %d %s", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/agents/"+aBeta.ID.String(), viewer, nil, hdr); r.code != http.StatusNoContent {
		t.Errorf("managed grant (beta) must apply, got %d %s", r.code, r.raw)
	}
	if r := h.do("DELETE", "/v1/agents/"+aDefault.ID.String(), viewer, nil, hdr); r.code != http.StatusForbidden {
		t.Errorf("neither surface reaches the default workspace, got %d %s", r.code, r.raw)
	}
}

// --- agent-group SCOPE projection (resource-side fold) ------------------------------

func TestScopedAdminAgentGroupScopeGrant(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "agco")

	member := h.createAgentIn(tenant, "member", model.ID(""))
	outsider := h.createAgentIn(tenant, "outsider", model.ID(""))
	h.addAgentToGroup(tenant, member.ID, "fleet", model.ID(""))
	uid, utok := h.roleUser(admin, tenant, "u@agco.io", auth.RoleViewer)

	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": uid, "role": "editor", "scope_tree": "agent_group", "scope_ref": "fleet",
	}); r.code != http.StatusCreated {
		t.Fatalf("agent-group grant = %d %s", r.code, r.raw)
	}

	p := h.principalOf(utok)
	if sd := h.scoped(tenant, p, "agent:write", member.ID); sd.Effect != auth.EffectGrant {
		t.Errorf("a group member must be GRANTED, got %v (%s)", sd.Effect, sd.Reason)
	}
	if sd := h.scoped(tenant, p, "agent:write", outsider.ID); sd.Effect != auth.EffectAbstain {
		t.Errorf("a non-member must ABSTAIN, got %v (%s)", sd.Effect, sd.Reason)
	}
}

// --- a confined (non-member) workspace-admin: scope grant without a tenant floor -----

func TestScopedAdminConfinedNonMember(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "confco")

	ws := h.createWorkspace(tenant, "ops")
	inOps := h.createAgentIn(tenant, "ops-bot", ws)
	outside := h.createAgentIn(tenant, "other", model.ID(""))

	// A user with NO membership in the tenant (only the scoped grant will speak for it).
	cr := h.do("POST", "/v1/users", admin, map[string]any{"email": "lone@confco.io", "password": "memberpass1"}, nil)
	if cr.code != http.StatusCreated {
		t.Fatalf("create user = %d %s", cr.code, cr.raw)
	}
	uid := cr.body["id"].(string)
	lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": "lone@confco.io", "password": "memberpass1"}, nil)
	if lr.code != http.StatusOK {
		t.Fatalf("login = %d %s", lr.code, lr.raw)
	}

	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": uid, "role": "admin", "scope_tree": "workspace", "scope_ref": "ops",
	}); r.code != http.StatusCreated {
		t.Fatalf("confined-admin grant = %d %s", r.code, r.raw)
	}

	p := h.principalOf(lr.body["token"].(string))
	if _, ok := p.RoleIn(tenant); ok {
		t.Fatal("the confined user must have NO tenant membership")
	}
	// Inside its workspace it is granted; outside, it abstains (no RBAC floor to fall to).
	if sd := h.scoped(tenant, p, "agent:write", inOps.ID); sd.Effect != auth.EffectGrant {
		t.Errorf("confined admin must be granted in its workspace, got %v (%s)", sd.Effect, sd.Reason)
	}
	if sd := h.scoped(tenant, p, "agent:write", outside.ID); sd.Effect != auth.EffectAbstain {
		t.Errorf("confined admin must abstain outside its workspace, got %v (%s)", sd.Effect, sd.Reason)
	}
}

// --- the update path is ceilinged too (regression for the review's HIGH finding) -----

func TestScopedAdminRoleUpdateCeiling(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin() // superadmin
	tenant := h.createOrg(admin, "upco")
	h.createWorkspace(tenant, "w")
	_, tadmin := h.roleUser(admin, tenant, "ta@upco.io", auth.RoleAdmin)
	vUID, _ := h.roleUser(admin, tenant, "v@upco.io", auth.RoleViewer)

	// A tenant admin (read+write) creates a custom role within its ceiling and assigns it.
	if r := h.rbac("POST", "roles", tadmin, tenant, map[string]any{
		"name": "wsrole", "permissions": []string{"agent:write"},
	}); r.code != http.StatusCreated {
		t.Fatalf("create role = %d %s", r.code, r.raw)
	}
	if r := h.rbac("POST", "grants", tadmin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": vUID, "role": "wsrole", "role_custom": true,
		"scope_tree": "workspace", "scope_ref": "w",
	}); r.code != http.StatusCreated {
		t.Fatalf("assign role = %d %s", r.code, r.raw)
	}

	// Widening the ASSIGNED role to add the owner-tier resource admin verb is blocked —
	// the exact escalation the direct grant path already rejects.
	if r := h.rbac("PUT", "roles/wsrole", tadmin, tenant, map[string]any{
		"permissions": []string{"agent:write", "agent:admin"},
	}); r.code != http.StatusForbidden {
		t.Errorf("widening an assigned role past the ceiling must be 403, got %d %s", r.code, r.raw)
	}
	// The superadmin (root) is exempt.
	if r := h.rbac("PUT", "roles/wsrole", admin, tenant, map[string]any{
		"permissions": []string{"agent:write", "agent:admin"},
	}); r.code != http.StatusOK {
		t.Errorf("superadmin role update must be 200, got %d %s", r.code, r.raw)
	}
	// An UNASSIGNED role can be freely widened by the tenant admin (inert until assigned).
	if r := h.rbac("POST", "roles", tadmin, tenant, map[string]any{
		"name": "unassigned", "permissions": []string{"agent:read"},
	}); r.code != http.StatusCreated {
		t.Fatalf("create unassigned role = %d %s", r.code, r.raw)
	}
	if r := h.rbac("PUT", "roles/unassigned", tadmin, tenant, map[string]any{
		"permissions": []string{"agent:admin"},
	}); r.code != http.StatusOK {
		t.Errorf("widening an unassigned role is inert and allowed, got %d %s", r.code, r.raw)
	}
}

func TestScopedAdminGroupUpdateCeiling(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "gupco")
	h.createWorkspace(tenant, "w")
	_, tadmin := h.roleUser(admin, tenant, "ta@gupco.io", auth.RoleAdmin)
	vUID, _ := h.roleUser(admin, tenant, "v@gupco.io", auth.RoleViewer)

	if r := h.rbac("POST", "permission-groups", tadmin, tenant, map[string]any{
		"name": "g1", "permissions": []string{"agent:write"},
	}); r.code != http.StatusCreated {
		t.Fatalf("create group = %d %s", r.code, r.raw)
	}
	if r := h.rbac("POST", "roles", tadmin, tenant, map[string]any{
		"name": "r1", "permissions": []string{}, "groups": []string{"g1"},
	}); r.code != http.StatusCreated {
		t.Fatalf("create role = %d %s", r.code, r.raw)
	}
	if r := h.rbac("POST", "grants", tadmin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": vUID, "role": "r1", "role_custom": true,
		"scope_tree": "workspace", "scope_ref": "w",
	}); r.code != http.StatusCreated {
		t.Fatalf("assign role = %d %s", r.code, r.raw)
	}
	// Widening the group (which r1 includes, and r1 is assigned) past the ceiling is blocked.
	if r := h.rbac("PUT", "permission-groups/g1", tadmin, tenant, map[string]any{
		"permissions": []string{"agent:write", "agent:admin"},
	}); r.code != http.StatusForbidden {
		t.Errorf("widening an in-use group past the ceiling must be 403, got %d %s", r.code, r.raw)
	}
}

func TestScopedAdminAdversarialNarrowRoleCannotDelegateBroaderScope(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	tenant := h.createOrg(root, "narrow-delegation")
	h.createWorkspace(tenant, "wa")
	h.createWorkspace(tenant, "wb")
	uID, uToken := h.roleUser(root, tenant, "u@narrow.example", auth.RoleViewer)
	vID, _ := h.roleUser(root, tenant, "v@narrow.example", auth.RoleViewer)

	if r := h.rbac("POST", "grants", root, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": uID, "role": "admin",
		"scope_tree": "workspace", "scope_ref": "wa",
	}); r.code != http.StatusCreated {
		t.Fatalf("seed U admin@wa = %d %s", r.code, r.raw)
	}

	for _, scope := range []map[string]any{
		{"scope_tree": "tenant"},
		{"scope_tree": "workspace", "scope_ref": "wb"},
	} {
		body := map[string]any{"subject_kind": "user", "subject_ref": vID, "role": "admin"}
		for k, v := range scope {
			body[k] = v
		}
		if r := h.rbac("POST", "grants", uToken, tenant, body); r.code != http.StatusForbidden {
			t.Errorf("admin@wa must not delegate the same role to broader/outside scope %v: got %d %s", scope, r.code, r.raw)
		}
	}
}

func TestScopedAdminAdversarialDelegationChainCannotLaunderScope(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	tenant := h.createOrg(root, "chain-delegation")
	h.createWorkspace(tenant, "wa")
	h.createWorkspace(tenant, "wb")
	aID, aToken := h.roleUser(root, tenant, "a@chain.example", auth.RoleViewer)
	bID, bToken := h.roleUser(root, tenant, "b@chain.example", auth.RoleViewer)
	cID, _ := h.roleUser(root, tenant, "c@chain.example", auth.RoleViewer)

	if r := h.rbac("POST", "grants", root, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": aID, "role": "admin",
		"scope_tree": "workspace", "scope_ref": "wa",
	}); r.code != http.StatusCreated {
		t.Fatalf("seed A admin@wa = %d %s", r.code, r.raw)
	}
	if r := h.rbac("POST", "grants", aToken, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": bID, "role": "admin",
		"scope_tree": "workspace", "scope_ref": "wa",
	}); r.code != http.StatusCreated {
		t.Fatalf("A must be able to delegate B admin@wa within the same ceiling: %d %s", r.code, r.raw)
	}

	for _, attempt := range []map[string]any{
		{"role": "editor", "scope_tree": "workspace", "scope_ref": "wb"},
		{"role": "admin", "scope_tree": "tenant"},
	} {
		body := map[string]any{"subject_kind": "user", "subject_ref": cID}
		for k, v := range attempt {
			body[k] = v
		}
		if r := h.rbac("POST", "grants", bToken, tenant, body); r.code != http.StatusForbidden {
			t.Errorf("B must not launder A's wa ceiling through chained delegation %v: got %d %s", attempt, r.code, r.raw)
		}
	}
}

func TestScopedAdminAdversarialAssignedTenantRoleCannotBeWidened(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	tenant := h.createOrg(root, "role-widening")
	_, tenantAdmin := h.roleUser(root, tenant, "admin@widen.example", auth.RoleAdmin)
	vID, _ := h.roleUser(root, tenant, "v@widen.example", auth.RoleViewer)

	if r := h.rbac("POST", "roles", tenantAdmin, tenant, map[string]any{
		"name": "limited-operator", "permissions": []string{"agent:write"},
	}); r.code != http.StatusCreated {
		t.Fatalf("create limited role = %d %s", r.code, r.raw)
	}
	if r := h.rbac("POST", "grants", tenantAdmin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": vID, "role": "limited-operator", "role_custom": true,
		"scope_tree": "tenant",
	}); r.code != http.StatusCreated {
		t.Fatalf("assign limited role at tenant scope = %d %s", r.code, r.raw)
	}
	if r := h.rbac("PUT", "roles/limited-operator", tenantAdmin, tenant, map[string]any{
		"permissions": []string{"agent:write", "agent:admin"},
	}); r.code != http.StatusForbidden {
		t.Errorf("widening an assigned tenant role past the editor's ceiling must deny: got %d %s", r.code, r.raw)
	}
}

// --- revoke is ceilinged: a scoped-admin cannot revoke outside its authority ---------

func TestScopedAdminRevokeCeiling(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "revco")
	h.createWorkspace(tenant, "wa")
	h.createWorkspace(tenant, "wb")
	uUID, uTok := h.roleUser(admin, tenant, "u@revco.io", auth.RoleViewer)
	vUID, _ := h.roleUser(admin, tenant, "v@revco.io", auth.RoleViewer)

	// U is a workspace-admin of wa. A separate grant lives in wb (created by superadmin).
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": uUID, "role": "admin", "scope_tree": "workspace", "scope_ref": "wa",
	}); r.code != http.StatusCreated {
		t.Fatalf("seed workspace-admin = %d %s", r.code, r.raw)
	}
	gb := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": vUID, "role": "editor", "scope_tree": "workspace", "scope_ref": "wb",
	})
	if gb.code != http.StatusCreated {
		t.Fatalf("seed wb grant = %d %s", gb.code, gb.raw)
	}
	wbGrant, _ := gb.body["id"].(string)

	// U creates a grant inside wa (it may), then revokes it (it may).
	ga := h.rbac("POST", "grants", uTok, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": vUID, "role": "editor", "scope_tree": "workspace", "scope_ref": "wa",
	})
	if ga.code != http.StatusCreated {
		t.Fatalf("U sub-delegate in wa = %d %s", ga.code, ga.raw)
	}
	waGrant, _ := ga.body["id"].(string)
	if r := h.rbac("DELETE", "grants/"+waGrant, uTok, tenant, nil); r.code != http.StatusNoContent {
		t.Errorf("U must revoke a grant it created in wa, got %d %s", r.code, r.raw)
	}
	// U must NOT revoke the wb grant (outside its delegation ceiling).
	if r := h.rbac("DELETE", "grants/"+wbGrant, uTok, tenant, nil); r.code != http.StatusForbidden {
		t.Errorf("U must NOT revoke a grant outside its scope, got %d %s", r.code, r.raw)
	}
}

// --- an agent_group scope rejects a non-agent resource-class (silently-inert guard) --

func TestScopedAdminAgentGroupClassRejected(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "agcls")
	agent := h.createAgentIn(tenant, "a", model.ID(""))
	h.addAgentToGroup(tenant, agent.ID, "fleet", model.ID(""))
	uUID, _ := h.roleUser(admin, tenant, "u@agcls.io", auth.RoleViewer)

	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": uUID, "role": "editor", "scope_tree": "agent_group", "scope_ref": "fleet", "scope_class": "model",
	}); r.code != http.StatusBadRequest {
		t.Errorf("agent_group + non-agent class must be 400, got %d %s", r.code, r.raw)
	}
	// class=agent (or empty) is fine.
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": uUID, "role": "editor", "scope_tree": "agent_group", "scope_ref": "fleet", "scope_class": "agent",
	}); r.code != http.StatusCreated {
		t.Errorf("agent_group + class=agent must be 201, got %d %s", r.code, r.raw)
	}
}

// --- the delegation-authority read surfaces the actor's ceiling ---------------

func TestScopedAdminDelegationAuthority(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin() // superadmin (root)
	tenant := h.createOrg(admin, "authco")
	h.createWorkspace(tenant, "w")
	taUID, tadmin := h.roleUser(admin, tenant, "ta@authco.io", auth.RoleAdmin)
	_ = taUID
	wsUID, wsadmin := h.roleUser(admin, tenant, "ws@authco.io", auth.RoleViewer)

	// Superadmin: unbounded root — superadmin:true, no domains needed.
	if r := h.rbac("GET", "delegation-authority", admin, tenant, nil); r.code != http.StatusOK {
		t.Fatalf("superadmin delegation-authority = %d %s", r.code, r.raw)
	} else if su, _ := r.body["superadmin"].(bool); !su {
		t.Errorf("superadmin must report superadmin:true, got %s", r.raw)
	}

	// Tenant admin (read+write): one tenant-wide domain conferring read+write but NOT the
	// owner-tier resource admin verb.
	r := h.rbac("GET", "delegation-authority", tadmin, tenant, nil)
	if r.code != http.StatusOK {
		t.Fatalf("tenant-admin delegation-authority = %d %s", r.code, r.raw)
	}
	if su, _ := r.body["superadmin"].(bool); su {
		t.Errorf("a tenant admin is not a superadmin, got %s", r.raw)
	}
	doms, _ := r.body["domains"].([]any)
	if len(doms) != 1 {
		t.Fatalf("tenant admin must have exactly one (tenant) domain, got %s", r.raw)
	}
	d0, _ := doms[0].(map[string]any)
	if tree, _ := d0["scope_tree"].(string); tree != "tenant" {
		t.Errorf("tenant-admin domain must be tenant-scoped, got %v", d0["scope_tree"])
	}
	perms, _ := d0["permissions"].([]any)
	if !anyEq(perms, "agent:read") || !anyEq(perms, "agent:write") {
		t.Errorf("tenant-admin domain must confer agent read+write, got %v", perms)
	}
	if anyEq(perms, "agent:admin") {
		t.Errorf("tenant-admin domain must NOT confer the owner-tier agent:admin verb, got %v", perms)
	}

	// Make the viewer a workspace-admin of w; its ceiling becomes a single workspace
	// domain (admin-capable ⇒ may sub-delegate within w), read+write only.
	if g := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": wsUID, "role": "admin", "scope_tree": "workspace", "scope_ref": "w",
	}); g.code != http.StatusCreated {
		t.Fatalf("seed workspace-admin = %d %s", g.code, g.raw)
	}
	wr := h.rbac("GET", "delegation-authority", wsadmin, tenant, nil)
	if wr.code != http.StatusOK {
		t.Fatalf("workspace-admin delegation-authority = %d %s", wr.code, wr.raw)
	}
	wdoms, _ := wr.body["domains"].([]any)
	if len(wdoms) != 1 {
		t.Fatalf("workspace-admin must have exactly one (workspace) domain, got %s", wr.raw)
	}
	wd0, _ := wdoms[0].(map[string]any)
	if tree, _ := wd0["scope_tree"].(string); tree != "workspace" {
		t.Errorf("domain must be workspace-scoped, got %v", wd0["scope_tree"])
	}
	if ref, _ := wd0["scope_ref"].(string); ref != "w" {
		t.Errorf("domain ref must be w, got %v", wd0["scope_ref"])
	}
	wperms, _ := wd0["permissions"].([]any)
	if !anyEq(wperms, "agent:write") || anyEq(wperms, "agent:admin") {
		t.Errorf("workspace-admin domain must confer write but not the admin verb, got %v", wperms)
	}
}

// anyEq reports whether a []any of strings contains s.
func anyEq(in []any, s string) bool {
	for _, v := range in {
		if str, ok := v.(string); ok && str == s {
			return true
		}
	}
	return false
}
