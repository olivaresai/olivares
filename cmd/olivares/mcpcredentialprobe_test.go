// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

func TestMCPManagementProbeRefusesShortCredentialBeforeNetwork(t *testing.T) {
	for _, password := range []string{"", "abc", "1234567"} {
		t.Run(fmt.Sprintf("length_%d", len(password)), func(t *testing.T) {
			m, f, p := mcpManagementFixture(t)
			calls := 0
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var req struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					t.Error("fixture request")
					return
				}
				if req.Method == "notifications/initialized" {
					w.WriteHeader(http.StatusAccepted)
					return
				}
				result := `{"tools":[]}`
				if req.Method == "initialize" {
					result = `{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fixture","version":"1"}}`
				}
				w.Header().Set("Content-Type", "application/json")
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
			header := "Basic " + base64.StdEncoding.EncodeToString([]byte("fixture-user:"+password))
			if _, err := m.secrets.Put(t.Context(), p, f.tenant, "mcp/short", header, "fixture"); err != nil {
				t.Fatal(err)
			}
			out, err := m.PutServer(t.Context(), p, f.tenant, 0, "", auth.MCPGatewayServerInput{Name: "Fixture", Transport: "streamable_http", URL: upstream.URL, CredentialRef: "store:mcp/short", EgressCIDRs: []string{"127.0.0.1/32"}})
			if err != nil {
				t.Fatal(err)
			}
			out, err = m.TestServer(t.Context(), p, f.tenant, out.Version, out.Servers[0].ID)
			if err != nil || calls != 0 || out.Servers[0].Probe.State != "credential_unavailable" {
				t.Fatalf("short probe credential transmitted: calls=%d error=%v", calls, err != nil)
			}
		})
	}
}

