// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

func validMCPDurableTasksConfig() *mcpDurableTasksConfig {
	return &mcpDurableTasksConfig{
		WorkspaceID: model.NewID().String(), BindingSpecID: model.NewID().String(),
		BindingSpecGeneration: 3, OwnerKind: "agent", OwnerRef: "agent:operations",
		InterruptChannelID: model.NewID().String(), InterruptSenderUserID: model.NewID().String(),
		InterruptRecipientUserID: model.NewID().String(),
	}
}

func TestMCPDurableTasksOperatorJSONShape(t *testing.T) {
	const raw = `{"mcp":{"durable_tasks":{"workspace_id":"11111111-1111-4111-8111-111111111111","binding_spec_id":"22222222-2222-4222-8222-222222222222","binding_spec_generation":7,"owner_kind":"agent","owner_ref":"agent:operations","interrupt_channel_id":"33333333-3333-4333-8333-333333333333","interrupt_sender_user_id":"44444444-4444-4444-8444-444444444444","interrupt_recipient_user_id":"55555555-5555-4555-8555-555555555555"}}}`
	var cfg agentGatewayConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("decode operator config: %v", err)
	}
	if cfg.MCP == nil || cfg.MCP.DurableTasks == nil {
		t.Fatal("durable_tasks JSON block was not decoded")
	}
	got := cfg.MCP.DurableTasks
	if got.WorkspaceID != "11111111-1111-4111-8111-111111111111" ||
		got.BindingSpecID != "22222222-2222-4222-8222-222222222222" ||
		got.BindingSpecGeneration != 7 || got.OwnerKind != "agent" || got.OwnerRef != "agent:operations" ||
		got.InterruptChannelID != "33333333-3333-4333-8333-333333333333" ||
		got.InterruptSenderUserID != "44444444-4444-4444-8444-444444444444" ||
		got.InterruptRecipientUserID != "55555555-5555-4555-8555-555555555555" {
		t.Fatalf("decoded durable_tasks route = %#v", got)
	}
}

func TestMCPDurableTasksConfigOffAndExplicitRoute(t *testing.T) {
	off, err := buildMCPDurableTaskStore(nil, "", nil)
	if err != nil || off != nil {
		t.Fatalf("absent durable_tasks = (%T, %v), want explicit OFF", off, err)
	}

	tenant := model.NewTenantID()
	cfg := validMCPDurableTasksConfig()
	store, err := buildMCPDurableTaskStore(
		&engine{sessionsMod: sessions.New()}, tenant, cfg,
	)
	if err != nil {
		t.Fatalf("build configured durable task store: %v", err)
	}
	wired, ok := store.(*mcpDurableTaskStore)
	if !ok || wired == nil {
		t.Fatalf("configured durable task store = %T, want production adapter", store)
	}
	if wired.tenant != tenant || wired.config.WorkspaceID.String() != cfg.WorkspaceID ||
		wired.config.BindingSpecID.String() != cfg.BindingSpecID ||
		wired.config.Generation != cfg.BindingSpecGeneration ||
		wired.config.OwnerKind != cfg.OwnerKind || wired.config.OwnerRef != cfg.OwnerRef ||
		wired.config.InterruptRoute.ChannelID.String() != cfg.InterruptChannelID ||
		wired.config.InterruptRoute.SenderUserID.String() != cfg.InterruptSenderUserID ||
		wired.config.InterruptRoute.RecipientUserID.String() != cfg.InterruptRecipientUserID {
		t.Fatalf("configured route was not preserved: %#v", wired)
	}
}

func TestMCPDurableTasksConfigIncompleteRefusesComposition(t *testing.T) {
	tenant := model.NewTenantID()
	valid := func() *mcpDurableTasksConfig { return validMCPDurableTasksConfig() }
	tests := []struct {
		name   string
		eng    *engine
		tenant model.TenantID
		mutate func(*mcpDurableTasksConfig)
	}{
		{name: "sessions kernel unavailable", eng: &engine{}, tenant: tenant},
		{name: "tenant absent", eng: &engine{sessionsMod: sessions.New()}},
		{name: "workspace absent", eng: &engine{sessionsMod: sessions.New()}, tenant: tenant,
			mutate: func(cfg *mcpDurableTasksConfig) { cfg.WorkspaceID = "" }},
		{name: "binding spec absent", eng: &engine{sessionsMod: sessions.New()}, tenant: tenant,
			mutate: func(cfg *mcpDurableTasksConfig) { cfg.BindingSpecID = "" }},
		{name: "binding generation absent", eng: &engine{sessionsMod: sessions.New()}, tenant: tenant,
			mutate: func(cfg *mcpDurableTasksConfig) { cfg.BindingSpecGeneration = 0 }},
		{name: "owner kind invalid", eng: &engine{sessionsMod: sessions.New()}, tenant: tenant,
			mutate: func(cfg *mcpDurableTasksConfig) { cfg.OwnerKind = "remote" }},
		{name: "owner ref absent", eng: &engine{sessionsMod: sessions.New()}, tenant: tenant,
			mutate: func(cfg *mcpDurableTasksConfig) { cfg.OwnerRef = "" }},
		{name: "interrupt channel absent", eng: &engine{sessionsMod: sessions.New()}, tenant: tenant,
			mutate: func(cfg *mcpDurableTasksConfig) { cfg.InterruptChannelID = "" }},
		{name: "interrupt sender absent", eng: &engine{sessionsMod: sessions.New()}, tenant: tenant,
			mutate: func(cfg *mcpDurableTasksConfig) { cfg.InterruptSenderUserID = "" }},
		{name: "interrupt recipient absent", eng: &engine{sessionsMod: sessions.New()}, tenant: tenant,
			mutate: func(cfg *mcpDurableTasksConfig) { cfg.InterruptRecipientUserID = "" }},
		{name: "interrupt route self-targets", eng: &engine{sessionsMod: sessions.New()}, tenant: tenant,
			mutate: func(cfg *mcpDurableTasksConfig) { cfg.InterruptRecipientUserID = cfg.InterruptSenderUserID }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := valid()
			if tc.mutate != nil {
				tc.mutate(cfg)
			}
			if got, err := buildMCPDurableTaskStore(tc.eng, tc.tenant, cfg); err == nil || got != nil {
				t.Fatalf("incomplete durable_tasks = (%T, %v), want nil + error", got, err)
			}
		})
	}
}

