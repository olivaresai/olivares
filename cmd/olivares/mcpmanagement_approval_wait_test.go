// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/internal/mcpgateway"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// The protocol client crosses /session/mcp with a real issued session credential;
// the human uses the production approvals API. Only the provider process and the
// HTTPS tool's effect are fixtures. No vendor account or internal gate is mocked.
func TestManagedSessionMCPApprovalWaitsAndContinuesSameCall(t *testing.T) {
	for _, outcome := range []string{"approve", "reject", "timeout", "revoked"} {
		t.Run(outcome, func(t *testing.T) {
			f := newManagedMCPApprovalFixture(t)
			attempts := 1
			if outcome == "approve" {
				attempts = 2 // A second identical call needs a fresh decision.
			}
			for attempt := 0; attempt < attempts; attempt++ {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				if outcome == "timeout" {
					ctx, cancel = context.WithTimeout(t.Context(), time.Second)
					defer cancel()
				}
				req := httptest.NewRequest("POST", "/session/mcp", strings.NewReader(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":{"text":"exact input"}}}`, attempt+1, f.alias))).WithContext(ctx)
				req.Header.Set("Authorization", "Bearer "+f.token)
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				done := make(chan struct{})
				go func() { defer close(done); f.management.ServeSessionHTTP(w, req) }()
				defer func() { cancel(); <-done }()
				ref := f.pendingApproval(ctx, done, w)
				select {
				case <-done:
					t.Fatalf("MCP call returned before human review: %s", w.Body.String())
				default:
				}
				if f.calls.Load() != int32(attempt) {
					t.Fatal("destructive tool ran before human review")
				}
				f.waitForRunProjection(ctx, ref)
				if outcome == "revoked" {
					f.credentials.Revoke(f.tenant, f.run)
				}
				if outcome != "timeout" {
					decision := "approve"
					if outcome == "reject" {
						decision = "reject"
					}
					if code, _ := f.h.req("POST", "/v1/m/governance/approvals/"+ref+"/decisions", f.h.adminToken, f.h.tenantA, map[string]any{"decision": decision}); code != http.StatusCreated && code != http.StatusOK {
						t.Fatalf("human decision = %d", code)
					}
				}
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("terminal review did not return to the same MCP call")
				}
				var response struct {
					Result json.RawMessage `json:"result"`
					Error  json.RawMessage `json:"error"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				want := int32(0)
				if outcome == "approve" {
					want = int32(attempt + 1)
					if len(response.Error) != 0 || !bytes.Contains(response.Result, []byte("exact input")) {
						t.Fatalf("approved call did not complete: %s", w.Body.String())
					}
				} else if len(response.Error) == 0 || bytes.Contains(w.Body.Bytes(), []byte("(pending)")) {
					t.Fatalf("terminal refusal is unavailable: %s", w.Body.String())
				}
				if f.calls.Load() != want {
					t.Fatalf("executions = %d, want %d", f.calls.Load(), want)
				}
				f.waitForRunProjection(t.Context(), "")
				if outcome == "timeout" {
					var list struct {
						Items []json.RawMessage `json:"items"`
					}
					if code := f.h.reqInto("GET", "/v1/m/governance/approvals?status=pending", f.h.adminToken, f.h.tenantA, nil, &list); code != http.StatusOK || len(list.Items) != 0 {
						t.Fatal("timed-out call left a pending approval")
					}
				}
			}
		})
	}
}

type managedMCPApprovalFixture struct {
	t                 *testing.T
	h                 *harness
	management        *mcpManagement
	credentials       *sessionHookCredentials
	tenant            model.TenantID
	run, token, alias string
	calls             atomic.Int32
	executed          chan json.RawMessage
	process           *mcpLifecycleProcess
	onUpstreamList    atomic.Pointer[func()] // runs once, on the next upstream tools/list
}

func newManagedMCPApprovalFixture(t *testing.T, catalogue ...string) *managedMCPApprovalFixture {
	t.Helper()
	advertised := `{"tools":[{"name":"write_echo","inputSchema":{"type":"object"},"annotations":{"destructiveHint":true}}]}`
	if len(catalogue) == 1 {
		advertised = catalogue[0]
	}
	var tools struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if len(catalogue) > 1 || json.Unmarshal([]byte(advertised), &tools) != nil {
		t.Fatal("invalid upstream fixture catalogue")
	}
	validNames := map[string]bool{}
	for _, tool := range tools.Tools {
		validNames[tool.Name] = true
	}
	h := newHarness(t)
	f := &managedMCPApprovalFixture{t: t, h: h, tenant: model.TenantID(h.tenantA), executed: make(chan json.RawMessage, 8)}
	m := h.set.sessions
	sessions.WithRunner(mcpLifecycleRunner{launched: func(p *mcpLifecycleProcess) { f.process = p }})(m)
	m.UseExecutionEnvironmentRef("managed-mcp-approval-test")
	f.credentials = newSessionHookCredentials(h.authr, h.st, m, h.set.gov)
	sessions.WithLaunchGate(approvalProjectionLaunchGate(func(ctx context.Context, tenant model.TenantID, in sessions.LaunchIntent) (sessions.LaunchDecision, error) {
		var err error
		f.token, err = f.credentials.mint(ctx, tenant, in)
		return sessions.LaunchDecision{Allowed: err == nil}, err
	}))(m)
	var profile struct {
		Ref string `json:"profile_ref"`
	}
	if code := h.reqInto("POST", "/v1/m/sessions/provider-profiles", h.adminToken, h.tenantA, map[string]any{"driver": "claude", "auth_source": "provider_account_home", "config_home": t.TempDir(), "user_home": t.TempDir()}, &profile); code != http.StatusCreated {
		t.Fatalf("profile = %d", code)
	}
	var run struct {
		Ref string `json:"run_ref"`
	}
	if code := h.reqInto("POST", "/v1/m/sessions/runs", h.adminToken, h.tenantA, map[string]any{"transport": "stream-json", "permission_mode": "default", "isolation": "native", "provider_profile_ref": profile.Ref}, &run); code != http.StatusCreated {
		t.Fatalf("launch = %d", code)
	}
	f.run = run.Ref
	t.Cleanup(func() { h.req("POST", "/v1/m/sessions/runs/"+f.run+"/stop", h.adminToken, h.tenantA, nil) })
	h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
	h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
	sealer, err := newSecretSealer(t.TempDir(), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	f.management, err = newMCPManagement(h.st, auth.NewSecretStore(h.st, sealer), mcpgateway.Config{}, false)
	if err != nil {
		t.Fatal(err)
	}
	bridge := newApprovalBridge(approvalBridgeConfig{}, discardLog())
	bridge.LocalProposer = h.set.gov.EngineApprovals()
	f.management.eng = &engine{store: h.st, authr: h.authr, sessionsMod: m, sessionHooks: f.credentials, engineApprovals: h.set.gov.EngineApprovals(), approvalBridge: bridge, log: discardLog()}
	f.management.UseSessionCredentials(f.credentials.SessionCredentials)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if in.Method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if in.Method == "tools/list" {
			if hook := f.onUpstreamList.Swap(nil); hook != nil {
				(*hook)()
			}
		}
		result := advertised
		if in.Method == "initialize" {
			result = `{"protocolVersion":"2025-11-25","capabilities":{"tools":{}},"serverInfo":{"name":"fixture","version":"1"}}`
		}
		if in.Method == "tools/call" {
			var params struct {
				Name      string                     `json:"name"`
				Arguments map[string]json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(in.Params, &params); err != nil || !validNames[params.Name] || string(params.Arguments["text"]) != `"exact input"` {
				t.Errorf("approved upstream input changed: %s", in.Params)
			}
			f.calls.Add(1)
			f.executed <- append(json.RawMessage(nil), in.Params...)
			result = `{"content":[{"type":"text","text":"exact input"}]}`
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":%s}`, in.ID, result)
	}))
	t.Cleanup(upstream.Close)
	roots := x509.NewCertPool()
	roots.AddCert(upstream.Certificate())
	original := http.DefaultTransport
	transport := original.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = original; transport.CloseIdleConnections() })
	actor, err := h.authr.Authenticate(t.Context(), h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	definition := auth.MCPGatewayServerInput{Name: "Approval HTTPS", URL: upstream.URL, EgressCIDRs: []string{"127.0.0.1/32"}}
	snapshot, err := f.management.PutServer(t.Context(), actor, f.tenant, 0, "", definition)
	if err != nil {
		t.Fatal(err)
	}
	id := snapshot.Servers[0].ID
	snapshot, err = f.management.TestServer(t.Context(), actor, f.tenant, snapshot.Version, id)
	if err != nil || snapshot.Servers[0].Probe.State != "ok" {
		t.Fatalf("probe = %+v, %v", snapshot.Servers[0].Probe, err)
	}
	definition.Enabled = true
	definition.AllowedTools = []auth.MCPGatewayToolPolicy{{Name: "write_echo", RequiredScope: "tools:call", Destructive: true}}
	if _, err := f.management.PutServer(t.Context(), actor, f.tenant, snapshot.Version, id, definition); err != nil {
		t.Fatal(err)
	}
	f.alias = managedToolAlias(id, "write_echo")
	return f
}

func (f *managedMCPApprovalFixture) pendingApproval(ctx context.Context, done <-chan struct{}, w *httptest.ResponseRecorder) string {
	f.t.Helper()
	for {
		var list struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		if code := f.h.reqInto("GET", "/v1/m/governance/approvals?status=pending", f.h.adminToken, f.h.tenantA, nil, &list); code != http.StatusOK {
			f.t.Fatalf("approval list = %d", code)
		}
		if len(list.Items) > 0 {
			return list.Items[0].ID
		}
		select {
		case <-done:
			f.t.Fatalf("call ended without review: status %d, %s", w.Code, w.Body.String())
		case <-ctx.Done():
			f.t.Fatal("no approval appeared")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (f *managedMCPApprovalFixture) waitForRunProjection(ctx context.Context, want string) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for {
		var row map[string]any
		if code := f.h.reqInto("GET", "/v1/m/sessions/runs/"+f.run, f.h.adminToken, f.h.tenantA, nil, &row); code != http.StatusOK {
			f.t.Fatalf("run detail = %d", code)
		}
		got, exists := row["pending_approval_ref"]
		if want == "" && !exists || want != "" && got == want {
			return
		}
		select {
		case <-ctx.Done():
			f.t.Fatalf("pending_approval_ref = %v, want %q", got, want)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
