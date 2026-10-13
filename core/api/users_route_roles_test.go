// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// GET /v1/users lists the accounts of the whole deployment, across every
// organization, so it belongs to the superadmin. The console's users table reads
// GET /v1/members, the organization's own roster, which an organization admin holds
// (#1787: an admin saw the table and got 403 on the other route). Both halves are
// pinned for every built-in role, so neither route is widened to answer the other.
func TestUsersRouteIsDeploymentWideWhileMembersIsTheOrganizationRoster(t *testing.T) {
	h := newHarness(t)
	super := h.adminLogin()
	tenant := h.createOrg(super, "acme")
	other := h.createOrg(super, "globex")
	byRole := map[string]string{
		auth.RoleAdmin:  h.tenantTokenRole(super, tenant, "orgadmin@acme.io", auth.RoleAdmin),
		auth.RoleEditor: h.tenantTokenRole(super, tenant, "editor@acme.io", auth.RoleEditor),
		auth.RoleViewer: h.tenantTokenRole(super, tenant, "viewer@acme.io", auth.RoleViewer),
	}
	h.tenantTokenRole(super, other, "ceo@globex.io", auth.RoleAdmin)

	emails := func(r resp) []string {
		var out []string
		items, _ := r.body["items"].([]any)
		for _, item := range items {
			row, _ := item.(map[string]any)
			email, _ := row["email"].(string)
			out = append(out, email)
		}
		return out
	}
	refused := func(name string, r resp, status int) {
		t.Helper()
		if r.code != status {
			t.Fatalf("%s = %d %s, want %d", name, r.code, r.raw, status)
		}
		if e, _ := r.body["error"].(map[string]any); status == http.StatusForbidden && e["code"] != "forbidden" {
			t.Fatalf("%s error code = %v, want the published forbidden", name, e["code"])
		}
	}

	// The deployment-wide account surface: the superadmin only, whatever the role.
	for _, path := range []string{"/v1/users", "/v1/users/superadmins"} {
		if r := h.do("GET", path, super, nil, nil); r.code != http.StatusOK {
			t.Fatalf("superadmin GET %s = %d %s", path, r.code, r.raw)
		}
		for role, token := range byRole {
			refused(role+" GET "+path, h.do("GET", path, token, nil, tenantHdr(tenant)), http.StatusForbidden)
		}
		refused("anonymous GET "+path, h.do("GET", path, "", nil, nil), http.StatusUnauthorized)
	}
	refused("anonymous POST /v1/users", h.do("POST", "/v1/users", "", map[string]any{"email": "x@acme.io", "password": "memberpass1"}, nil), http.StatusUnauthorized)
	const someone = "00000000-0000-7000-8000-000000000001"
	for role, token := range byRole {
		refused(role+" POST /v1/users", h.do("POST", "/v1/users", token, map[string]any{"email": "x@acme.io", "password": "memberpass1"}, tenantHdr(tenant)), http.StatusForbidden)
		for _, verb := range []string{"disable", "enable"} {
			refused(role+" POST /v1/users/{id}/"+verb, h.do("POST", "/v1/users/"+someone+"/"+verb, token, nil, tenantHdr(tenant)), http.StatusForbidden)
		}
	}
	all := emails(h.do("GET", "/v1/users", super, nil, nil))
	for _, want := range []string{"orgadmin@acme.io", "ceo@globex.io"} {
		if !slices.Contains(all, want) {
			t.Fatalf("superadmin GET /v1/users = %v, missing %s", all, want)
		}
	}

	// The organization roster the console reads: the admin sees its own members and
	// nobody else's; editor and viewer lack user:read.
	roster := h.do("GET", "/v1/members", byRole[auth.RoleAdmin], nil, tenantHdr(tenant))
	if roster.code != http.StatusOK {
		t.Fatalf("admin GET /v1/members = %d %s", roster.code, roster.raw)
	}
	mine := emails(roster)
	for _, want := range []string{"orgadmin@acme.io", "editor@acme.io", "viewer@acme.io"} {
		if !slices.Contains(mine, want) {
			t.Fatalf("admin GET /v1/members = %v, missing %s", mine, want)
		}
	}
	if slices.Contains(mine, "ceo@globex.io") {
		t.Fatalf("admin GET /v1/members of acme lists another organization's account: %v", mine)
	}
	for _, role := range []string{auth.RoleEditor, auth.RoleViewer} {
		refused(role+" GET /v1/members", h.do("GET", "/v1/members", byRole[role], nil, tenantHdr(tenant)), http.StatusForbidden)
	}
	// An admin of one organization cannot read another's roster by naming it.
	refused("acme admin naming globex", h.do("GET", "/v1/members", byRole[auth.RoleAdmin], nil, tenantHdr(other)), http.StatusForbidden)
}
