// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"
)

// TestAuditRecentIsTheBellRead: newest first, no audit.read rows, and polling it does not
// grow the ledger. A ledger read by a person still records itself.
func TestAuditRecentIsTheBellRead(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	for _, name := range []string{"first", "second", "third"} {
		if r := h.do("POST", "/v1/agents", admin, map[string]any{"name": name, "kind": "k"}, tenantHdr(tenant)); r.code != http.StatusCreated {
			t.Fatalf("create = %d %s", r.code, r.raw)
		}
	}
	// A person's read appends one audit.read; the bell must skip it.
	if r := h.do("GET", "/v1/audit", admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("audit list = %d %s", r.code, r.raw)
	}
	recent := func() resp {
		r := h.do("GET", "/v1/audit/recent?limit=2", admin, nil, tenantHdr(tenant))
		if r.code != http.StatusOK {
			t.Fatalf("recent = %d %s", r.code, r.raw)
		}
		return r
	}
	first := recent()
	items := first.body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2: %s", len(items), first.raw)
	}
	newest, older := items[0].(map[string]any), items[1].(map[string]any)
	if newest["action"] == "audit.read" || older["action"] == "audit.read" {
		t.Fatalf("audit.read in the bell read: %s", first.raw)
	}
	if newest["seq"].(float64) <= older["seq"].(float64) {
		t.Fatalf("not newest first: %s", first.raw)
	}
	for i := 0; i < 3; i++ {
		recent()
	}
	if again := recent(); again.body["head_seq"] != first.body["head_seq"] {
		t.Fatalf("polling grew the ledger: head %v -> %v", first.body["head_seq"], again.body["head_seq"])
	}
	// The explicit read is still recorded.
	before := first.body["head_seq"].(float64)
	h.do("GET", "/v1/audit", admin, nil, tenantHdr(tenant))
	if after := recent().body["head_seq"].(float64); after != before+1 {
		t.Fatalf("explicit read: head %v -> %v, want one audit.read", before, after)
	}
}
