// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

type capturedMCPSubscriptionRequest struct {
	authorization string
	method        string
	version       string
	body          string
}

func mcpSubscriptionRequest(token, cursor string) *http.Request {
	body := `{"jsonrpc":"2.0","id":7,"method":"subscriptions/listen","params":{"notifications":{"toolsListChanged":true},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"gateway-test","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}`
	request := httptest.NewRequest(http.MethodPost, mcpReviewResource, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2026-07-28")
	request.Header.Set("Mcp-Method", mcpc.SubscriptionListenMethod)
	if cursor != "" {
		request.Header.Set("Last-Event-ID", cursor)
	}
	return request
}

func mcpSubscriptionEventIDs(body string) []string {
	var ids []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "id: ") {
			ids = append(ids, strings.TrimPrefix(line, "id: "))
		}
	}
	return ids
}

func TestMCPDurableSubscriptionsOperatorConfig(t *testing.T) {
	const raw = `{"mcp":{"durable_subscriptions":{"workspace_id":"11111111-1111-7111-8111-111111111111"}}}`
	var decoded agentGatewayConfig
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("decode durable subscription config: %v", err)
	}
	if decoded.MCP == nil || decoded.MCP.DurableSubscriptions == nil ||
		decoded.MCP.DurableSubscriptions.WorkspaceID != "11111111-1111-7111-8111-111111111111" {
		t.Fatalf("decoded durable subscription config = %#v", decoded.MCP)
	}
	if off, err := buildMCPSubscriptionLedger(nil, "", nil, ""); err != nil || off != nil {
		t.Fatalf("absent durable subscription config = (%T, %v), want explicit off", off, err)
	}

	tenant, workspace := model.NewTenantID(), model.NewID()
	configured, err := buildMCPSubscriptionLedger(
		&engine{sessionsMod: sessions.New()}, tenant,
		&mcpDurableSubscriptionsConfig{WorkspaceID: workspace.String()},
		"HTTPS://MCP.EXAMPLE/gateway/",
	)
	if err != nil {
		t.Fatalf("build durable subscription config: %v", err)
	}
	wired, ok := configured.(*mcpSubscriptionLedger)
	if !ok || wired.tenant != tenant || wired.workspace != workspace ||
		wired.peerAuthority != "https://mcp.example/gateway" {
		t.Fatalf("wired durable subscription route = %#v", configured)
	}

	tests := []struct {
		name      string
		eng       *engine
		tenant    model.TenantID
		workspace string
		peer      string
	}{
		{name: "sessions unavailable", eng: &engine{}, tenant: tenant, workspace: workspace.String(), peer: "https://mcp.example"},
		{name: "tenant unavailable", eng: &engine{sessionsMod: sessions.New()}, workspace: workspace.String(), peer: "https://mcp.example"},
		{name: "workspace invalid", eng: &engine{sessionsMod: sessions.New()}, tenant: tenant, workspace: "invalid", peer: "https://mcp.example"},
		{name: "peer unavailable", eng: &engine{sessionsMod: sessions.New()}, tenant: tenant, workspace: workspace.String()},
		{name: "peer query rejected", eng: &engine{sessionsMod: sessions.New()}, tenant: tenant, workspace: workspace.String(), peer: "https://mcp.example/?route=other"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := buildMCPSubscriptionLedger(
				tc.eng, tc.tenant, &mcpDurableSubscriptionsConfig{WorkspaceID: tc.workspace}, tc.peer,
			)
			if err == nil || got != nil {
				t.Fatalf("invalid durable subscription config = (%T, %v), want nil + error", got, err)
			}
		})
	}
}

