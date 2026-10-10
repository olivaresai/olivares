// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMCPGatewayOperatorConditionsDecide: Cedar conditions in the operator file
// reach the gateway's decision table, and a rule the gateway cannot honor keeps
// the MCP surface from mounting.
func TestMCPGatewayOperatorConditionsDecide(t *testing.T) {
	token, jwks := mintReviewToken(t, mcpReviewResource, "tools:write")
	doc := func(conditions string) string {
		c, err := json.Marshal(conditions)
		if err != nil {
			t.Fatal(err)
		}
		return `{"mcp":{"resource":"` + mcpReviewResource + `",` +
			`"authorization_servers":["https://auth.review.example"],` +
			`"issuer":"https://auth.review.example","issuer_jwks":` + string(jwks) + `,` +
			`"tools":[{"name":"fs_write","required_scope":"tools:write","conditions":` + string(c) + `}]}}`
	}
	cfg := loadGatewayMCPConfig(t, doc(`@id("no-etc") forbid(principal, action, resource) when { context.arguments.path like "/etc/*" };`))
	rs, _, err := buildMCPResourceServer(&engine{log: discardLogger()}, cfg, discardLogger())
	if err != nil {
		t.Fatalf("construction: %v", err)
	}
	call := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, mcpReviewResource, strings.NewReader(
			`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fs_write","arguments":{"path":"`+path+`"}}}`))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, req)
		return w
	}
	const refused = "tool call not permitted by server policy"
	if w := call("/etc/passwd"); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), refused) {
		t.Errorf("forbidden path: %d %s, want the policy refusal", w.Code, w.Body.String())
	}
	if w := call("/tmp/a"); strings.Contains(w.Body.String(), refused) {
		t.Errorf("allowed path was refused by policy: %d %s", w.Code, w.Body.String())
	}
	for _, bad := range []string{
		`permit(principal, action, resource);`,
		`forbid(principal in Group::"contractors", action, resource);`,
	} {
		cfg := loadGatewayMCPConfig(t, doc(bad))
		if rs, _, err := buildMCPResourceServer(&engine{log: discardLogger()}, cfg, discardLogger()); err == nil || rs != nil {
			t.Errorf("conditions %q mounted the gateway", bad)
		}
	}
}
