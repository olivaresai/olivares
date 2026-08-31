// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// workspace confinement, end to end: a workspace-scoped membership
// (Membership.WorkspaceID) confines its holder to that workspace. The engine FORBIDS
// any action targeting a DIFFERENT workspace (overriding the tenant-wide RBAC role), and the
// member roster is row-filtered to the caller's workspace.

// confinedUser creates a user and grants it a membership CONFINED to workspace ws, then logs
// it in (mirrors roleUser but adds the workspace_id).
func (h *harness) confinedUser(admin string, tenant model.TenantID, email, role string, ws model.ID) (string, string) {
	h.t.Helper()
	r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusCreated {
		h.t.Fatalf("create user %s = %d %s", email, r.code, r.raw)
	}
	uid := r.body["id"].(string)
	if r := h.do("POST", "/v1/memberships", admin, map[string]any{
		"user_id": uid, "tenant": tenant.String(), "role": role, "workspace_id": ws.String(),
	}, nil); r.code != http.StatusCreated {
		h.t.Fatalf("grant confined %s = %d %s", email, r.code, r.raw)
	}
	r = h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "memberpass1"}, nil)
	if r.code != http.StatusOK {
		h.t.Fatalf("login %s = %d %s", email, r.code, r.raw)
	}
	token := r.body["token"].(string)
	h.stepUp(token)
	return uid, token
}

// A workspace-confined admin may act ONLY within its workspace; the tenant-wide RBAC role no
// longer reaches another workspace (or the default workspace).
func TestWorkspaceConfinementEnforcesEntityAccess(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "confineco")
	hdr := tenantHdr(tenant)

	wsA := h.createWorkspace(tenant, "wa")
	wsB := h.createWorkspace(tenant, "wb")
	inA := h.createAgentIn(tenant, "a-bot", wsA)
	inB := h.createAgentIn(tenant, "b-bot", wsB)
	inDefault := h.createAgentIn(tenant, "d-bot", model.ID(""))

	// U is a tenant admin CONFINED to workspace wa.
	_, uTok := h.confinedUser(admin, tenant, "u@confineco.io", "admin", wsA)

	// Within its workspace, the confined admin acts as an admin (delete an agent in wa)...
	if r := h.do("DELETE", "/v1/agents/"+inA.ID.String(), uTok, nil, hdr); r.code != http.StatusNoContent {
		t.Errorf("confined admin must act within its workspace, got %d %s", r.code, r.raw)
	}
	// ...but is FORBIDDEN in another workspace, despite the tenant-wide admin role...
	if r := h.do("DELETE", "/v1/agents/"+inB.ID.String(), uTok, nil, hdr); r.code != http.StatusForbidden {
		t.Errorf("confined admin must NOT reach another workspace, got %d %s", r.code, r.raw)
	}
	// ...and also forbidden in the DEFAULT workspace (it is confined to wa, not default).
	if r := h.do("DELETE", "/v1/agents/"+inDefault.ID.String(), uTok, nil, hdr); r.code != http.StatusForbidden {
		t.Errorf("a wa-confined admin must NOT reach the default workspace, got %d %s", r.code, r.raw)
	}

	// A superadmin is never confined — it deletes across workspaces.
	if r := h.do("DELETE", "/v1/agents/"+inB.ID.String(), admin, nil, hdr); r.code != http.StatusNoContent {
		t.Errorf("superadmin must be unconfined, got %d %s", r.code, r.raw)
	}
}

// A tenant-wide admin (no workspace confinement) still reaches every workspace — the
// confinement is strictly additive.
func TestTenantWideAdminIsNotConfined(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "widead")
	hdr := tenantHdr(tenant)

	wsA := h.createWorkspace(tenant, "wa")
	inA := h.createAgentIn(tenant, "a-bot", wsA)

	_, uTok := h.roleUser(admin, tenant, "wide@widead.io", "admin") // tenant-wide admin
	if r := h.do("DELETE", "/v1/agents/"+inA.ID.String(), uTok, nil, hdr); r.code != http.StatusNoContent {
		t.Errorf("a tenant-wide admin must reach any workspace, got %d %s", r.code, r.raw)
	}
}

