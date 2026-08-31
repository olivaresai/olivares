// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

func TestSuperadminLifecycleEndpoints(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin() // bootstrap superadmin A (session, AAL1)

	// Create a second superadmin B via POST /v1/users.
	r := h.do("POST", "/v1/users", admin, map[string]any{
		"email": "b@acme.io", "display_name": "B", "password": "superpass-b1", "superadmin": true,
	}, nil)
	if r.code != http.StatusCreated {
		t.Fatalf("create superadmin B = %d %s", r.code, r.raw)
	}
	bID, _ := r.body["id"].(string)
	if bID == "" {
		t.Fatalf("create response carried no id: %s", r.raw)
	}

	// GET /v1/users/superadmins (read, no AAL3): A and B.
	r = h.do("GET", "/v1/users/superadmins", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("list superadmins = %d %s", r.code, r.raw)
	}
	items, _ := r.body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("superadmins = %d, want 2", len(items))
	}

	// Disable B WITHOUT AAL3 → 403 step_up_required (privileged account-lifecycle).
	r = h.do("POST", "/v1/users/"+bID+"/disable", admin, nil, nil)
	if r.code != http.StatusForbidden || errCode(r.body) != "step_up_required" {
		t.Fatalf("disable at AAL1 = %d %s, want 403 step_up_required", r.code, r.raw)
	}

	// Elevate, then disable B → 200 inactive.
	h.elevate(admin)
	r = h.do("POST", "/v1/users/"+bID+"/disable", admin, nil, nil)
	if r.code != http.StatusOK || r.body["status"] != "inactive" {
		t.Fatalf("disable B = %d %s", r.code, r.raw)
	}

	// A is now the LAST active superadmin: disabling it → 409 last_superadmin.
	aID := activeSuperadminID(t, h, admin)
	r = h.do("POST", "/v1/users/"+aID+"/disable", admin, nil, nil)
	if r.code != http.StatusConflict || errCode(r.body) != "last_superadmin" {
		t.Fatalf("disable last active superadmin = %d %s, want 409 last_superadmin", r.code, r.raw)
	}

	// Re-enable B → 200 active.
	r = h.do("POST", "/v1/users/"+bID+"/enable", admin, nil, nil)
	if r.code != http.StatusOK || r.body["status"] != "active" {
		t.Fatalf("enable B = %d %s", r.code, r.raw)
	}

	// A non-superadmin GLOBAL account cannot be governed here → 409 not_superadmin.
	cr := h.do("POST", "/v1/users", admin, map[string]any{
		"email": "member@acme.io", "display_name": "M", "password": "memberpass1", "superadmin": false,
	}, nil)
	if cr.code != http.StatusCreated {
		t.Fatalf("create member = %d %s", cr.code, cr.raw)
	}
	cID := cr.body["id"].(string)
	r = h.do("POST", "/v1/users/"+cID+"/disable", admin, nil, nil)
	if r.code != http.StatusConflict || errCode(r.body) != "not_superadmin" {
		t.Fatalf("disable non-superadmin = %d %s, want 409 not_superadmin", r.code, r.raw)
	}
}

func TestSuperadminLifecycleSuperadminOnly(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	// Create superadmin B so there is a real target id to attempt against.
	br := h.do("POST", "/v1/users", admin, map[string]any{
		"email": "b@acme.io", "password": "superpass-b1", "superadmin": true,
	}, nil)
	if br.code != http.StatusCreated {
		t.Fatalf("create superadmin B = %d %s", br.code, br.raw)
	}
	bID := br.body["id"].(string)

	// A tenant admin (not a superadmin) is refused on both the read and the write.
	member := h.mkMember(admin, "ta@acme.io", "tadminpass1", auth.RoleAdmin, tenant)
	if r := h.do("GET", "/v1/users/superadmins", member, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("tenant-admin list superadmins = %d, want 403", r.code)
	}
	h.elevate(member) // even at AAL3 the RBAC gate (superadmin-only) denies first
	if r := h.do("POST", "/v1/users/"+bID+"/disable", member, nil, tenantHdr(tenant)); r.code != http.StatusForbidden {
		t.Fatalf("tenant-admin disable superadmin = %d, want 403", r.code)
	}
}

// activeSuperadminID returns the id of an ACTIVE superadmin from the list endpoint.
func activeSuperadminID(t *testing.T, h *harness, admin string) string {
	t.Helper()
	r := h.do("GET", "/v1/users/superadmins", admin, nil, nil)
	if r.code != http.StatusOK {
		t.Fatalf("list superadmins = %d %s", r.code, r.raw)
	}
	for _, it := range r.body["items"].([]any) {
		u := it.(map[string]any)
		if u["status"] == "active" {
			return u["id"].(string)
		}
	}
	t.Fatalf("no active superadmin found in %s", r.raw)
	return ""
}
