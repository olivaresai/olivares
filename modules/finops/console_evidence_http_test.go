// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package finops_test

import (
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/modules/finops"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestFinopsConsoleReferenceHTTP(t *testing.T) {
	h := newHarness(t, finops.New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "console-evidence")
	viewer := h.roleToken(admin, tenant, "reader@console.test", auth.RoleViewer)
	other := h.createOrg(admin, "console-other")
	neighbour := h.roleToken(admin, other, "other@console.test", auth.RoleViewer)
	for _, name := range []string{"a", "b"} {
		r := h.do("POST", "/v1/m/finops/budgets", admin, map[string]any{"name": name, "enabled": true, "dimension": "global", "limit_micro_usd": 1000, "thresholds": []float64{1}}, tenantHdr(tenant))
		if r.code != 201 {
			t.Fatalf("budget: %d %s", r.code, r.raw)
		}
	}
	r := h.do("POST", "/v1/m/finops/cost", admin, map[string]any{"provider_ref": "fixture", "model_ref": "fixture", "input_tokens": 1, "output_tokens": 1, "cost_micro_usd": 2000, "occurred_at": time.Now().UTC().Format(time.RFC3339Nano)}, tenantHdr(tenant))
	if r.code != 202 {
		t.Fatalf("cost: %d %s", r.code, r.raw)
	}
	r = h.do("GET", "/v1/m/finops/alerts?limit=1", viewer, nil, tenantHdr(tenant))
	if r.code != 200 {
		t.Fatal(r.raw)
	}
	items := r.body["items"].([]any)
	if len(items) != 1 || r.body["has_more"] != true {
		t.Fatalf("pagination: %s", r.raw)
	}
	id := items[0].(map[string]any)["id"].(string)
	cursor := r.body["cursor"].(string)
	r = h.do("GET", "/v1/m/finops/alerts?limit=1&cursor="+url.QueryEscape(cursor), viewer, nil, tenantHdr(tenant))
	if r.code != 200 || len(r.body["items"].([]any)) != 1 || r.body["items"].([]any)[0].(map[string]any)["id"] == id {
		t.Fatalf("next page: %d %s", r.code, r.raw)
	}
	for _, tc := range []struct {
		name, path, token string
		headers           map[string]string
		code, items       int
	}{
		{"reference", "/v1/m/finops/alerts?alert_id=" + id, viewer, tenantHdr(tenant), 200, 1},
		{"invalid", "/v1/m/finops/alerts?alert_id=bad", viewer, tenantHdr(tenant), 400, -1},
		{"unauthenticated", "/v1/m/finops/alerts?alert_id=" + id, "", tenantHdr(tenant), 401, -1},
		{"membership-denied", "/v1/m/finops/alerts?alert_id=" + id, viewer, tenantHdr(other), 403, -1},
		{"authorized-other-tenant", "/v1/m/finops/alerts?alert_id=" + id, neighbour, tenantHdr(other), 200, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := h.do(http.MethodGet, tc.path, tc.token, nil, tc.headers)
			if r.code != tc.code {
				t.Fatalf("status %d want %d: %s", r.code, tc.code, r.raw)
			}
			if tc.items >= 0 && len(r.body["items"].([]any)) != tc.items {
				t.Fatalf("items: %s", r.raw)
			}
			t.Logf("HTTP=%d expected-items=%d", r.code, tc.items)
		})
	}
}
