// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func TestTokenListOpenAPIVisibilityAndQueriesMatchRuntime(t *testing.T) {
	h := newHarness(t)
	root := h.adminLogin()
	tenantA := h.createOrg(root, "token-contract-a")
	tenantB := h.createOrg(root, "token-contract-b")
	loginAdmin := func(email string, tenant model.TenantID) string {
		t.Helper()
		r := h.do("POST", "/v1/users", root, map[string]any{
			"email": email, "password": "tokencontractpass1", "tenant": tenant.String(), "role": auth.RoleAdmin,
		}, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("create tenant admin: status=%d", r.code)
		}
		login := h.do("POST", "/v1/auth/login", "", map[string]any{
			"email": email, "password": "tokencontractpass1",
		}, nil)
		if login.code != http.StatusOK {
			t.Fatalf("login tenant admin: status=%d", login.code)
		}
		return login.body["token"].(string)
	}
	adminA := loginAdmin("token-a@fixture.test", tenantA)
	adminB := loginAdmin("token-b@fixture.test", tenantB)
	issue := func(caller string, tenant model.TenantID, name string) string {
		t.Helper()
		r := h.do("POST", "/v1/tokens", caller, map[string]any{
			"name": name, "tenant": tenant.String(), "role": auth.RoleAdmin,
		}, nil)
		if r.code != http.StatusCreated {
			t.Fatalf("issue token %s: status=%d", name, r.code)
		}
		return r.body["id"].(string)
	}
	// A tenant administrator also sees tokens issued by another user in that tenant.
	activeA := issue(root, tenantA, "a-active")
	revokedA := issue(adminA, tenantA, "a-revoked")
	activeB := issue(adminB, tenantB, "b-active")
	if r := h.do("DELETE", "/v1/tokens/"+revokedA, root, nil, nil); r.code != http.StatusNoContent {
		t.Fatalf("revoke token: status=%d", r.code)
	}
	for _, tc := range []struct {
		name, caller, query string
		want                []string
	}{
		{"superadmin across issuers and tenants", root, "", []string{activeA, activeB}},
		{"superadmin including revoked", root, "?include_revoked=true", []string{activeA, revokedA, activeB}},
		{"tenant admin", adminA, "", []string{activeA}},
		{"tenant admin including revoked", adminA, "?include_revoked=true", []string{activeA, revokedA}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := h.do("GET", "/v1/tokens"+tc.query, tc.caller, nil, nil)
			if r.code != http.StatusOK {
				t.Fatalf("list tokens: status=%d", r.code)
			}
			items := r.body["items"].([]any)
			if len(items) != len(tc.want) {
				t.Fatalf("token count=%d, want %d", len(items), len(tc.want))
			}
			seen := make(map[string]bool)
			for _, raw := range items {
				seen[raw.(map[string]any)["id"].(string)] = true
			}
			for _, id := range tc.want {
				if !seen[id] {
					t.Fatalf("visible token %s missing", id)
				}
			}
		})
	}
	first := h.do("GET", "/v1/tokens?limit=1", root, nil, nil)
	if first.code != http.StatusOK || len(first.body["items"].([]any)) != 1 || first.body["has_more"] != true {
		t.Fatalf("first token page: status=%d body=%v", first.code, first.body)
	}
	cursor := first.body["cursor"].(string)
	second := h.do("GET", "/v1/tokens?limit=1&cursor="+cursor, root, nil, nil)
	if second.code != http.StatusOK || len(second.body["items"].([]any)) != 1 || second.body["has_more"] != false {
		t.Fatalf("second token page: status=%d body=%v", second.code, second.body)
	}
	if first.body["items"].([]any)[0].(map[string]any)["id"] == second.body["items"].([]any)[0].(map[string]any)["id"] {
		t.Fatal("token cursor repeated the first page")
	}
	doc := decodeDoc(t, rawGet(h, "/openapi.json", "", nil))
	op := doc["paths"].(map[string]any)["/v1/tokens"].(map[string]any)["get"].(map[string]any)
	if summary := op["summary"].(string); !strings.Contains(summary, "visible") {
		t.Errorf("summary must describe visibility across token issuers: %s", summary)
	}
	params := make(map[string]map[string]any)
	if rawParams, ok := op["parameters"].([]any); ok {
		for _, raw := range rawParams {
			param := raw.(map[string]any)
			if param["in"] == "query" {
				params[param["name"].(string)] = param
			}
		}
	}
	for name, typ := range map[string]string{"limit": "integer", "cursor": "string", "include_revoked": "boolean"} {
		param, ok := params[name]
		if !ok {
			t.Errorf("OpenAPI omits the %s query the token handler accepts", name)
			continue
		}
		if param["required"] != false || param["schema"].(map[string]any)["type"] != typ {
			t.Errorf("%s must be an optional %s: %v", name, typ, param)
		}
	}
	if param := params["limit"]; param != nil && param["schema"].(map[string]any)["default"] != float64(100) {
		t.Error("token list default must remain 100")
	}
	if param := params["include_revoked"]; param != nil && param["schema"].(map[string]any)["default"] != false {
		t.Error("include_revoked must default to false, as the HTTP handler does")
	}
}
