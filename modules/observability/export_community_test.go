// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package observability

import (
	"net/http"
	"testing"
)

func TestCommunityTraceExportUnavailable(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedTwoTraces(h, tenant)
	if r := h.do("GET", ingestionPath, admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("ingestion health=%d", r.code)
	} else {
		for _, row := range listOf(r.body["standards"]) {
			for _, id := range []string{"ocsf", "asim_agentevent", "siem_unified"} {
				if strOf(row["id"]) == id && strOf(row["status"]) != "unavailable" {
					t.Fatalf("Community advertises %s as %v", id, row["status"])
				}
			}
		}
	}

	if r := h.do("GET", tracesPath+"/"+traceA+"/export", admin, nil, tenantHdr(tenant)); r.code != http.StatusNotImplemented {
		t.Fatalf("export = %d, want 501", r.code)
	}
	if r := h.do("GET", tracesPath+"/"+traceA, admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
		t.Fatalf("local detail = %d, want 200", r.code)
	}
}
