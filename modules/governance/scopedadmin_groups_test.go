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

// --- group-subject helpers (the auth partition is reached through the real
// Authenticator the harness wires; the governance module never touches it) ----------

// createGroup provisions a directory group in tenant with the given member user ids
// (the members must already hold a tenant membership) and returns its id. The root
// superadmin is the actor — the same operator path /v1/scim and /v1/groups drive.
func (h *harness) createGroup(super auth.Principal, tenant model.TenantID, name, externalID string, members ...string) model.ID {
	h.t.Helper()
	ids := make([]model.ID, len(members))
	for i, m := range members {
		ids[i] = model.ID(m)
	}
	g, err := h.authr.SCIMCreateGroup(context.Background(), super, tenant, auth.SCIMGroupInput{
		DisplayName: name, ExternalID: externalID, Members: ids,
	})
	if err != nil {
		h.t.Fatalf("create group %s: %v", name, err)
	}
	return g.Group.ID
}

// nestGroup nests child under parent (operator path, superadmin authority).
func (h *harness) nestGroup(super auth.Principal, tenant model.TenantID, child, parent model.ID) {
	h.t.Helper()
	if _, err := h.authr.ConfigureGroupParent(context.Background(), super, tenant, child, parent); err != nil {
		h.t.Fatalf("nest group: %v", err)
	}
}

// --- e2e: a group-subject grant authorizes every member within the scope -------------

func TestScopedAdminGroupSubjectE2E(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	super := h.principalOf(admin)
	tenant := h.createOrg(admin, "grpco")
	hdr := tenantHdr(tenant)

	payments := h.createWorkspace(tenant, "payments")
	inPayments := h.createAgentIn(tenant, "pay-bot", payments)
	inDefault := h.createAgentIn(tenant, "default-bot", model.ID(""))

	memberUID, memberTok := h.roleUser(admin, tenant, "m@grpco.io", auth.RoleViewer)
	_, nonTok := h.roleUser(admin, tenant, "n@grpco.io", auth.RoleViewer)
	gid := h.createGroup(super, tenant, "Engineering", "eng-1", memberUID)

	// A scoped grant whose SUBJECT is the group: members of Engineering act as editors in payments.
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "group", "subject_ref": gid.String(),
		"role": "editor", "scope_tree": "workspace", "scope_ref": "payments",
	}); r.code != http.StatusCreated {
		t.Fatalf("group-subject grant = %d %s", r.code, r.raw)
	}

	// The member (re-authenticated, so the principal carries the gated group) is granted.
	memberP := h.principalOf(memberTok)
	if got := memberP.GroupsIn(tenant); len(got) != 1 || got[0] != gid.String() {
		t.Fatalf("member principal must carry group %s, got %v", gid, got)
	}
	if sd := h.scoped(tenant, memberP, "agent:write", inPayments.ID); sd.Effect != auth.EffectGrant {
		t.Errorf("a group member must be GRANTED in the scope, got %v (%s)", sd.Effect, sd.Reason)
	}
	if sd := h.scoped(tenant, memberP, "agent:write", inDefault.ID); sd.Effect != auth.EffectAbstain {
		t.Errorf("the grant must not reach the default workspace, got %v (%s)", sd.Effect, sd.Reason)
	}

	// Proven on the real chokepoint (DELETE = agent:write): the member deletes in payments...
	if r := h.do("DELETE", "/v1/agents/"+inPayments.ID.String(), memberTok, nil, hdr); r.code != http.StatusNoContent {
		t.Errorf("group member delete in payments must be 204, got %d %s", r.code, r.raw)
	}
	// ...but the non-member viewer (carries no group) cannot.
	nonP := h.principalOf(nonTok)
	if len(nonP.GroupsIn(tenant)) != 0 {
		t.Errorf("a non-member must carry no group, got %v", nonP.GroupsIn(tenant))
	}
	if r := h.do("DELETE", "/v1/agents/"+inDefault.ID.String(), nonTok, nil, hdr); r.code != http.StatusForbidden {
		t.Errorf("a non-member viewer must be 403, got %d %s", r.code, r.raw)
	}

	if !contains(h.auditActions(tenant), "governance.rbac.grant") {
		t.Error("the group-subject grant must be audited (governance.rbac.grant)")
	}
}

// --- e2e: a grant on a PARENT group reaches a member of a CHILD group -----------------

