// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestManagedSessionHTTPSInitializesAndKeepsIdentity(t *testing.T) {
	m, f, actor := mcpManagementFixture(t)
	var mu sync.Mutex
	initialized := map[string]bool{}
	called := map[string]int{}
	nextSession := 0
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer fixture-upstream-credential" {
			t.Error("session did not use its separate upstream credential")
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		sid := r.Header.Get("Mcp-Session-Id")
		if req.Method == "initialize" {
			nextSession++
			sid = fmt.Sprintf("fixture-session-%d", nextSession)
			initialized[sid] = false
			w.Header().Set("Mcp-Session-Id", sid)
		} else {
			_, known := initialized[sid]
			if !known || r.Header.Get("MCP-Protocol-Version") != "2025-11-25" {
				http.Error(w, "initialize required", http.StatusBadRequest)
				return
			}
			if req.Method == "notifications/initialized" {
				initialized[sid] = true
				w.WriteHeader(http.StatusAccepted)
				return
			}
			if !initialized[sid] {
				http.Error(w, "initialized notification required", http.StatusBadRequest)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		result := `{"tools":[{"name":"echo","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}}]}`
		if req.Method == "initialize" {
			result = `{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fixture","version":"1"}}`
		}
		if req.Method == "tools/call" {
			called[sid]++
			result = `{"content":[{"type":"text","text":"session call succeeded"}]}`
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, req.ID, result)
	}))
	t.Cleanup(upstream.Close)
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	original := http.DefaultTransport
	base := original.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = base
	t.Cleanup(func() { http.DefaultTransport = original })
	if _, err := m.secrets.Put(t.Context(), actor, f.tenant, "mcp/https-session", "Bearer fixture-upstream-credential", "fixture"); err != nil {
		t.Fatal(err)
	}
	in := auth.MCPGatewayServerInput{Name: "Session HTTPS", URL: upstream.URL, CredentialRef: "store:mcp/https-session", EgressCIDRs: []string{"127.0.0.1/32"}}
	out, err := m.PutServer(t.Context(), actor, f.tenant, 0, "", in)
	if err != nil {
		t.Fatal(err)
	}
	out, err = m.TestServer(t.Context(), actor, f.tenant, out.Version, out.Servers[0].ID)
	if err != nil || out.Servers[0].Probe.State != "ok" {
		t.Fatalf("administrator's HTTPS test failed: %v", err)
	}
	in.Enabled = true
	out, err = m.PutServer(t.Context(), actor, f.tenant, out.Version, out.Servers[0].ID, in)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for _, run := range []string{"session-a", "session-b"} {
		p := auth.Principal{SessionRunRef: run, SessionIdentity: run}
		companion := sessions.RuntimeCompanion{Context: ctx, LaunchID: model.NewID()}
		entry, err := m.cachedSessionServer(ctx, f.tenant, p, companion, out.Version, out.Servers[0])
		if err != nil {
			t.Fatalf("enabled HTTPS server unavailable to session: %v", err)
		}
		t.Cleanup(entry.close)
		tools, err := listManagedTools(ctx, entry.upstream)
		if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
			t.Fatalf("session could not list the tested tool: %v", err)
		}
		// This is the identity SessionToolServer supplies after authentication;
		// catalog inspection and the real call must use one upstream handshake.
		result, err := entry.upstream.Forward(ctx, mcpc.UpstreamRequest{Method: "tools/call", Subject: p.SessionIdentity, ClientID: companion.LaunchID.String(), Scopes: []string{"tools:call"}, Params: []byte(`{"name":"echo","arguments":{}}`)})
		if err != nil || !strings.Contains(string(result.Result), "session call succeeded") {
			t.Fatalf("listed HTTPS tool failed for its session identity: %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if nextSession != 3 || len(called) != 2 {
		t.Fatalf("session launches did not get separate initialized upstream sessions: initializes=%d callers=%d", nextSession, len(called))
	}
	for _, calls := range called {
		if calls != 1 {
			t.Fatal("a tool call was repeated or shared across launches")
		}
	}
}
