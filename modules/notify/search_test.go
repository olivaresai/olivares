// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"net/http"
	"strings"
	"testing"
)

// TestS437_SearchRoutes proves the module's federated-search provider end to
// end over GET /v1/search: registration via the Searcher seam, case-insensitive
// name matching and the non-sensitive detail (enabled state only — never the
// destination or match criteria).
func TestS437_SearchRoutes(t *testing.T) {
	h := newHarness(t, WithDispatcher(newFakeDispatcher("pagerduty-primary")))
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")

	h.mustCreateRoute(editor, tenant, map[string]any{
		// A distinctive destination: a short needle like "d1" can appear by
		// chance inside a generated UUID and make the no-leak check flaky.
		"name": "Security-Escalations", "destination": "pagerduty-primary", "match_kinds": []string{"security_*"},
	})

	r := h.do("GET", "/v1/search?q=SECURITY", viewer, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("search = %d %s", r.code, r.raw)
	}
	results := r.body["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("want 1 hit, got %s", r.raw)
	}
	hit := results[0].(map[string]any)
	if hit["kind"] != "notify.route" || hit["name"] != "Security-Escalations" || hit["detail"] != "enabled" {
		t.Fatalf("unexpected hit: %v", hit)
	}
	// The destination and match criteria never surface in search.
	for _, needle := range []string{"pagerduty-primary", "security_*"} {
		if strings.Contains(r.raw, needle) {
			t.Fatalf("route config %q leaked into search response: %s", needle, r.raw)
		}
	}
}
