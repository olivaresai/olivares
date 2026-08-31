// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// TestAssignmentCRUD exercises the connector assignment write API round-trip:
// create, get, list, update, delete.
func TestAssignmentCRUD(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "engineering")
	h.createWorkspace(tenant, "marketing")

	// Create assignment: assign connector "github" to workspace "engineering".
	r := h.do("POST", "/v1/m/sourcescope/assignments", admin, map[string]any{
		"connector_name": "github", "workspace_ref": "engineering", "enabled": true,
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create assignment = %d %s", r.code, r.raw)
	}
	id, _ := r.body["id"].(string)
	if id == "" {
		t.Fatalf("create returned no id: %s", r.raw)
	}
	if r.body["connector_name"] != "github" || r.body["workspace_ref"] != "engineering" {
		t.Fatalf("assignment fields mismatch: %s", r.raw)
	}

	// Get by id.
	g := h.do("GET", "/v1/m/sourcescope/assignments/"+id, admin, nil, tenantHdr(tenant))
	if g.code != http.StatusOK || g.body["connector_name"] != "github" {
		t.Fatalf("get = %d %s", g.code, g.raw)
	}

	// Create second assignment: same connector to a different workspace. Since this
	// is a RELAXATION (marketing gains access to a connector it could not reach), so it is
	// proposed, not applied, and a DISTINCT approver has to land it.
	approver := h.tokenFor(admin, tenant, "crud-approver@acme.io", "admin")
	r2 := h.do("POST", "/v1/m/sourcescope/assignments", admin, map[string]any{
		"connector_name": "github", "workspace_ref": "marketing", "enabled": true,
	}, tenantHdr(tenant))
	if r2.code != http.StatusAccepted || r2.body["op"] != "assignment_create" {
		t.Fatalf("create second assignment = %d, want 202 assignment_create: %s", r2.code, r2.raw)
	}
	if a := h.do("POST", "/v1/m/sourcescope/posture-requests/"+r2.body["id"].(string)+"/approve", approver, nil, tenantHdr(tenant)); a.code != http.StatusOK {
		t.Fatalf("approve second assignment = %d %s", a.code, a.raw)
	}

	// List all.
	l := h.do("GET", "/v1/m/sourcescope/assignments", admin, nil, tenantHdr(tenant))
	if l.code != http.StatusOK || len(items(l)) != 2 {
		t.Fatalf("list all = %d, want 2 items, got %d: %s", l.code, len(items(l)), l.raw)
	}

	// List filtered by connector_name.
	lf := h.do("GET", "/v1/m/sourcescope/assignments?connector_name=github", admin, nil, tenantHdr(tenant))
	if lf.code != http.StatusOK || len(items(lf)) != 2 {
		t.Fatalf("list by connector = %d, want 2 items: %s", lf.code, lf.raw)
	}

	// List filtered by workspace_ref.
	lw := h.do("GET", "/v1/m/sourcescope/assignments?workspace_ref=engineering", admin, nil, tenantHdr(tenant))
	if lw.code != http.StatusOK || len(items(lw)) != 1 {
		t.Fatalf("list by workspace = %d, want 1 item: %s", lw.code, lw.raw)
	}

	// Update: disable.
	u := h.do("PUT", "/v1/m/sourcescope/assignments/"+id, admin, map[string]any{
		"enabled": false, "note": "paused",
	}, tenantHdr(tenant))
	if u.code != http.StatusOK {
		t.Fatalf("update = %d %s", u.code, u.raw)
	}
	if u.body["note"] != "paused" {
		t.Fatalf("update note mismatch: %s", u.raw)
	}

	// Delete. Two rows exist, so this one is not the last: the connector keeps `marketing`
	// and stays confined, which is a tightening and applies immediately (204). The LAST-row
	// delete is the relaxation, and TestAssignmentDeleteOfLastRowIsDualControlled owns it.
	d := h.do("DELETE", "/v1/m/sourcescope/assignments/"+id, admin, nil, tenantHdr(tenant))
	if d.code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", d.code, d.raw)
	}

	// Verify deleted.
	gd := h.do("GET", "/v1/m/sourcescope/assignments/"+id, admin, nil, tenantHdr(tenant))
	if gd.code != http.StatusNotFound {
		t.Fatalf("after delete get = %d, want 404", gd.code)
	}
}

// TestAssignmentValidation rejects malformed assignments.
func TestAssignmentValidation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "payments")

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"missing connector_name", map[string]any{"connector_name": "", "workspace_ref": "payments"}, 400},
		{"missing workspace_ref", map[string]any{"connector_name": "github", "workspace_ref": ""}, 400},
		{"unknown workspace", map[string]any{"connector_name": "github", "workspace_ref": "ghost"}, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := h.do("POST", "/v1/m/sourcescope/assignments", admin, c.body, tenantHdr(tenant))
			if r.code != c.want {
				t.Errorf("got %d, want %d: %s", r.code, c.want, r.raw)
			}
		})
	}
}

// TestAssignmentUniqueConflict rejects a duplicate (connector, workspace).
func TestAssignmentUniqueConflict(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	h.createWorkspace(tenant, "engineering")

	body := map[string]any{"connector_name": "github", "workspace_ref": "engineering", "enabled": true}
	r := h.do("POST", "/v1/m/sourcescope/assignments", admin, body, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("first create = %d %s", r.code, r.raw)
	}
	r2 := h.do("POST", "/v1/m/sourcescope/assignments", admin, body, tenantHdr(tenant))
	if r2.code != http.StatusConflict {
		t.Fatalf("duplicate create = %d, want 409: %s", r2.code, r2.raw)
	}
}