func TestMCPGatewaySubscriptionRelayWiresDurableSessionsAndResumes(t *testing.T) {
	sessionsModule, st, tenant := newSessionsStore(t)
	var workspaceID model.ID
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		workspace, err := sc.DefaultWorkspace(context.Background())
		workspaceID = workspace.ID
		return err
	}); err != nil {
		t.Fatalf("read subscription workspace: %v", err)
	}

	var mu sync.Mutex
	var captured []capturedMCPSubscriptionRequest
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var envelope json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Errorf("decode upstream subscription request: %v", err)
		}
		mu.Lock()
		captured = append(captured, capturedMCPSubscriptionRequest{
			authorization: r.Header.Get("Authorization"), method: r.Header.Get("Mcp-Method"),
			version: r.Header.Get("MCP-Protocol-Version"), body: string(envelope),
		})
		call := len(captured)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/subscriptions/acknowledged\",\"params\":{\"_meta\":{\"io.modelcontextprotocol/subscriptionId\":1}}}\n\n")
		if call == 1 {
			_, _ = fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\",\"params\":{\"_meta\":{\"io.modelcontextprotocol/subscriptionId\":1},\"change\":1}}\n\n")
			_, _ = fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\",\"params\":{\"_meta\":{\"io.modelcontextprotocol/subscriptionId\":1},\"change\":2}}\n\n")
			return // deliberate transport truncation: no correlated result
		}
		_, _ = fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\n\n")
	}))
	defer upstream.Close()

	token, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")
	cfg := &mcpGatewayConfig{
		Resource: mcpReviewResource, AuthorizationServers: []string{"https://auth.review.example"},
		Issuer: "https://auth.review.example", IssuerJWKS: json.RawMessage(jwks),
		Tenant: tenant.String(), UpstreamURL: upstream.URL, UpstreamAuth: "Bearer upstream-only",
		Tools:                []mcpc.ToolPolicy{{Name: "search", RequiredScope: "tools:read"}},
		NextRevisionHeaders:  true,
		DurableSubscriptions: &mcpDurableSubscriptionsConfig{WorkspaceID: workspaceID.String()},
	}
	eng := &engine{store: st, sessionsMod: sessionsModule, log: discardLogger()}
	firstRS, _, err := buildMCPResourceServer(eng, cfg, discardLogger())
	if err != nil {
		t.Fatalf("build first subscription Resource Server: %v", err)
	}
	first := httptest.NewRecorder()
	firstRS.ServeHTTP(first, mcpSubscriptionRequest(token, ""))
	firstIDs := mcpSubscriptionEventIDs(first.Body.String())
	if first.Code != http.StatusOK || len(firstIDs) != 2 || strings.Contains(first.Body.String(), `"result"`) {
		t.Fatalf("first truncated downstream = status %d ids=%v body=%s", first.Code, firstIDs, first.Body.String())
	}

	filterJSON, err := json.Marshal(mcpc.SubscriptionFilter{ToolsListChanged: true})
	if err != nil {
		t.Fatalf("marshal filter: %v", err)
	}
	filterDigest := sha256.Sum256(filterJSON)
	ledger, err := newMCPSubscriptionLedger(tenant, workspaceID, upstream.URL, sessionsModule)
	if err != nil {
		t.Fatalf("rebuild subscription ledger adapter: %v", err)
	}
	page, err := ledger.CatchUp(context.Background(), mcpc.SubscriptionCatchUpRequest{
		Route: mcpc.SubscriptionRoute{
			Tenant: tenant.String(), Subject: "agent:review",
			FilterDigest: hex.EncodeToString(filterDigest[:]),
		},
		Limit: 10,
	})
	if err != nil || len(page.Events) != 2 || page.Events[0].Cursor != firstIDs[0] ||
		page.Events[1].Cursor != firstIDs[1] {
		t.Fatalf("sessions-backed subscription page = %#v, err=%v", page, err)
	}

	// Reconstructing the Resource Server simulates the gateway process state
	// disappearing. The sessions ledger remains authoritative and emits event 2
	// strictly after the downstream's last acknowledged event 1.
	restartedRS, _, err := buildMCPResourceServer(eng, cfg, discardLogger())
	if err != nil {
		t.Fatalf("build restarted subscription Resource Server: %v", err)
	}
	restarted := httptest.NewRecorder()
	restartedRS.ServeHTTP(restarted, mcpSubscriptionRequest(token, firstIDs[0]))
	restartedIDs := mcpSubscriptionEventIDs(restarted.Body.String())
	if restarted.Code != http.StatusOK || len(restartedIDs) != 1 || restartedIDs[0] != firstIDs[1] ||
		!strings.Contains(restarted.Body.String(), `"result"`) {
		t.Fatalf("restarted downstream = status %d ids=%v body=%s", restarted.Code, restartedIDs, restarted.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 2 {
		t.Fatalf("upstream subscription calls = %d, want 2", len(captured))
	}
	for _, request := range captured {
		if request.authorization != "Bearer upstream-only" ||
			request.method != mcpc.SubscriptionListenMethod || request.version != "2026-07-28" ||
			strings.Contains(request.body, token) || strings.Contains(request.body, tenant.String()) ||
			strings.Contains(request.body, "agent:review") {
			t.Fatalf("upstream subscription authority/routing = %#v", request)
		}
	}
}

func TestMCPGatewaySubscriptionRelayWithoutDurableLedgerReturns503(t *testing.T) {
	token, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer upstream.Close()
	cfg := &mcpGatewayConfig{
		Resource: mcpReviewResource, AuthorizationServers: []string{"https://auth.review.example"},
		Issuer: "https://auth.review.example", IssuerJWKS: json.RawMessage(jwks),
		Tenant: model.NewTenantID().String(), UpstreamURL: upstream.URL,
		Tools:               []mcpc.ToolPolicy{{Name: "search", RequiredScope: "tools:read"}},
		NextRevisionHeaders: true,
	}
	rs, _, err := buildMCPResourceServer(&engine{log: discardLogger()}, cfg, discardLogger())
	if err != nil {
		t.Fatalf("build Resource Server without subscription ledger: %v", err)
	}
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, mcpSubscriptionRequest(token, ""))
	if w.Code != http.StatusServiceUnavailable || calls != 0 {
		t.Fatalf("unwired durable subscription = status %d upstream calls %d body=%s", w.Code, calls, w.Body.String())
	}
}