// The member roster is row-filtered for a confined caller: it sees only members OF its
// workspace, never the tenant's full, cross-workspace user set.
func TestWorkspaceConfinementRosterFilter(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "rosterco")

	wsA := h.createWorkspace(tenant, "wa")
	// A confined admin in wa, a second member also confined to wa, and a tenant-wide member.
	_, aTok := h.confinedUser(admin, tenant, "a@rosterco.io", "admin", wsA)
	h.confinedUser(admin, tenant, "peer@rosterco.io", "editor", wsA)
	h.roleUser(admin, tenant, "wide@rosterco.io", "editor") // tenant-wide

	// The confined admin's roster shows ONLY the two wa-confined members (not the tenant-wide
	// one, nor the tenant-wide superadmin).
	r := h.do("GET", "/v1/members", aTok, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("confined admin must read the (filtered) roster, got %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	emails := map[string]bool{}
	for _, it := range items {
		m, _ := it.(map[string]any)
		if e, ok := m["email"].(string); ok {
			emails[e] = true
		}
	}
	if !emails["a@rosterco.io"] || !emails["peer@rosterco.io"] {
		t.Errorf("confined roster must include the wa members, got %v", emails)
	}
	if emails["wide@rosterco.io"] {
		t.Error("confined roster must NOT include a tenant-wide member")
	}
	if len(emails) != 2 {
		t.Errorf("confined roster must be exactly the 2 wa members, got %d: %v", len(emails), emails)
	}
}

// ADVERSARIAL-REVIEW FIX (high): a confined admin cannot create entities via a collection
// route — a WRITE with an indeterminate target workspace is deny-closed (the escape that let a
// confined admin create an agent in ANY workspace).
func TestWorkspaceConfinementBlocksCollectionCreate(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "createco")
	hdr := tenantHdr(tenant)
	wsA := h.createWorkspace(tenant, "wa")
	_, uTok := h.confinedUser(admin, tenant, "u@createco.io", "admin", wsA)

	// The confined admin's create is forbidden at authorization (before the body is read).
	if r := h.do("POST", "/v1/agents", uTok, map[string]any{"name": "x", "kind": "claude-code"}, hdr); r.code != http.StatusForbidden {
		t.Errorf("a confined admin must not create an agent via the collection route, got %d %s", r.code, r.raw)
	}
}

// ADVERSARIAL-REVIEW FIX (medium): the agent list is row-filtered for a confined caller — no
// cross-workspace reconnaissance (the parallel of the members-roster filter).
func TestWorkspaceConfinementFiltersAgentList(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "listco")
	hdr := tenantHdr(tenant)
	wsA := h.createWorkspace(tenant, "wa")
	wsB := h.createWorkspace(tenant, "wb")
	h.createAgentIn(tenant, "a-bot", wsA)
	h.createAgentIn(tenant, "b-bot", wsB)
	_, uTok := h.confinedUser(admin, tenant, "u@listco.io", "admin", wsA)

	r := h.do("GET", "/v1/agents", uTok, nil, hdr)
	if r.code != http.StatusOK {
		t.Fatalf("confined admin must read the (filtered) agent list, got %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("confined agent list must show exactly the 1 wa agent, got %d", len(items))
	}
	m, _ := items[0].(map[string]any)
	if ws, _ := m["workspace_id"].(string); ws != wsA.String() {
		t.Errorf("the only visible agent must be in wa (%s), got workspace_id=%q", wsA, ws)
	}
	// A superadmin sees both.
	rs := h.do("GET", "/v1/agents", admin, nil, hdr)
	if items, _ := rs.body["items"].([]any); len(items) != 2 {
		t.Errorf("superadmin must see all agents, got %d", len(items))
	}
}

// The workspace_id on a membership grant is validated to name a real workspace of the tenant.
func TestGrantMembershipWorkspaceValidation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "valco")
	ws := h.createWorkspace(tenant, "wa")

	r := h.do("POST", "/v1/users", admin, map[string]any{"email": "v@valco.io", "password": "memberpass1"}, nil)
	uid := r.body["id"].(string)

	// A bogus workspace id is rejected (deny-closed against a typo that would silently confine).
	if r := h.do("POST", "/v1/memberships", admin, map[string]any{
		"user_id": uid, "tenant": tenant.String(), "role": "editor", "workspace_id": "01JZZZZZZZZZZZZZZZZZZZZZZZ",
	}, nil); r.code != http.StatusBadRequest {
		t.Errorf("an unknown workspace_id must be 400, got %d %s", r.code, r.raw)
	}
	// A real workspace id is accepted and echoed back.
	rr := h.do("POST", "/v1/memberships", admin, map[string]any{
		"user_id": uid, "tenant": tenant.String(), "role": "editor", "workspace_id": ws.String(),
	}, nil)
	if rr.code != http.StatusCreated {
		t.Fatalf("valid workspace_id grant = %d %s", rr.code, rr.raw)
	}
	if got, _ := rr.body["workspace_id"].(string); got != ws.String() {
		t.Errorf("response must echo the workspace_id, got %q want %q", got, ws.String())
	}
}
