// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

func TestManagedMCPProbeCannotPersistDecodedBasicPassword(t *testing.T) {
	m, f, p := mcpManagementFixture(t)
	const password = "fixtureSecret123"
	header := "Basic " + base64.StdEncoding.EncodeToString([]byte("fixture-user:"+password))
	calls := 0
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		user, sentPassword, ok := r.BasicAuth()
		if !ok || user != "fixture-user" || sentPassword != password {
			t.Error("fixture outbound credential changed")
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("fixture request decoding failed")
			return
		}
		if req.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		result := `{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fixture","version":"1"}}`
		if req.Method == "tools/list" {
			// A valid tool name containing the password the actual upstream received.
			// Unicode escapes ensure the probe checks parsed JSON strings too.
			var escaped strings.Builder
			for _, c := range "search_" + sentPassword {
				fmt.Fprintf(&escaped, `\u%04x`, c)
			}
			result = `{"tools":[{"name":"` + escaped.String() + `","inputSchema":{"type":"object"}}]}`
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, result)
	}))
	defer upstream.Close()
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	original := http.DefaultTransport
	transport := original.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = original })
	if _, err := m.secrets.Put(t.Context(), p, f.tenant, "mcp/probe-basic", header, "fixture"); err != nil {
		t.Fatal(err)
	}
	out, err := m.PutServer(t.Context(), p, f.tenant, 0, "", auth.MCPGatewayServerInput{
		Name: "Fixture", Transport: "streamable_http", URL: upstream.URL,
		CredentialRef: "store:mcp/probe-basic", EgressCIDRs: []string{"127.0.0.1/32"},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err = m.TestServer(t.Context(), p, f.tenant, out.Version, out.Servers[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := m.store.Get(t.Context(), f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	for label, snapshot := range map[string]auth.MCPGatewaySnapshot{"probe-response": out, "durable-inventory": stored} {
		for _, row := range snapshot.Servers {
			for _, tool := range row.Probe.Tools {
				if strings.Contains(tool.Name, password) {
					t.Errorf("decoded Basic password visible in %s: probe_state=%s upstream_calls=%d credential_visible=true", label, row.Probe.State, calls)
				}
			}
		}
	}
}
