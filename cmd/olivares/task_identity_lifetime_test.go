// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/mcpgateway"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/engine/enginetest"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

type taskIdentityClock struct{ nanos atomic.Int64 }

func (c *taskIdentityClock) Now() model.Timestamp {
	return model.NewTimestamp(time.Unix(0, c.nanos.Load()))
}

// Both listeners are assembled by production builders over the real engine.
// The only external provider is a local Messages-protocol fixture. Credentials
// stay in memory and are never put in URLs, logs or process arguments.
func TestTaskIdentityMCPAndInferenceLifetime(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			ctx := t.Context()
			cfg := bootConfig{DataDir: t.TempDir(), Engine: backend, Version: "test", Logger: discardLog()}
			if backend == "postgres" {
				pg := enginetest.IsolatedPostgresSplitOwner(t)
				cfg.DSN, cfg.OwnerDSN, cfg.AdminDSN = pg.App, pg.Owner, pg.Admin
			}
			eng, err := boot(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = eng.Close() })
			clock := &taskIdentityClock{}
			clock.nanos.Store(time.Now().UnixNano())
			eng.authr = auth.NewAuthenticator(eng.store, clock)
			eng.sessionHooks = newSessionHookCredentials(eng.authr, eng.store, eng.sessionsMod, eng.killSwitch)
			if _, err := eng.authr.BootstrapSuperadmin(ctx, "identity@example.invalid", "task-identity-fixture-123"); err != nil {
				t.Fatal(err)
			}
			ordinary, _, err := eng.authr.Login(ctx, "identity@example.invalid", "task-identity-fixture-123", "fixture")
			if err != nil {
				t.Fatal(err)
			}
			launcher, err := eng.authr.Authenticate(ctx, ordinary)
			if err != nil {
				t.Fatal(err)
			}
			code, org, raw := doDemoViewJSON(t, eng.api.Handler(), http.MethodPost, "/v1/system/orgs", ordinary, "", map[string]any{"name": "Task identity", "slug": "task-identity"})
			if code != http.StatusCreated {
				t.Fatalf("organization: %d %s", code, raw)
			}
			tenant := model.TenantID(org["tenant_id"].(string))
			launchToken, _, err := eng.authr.IssueToken(ctx, launcher, auth.TokenSpec{Name: "task-launcher", BoundTenant: tenant, Role: auth.RoleEditor})
			if err != nil {
				t.Fatal(err)
			}
			launcher, err = eng.authr.Authenticate(ctx, launchToken)
			if err != nil {
				t.Fatal(err)
			}
			policy := map[string]any{"record_mandatory": true, "gate_model_access": false, "gate_budget": false, "gate_residency": false, "gate_context_window": false, "gate_dlp_request": false, "gate_dlp_response": false}
			if code, _, raw := doDemoViewJSON(t, eng.api.Handler(), http.MethodPut, "/v1/m/inferenceproxy/config", ordinary, tenant.String(), policy); code != http.StatusOK {
				t.Fatalf("configure fixture provider: %d %s", code, raw)
			}
			var forwards atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwards.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/count_tokens") {
					_, _ = io.WriteString(w, `{"input_tokens":2}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"msg_task_identity","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"fixture response"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":2}}`)
			}))
			t.Cleanup(upstream.Close)
			proxyConfig, _ := json.Marshal(inferenceProxyConfig{Listen: "127.0.0.1:0", Surface: "direct", Tenant: tenant.String(), BaseURL: upstream.URL, UpstreamKey: "fixture-provider-key"})
			configPath := filepath.Join(t.TempDir(), "inferenceproxy.json")
			if err := os.WriteFile(configPath, proxyConfig, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("OLIVARES_INFERENCE_PROXY_CONFIG", configPath)
			proxy, err := buildClaudeMessagesProxyServer(eng, discardLog(), "https://console.example.test")
			if err != nil || proxy == nil {
				t.Fatalf("build inference listener: %v", err)
			}
			inference := httptest.NewServer(proxy.Handler)
			t.Cleanup(inference.Close)
			eng.gatewayConfig = &mcpgateway.Config{Listen: "127.0.0.1:0", SessionTools: true}
			gateway, err := buildAgentGatewayServer(eng, discardLog())
			if err != nil || gateway == nil {
				t.Fatalf("build MCP listener: %v", err)
			}
			mcp := httptest.NewServer(gateway.Handler)
			t.Cleanup(mcp.Close)
			sid, err := eng.sessionsMod.ResolveSession(ctx, tenant, sessions.SessionBinding{Provider: sessions.ProviderOperated, ExternalID: "task-identity", Origin: sessions.OriginOperated})
			if err != nil {
				t.Fatal(err)
			}
			claim, err := eng.sessionsMod.Claim(ctx, tenant, sid, launcher.Actor(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			intent := sessions.LaunchIntent{RunRef: "task-identity", AgentRef: "task-agent", ClaimSID: sid, Holder: claim.Holder, Fence: claim.Fence, Actor: launcher.Actor(), ActorKind: launcher.ActorKind(), LauncherPrincipal: launcher}
			bearer, err := eng.sessionHooks.mint(ctx, tenant, intent)
			if err != nil {
				t.Fatal(err)
			}
			call := func(method, endpoint, token, body string, want int) {
				t.Helper()
				req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("anthropic-version", "2023-06-01")
				response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				result, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(result), token) {
					t.Fatal("response exposed the credential")
				}
				if response.StatusCode != want {
					t.Fatalf("%s: HTTP %d, want %d: %s", req.URL.Path, response.StatusCode, want, result)
				}
			}
			const mcpRequest = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
			const message = `{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"fixture"}]}]}`
			both := func(token string, want int) {
				t.Helper()
				before := forwards.Load()
				call(http.MethodPost, mcp.URL+"/session/mcp", token, mcpRequest, want)
				call(http.MethodPost, inference.URL+"/v1/messages", token, message, want)
				if want != http.StatusOK && forwards.Load() != before {
					t.Fatal("refused task reached the provider fixture")
				}
			}
			both(bearer, http.StatusOK)
			t.Log("active task: MCP catalog and Messages accepted the same launch bearer")
			if forwards.Load() == 0 {
				t.Fatal("live task did not reach the provider fixture")
			}
			principal, scope, err := eng.sessionHooks.Resolve(ctx, bearer)
			if err != nil || principal.AgentIdentity != intent.AgentRef || scope.SessionRef != sid || scope.RunRef != intent.RunRef || scope.TenantID != tenant || scope.Holder != claim.Holder || scope.Fence != claim.Fence || principal.SessionWorkspaceID != scope.WorkspaceID || principal.SessionFence != claim.Fence || principal.Superadmin {
				t.Fatal("task identity or launch scope changed")
			}
			apiServer := httptest.NewServer(eng.api.Handler())
			t.Cleanup(apiServer.Close)
			call(http.MethodGet, apiServer.URL+"/v1/auth/whoami", bearer, "", http.StatusUnauthorized)
			call(http.MethodGet, inference.URL+"/v1/organizations/spend_limits", bearer, "", http.StatusUnauthorized)
			// An ordinary credential keeps its published inference behavior.
			call(http.MethodPost, inference.URL+"/v1/messages", launchToken, message, http.StatusOK)
			beforeForeign := forwards.Load()
			foreignConfig := inferenceProxyConfig{Listen: "127.0.0.1:0", Surface: "direct", Tenant: model.NewTenantID().String(), BaseURL: upstream.URL, UpstreamKey: "fixture-provider-key"}
			foreignBytes, _ := json.Marshal(foreignConfig)
			if err := os.WriteFile(configPath, foreignBytes, 0600); err != nil {
				t.Fatal(err)
			}
			foreignProxy, err := buildClaudeMessagesProxyServer(eng, discardLog(), "https://console.example.test")
			if err != nil || foreignProxy == nil {
				t.Fatalf("build foreign-tenant listener: %v", err)
			}
			foreign := httptest.NewServer(foreignProxy.Handler)
			t.Cleanup(foreign.Close)
			call(http.MethodPost, foreign.URL+"/v1/messages", bearer, message, http.StatusForbidden)
			if forwards.Load() != beforeForeign {
				t.Fatal("task crossed its tenant boundary")
			}
			t.Log("task bearer refused by general API, inference administration and foreign tenant")
			if _, _, err := eng.sessionHooks.ResolveRun(ctx, model.TenantID(foreignConfig.Tenant), intent.RunRef); err == nil {
				t.Fatal("run resolved in another tenant")
			}
			rotated, err := eng.sessionHooks.mint(ctx, tenant, intent)
			if err != nil {
				t.Fatal(err)
			}
			both(bearer, http.StatusUnauthorized)
			both(rotated, http.StatusOK)
			eng.sessionHooks.Revoke(tenant, intent.RunRef)
			both(rotated, http.StatusUnauthorized)
			t.Log("rotated and revoked task bearers refused on MCP and Messages")
			expiring, err := eng.sessionHooks.mint(ctx, tenant, intent)
			if err != nil {
				t.Fatal(err)
			}
			clock.nanos.Add(int64(24*time.Hour - time.Nanosecond))
			both(expiring, http.StatusOK)
			clock.nanos.Add(int64(time.Nanosecond))
			both(expiring, http.StatusUnauthorized)
			t.Log("task bearer accepted immediately before expiry and refused at exact expiry")
			// The API launcher has no expiry, so the prior refusal belongs to the
			// session credential rather than an expired parent credential.
			if _, err := eng.authr.Authenticate(ctx, launchToken); err != nil {
				t.Fatal("expiry fixture expired the launcher")
			}
			ending, err := eng.sessionHooks.mint(ctx, tenant, intent)
			if err != nil {
				t.Fatal(err)
			}
			both(ending, http.StatusOK)
			if err := eng.sessionsMod.Release(ctx, tenant, sid, claim.Holder, claim.Fence); err != nil {
				t.Fatal(err)
			}
			both(ending, http.StatusUnauthorized)
			t.Log("released task claim: MCP and Messages refused the retained bearer")
			if _, _, err := eng.sessionHooks.ResolveRun(ctx, tenant, intent.RunRef); err == nil {
				t.Fatal("ended task kept in-process authority")
			}
			found := map[string]bool{}
			for _, event := range canonicalLedgerEventsFrom(t, eng.store, tenant, 0) {
				if strings.HasPrefix(event.event.Action, "inference.proxy.") {
					encoded, _ := json.Marshal(map[string]any{"event": event.event, "metadata": event.meta})
					for _, credential := range []string{bearer, rotated, expiring, ending} {
						if strings.Contains(string(encoded), credential) {
							t.Fatal("ledger exposed a task credential")
						}
					}
					found[event.event.Action] = true
				}
			}
			if !found["inference.proxy.authorized"] || !found["inference.proxy.recorded"] {
				t.Fatal("successful task inference was not persisted in the ledger")
			}
		})
	}
}
