// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// TestListMembersRosterAuthzAndIsolation covers the console members-grid seam
// (GET /v1/members) at the HTTP boundary: it is tenant-scoped, gated by the
// admin-tier user:read, never leaks another tenant's members, and carries the
// role but no secret.
func TestListMembersRosterAuthzAndIsolation(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenantA := h.createOrg(admin, "acme")
	tenantB := h.createOrg(admin, "globex")

	mkUser := func(email, pass, role string, tenant model.TenantID) string {
		r := h.do("POST", "/v1/users", admin, map[string]any{"email": email, "password": pass}, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("create user %s = %d %s", email, r.code, r.raw)
		}
		uid := r.body["id"].(string)
		if g := h.do("POST", "/v1/memberships", admin, map[string]any{"user_id": uid, "tenant": tenant.String(), "role": role}, nil); g.code != http.StatusCreated {
			t.Fatalf("grant %s = %d %s", email, g.code, g.raw)
		}
		lr := h.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": pass}, nil)
		if lr.code != http.StatusOK {
			t.Fatalf("login %s = %d %s", email, lr.code, lr.raw)
		}
		return lr.body["token"].(string)
	}

	// tenant A: an admin (holds user:read) and an editor (does not).
	boss := mkUser("boss@acme.com", "bosspass1234", auth.RoleAdmin, tenantA)
	hand := mkUser("hand@acme.com", "handpass1234", auth.RoleEditor, tenantA)
	// tenant B: an owner that A must never see.
	mkUser("chief@globex.com", "chiefpass1234", auth.RoleOwner, tenantB)

	// The admin sees A's full roster (boss + hand), with roles and no secret.
	r := h.do("GET", "/v1/members", boss, nil, tenantHdr(tenantA))
	if r.code != http.StatusOK {
		t.Fatalf("admin roster = %d %s", r.code, r.raw)
	}
	items := r.body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("A roster = %d members, want 2: %s", len(items), r.raw)
	}
	roles := map[string]string{}
	for _, it := range items {
		m := it.(map[string]any)
		if m["email"] == "chief@globex.com" {
			t.Fatalf("tenant leak: A roster contains a globex member: %s", r.raw)
		}
		if _, leaked := m["password_hash"]; leaked {
			t.Fatalf("roster leaked a password hash: %s", r.raw)
		}
		if _, leaked := m["password"]; leaked {
			t.Fatalf("roster leaked a password: %s", r.raw)
		}
		roles[m["email"].(string)] = m["role"].(string)
	}
	if roles["boss@acme.com"] != auth.RoleAdmin {
		t.Errorf("boss role = %q, want admin", roles["boss@acme.com"])
	}
	if roles["hand@acme.com"] != auth.RoleEditor {
		t.Errorf("hand role = %q, want editor", roles["hand@acme.com"])
	}

	// The editor lacks user:read — the roster is admin-tier.
	if e := h.do("GET", "/v1/members", hand, nil, tenantHdr(tenantA)); e.code != http.StatusForbidden {
		t.Fatalf("editor roster = %d, want 403", e.code)
	}

	// A's admin is not a member of B — forbidden, not a cross-tenant read.
	if x := h.do("GET", "/v1/members", boss, nil, tenantHdr(tenantB)); x.code != http.StatusForbidden {
		t.Fatalf("A admin in B = %d, want 403", x.code)
	}

	// The superadmin listing B sees only B's member, never A's.
	if b := h.do("GET", "/v1/members", admin, nil, tenantHdr(tenantB)); b.code != http.StatusOK {
		t.Fatalf("superadmin roster B = %d %s", b.code, b.raw)
	} else {
		bitems := b.body["items"].([]any)
		for _, it := range bitems {
			if em := it.(map[string]any)["email"]; em == "boss@acme.com" || em == "hand@acme.com" {
				t.Fatalf("tenant B roster leaked an acme member: %s", b.raw)
			}
		}
	}

	// Unauthenticated is rejected before any read.
	if u := h.do("GET", "/v1/members", "", nil, tenantHdr(tenantA)); u.code != http.StatusUnauthorized {
		t.Fatalf("no-auth roster = %d, want 401", u.code)
	}
}
