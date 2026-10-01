// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIndependentStaticBasicPasswordCannotReachToolsListClient(t *testing.T) {
	f := newMCPLedgerFixture(t)
	calls := 0
	header := "Basic " + base64.StdEncoding.EncodeToString([]byte("fixture-user:fixture-cut-secret"))
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != header {
			t.Error("static operator credential was not injected")
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Method != "tools/list" {
			t.Errorf("unexpected upstream method %s", req.Method)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []any{map[string]any{"name": "search", "description": func() string {
			_, password, ok := r.BasicAuth()
			if !ok {
				t.Error("bad Basic fixture")
			}
			return password
		}(), "inputSchema": map[string]any{"type": "object"}}}}})
	}))
	defer upstream.Close()
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	original := http.DefaultTransport
	base := original.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = base
	t.Cleanup(func() { http.DefaultTransport = original })
	// No native account, administrator role, or OAuth scope is assigned to this caller.
	token, jwks := mintReviewToken(t, mcpReviewResource, "")
	cfg := &mcpGatewayConfig{Resource: mcpReviewResource, AuthorizationServers: []string{"https://auth.review.example"}, Issuer: "https://auth.review.example", IssuerJWKS: jwks, Tenant: f.tenant.String(), UpstreamURL: upstream.URL, UpstreamAuth: header, Tools: []mcpc.ToolPolicy{{Name: "search", RequiredScope: "tools:read"}}}
	rs, _, err := buildMCPResourceServer(&engine{store: f.store, log: discardLogger()}, cfg, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	request := func(bearer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", mcpReviewResource, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
		r.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		rs.ServeHTTP(w, r)
		return w
	}
	if w := request(""); w.Code != 401 || calls != 0 {
		t.Fatalf("anonymous control: status=%d calls=%d", w.Code, calls)
	}
	if w := request("invalid-token"); w.Code != 401 || calls != 0 {
		t.Fatalf("invalid token control: status=%d calls=%d", w.Code, calls)
	}
	w := request(token)
	if w.Code != http.StatusBadGateway || calls != 1 {
		t.Fatalf("valid no-scope caller refusal: status=%d calls=%d", w.Code, calls)
	}
	if strings.Contains(w.Body.String(), "fixture-cut-secret") {
		t.Fatal("release cut static operator credential reached the tools/list HTTP client with valid issuer/audience token, no native role and no OAuth scope")
	}
}
