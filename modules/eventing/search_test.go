// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"net/http"
	"strings"
	"testing"
)

// TestS437_SearchSubscriptions proves the module's federated-search provider
// end to end over GET /v1/search: registration via the Searcher seam, the
// read-tier permission gate, case-insensitive name matching and the
// non-sensitive detail (enabled state only — never the endpoint).
func TestS437_SearchSubscriptions(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	viewer := h.roleToken(admin, tenant, "v@x.io", "viewer")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	_, secret := h.createSubscription(editor, tenant, map[string]any{
		"name": "Billing-Alerts", "event_types": []string{"finding.reported"},
		"role": "editor", "endpoint": "http://127.0.0.1:9/hook",
	})

	r := h.do("GET", "/v1/search?q=billing", viewer, nil, map[string]string{"X-Olivares-Tenant": tenant.String()})
	if r.code != http.StatusOK {
		t.Fatalf("search = %d %s", r.code, r.raw)
	}
	results := r.body["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("want 1 hit, got %s", r.raw)
	}
	hit := results[0].(map[string]any)
	if hit["kind"] != "eventing.subscription" || hit["name"] != "Billing-Alerts" {
		t.Fatalf("unexpected hit: %v", hit)
	}
	if hit["detail"] != "enabled" {
		t.Fatalf("detail must be the enabled state only, got %v", hit)
	}

	// The endpoint and secret must never surface anywhere in the response.
	for _, needle := range []string{"127.0.0.1:9", secret} {
		if strings.Contains(r.raw, needle) {
			t.Fatalf("sensitive value %q leaked into search response: %s", needle, r.raw)
		}
	}

	if r := h.do("GET", "/v1/search?q=zzz-no-match", viewer, nil, map[string]string{"X-Olivares-Tenant": tenant.String()}); r.code != http.StatusOK || len(r.body["results"].([]any)) != 0 {
		t.Fatalf("no-match search = %d %s", r.code, r.raw)
	}
}
