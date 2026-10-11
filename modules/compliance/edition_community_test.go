//go:build !enterprise

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package compliance

import (
	"net/http"
	"testing"
)

func TestCommunityCompliancePacksUnavailable(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "edition-check")
	tok := h.roleToken(admin, tenant, "v@edition.test", "viewer")
	for _, path := range []string{"/frameworks", "/frameworks/eu_ai_act", "/frameworks/eu_ai_act/status", "/frameworks/eu_ai_act/gaps", "/capabilities", "/summary", "/dora", "/calendar", "/hipaa/gap-report"} {
		t.Run(path, func(t *testing.T) {
			r := h.do("GET", "/v1/m/compliance"+path, tok, nil, tenantHdr(tenant))
			if r.code != http.StatusNotImplemented {
				t.Errorf("%s = %d; want 501: %s", path, r.code, r.raw)
			}
		})
	}
	for _, principal := range []struct {
		token string
		want  int
	}{{tok, http.StatusForbidden}, {admin, http.StatusNotImplemented}} {
		r := h.do("POST", "/v1/m/compliance/frameworks/eu_ai_act/evidence", principal.token, map[string]any{"scope_note": "Community refusal"}, tenantHdr(tenant))
		if r.code != principal.want {
			t.Fatalf("Community sealing = %d, want %d: %s", r.code, principal.want, r.raw)
		}
	}
}