func TestMCPGatewayDurableTasksAbsentKeepsResourceServerAvailable(t *testing.T) {
	token, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")
	upstream := durableTasksCapabilityUpstream(t)
	defer upstream.Close()
	cfg := &mcpGatewayConfig{
		Resource: mcpReviewResource, AuthorizationServers: []string{"https://auth.review.example"},
		Issuer: "https://auth.review.example", IssuerJWKS: json.RawMessage(jwks),
		Tenant:      model.NewTenantID().String(),
		Tools:       []mcpc.ToolPolicy{{Name: "search", RequiredScope: "tools:read"}},
		UpstreamURL: upstream.URL,
	}
	rs, _, err := buildMCPResourceServer(&engine{}, cfg, discardLogger())
	if err != nil || rs == nil {
		t.Fatalf("MCP Resource Server without durable_tasks = (%v, %v), want mounted synchronous surface", rs, err)
	}
	if capabilities := initializeMCPCapabilities(t, rs, token); capabilities["tasks"] != nil {
		t.Fatal("MCP Resource Server advertised Tasks without durable_tasks configuration")
	}
}

func TestMCPGatewayWiresConfiguredDurableTaskStore(t *testing.T) {
	sessionsModule, st, tenant := newSessionsStore(t)
	var workspaceID model.ID
	if err := st.View(context.Background(), tenant, func(sc store.Scope) error {
		workspace, err := sc.DefaultWorkspace(context.Background())
		workspaceID = workspace.ID
		return err
	}); err != nil {
		t.Fatalf("read default workspace: %v", err)
	}

	token, jwks := mintReviewToken(t, mcpReviewResource, "tools:read")
	upstream := durableTasksCapabilityUpstream(t)
	defer upstream.Close()
	activeSpec := activateMCPRestartSpec(t, sessionsModule, tenant, workspaceID, upstream.URL)
	cfg := &mcpGatewayConfig{
		Resource: mcpReviewResource, AuthorizationServers: []string{"https://auth.review.example"},
		Issuer: "https://auth.review.example", IssuerJWKS: json.RawMessage(jwks),
		Tenant: tenant.String(), UpstreamURL: upstream.URL,
		Tools:        []mcpc.ToolPolicy{{Name: "search", RequiredScope: "tools:read"}},
		DurableTasks: validMCPDurableTasksConfig(),
	}
	cfg.DurableTasks.WorkspaceID = workspaceID.String()
	cfg.DurableTasks.BindingSpecID = activeSpec.ID.String()
	cfg.DurableTasks.BindingSpecGeneration = activeSpec.Generation
	reconcileMux := newProtocolBindingReconcileMux()
	rs, _, err := buildMCPResourceServer(&engine{
		sessionsMod: sessionsModule, protocolBindingReconciler: reconcileMux,
	}, cfg, discardLogger())
	if err != nil {
		t.Fatalf("build MCP Resource Server with durable_tasks: %v", err)
	}
	if adapter, routeErr := reconcileMux.route(sessions.BindingProtocolMCP); routeErr != nil {
		t.Fatalf("MCP protocol binding reconcile route: %v", routeErr)
	} else if _, ok := adapter.(*mcpProtocolBindingReconciler); !ok {
		t.Fatalf("MCP protocol binding reconcile route = %T", adapter)
	}
	if capabilities := initializeMCPCapabilities(t, rs, token); capabilities["tasks"] == nil {
		t.Fatal("MCP Resource Server removed Tasks despite a configured durable store")
	}
}

func durableTasksCapabilityUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-11-25","capabilities":{"tools":{},"tasks":{}}}}`))
	}))
}

func initializeMCPCapabilities(t *testing.T, rs *mcpc.ResourceServer, token string) map[string]json.RawMessage {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, mcpReviewResource,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	rs.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("initialize status = %d; body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Result struct {
			Capabilities map[string]json.RawMessage `json:"capabilities"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	return response.Result.Capabilities
}

func TestMCPGatewayIncompleteDurableTasksFailsBeforeMount(t *testing.T) {
	cfg := &mcpGatewayConfig{
		Resource: "https://mcp.example.com/mcp", Tenant: model.NewTenantID().String(),
		DurableTasks: &mcpDurableTasksConfig{
			WorkspaceID: model.NewID().String(), BindingSpecID: model.NewID().String(),
			BindingSpecGeneration: 1, OwnerKind: "agent",
		},
	}
	_, _, err := buildMCPResourceServer(
		&engine{sessionsMod: sessions.New()}, cfg, discardLogger(),
	)
	if err == nil || !strings.Contains(err.Error(), "durable tasks") {
		t.Fatalf("incomplete durable_tasks error = %v, want durable route refusal", err)
	}
}
