// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func mcpManagementFixture(t *testing.T) (*mcpManagement, *mcpLedgerFixture, auth.Principal) {
	t.Helper()
	f := newMCPLedgerFixture(t)
	sealer, err := newSecretSealer(t.TempDir(), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	secrets := auth.NewSecretStore(f.store, sealer)
	m, err := newMCPManagement(f.store, secrets, agentGatewayConfig{}, false)
	if err != nil {
		t.Fatal(err)
	}
	m.eng = &engine{store: f.store, log: discardLogger()}
	actor := auth.Principal{Kind: auth.KindUser, UserID: model.NewID(), CredID: model.NewID(), Superadmin: true}
	return m, f, actor
}

func TestMCPManagementCredentialTenantAndNamespaceDenials(t *testing.T) {
	m, f, p := mcpManagementFixture(t)
	ctx := t.Context()
	if _, err := m.secrets.Put(ctx, p, auth.GlobalSecretScope, "mcp/example", "Bearer global-only", "fixture"); err != nil {
		t.Fatal(err)
	}
	in := auth.MCPGatewayServerInput{Name: "Fixture", Transport: "streamable_http", URL: "https://fixture.test/mcp", CredentialRef: "store:mcp/example"}
	if _, err := m.PutServer(ctx, p, f.tenant, 0, "", in); !errors.Is(err, auth.ErrSecretNotFound) {
		t.Fatal("global credential fallback")
	}
	if _, err := m.secrets.Put(ctx, p, f.tenant, "provider/example", "Bearer provider-only", "fixture"); err != nil {
		t.Fatal(err)
	}
	in.CredentialRef = "store:provider/example"
	if _, err := m.PutServer(ctx, p, f.tenant, 0, "", in); !errors.Is(err, auth.ErrMCPGatewayInvalid) {
		t.Fatal("provider credential rebound to MCP")
	}
	if _, err := m.secrets.Put(ctx, p, f.tenant, "mcp/example", "Bearer tenant-only", "fixture"); err != nil {
		t.Fatal(err)
	}
	in.CredentialRef = "store:mcp/example"
	out, err := m.PutServer(ctx, p, f.tenant, 0, "", in)
	if err != nil {
		t.Fatal(err)
	}
	provider := tenantMCPCredentialProvider{store: m.secrets, tenant: f.tenant, ref: in.CredentialRef, target: in.URL}
	if _, err := provider.Credential(ctx, "https://other.test/mcp"); !errors.Is(err, errManagedMCPEgress) {
		t.Fatal("credential bound to wrong destination")
	}
	own, err := provider.Credential(ctx, in.URL)
	if err != nil || own != "Bearer tenant-only" {
		t.Fatal("tenant credential resolution")
	}
	provider.tenant = model.TenantID(model.NewID().String())
	if _, err := provider.Credential(ctx, in.URL); !errors.Is(err, auth.ErrSecretNotFound) {
		t.Fatal("foreign tenant credential fallback")
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "tenant-only") || strings.Contains(string(raw), "global-only") {
		t.Fatal("DTO leaked value")
	}
}