func TestScopedAdminNestedGroupE2E(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	super := h.principalOf(admin)
	tenant := h.createOrg(admin, "nestco")

	ops := h.createWorkspace(tenant, "ops")
	inOps := h.createAgentIn(tenant, "ops-bot", ops)

	memberUID, memberTok := h.roleUser(admin, tenant, "m@nestco.io", auth.RoleViewer)
	child := h.createGroup(super, tenant, "Backend", "be-1", memberUID)
	parent := h.createGroup(super, tenant, "AllEng", "all-1") // no direct members
	h.nestGroup(super, tenant, child, parent)

	// The grant targets the PARENT group; the member is only in the CHILD.
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "group", "subject_ref": parent.String(),
		"role": "editor", "scope_tree": "workspace", "scope_ref": "ops",
	}); r.code != http.StatusCreated {
		t.Fatalf("parent-group grant = %d %s", r.code, r.raw)
	}

	memberP := h.principalOf(memberTok)
	carried := map[string]bool{}
	for _, g := range memberP.GroupsIn(tenant) {
		carried[g] = true
	}
	if !carried[child.String()] || !carried[parent.String()] {
		t.Fatalf("a nested member must carry child AND parent, got %v (child=%s parent=%s)", memberP.GroupsIn(tenant), child, parent)
	}
	if sd := h.scoped(tenant, memberP, "agent:write", inOps.ID); sd.Effect != auth.EffectGrant {
		t.Errorf("a grant on the parent group must reach a child member, got %v (%s)", sd.Effect, sd.Reason)
	}
}

// --- the group subject feeds the delegation ceiling (grantAppliesToActor=group) -------

func TestScopedAdminGroupSubjectFeedsCeiling(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	super := h.principalOf(admin)
	tenant := h.createOrg(admin, "gauth")
	h.createWorkspace(tenant, "w")
	taUID, tadmin := h.roleUser(admin, tenant, "ta@gauth.io", auth.RoleAdmin)
	gid := h.createGroup(super, tenant, "WSAdmins", "wsa-1", taUID)

	// The group is an ADMIN-capable subject in workspace w.
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "group", "subject_ref": gid.String(),
		"role": "admin", "scope_tree": "workspace", "scope_ref": "w",
	}); r.code != http.StatusCreated {
		t.Fatalf("group admin grant = %d %s", r.code, r.raw)
	}

	// The tenant admin (reaches the endpoint via RBAC) is ALSO a member of that group, so
	// its delegation ceiling includes the workspace-w domain derived from the group grant.
	r := h.rbac("GET", "delegation-authority", tadmin, tenant, nil)
	if r.code != http.StatusOK {
		t.Fatalf("delegation-authority = %d %s", r.code, r.raw)
	}
	doms, _ := r.body["domains"].([]any)
	var sawTenant, sawGroupWS bool
	for _, d := range doms {
		m, _ := d.(map[string]any)
		switch m["scope_tree"] {
		case "tenant":
			sawTenant = true
		case "workspace":
			if m["scope_ref"] == "w" {
				sawGroupWS = true
			}
		}
	}
	if !sawTenant || !sawGroupWS {
		t.Errorf("a member of an admin-capable group subject must see the group's workspace in its ceiling, got %s", r.raw)
	}
}

// --- validation + the group hierarchy endpoint ---------------------------------------

func TestGroupSubjectValidationAndCatalog(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "gvalco")

	// A group subject requires a (non-empty) subject_ref.
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "group", "subject_ref": "", "role": "editor", "scope_tree": "tenant",
	}); r.code != http.StatusBadRequest {
		t.Errorf("empty group subject ref must be 400, got %d %s", r.code, r.raw)
	}
	// A grant to a non-existent group id is accepted by SHAPE (inert/deny-closed —
	// the auth partition is unreachable from the module, exactly like a user subject).
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "group", "subject_ref": "01999999-0000-7000-8000-000000000000",
		"role": "editor", "scope_tree": "tenant",
	}); r.code != http.StatusCreated {
		t.Errorf("a syntactically-valid group ref must be accepted (inert), got %d %s", r.code, r.raw)
	}
	// The catalog advertises the new subject kind for the console.
	r := h.rbac("GET", "catalog", admin, tenant, nil)
	if r.code != http.StatusOK {
		t.Fatalf("catalog = %d %s", r.code, r.raw)
	}
	subjects, _ := r.body["subject_kinds"].([]any)
	if !anyEq(subjects, "group") || !anyEq(subjects, "user") || !anyEq(subjects, "role") {
		t.Errorf("catalog subject_kinds must include user/role/group, got %v", subjects)
	}
}

// --- the group-hierarchy REST endpoint is owner-gated and acyclic --------------------

func TestSetGroupParentEndpoint(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	super := h.principalOf(admin)
	tenant := h.createOrg(admin, "gepco")
	_, ownerTok := h.roleUser(admin, tenant, "owner@gepco.io", auth.RoleOwner)
	_, adminTok := h.roleUser(admin, tenant, "adm@gepco.io", auth.RoleAdmin)

	child := h.createGroup(super, tenant, "Child", "c")
	parent := h.createGroup(super, tenant, "Parent", "p")

	// A tenant admin (not owner) cannot reshape the hierarchy (403).
	if r := h.do("PUT", "/v1/groups/"+child.String()+"/parent", adminTok, map[string]any{"parent_id": parent.String()}, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Errorf("admin nesting must be 403, got %d %s", r.code, r.raw)
	}
	// An owner may.
	if r := h.do("PUT", "/v1/groups/"+child.String()+"/parent", ownerTok, map[string]any{"parent_id": parent.String()}, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Errorf("owner nesting must be 200, got %d %s", r.code, r.raw)
	}
	// A cycle (nesting the parent under its own child) is 409.
	if r := h.do("PUT", "/v1/groups/"+parent.String()+"/parent", ownerTok, map[string]any{"parent_id": child.String()}, tenantHdr(tenant)); r.code != http.StatusConflict {
		t.Errorf("cycle must be 409, got %d %s", r.code, r.raw)
	}
}

