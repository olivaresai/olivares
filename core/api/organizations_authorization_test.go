// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package api_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// Both editions evaluate identity and system authority before edition availability.
func TestOrganizationManagementAuthorization(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "authorization")
	member := h.mkMember(admin, "org-admin@example.test", "strong-password-123", auth.RoleAdmin, tenant)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/v1/system/orgs"},
		{http.MethodPost, "/v1/system/orgs"},
		{http.MethodDelete, "/v1/system/orgs/" + tenant.String()},
		{http.MethodPut, "/v1/system/orgs/" + tenant.String() + "/region"},
		{http.MethodPut, "/v1/system/orgs/" + tenant.String() + "/status"},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			for _, principal := range []struct {
				token  string
				status int
			}{{"", http.StatusUnauthorized}, {member, http.StatusForbidden}} {
				r := h.do(route.method, route.path, principal.token, nil, nil)
				if r.code != principal.status {
					t.Fatalf("authorization = %d %s, want %d", r.code, r.raw, principal.status)
				}
			}
		})
	}
}
