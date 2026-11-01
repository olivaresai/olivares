// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package sessions

import (
	"context"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Governed connections (a database, an SSH host or a service a session reaches through
// the engine, granted down the organization tree) are a Business capability. Community
// keeps the published routes and answers 501 business_required for each of them.
func TestCommunityConnectionsRequireBusiness(t *testing.T) {
	h := newHarness(t, New())
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	body := map[string]any{"kind": "database", "target": "db.internal:5432", "credential_ref": "store:connection/prod-db"}
	const workspace = "00000000-0000-4000-8000-000000000001"
	for _, c := range []struct{ method, path string }{
		{"GET", "/v1/m/sessions/connections"},
		{"PUT", "/v1/m/sessions/connections/prod-db"},
		{"DELETE", "/v1/m/sessions/connections/prod-db"},
		{"POST", "/v1/m/sessions/connections/prod-db/test"},
		{"PUT", "/v1/m/sessions/connections/prod-db/grants/" + workspace},
		{"DELETE", "/v1/m/sessions/connections/prod-db/grants/" + workspace},
	} {
		r := h.doJSON(c.method, c.path, admin, body, tenantHdr(tenant))
		if r.code != http.StatusNotImplemented {
			t.Fatalf("%s %s = %d %s, want 501", c.method, c.path, r.code, r.raw)
		}
		errBody, _ := r.body["error"].(map[string]any)
		if errBody["code"] != "business_required" {
			t.Fatalf("%s %s error = %s, want code business_required", c.method, c.path, r.raw)
		}
	}
}

// A session in Community is offered no connection tool and no call is taken for the
// brokered surface, so the session tool endpoint answers exactly as before.
func TestCommunityOffersNoConnectionTools(t *testing.T) {
	m := New()
	tenant := model.TenantID("acme")
	tools, err := m.ConnectionTools(context.Background(), auth.Principal{}, tenant)
	if err != nil || len(tools) != 0 {
		t.Fatalf("ConnectionTools = %v, %v; want none", tools, err)
	}
	for _, name := range []string{"connections_list", "connection_query", "connection_exec", "connection_request", "connection_check"} {
		out, handled, err := m.CallConnection(context.Background(), auth.Principal{}, tenant, name, nil)
		if handled || err != nil || out.Text != "" {
			t.Fatalf("CallConnection(%s) = %+v, handled=%v, err=%v; want not handled", name, out, handled, err)
		}
	}
}
