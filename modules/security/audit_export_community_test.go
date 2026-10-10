// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

//go:build !enterprise

package security

import (
	"net/http"
	"testing"
)

func TestCommunityForensicExportUnavailable(t *testing.T) {
	h := newHarness(t, nil)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "forensic-edition")
	cr := h.do("POST", "/v1/m/security/cases", admin, map[string]any{"title": "edition case"}, tenantHdr(tenant))
	if cr.code != http.StatusCreated {
		t.Fatalf("case = %d %s", cr.code, cr.raw)
	}
	id := cr.body["id"].(string)
	for _, suffix := range []string{"", "?format=cef", "?format=json"} {
		r := h.do("GET", "/v1/m/security/cases/"+id+"/export"+suffix, admin, nil, tenantHdr(tenant))
		if r.code != http.StatusNotImplemented {
			t.Errorf("export%s = %d %s", suffix, r.code, r.raw)
		}
	}
	r := h.do("GET", "/v1/m/security/cases/"+id+"/timeline", admin, nil, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("timeline = %d %s", r.code, r.raw)
	}
}