func TestMCPManagementFileOwnershipAndSanitizedRead(t *testing.T) {
	m, f, p := mcpManagementFixture(t)
	file, err := newMCPManagement(f.store, m.secrets, agentGatewayConfig{SessionTools: true, MCP: &mcpGatewayConfig{Tenant: f.tenant.String(), Resource: "https://plane.test/mcp", UpstreamURL: "https://up.test/mcp?token=fixture-private", UpstreamAuth: "Bearer fixture-private"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	out, err := file.Get(t.Context(), f.tenant)
	if err != nil || out.Source != "file" || !out.ReadOnly || !out.SessionTools {
		t.Fatal("legacy source changed")
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "fixture-private") {
		t.Fatal("file DTO exposed credential")
	}
	if out.Servers[0].ProposedAllow == nil || *out.Servers[0].ProposedAllow == nil || len(*out.Servers[0].ProposedAllow) != 0 {
		t.Fatal("file source without a tested catalogue must carry an empty proposal")
	}
	other, err := file.Get(t.Context(), model.TenantID(model.NewID().String()))
	if err != nil || len(other.Servers) != 0 {
		t.Fatal("file tenant inventory escaped")
	}
	if _, err := file.SetSessionTools(t.Context(), p, f.tenant, 0, false); !errors.Is(err, auth.ErrMCPGatewayFileOwned) {
		t.Fatal("file switch overridden")
	}
	if _, err := file.DeleteServer(t.Context(), p, f.tenant, 0, "file"); !errors.Is(err, auth.ErrMCPGatewayFileOwned) {
		t.Fatal("file row deleted")
	}
	if _, err := file.TestServer(t.Context(), p, f.tenant, 0, "file"); !errors.Is(err, auth.ErrMCPGatewayFileOwned) {
		t.Fatal("file test bypass")
	}
}

func TestMCPManagementRuntimeTestEnableAudienceAndDisable(t *testing.T) {
	m, f, p := mcpManagementFixture(t)
	calls := 0
	toolCalls := 0
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer fixture-upstream" {
			t.Error("independent upstream credential missing")
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Fatal("body")
		}
		if req.Method == "notifications/initialized" {
			if len(req.ID) != 0 {
				t.Error("notification id")
			}
			w.WriteHeader(202)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		result := `{"tools":[{"name":"search","inputSchema":{"type":"object"}}]}`
		if req.Method == "initialize" {
			w.Header().Set("Mcp-Session-Id", "upstream-fixture")
			result = `{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fixture","version":"1"}}`
		}
		if req.Method == "tools/call" {
			toolCalls++
			result = `{"content":[{"type":"text","text":"fixture"}]}`
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, result)
	}))
	defer upstream.Close()
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	original := http.DefaultTransport
	base := original.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = base
	t.Cleanup(func() { http.DefaultTransport = original })
	if _, err := m.secrets.Put(t.Context(), p, f.tenant, "mcp/fixture", "Bearer fixture-upstream", "fixture"); err != nil {
		t.Fatal(err)
	}
	in := auth.MCPGatewayServerInput{Name: "Fixture", Transport: "streamable_http", URL: upstream.URL, CredentialRef: "store:mcp/fixture"}
	out, err := m.PutServer(t.Context(), p, f.tenant, 0, "", in)
	if err != nil {
		t.Fatal(err)
	}
	id := out.Servers[0].ID
	// Reserved address floor blocks even a configured host until explicitly granted.
	out, err = m.TestServer(t.Context(), p, f.tenant, out.Version, id)
	if err != nil || out.Servers[0].Probe.State != "egress_denied" || calls != 0 {
		t.Fatalf("reserved egress: %s %v calls=%d", out.Servers[0].Probe.State, err, calls)
	}
	in.EgressCIDRs = []string{"127.0.0.1/32"}
	out, err = m.PutServer(t.Context(), p, f.tenant, out.Version, id, in)
	if err != nil {
		t.Fatal(err)
	}
	out, err = m.TestServer(t.Context(), p, f.tenant, out.Version, id)
	if err != nil || out.Servers[0].Probe.State != "ok" || len(out.Servers[0].Probe.Tools) != 1 || toolCalls != 0 {
		t.Fatalf("inspection: %+v %v calls=%d", out, err, calls)
	}
	resource := "https://plane.test/mcp/gateway/" + f.tenant.String() + "/" + id
	token, jwks := mintReviewToken(t, resource, "tools:read")
	in.Trust = auth.MCPGatewayTrust{Resource: resource, Issuer: "https://auth.review.example", JWKS: jwks}
	in.AllowedTools = []auth.MCPGatewayToolPolicy{{Name: "search", RequiredScope: "tools:read"}}
	in.Enabled = true
	out, err = m.PutServer(t.Context(), p, f.tenant, out.Version, id, in)
	if err != nil {
		t.Fatal(err)
	}
	request := func(tenant model.TenantID, serverID, bearer, method string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "https://plane.test/mcp/gateway/"+tenant.String()+"/"+serverID, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":{}}`))
		r.Header.Set("Authorization", "Bearer "+bearer)
		c := chi.NewRouteContext()
		c.URLParams.Add("tenant", tenant.String())
		c.URLParams.Add("id", serverID)
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, c))
		w := httptest.NewRecorder()
		m.ServeGatewayHTTP(w, r)
		return w
	}
	before := calls
	if w := request(f.tenant, id, "invalid-token", "initialize"); w.Code != 401 || calls != before {
		t.Fatalf("invalid bearer=%d effects=%d", w.Code, calls-before)
	}
	foreign := model.TenantID(model.NewID().String())
	if w := request(foreign, id, token, "initialize"); w.Code != 404 || calls != before {
		t.Fatal("foreign tenant forwarding")
	}
	if w := request(f.tenant, id, token, "initialize"); w.Code != 200 {
		t.Fatalf("initialize=%d %s", w.Code, w.Body.String())
	}
	if w := request(f.tenant, id, token, "tools/list"); w.Code != 200 || !strings.Contains(w.Body.String(), "search") {
		t.Fatalf("catalog=%d %s", w.Code, w.Body.String())
	}
	in.Enabled = false
	out, err = m.PutServer(t.Context(), p, f.tenant, out.Version, id, in)
	if err != nil {
		t.Fatal(err)
	}
	before = calls
	if w := request(f.tenant, id, token, "tools/list"); w.Code != 404 || calls != before {
		t.Fatal("disabled cached server dispatched")
	}
	out, err = m.DeleteServer(t.Context(), p, f.tenant, out.Version, id)
	if err != nil || len(out.Servers) != 0 {
		t.Fatal("remove")
	}
}
