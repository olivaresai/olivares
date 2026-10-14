//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"
)

// The recorded access edges are a privileged read of the estate: no credential, no answer.
func TestAccessEdgesRequireAuthentication(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "access-edges")
	if res := h.do("GET", "/v1/access-edges", "", nil, tenantHdr(tenant)); res.code != http.StatusUnauthorized {
		t.Errorf("unauthenticated GET /v1/access-edges = %d, want 401", res.code)
	}
	if res := h.do("GET", "/v1/access-edges", admin, nil, tenantHdr(tenant)); res.code != http.StatusOK {
		t.Errorf("admin GET /v1/access-edges = %d %s, want 200 in Community", res.code, res.raw)
	}
}

func TestCommunityCAEPReceiverRefusesAfterAuthentication(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "identity-edition")
	bearer := h.scimToken(h.principalOf(admin), tenant)
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/v1/ssf/events", "malformed SET"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			res := h.scim(tc.method, tc.path, bearer, tc.body)
			if res.code != http.StatusNotImplemented {
				t.Errorf("Community %s %s = %d %s, want 501", tc.method, tc.path, res.code, res.raw)
			}
			if res := h.do(tc.method, tc.path, "", nil, tenantHdr(tenant)); res.code != http.StatusUnauthorized {
				t.Errorf("unauthenticated %s = %d, want 401", tc.path, res.code)
			}
		})
	}
}
