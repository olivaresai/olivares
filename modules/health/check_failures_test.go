// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package health

import (
	"net/http"
	"testing"
	"time"
)

// TestCheckFailuresOverSevenDays: a check row carries failures_7d, the number of times
// its subject went down in the last seven days, read from the transition ledger. A
// failure older than seven days does not count, and last_latency_ms is unchanged.
func TestCheckFailuresOverSevenDays(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	editor := h.roleToken(admin, tenant, "e@x.io", "editor")

	id := strOf(h.createCheck(editor, tenant, map[string]any{"subject_kind": "agent", "subject_ref": "agent-f"}).body["id"])
	for _, s := range []string{"down", "healthy"} { // eight days ago: outside the window
		h.report(editor, tenant, id, s, 10, "")
	}
	h.clk.advance(8 * 24 * time.Hour)
	for _, s := range []string{"down", "degraded", "healthy", "down", "healthy"} {
		h.report(editor, tenant, id, s, 30, "")
	}

	get := h.do("GET", "/v1/m/health/checks/"+id, editor, nil, tenantHdr(tenant))
	if get.code != http.StatusOK || get.body["failures_7d"] != float64(2) || get.body["last_latency_ms"] != float64(30) {
		t.Fatalf("check = %d %s, want failures_7d 2 and last_latency_ms 30", get.code, get.raw)
	}
	for _, path := range []string{"/v1/m/health/checks", "/v1/m/health/status"} {
		list := h.do("GET", path, editor, nil, tenantHdr(tenant))
		items := listItems(list)
		if list.code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["failures_7d"] != float64(2) {
			t.Fatalf("%s = %d %s, want one row with failures_7d 2", path, list.code, list.raw)
		}
	}
}