func TestMCPManagementProbeGuardsEveryCredentialedResponse(t *testing.T) {
	const password = "9nV6rC2x"
	pair := "fixture-user:" + password
	payload := base64.StdEncoding.EncodeToString([]byte(pair))
	basic := "Basic " + payload
	for _, tc := range []struct {
		name, header, echo, at string
		escaped, sse, rpcError bool
		wantCalls              int
	}{
		{name: "basic_literal_password_name", header: basic, echo: password, at: "tools/list", wantCalls: 3},
		{name: "basic_escaped_password_name", header: basic, echo: password, at: "tools/list", escaped: true, wantCalls: 3},
		{name: "basic_header_initialize", header: basic, echo: basic, at: "initialize", wantCalls: 1},
		{name: "basic_pair_error_initialize", header: basic, echo: pair, at: "initialize", escaped: true, rpcError: true, wantCalls: 1},
		{name: "basic_payload_second_page", header: basic, echo: payload, at: "second_page", wantCalls: 4},
		{name: "basic_pair_sse_description", header: basic, echo: pair, at: "tools/list", escaped: true, sse: true, wantCalls: 3},
		{name: "basic_password_sse_error_data", header: basic, echo: password, at: "tools/list", escaped: true, sse: true, rpcError: true, wantCalls: 3},
		{name: "basic_password_notification", header: basic, echo: password, at: "notifications/initialized", escaped: true, wantCalls: 2},
		{name: "bearer_token_initialize", header: "Bearer k8P2n5T9q4R7", echo: "k8P2n5T9q4R7", at: "initialize", wantCalls: 1},
		{name: "bearer_header_description", header: "Bearer k8P2n5T9q4R7", echo: "Bearer k8P2n5T9q4R7", at: "tools/list", wantCalls: 3},
		{name: "opaque_token_name", header: "t4J8m2R9n6P3", echo: "t4J8m2R9n6P3", at: "tools/list", wantCalls: 3},
		{name: "healthy_basic_eight_bytes", header: basic, wantCalls: 3},
		{name: "healthy_bearer", header: "Bearer k8P2n5T9q4R7", wantCalls: 3},
		{name: "healthy_without_credential", wantCalls: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, f, p := mcpManagementFixture(t)
			var logs bytes.Buffer
			m.eng.log = slog.New(slog.NewJSONHandler(&logs, nil))
			calls, lists := 0, 0
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != tc.header {
					t.Error("probe outbound credential changed")
				}
				var req struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					t.Error("fixture request decoding")
					return
				}
				if req.Method == "tools/list" {
					lists++
				}
				attack := tc.at == req.Method || tc.at == "second_page" && lists == 2
				encoded, _ := json.Marshal("echo_" + tc.echo)
				if tc.escaped {
					var escaped strings.Builder
					for _, c := range "echo_" + tc.echo {
						fmt.Fprintf(&escaped, `\u%04x`, c)
					}
					encoded = []byte(`"` + escaped.String() + `"`)
				}
				if req.Method == "notifications/initialized" {
					w.WriteHeader(http.StatusAccepted)
					if attack {
						fmt.Fprintf(w, `{"echo":%s}`, encoded)
					}
					return
				}
				result := `{"tools":[{"name":"search","description":"healthy prose, password=abc","inputSchema":{"type":"object"}}]}`
				if req.Method == "initialize" {
					result = `{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fixture","version":"1"}}`
				} else if req.Method != "tools/list" {
					t.Error("probe attempted an operation beyond discovery")
				}
				if tc.at == "second_page" && req.Method == "tools/list" && lists == 1 {
					result = `{"tools":[{"name":"first","inputSchema":{"type":"object"}}],"nextCursor":"next"}`
				}
				if attack {
					switch {
					case req.Method == "initialize":
						result = fmt.Sprintf(`{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":%s,"version":"1"}}`, encoded)
					case tc.echo == password || !strings.HasPrefix(tc.header, "Bearer ") && tc.header != basic:
						result = fmt.Sprintf(`{"tools":[{"name":%s,"inputSchema":{"type":"object"}}]}`, encoded)
					default:
						result = fmt.Sprintf(`{"tools":[{"name":"search","description":%s,"inputSchema":{"type":"object"}}]}`, encoded)
					}
				}
				body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, result)
				if attack && tc.rpcError {
					body = fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32603,"message":"fixture refusal","data":%s}}`, req.ID, encoded)
				}
				w.Header().Set("Content-Type", "application/json")
				if attack && tc.sse {
					w.Header().Set("Content-Type", "text/event-stream")
					body = "data: " + body + "\n\n"
				}
				fmt.Fprint(w, body)
			}))
			defer upstream.Close()
			roots := x509.NewCertPool()
			roots.AddCert(upstream.Certificate())
			original := http.DefaultTransport
			transport := original.(*http.Transport).Clone()
			transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
			http.DefaultTransport = transport
			t.Cleanup(func() { http.DefaultTransport = original })
			ref := ""
			if tc.header != "" {
				if _, err := m.secrets.Put(t.Context(), p, f.tenant, "mcp/probe", tc.header, "fixture"); err != nil {
					t.Fatal(err)
				}
				ref = "store:mcp/probe"
			}
			out, err := m.PutServer(t.Context(), p, f.tenant, 0, "", auth.MCPGatewayServerInput{Name: "Fixture", Transport: "streamable_http", URL: upstream.URL, CredentialRef: ref, EgressCIDRs: []string{"127.0.0.1/32"}})
			if err != nil {
				t.Fatal(err)
			}
			version := out.Version
			out, err = m.TestServer(t.Context(), p, f.tenant, version, out.Servers[0].ID)
			if err != nil || out.Version != version+1 || calls != tc.wantCalls {
				t.Fatalf("probe outcome: error=%t calls=%d version=%d", err != nil, calls, out.Version)
			}
			stored, err := m.store.Get(t.Context(), f.tenant)
			if err != nil {
				t.Fatal(err)
			}
			for _, snapshot := range []auth.MCPGatewaySnapshot{out, stored} {
				raw, err := json.Marshal(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				for _, value := range []string{tc.header, tc.echo} {
					if value != "" && bytes.Contains(raw, []byte(value)) {
						t.Fatal("credential visible in returned or durable snapshot")
					}
				}
				probe := snapshot.Servers[0].Probe
				if tc.at != "" {
					if probe.State != "invalid_response" || len(probe.Tools) != 0 || snapshot.Servers[0].Enabled {
						t.Fatalf("credential echo accepted: state=%s tools=%d", probe.State, len(probe.Tools))
					}
				} else if probe.State != "ok" || len(probe.Tools) != 1 || probe.Tools[0].Name != "search" || len(probe.Tools[0].Fingerprint) != 64 {
					t.Fatal("healthy probe was not retained")
				}
			}
			found := 0
			for _, ev := range mcpLedgerEventsFrom(t, f.store, auth.GlobalSecretScope, 1) {
				if ev.Action == "mcp_gateway.server.test" && ev.Actor == p.Actor() && ev.ActorKind == p.ActorKind() && len(ev.Sig) == 64 && ev.Seq > 0 {
					found++
				}
			}
			if found != 1 {
				t.Fatal("probe lost its durable attributed audit")
			}
			if tc.at != "" && !strings.Contains(logs.String(), "upstream credential disclosure refused") {
				t.Fatal("operator log lost the fixed credential refusal")
			}
			for _, value := range []string{tc.header, tc.echo} {
				if value != "" && strings.Contains(logs.String(), value) {
					t.Fatal("operator log exposed credential")
				}
			}
		})
	}
}
