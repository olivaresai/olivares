// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

// Use the engine's persisted workspace-confinement evaluator, not a literal
// allow policy. Its recipient read must finish before the inbox-only mutation.
func TestSessionMCPPeerDeliveryWithComposedAuthorization(t *testing.T) {
	testSessionMCPPeerDelivery(t, model.NewID().String())
}

func TestSessionMCPPeerDeliveryWithStringIdempotencyKey(t *testing.T) {
	testSessionMCPPeerDelivery(t, "dogfood-rc10-"+model.NewID().String())
}

func testSessionMCPPeerDelivery(t *testing.T, key string) {
	t.Helper()
	h := newHarness(t)
	m := h.set.sessions
	m.UseWorkAuthorizer(auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
	m.UseWorkIdentityResolver(workIdentityResolver{st: h.st, sessions: m, agentLifecycle: h.set.gov})
	m.UseWorkContentGuard(workContentGuard{})
	sessions.WithRunner(approvalProjectionRunner{})(m)
	m.EnableProfiledLaunches()
	m.UseExecutionEnvironmentRef("peer-delivery-test")
	credentials := newSessionHookCredentials(h.authr, h.st, m, h.set.gov)
	tokens, identities := map[string]string{}, map[string]string{}
	m.UseLaunchGate(approvalProjectionLaunchGate(func(ctx context.Context, tenant model.TenantID, intent sessions.LaunchIntent) (sessions.LaunchDecision, error) {
		token, err := credentials.mint(ctx, tenant, intent)
		tokens[intent.RunRef], identities[intent.RunRef] = token, intent.ClaimSID
		return sessions.LaunchDecision{Allowed: err == nil}, err
	}))
	launch := func() string {
		t.Helper()
		var profile struct {
			Ref string `json:"profile_ref"`
		}
		if code := h.reqInto(http.MethodPost, "/v1/m/sessions/provider-profiles", h.adminToken, h.tenantA, map[string]any{
			"driver": "claude", "auth_source": "provider_account_home", "config_home": t.TempDir(), "user_home": t.TempDir(),
		}, &profile); code != http.StatusCreated {
			t.Fatalf("profile = %d", code)
		}
		var folder struct {
			Ref string `json:"workspace_ref"`
		}
		if code := h.reqInto(http.MethodPost, "/v1/m/sessions/workspaces", h.adminToken, h.tenantA, map[string]any{
			"name": "Owned peer folder", "root_path": t.TempDir(),
		}, &folder); code != http.StatusCreated {
			t.Fatalf("folder = %d", code)
		}
		var run struct {
			Ref string `json:"run_ref"`
		}
		code, raw := h.req(http.MethodPost, "/v1/m/sessions/runs", h.adminToken, h.tenantA, map[string]any{
			"transport": "stream-json", "permission_mode": "default", "isolation": "native",
			"provider_profile_ref": profile.Ref, "workspace_ref": folder.Ref,
		})
		if code != http.StatusCreated {
			t.Fatalf("launch = %d %s", code, raw)
		}
		if err := json.Unmarshal(raw, &run); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { h.req(http.MethodPost, "/v1/m/sessions/runs/"+run.Ref+"/stop", h.adminToken, h.tenantA, nil) })
		return run.Ref
	}
	sender, recipient := launch(), launch()
	selectPeers := func(peers []string) {
		t.Helper()
		if code, _ := h.req(http.MethodPut, "/v1/m/sessions/runs/"+sender+"/peers", h.adminToken, h.tenantA, map[string]any{"peers": peers}); code != http.StatusOK {
			t.Fatalf("select peers = %d", code)
		}
	}
	selectPeers([]string{identities[recipient]})
	handler := &sessionMCPHandler{authr: credentials, issuedSessionOnly: true, work: m.CallSessionWork}
	call := func(run, name string, arguments any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": arguments}})
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		r := httptest.NewRequest(http.MethodPost, "/session/mcp", bytes.NewReader(raw)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+tokens[run])
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		var reply struct {
			Result struct {
				Content map[string]any `json:"structuredContent"`
			} `json:"result"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || w.Code != http.StatusOK || reply.Result.Content == nil {
			t.Fatalf("%s = %d %s", name, w.Code, w.Body.String())
		}
		return reply.Result.Content
	}
	message := map[string]any{"to_sid": identities[recipient], "title": "Peer report", "brief_md": "MCC peer delivery", "idempotency_key": key}
	sent := call(sender, "olivares_peer_send", message)
	inbox := call(recipient, "olivares_peer_inbox", map[string]any{})
	items, _ := inbox["body"].(map[string]any)["items"].([]any)
	if sent["http_status"] != float64(http.StatusOK) || inbox["http_status"] != float64(http.StatusOK) || len(items) != 1 {
		t.Fatalf("peer delivery: send=%v inbox=%v; want send200 and one recipient item", sent, inbox)
	}
	if item := items[0].(map[string]any); item["owner_ref"] != identities[recipient] || item["provenance_ref"] != identities[sender] || item["brief_md"] != message["brief_md"] {
		t.Fatalf("peer envelope changed: %v", item)
	}
	call(sender, "olivares_peer_send", message)
	if again := call(recipient, "olivares_peer_inbox", map[string]any{}); len(again["body"].(map[string]any)["items"].([]any)) != 1 {
		t.Fatal("idempotent peer retry duplicated the inbox item")
	}
	message["brief_md"] = "Changed payload with the same key"
	if conflict := call(sender, "olivares_peer_send", message); conflict["http_status"] != float64(http.StatusConflict) {
		t.Fatalf("changed peer retry = %v", conflict)
	}
	if own := call(sender, "olivares_peer_inbox", map[string]any{}); len(own["body"].(map[string]any)["items"].([]any)) != 0 {
		t.Fatal("sender inbox exposed recipient items")
	}
	selectPeers([]string{})
	message["idempotency_key"] = model.NewID().String()
	if denied := call(sender, "olivares_peer_send", message); denied["http_status"] != float64(http.StatusForbidden) {
		t.Fatalf("revoked peer send = %v", denied)
	}
}
