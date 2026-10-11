// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package api_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestCommunityAuditExportRouteUnavailable(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "edition-audit")
	for _, query := range []string{"", "?format=cef", "?format=otlp&from=1"} {
		r := h.do("GET", "/v1/audit/export"+query, admin, nil, tenantHdr(tenant))
		if r.code != http.StatusNotImplemented || !strings.Contains(r.raw, "audit_export_unavailable") {
			t.Errorf("export%s = %d %s, want 501 audit_export_unavailable", query, r.code, r.raw)
		}
	}
	for _, path := range []string{"/v1/audit", "/v1/audit/verify", "/v1/audit/pubkey"} {
		if r := h.do("GET", path, admin, nil, tenantHdr(tenant)); r.code != http.StatusOK {
			t.Errorf("Community ledger %s = %d %s", path, r.code, r.raw)
		}
	}
}

const auditExportStatus = http.StatusNotImplemented
