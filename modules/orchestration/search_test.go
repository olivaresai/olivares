// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"net/http"
	"strings"
	"testing"
)

// TestS437_SearchSchedules proves the module's federated-search provider end to
// end over GET /v1/search: registration via the Searcher seam, case-insensitive
// name matching and the non-sensitive detail (desired status only — never the
// subject ref or cadence spec).
func TestS437_SearchSchedules(t *testing.T) {
	h, _ := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	if r := h.do("POST", "/v1/m/orchestration/schedules", editor, map[string]any{
		"name": "Nightly-Reconcile", "subject_kind": "agent", "subject_ref": "agent:recon-77", "trigger_kind": "manual",
	}, tenantHdr(tenant)); r.code != http.StatusCreated {
		t.Fatalf("create schedule = %d %s", r.code, r.raw)
	}

	r := h.do("GET", "/v1/search?q=nightly", editor, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("search = %d %s", r.code, r.raw)
	}
	results := r.body["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("want 1 hit, got %s", r.raw)
	}
	hit := results[0].(map[string]any)
	if hit["kind"] != "orchestration.schedule" || hit["name"] != "Nightly-Reconcile" || hit["detail"] != "active" {
		t.Fatalf("unexpected hit: %v", hit)
	}
	if strings.Contains(r.raw, "agent:recon-77") {
		t.Fatalf("subject ref leaked into search response: %s", r.raw)
	}
}