// TestAssignmentGatesResolverVisibility verifies that once a connector has any
// assignment row, a workspace NOT assigned is denied (deny-closed), while an assigned
// workspace is allowed. A connector with NO assignments is globally visible.
func TestAssignmentGatesResolverVisibility(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	wsEng := h.createWorkspace(tenant, "engineering")
	wsMkt := h.createWorkspace(tenant, "marketing")

	// Create agents in each workspace.
	agentEng := h.createAgent(tenant, "eng-bot", wsEng)
	h.createSession(tenant, "eng-session", agentEng.ID, wsEng)
	agentMkt := h.createAgent(tenant, "mkt-bot", wsMkt)
	h.createSession(tenant, "mkt-session", agentMkt.ID, wsMkt)

	// Create confined principals (no tenant role) for each workspace.
	pEng := h.principalFor(admin, tenant, "eng@acme.io", "")
	pMkt := h.principalFor(admin, tenant, "mkt@acme.io", "")

	// Before any assignment: both can resolve the source (unbound = global).
	dec, err := h.resolver.ResolveForSession(t.Context(), tenant, pEng, "eng-session", "data", "github")
	if err != nil || !dec.Allowed {
		t.Fatalf("eng-bot pre-assignment: want allowed, got %v %v", dec, err)
	}
	dec, err = h.resolver.ResolveForSession(t.Context(), tenant, pMkt, "mkt-session", "data", "github")
	if err != nil || !dec.Allowed {
		t.Fatalf("mkt-bot pre-assignment: want allowed, got %v %v", dec, err)
	}

	// Assign "github" to engineering only.
	r := h.do("POST", "/v1/m/sourcescope/assignments", admin, map[string]any{
		"connector_name": "github", "workspace_ref": "engineering", "enabled": true,
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create assignment = %d %s", r.code, r.raw)
	}

	// Engineering can still resolve.
	dec, err = h.resolver.ResolveForSession(t.Context(), tenant, pEng, "eng-session", "data", "github")
	if err != nil || !dec.Allowed {
		t.Fatalf("eng-bot post-assignment: want allowed, got %v %v", dec, err)
	}

	// Marketing is denied (connector assigned but not to their workspace).
	dec, err = h.resolver.ResolveForSession(t.Context(), tenant, pMkt, "mkt-session", "data", "github")
	if err != nil {
		t.Fatalf("mkt-bot post-assignment: unexpected error %v", err)
	}
	if dec.Allowed {
		t.Fatalf("mkt-bot post-assignment: want denied, got allowed: %s", dec.Reason)
	}
}

// TestAssignmentTenantRBACOverride verifies that a tenant-wide admin (RBAC) can
// still see a connector even if not assigned to their workspace (soft-isolation).
func TestAssignmentTenantRBACOverride(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	wsEng := h.createWorkspace(tenant, "engineering")
	wsMkt := h.createWorkspace(tenant, "marketing")

	agentMkt := h.createAgent(tenant, "mkt-bot", wsMkt)
	h.createSession(tenant, "mkt-session", agentMkt.ID, wsMkt)
	_ = wsEng

	// Give the marketing user a tenant-wide viewer role (RBAC).
	pMkt := h.principalFor(admin, tenant, "mkt-admin@acme.io", "viewer")

	// Assign "github" to engineering only.
	r := h.do("POST", "/v1/m/sourcescope/assignments", admin, map[string]any{
		"connector_name": "github", "workspace_ref": "engineering", "enabled": true,
	}, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		t.Fatalf("create assignment = %d %s", r.code, r.raw)
	}

	// Marketing user with tenant RBAC can still resolve (soft-isolation override).
	dec, err := h.resolver.ResolveForSession(t.Context(), tenant, pMkt, "mkt-session", "data", "github")
	if err != nil || !dec.Allowed {
		t.Fatalf("mkt-admin with RBAC: want allowed, got %v %v", dec, err)
	}
}

// TestCrossTenantAssignmentIsolation verifies that assignments in one tenant do
// not leak to another.
func TestCrossTenantAssignmentIsolation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant1 := h.createOrg(admin, "acme")
	tenant2 := h.createOrg(admin, "corp")
	h.createWorkspace(tenant1, "eng")
	h.createWorkspace(tenant2, "eng")

	// Create assignment in tenant1.
	r := h.do("POST", "/v1/m/sourcescope/assignments", admin, map[string]any{
		"connector_name": "github", "workspace_ref": "eng", "enabled": true,
	}, tenantHdr(tenant1))
	if r.code != http.StatusCreated {
		t.Fatalf("create = %d %s", r.code, r.raw)
	}

	// List in tenant2: should be empty (no leakage).
	l := h.do("GET", "/v1/m/sourcescope/assignments", admin, nil, tenantHdr(tenant2))
	if l.code != http.StatusOK || len(items(l)) != 0 {
		t.Fatalf("cross-tenant list = %d, items=%d, want 0: %s", l.code, len(items(l)), l.raw)
	}
}

// createAssignment is a test helper that creates a connector assignment.
func (h *harness) createAssignment(token string, tenant model.TenantID, body map[string]any) resp {
	return h.do("POST", "/v1/m/sourcescope/assignments", token, body, tenantHdr(tenant))
}