// --- e2e (U7): delegated admin DIRECTED by an IdP/directory group ----------------
//
// The wire-proof for U7: an admin-capable group-subject grant now opens the delegation
// console to the group's GATED members. This flips a real Authorize(governance:rbac:admin)
// decision — before U7 the sub-delegation permit was emitted only for USER subjects, so a
// group member (however privileged the group grant) hit 403 at the rbac API. The delta
// stays doubly safe: deny-closed admission (only a direct tenant member carries the group,
// so a stranger in the group gets nothing) and a bounded ceiling (canDelegate clamps the
// sub-grant to the group grant's scope). A ROLE subject stays access-only.
func TestScopedAdminGroupDelegationE2E(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	super := h.principalOf(admin)
	tenant := h.createOrg(admin, "gdelco")
	h.createWorkspace(tenant, "payments")
	h.createWorkspace(tenant, "ops")

	// An EDITOR (below admin → NO rbac:admin from flat RBAC) who is a member of a group that
	// holds an ADMIN-capable grant over workspace "payments".
	memberUID, memberTok := h.roleUser(admin, tenant, "eng@gdelco.io", auth.RoleEditor)
	gid := h.createGroup(super, tenant, "PlatformAdmins", "pa-1", memberUID)
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "group", "subject_ref": gid.String(),
		"role": "admin", "scope_tree": "workspace", "scope_ref": "payments",
	}); r.code != http.StatusCreated {
		t.Fatalf("group admin grant = %d %s", r.code, r.raw)
	}
	targetUID, _ := h.roleUser(admin, tenant, "t@gdelco.io", auth.RoleViewer)

	// THE U7 DECISION FLIP: the editor reaches the rbac API PURELY via the group's delegation
	// permit and sub-delegates WITHIN payments → 201 (was 403 pre-U7: an editor has no
	// rbac:admin, and a group subject emitted no delegation permit).
	if r := h.rbac("POST", "grants", memberTok, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": targetUID,
		"role": "viewer", "scope_tree": "workspace", "scope_ref": "payments",
	}); r.code != http.StatusCreated {
		t.Fatalf("a group member must sub-delegate within the group's scope (U7 flip), got %d %s", r.code, r.raw)
	}

	// BOUNDED CEILING: the same member cannot sub-delegate OUTSIDE the group grant's scope
	// (ops) → 403. Reaching the API is not unlimited authority; canDelegate clamps it.
	if r := h.rbac("POST", "grants", memberTok, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": targetUID,
		"role": "viewer", "scope_tree": "workspace", "scope_ref": "ops",
	}); r.code != http.StatusForbidden {
		t.Errorf("sub-delegation outside the group's scope must be 403 (ceiling), got %d %s", r.code, r.raw)
	}

	// DENY-CLOSED: an editor who is NOT in the group has no delegation permit → 403. The
	// permit reaches gated group members only, never every editor.
	_, nonTok := h.roleUser(admin, tenant, "non@gdelco.io", auth.RoleEditor)
	if r := h.rbac("POST", "grants", nonTok, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": targetUID,
		"role": "viewer", "scope_tree": "workspace", "scope_ref": "payments",
	}); r.code != http.StatusForbidden {
		t.Errorf("a non-member editor must not reach the delegation API, got %d %s", r.code, r.raw)
	}

	// ROLE SUBJECT STAYS ACCESS-ONLY: an admin-capable ROLE grant ("every editor is admin in
	// ops") confers scoped access but NOT the delegation permit, so the role-matched editor
	// still cannot reach the rbac API — guarding the deliberate role carve-out.
	if r := h.rbac("POST", "grants", admin, tenant, map[string]any{
		"subject_kind": "role", "subject_ref": "editor",
		"role": "admin", "scope_tree": "workspace", "scope_ref": "ops",
	}); r.code != http.StatusCreated {
		t.Fatalf("role-subject admin grant = %d %s", r.code, r.raw)
	}
	if r := h.rbac("POST", "grants", nonTok, tenant, map[string]any{
		"subject_kind": "user", "subject_ref": targetUID,
		"role": "viewer", "scope_tree": "workspace", "scope_ref": "ops",
	}); r.code != http.StatusForbidden {
		t.Errorf("a role-subject admin grant must not open the delegation console, got %d %s", r.code, r.raw)
	}
}
