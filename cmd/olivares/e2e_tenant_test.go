// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// Multi-tenant isolation and RBAC verb tiers exercised over the REAL flow, not a
// unit test: tenant A holds the seeded estate, tenant B is empty, and a member
// scoped to A can read A but is forbidden B (data scoping + authz isolation).
// Viewing the access graph is a PRIVILEGED read (docs/SECURITY-HARDENING.md): the lowest viewer
// role is refused, an editor (or higher) scoped to A may read A's graph.

import (
	"net/http"
	"testing"
)

// newUser creates a user and (optionally) a tenant membership, then logs in and
// returns the session token.
func (h *harness) newUser(email, password, tenant, role string) string {
	h.t.Helper()
	var u struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/users", h.adminToken, "", map[string]any{
		"email": email, "display_name": email, "password": password, "superadmin": false,
	}, &u); code != http.StatusCreated || u.ID == "" {
		h.t.Fatalf("create user %q = %d", email, code)
	}
	if tenant != "" {
		if code, raw := h.req("POST", "/v1/memberships", h.adminToken, "", map[string]any{
			"user_id": u.ID, "tenant": tenant, "role": role,
		}); code != http.StatusCreated {
			h.t.Fatalf("grant membership = %d: %s", code, raw)
		}
	}
	var login struct {
		Token string `json:"token"`
	}
	if code := h.reqInto("POST", "/v1/auth/login", "", "", map[string]any{
		"email": email, "password": password,
	}, &login); code != http.StatusOK || login.Token == "" {
		h.t.Fatalf("login %q = %d", email, code)
	}
	// suite operators act over step-up-verified (AAL3) sessions, like a
	// production human after the WebAuthn/PIV ceremony.
	h.stepUp(login.Token)
	return login.Token
}

func TestE2E_TenantIsolation(t *testing.T) {
	h := newHarness(t)
	viewer := h.newUser("viewer-a@e2e.test", "viewer-pass-1", h.tenantA, "viewer")
	editor := h.newUser("editor-a@e2e.test", "editor-pass-1", h.tenantA, "editor")

	// The access graph is a privileged read: a viewer scoped to A is forbidden it
	// (docs/SECURITY-HARDENING.md), even in their own tenant — the recon map is not a viewer surface.
	if code, _ := h.req("GET", "/v1/m/accessmap/graph", viewer, h.tenantA, nil); code != http.StatusForbidden {
		t.Errorf("viewer GET graph in own tenant A = %d, want 403 (privileged)", code)
	}

	// An editor scoped to A reads A's estate.
	a := h.getJSON(editor, h.tenantA, "/v1/m/accessmap/graph?limit=50")
	if len(items2(a, "edges")) == 0 {
		t.Fatal("editor sees no edges in their own tenant A")
	}

	// Tenant B is a different tenant the editor has NO membership in → 403, never
	// an empty 200 (no information leak about B's existence/content).
	if code, _ := h.req("GET", "/v1/m/accessmap/graph", editor, h.tenantB, nil); code != http.StatusForbidden {
		t.Errorf("editor GET graph in tenant B = %d, want 403", code)
	}
	if code, _ := h.req("GET", "/v1/m/finops/spend/summary", viewer, h.tenantB, nil); code != http.StatusForbidden {
		t.Errorf("viewer GET finops in tenant B = %d, want 403", code)
	}

	// The superadmin CAN scope into B (it has every grant) but B holds none of A's
	// data — the estate did not leak across the tenant boundary.
	b := h.getJSON(h.adminToken, h.tenantB, "/v1/m/accessmap/graph?limit=50")
	if n := len(items2(b, "edges")); n != 0 {
		t.Errorf("tenant B leaked %d edges from tenant A", n)
	}
	bSum := h.getJSON(h.adminToken, h.tenantB, "/v1/m/inventory/summary")
	if tot, _ := bSum["total"].(float64); tot != 0 {
		t.Errorf("tenant B inventory total = %v, want 0", bSum["total"])
	}
}

func TestE2E_RBACVerbTiers(t *testing.T) {
	h := newHarness(t)
	viewer := h.newUser("viewer-rbac@e2e.test", "viewer-pass-2", h.tenantA, "viewer")
	editor := h.newUser("editor-rbac@e2e.test", "editor-pass-2", h.tenantA, "editor")

	// A viewer reads module data (verb-tier read auto-granted) ...
	if code, _ := h.req("GET", "/v1/m/finops/spend/summary", viewer, h.tenantA, nil); code != http.StatusOK {
		t.Errorf("viewer GET finops = %d, want 200", code)
	}
	// ... but cannot write a core entity (lacks agent:write).
	if code, _ := h.req("POST", "/v1/agents", viewer, h.tenantA, map[string]any{
		"name": "x", "kind": "x", "external_id": "viewer-should-fail", "status": "active",
	}); code != http.StatusForbidden {
		t.Errorf("viewer POST agent = %d, want 403", code)
	}
	// An editor can.
	if code, _ := h.req("POST", "/v1/agents", editor, h.tenantA, map[string]any{
		"name": "y", "kind": "y", "external_id": "editor-ok", "status": "active",
	}); code != http.StatusCreated {
		t.Errorf("editor POST agent = %d, want 201", code)
	}

	// Superadmin-only surfaces stay closed to a tenant role and to non-superadmins.
	if code, _ := h.req("GET", "/v1/users", viewer, h.tenantA, nil); code != http.StatusForbidden {
		t.Errorf("viewer GET /v1/users = %d, want 403", code)
	}
	if code, _ := h.req("POST", "/v1/system/orgs", editor, "", map[string]any{
		"name": "z", "slug": "z-should-fail",
	}); code != http.StatusForbidden {
		t.Errorf("editor POST /v1/system/orgs = %d, want 403", code)
	}
}
