// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"net/http"
	"strings"
	"testing"
)

// TestS437_SearchPolicies proves the module's federated-search provider end to
// end over GET /v1/search: registration via the Searcher seam, case-insensitive
// name matching, the governance-kinds-only filter and the non-sensitive detail
// (kind + enabled state — never the spec).
func TestS437_SearchPolicies(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")

	h.authorPolicy(admin, tenant, "Freeze-Writes", map[string]any{
		"rules": []any{map[string]any{"deny": true, "verb": "write"}},
	})

	r := h.do("GET", "/v1/search?q=freeze", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("search = %d %s", r.code, r.raw)
	}
	var hit map[string]any
	for _, it := range r.body["results"].([]any) {
		m := it.(map[string]any)
		if m["kind"] == "governance.policy" {
			hit = m
			break
		}
	}
	if hit == nil || hit["name"] != "Freeze-Writes" || hit["detail"] != "abac · enabled" {
		t.Fatalf("policy hit missing or wrong: %s", r.raw)
	}
	// The policy spec never surfaces in search.
	if strings.Contains(r.raw, "\"deny\"") || strings.Contains(r.raw, "\"rules\"") {
		t.Fatalf("policy spec leaked into search response: %s", r.raw)
	}
}
