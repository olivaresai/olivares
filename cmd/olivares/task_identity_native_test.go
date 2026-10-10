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
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/mcpgateway"
	"github.com/olivaresai/olivares/core/engine/enginetest"
)

// Run locally through heavy-job: this starts the production native runner with
// a provider-protocol fixture. The credential reaches the test only in a private
// loopback header from that child; it is never written to disk or stdout.
func TestTaskIdentityNativeMCPAndInference(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			python, err := exec.LookPath("python3")
			if err != nil {
				t.Fatal("native identity qualification requires python3")
			}
			credentials := make(chan string, 1)
			capture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				credential := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				if !strings.HasPrefix(credential, sessionHookTokenPrefix) {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				select {
				case credentials <- credential:
					w.WriteHeader(http.StatusNoContent)
				default:
					w.WriteHeader(http.StatusConflict)
				}
			}))
			t.Cleanup(capture.Close)
			folder, home := t.TempDir(), t.TempDir()
			agent := filepath.Join(t.TempDir(), "identity-agent.py")
			fixture := "#!" + python + "\nimport json, os, sys, urllib.request\n" +
				"opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))\n" +
				"request = urllib.request.Request(" + `"` + capture.URL + `"` + ", data=b'', headers={'Authorization': 'Bearer ' + os.environ['OLIVARES_HOOK_PEP_TOKEN']})\n" +
				"opener.open(request, timeout=10).close()\n" +
				"print(json.dumps({'type':'system','subtype':'init','session_id':'task-identity-native'}), flush=True)\n" +
				"for line in sys.stdin:\n    pass\n"
			if err := os.WriteFile(agent, []byte(fixture), 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"OLIVARES_SOURCES_CONFIG", "OLIVARES_AGENT_GATEWAY_CONFIG", "OLIVARES_HOOK_PEP_CONFIG", "OLIVARES_COMMUNICATION_ACTIVATION", envSessionTokenFile, envSessionRuntimeWIF} {
				t.Setenv(name, "")
			}
			t.Setenv(envSessionClaudeBin, agent)
			cfg := bootConfig{DataDir: t.TempDir(), Engine: backend, Version: "test", Logger: discardLog()}
			if backend == "postgres" {
				pg := enginetest.IsolatedPostgresSplitOwner(t)
				cfg.DSN, cfg.OwnerDSN, cfg.AdminDSN = pg.App, pg.Owner, pg.Admin
			}
			eng, err := boot(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = eng.Close() })
			if _, err := eng.authr.BootstrapSuperadmin(t.Context(), "native-identity@example.invalid", "native-identity-fixture-123"); err != nil {
				t.Fatal(err)
			}
			admin, _, err := eng.authr.Login(t.Context(), "native-identity@example.invalid", "native-identity-fixture-123", "fixture")
			if err != nil {
				t.Fatal(err)
			}
			var tenant string
			do := func(method, path string, body any, want int) map[string]any {
				t.Helper()
				code, result, raw := doDemoViewJSON(t, eng.api.Handler(), method, path, admin, tenant, body)
				if code != want {
					t.Fatalf("%s %s: HTTP %d, want %d: %s", method, path, code, want, raw)
				}
				return result
			}
			org := do(http.MethodPost, "/v1/system/orgs", map[string]any{"name": "Native task identity", "slug": "native-task-identity"}, http.StatusCreated)
			tenant = org["tenant_id"].(string)
			policy := map[string]any{"record_mandatory": true, "gate_model_access": false, "gate_budget": false, "gate_residency": false, "gate_context_window": false, "gate_dlp_request": false, "gate_dlp_response": false}
			do(http.MethodPut, "/v1/m/inferenceproxy/config", policy, http.StatusOK)
			var forwards atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwards.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"msg_native_identity","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"fixture response"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":2}}`)
			}))
			t.Cleanup(upstream.Close)
			config, _ := json.Marshal(inferenceProxyConfig{Listen: "127.0.0.1:0", Surface: "direct", Tenant: tenant, BaseURL: upstream.URL, UpstreamKey: "fixture-provider-key"})
			configPath := filepath.Join(t.TempDir(), "inferenceproxy.json")
			if err := os.WriteFile(configPath, config, 0600); err != nil {
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
			pep, err := buildClaudeHookPEPServer(eng, discardLog())
			if err != nil || pep == nil {
				t.Fatalf("build launch hook listener: %v", err)
			}
			hooks := httptest.NewServer(pep.Handler)
			t.Cleanup(hooks.Close)
			if err := eng.hookCredentials().bindEndpoint(hooks.Listener.Addr().String()); err != nil {
				t.Fatal(err)
			}
			workspace := do(http.MethodPost, "/v1/m/sessions/workspaces", map[string]any{"root_path": folder, "name": "identity folder"}, http.StatusCreated)
			profile := do(http.MethodPost, "/v1/m/sessions/provider-profiles", map[string]any{"driver": "claude", "auth_source": "provider_account_home", "config_home": home, "user_home": home, "display_name": "identity fixture"}, http.StatusCreated)
			run := do(http.MethodPost, "/v1/m/sessions/runs", map[string]any{"name": "Native task identity", "transport": "stream-json", "permission_mode": "plan", "isolation": "native", "workspace_ref": workspace["workspace_ref"], "provider_profile_ref": profile["profile_ref"]}, http.StatusCreated)
			if run["state"] != "running" {
				t.Fatalf("native fixture did not launch: %v", run)
			}
			runRef := run["run_ref"].(string)
			t.Cleanup(func() { do(http.MethodPost, "/v1/m/sessions/runs/"+runRef+"/stop", nil, http.StatusOK) })
			var bearer string
			select {
			case bearer = <-credentials:
			case <-time.After(10 * time.Second):
				t.Fatal("the native child did not receive its task credential")
			}
			principal, scope, err := eng.sessionHooks.Resolve(t.Context(), bearer)
			if err != nil {
				t.Fatalf("resolve native task credential: %v", err)
			}
			if scope.RunRef != runRef || principal.SessionRunRef != runRef || scope.SessionRef == "" || principal.SessionIdentity != scope.SessionRef || scope.Fence <= 0 || principal.SessionFence != scope.Fence {
				t.Fatal("native launch lost its session identity or claim fence")
			}
			// A human-launched run has a distinct session identity. AgentIdentity
			// is populated only when an authenticated agent drove the launch.
			if principal.Superadmin || principal.AgentIdentity != scope.AgentRef || scope.TenantID.String() != tenant || principal.SessionWorkspaceID != scope.WorkspaceID {
				t.Fatal("native launch changed its principal or tenant/workspace scope")
			}
			if scope.FolderPath != folder {
				t.Fatal("native launch lost its folder boundary")
			}
			call := func(endpoint, body string, want int, isMCP bool) {
				t.Helper()
				req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint, strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+bearer)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("anthropic-version", "2023-06-01")
				response, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				raw, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(raw), bearer) {
					t.Fatal("native task response exposed its credential")
				}
				if response.StatusCode != want {
					t.Fatalf("%s: HTTP %d, want %d: %s", req.URL.Path, response.StatusCode, want, raw)
				}
				if isMCP && want == http.StatusOK {
					var result struct {
						Error  json.RawMessage `json:"error"`
						Result struct {
							IsError           bool `json:"isError"`
							StructuredContent struct {
								Status int `json:"http_status"`
							} `json:"structuredContent"`
						} `json:"result"`
					}
					if err := json.Unmarshal(raw, &result); err != nil || len(result.Error) > 0 || result.Result.IsError || result.Result.StructuredContent.Status != http.StatusOK {
						t.Fatalf("native MCP work operation failed: %s", raw)
					}
				}
			}
			const workList = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"olivares_work_list","arguments":{"kind":"work","limit":5}}}`
			const message = `{"model":"claude-sonnet-4-6","max_tokens":16,"messages":[{"role":"user","content":[{"type":"text","text":"fixture"}]}]}`
			call(mcp.URL+"/session/mcp", workList, http.StatusOK, true)
			call(inference.URL+"/v1/messages", message, http.StatusOK, false)
			t.Log("native task: real MCP work operation and Messages accepted its provisioned bearer")
			beforeStop := forwards.Load()
			if beforeStop == 0 {
				t.Fatal("native task did not reach its inference provider")
			}
			do(http.MethodPost, "/v1/m/sessions/runs/"+runRef+"/stop", nil, http.StatusOK)
			deadline := time.Now().Add(10 * time.Second)
			for {
				ended := do(http.MethodGet, "/v1/m/sessions/runs/"+runRef, nil, http.StatusOK)
				if ended["state"] == "stopped" {
					if ended["pid"] != nil {
						t.Fatal("stopped task still advertises its child process")
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("authorized native stop did not finish")
				}
				time.Sleep(10 * time.Millisecond)
			}
			call(mcp.URL+"/session/mcp", workList, http.StatusUnauthorized, true)
			call(inference.URL+"/v1/messages", message, http.StatusUnauthorized, false)
			if forwards.Load() != beforeStop {
				t.Fatal("stopped native task reached its inference provider")
			}
			t.Log("authorized native stop: MCP work and Messages refused the retained bearer")
		})
	}
}
