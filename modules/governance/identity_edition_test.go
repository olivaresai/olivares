//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"testing"
)

// The identity inventory stays in Community; lifecycle, federation, key and residency
// posture, and the directory roster sync are Business and refuse after authentication.
func TestCommunityIdentityInventoryServedAndBusinessSeamsRefuse(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "edition-roster")
	for _, path := range []string{"/v1/m/governance/identities", "/v1/m/governance/groups", "/v1/m/governance/bindings"} {
		t.Run(path, func(t *testing.T) {
			got := h.do("GET", path, admin, nil, tenantHdr(tenant))
			if got.code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200; %s", path, got.code, got.raw)
			}
		})
	}
	for _, path := range []string{"/v1/m/governance/nhi", "/v1/m/identity/wif", "/v1/m/identity/external-keys", "/v1/m/identity/residency"} {
		t.Run(path, func(t *testing.T) {
			got := h.do("GET", path, admin, nil, tenantHdr(tenant))
			if got.code != http.StatusNotImplemented {
				t.Fatalf("GET %s = %d, want 501; %s", path, got.code, got.raw)
			}
		})
	}
	got := h.do("POST", "/v1/m/governance/roster/sync", admin, nil, tenantHdr(tenant))
	if got.code != http.StatusNotImplemented {
		t.Fatalf("roster sync = %d, want 501; %s", got.code, got.raw)
	}
	// Agent registration and the NHI lifecycle writes stay Business after authentication too.
	for _, tc := range []struct{ method, path string }{
		{"POST", "/v1/m/governance/agents"},
		{"GET", "/v1/m/governance/nhi/posture"},
		{"GET", "/v1/m/governance/nhi/svc-1"},
		{"GET", "/v1/m/governance/nhi/svc-1/events"},
		{"PUT", "/v1/m/governance/nhi/svc-1/ownership"},
		{"PUT", "/v1/m/governance/nhi/svc-1/policy"},
		{"POST", "/v1/m/governance/nhi/svc-1/rotate"},
		{"POST", "/v1/m/governance/nhi/svc-1/offboard"},
		{"POST", "/v1/m/governance/nhi/svc-1/offboard/finalize"},
		{"POST", "/v1/m/governance/nhi/svc-1/restore"},
		{"POST", "/v1/m/governance/nhi/sweep"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			got := h.do(tc.method, tc.path, admin, map[string]any{}, tenantHdr(tenant))
			if got.code != http.StatusNotImplemented {
				t.Fatalf("%s %s = %d, want 501; %s", tc.method, tc.path, got.code, got.raw)
			}
		})
	}
}
