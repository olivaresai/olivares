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
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/secure"
)

// Follow Add -> Test -> enable without an explicit allow list over the real
// product API, then call as an issued live session. Explicit operator policies
// alone supplies only proposals. Every default asks; explicit operator policies
// override the hints and an explicit empty list must never acquire defaults.
func TestManagedMCPEnableDefaultsFromAnnotationsAndKeepsExplicitDecisions(t *testing.T) {
	f := newManagedMCPApprovalFixture(t, `{"tools":[
		{"name":"read_echo","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true}},
		{"name":"readonly_other_hint","inputSchema":{"type":"object"},"annotations":{"readOnlyHint":true,"destructiveHint":true}},
		{"name":"write_echo","inputSchema":{"type":"object"},"annotations":{"destructiveHint":true}},
		{"name":"unannotated","inputSchema":{"type":"object"}}
	]}`)
	dir := t.TempDir()
	key, _, err := secure.LoadOrCreateSigningKey(filepath.Join(dir, "audit-signing.key"))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := audit.NewSigner(key)
	if err != nil {
		t.Fatal(err)
	}
	server, err := api.New(api.Options{Store: f.h.st, Authenticator: f.h.authr, PrincipalEvidenceProducer: f.h.authr,
		Signer: signer, SetupToken: secure.NewSetupToken(filepath.Join(dir, "setup.token")),
		Authorizer: auth.NewAuthorizer(f.h.set.gov.RequestEvaluator(), auth.WithScopedGrants(f.h.set.gov.ScopedGrants())),
		Modules:    f.h.set.all, Logger: discardLogger(), Version: "test", MCPGateway: f.management, MCPGatewayRuntime: f.management})
	if err != nil {
		t.Fatal(err)
	}
	f.h.h = server.Handler()
	do := func(method, path string, body any, want int) auth.MCPGatewaySnapshot {
		t.Helper()
		var out auth.MCPGatewaySnapshot
		code, raw := f.h.req(method, path, f.h.adminToken, f.h.tenantA, body)
		if code != want || json.Unmarshal(raw, &out) != nil {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, code, want, raw)
		}
		for _, row := range out.Servers {
			if row.ProposedAllow == nil || *row.ProposedAllow == nil {
				t.Fatalf("%s %s did not carry a proposed_allow array beside probe", method, path)
			}
		}
		return out
	}
	assertProposal := func(snap auth.MCPGatewaySnapshot, want ...string) {
		t.Helper()
		if !slices.Equal(*snap.Servers[0].ProposedAllow, want) {
			t.Fatalf("proposed_allow = %v, want %v", *snap.Servers[0].ProposedAllow, want)
		}
	}
	const root = "/v1/console/mcp-gateway"
	snap := do("GET", root, nil, http.StatusOK)
	definition := snap.Servers[0].MCPGatewayServerInput
	snap = do("DELETE", root+"/servers/"+snap.Servers[0].ID, map[string]any{"version": snap.Version}, http.StatusOK)
	// Fresh Add sends only connection facts; enable alone chooses the tested defaults.
	in := map[string]any{"name": "Defaults HTTPS", "url": definition.URL, "egress_cidrs": definition.EgressCIDRs}
	snap = do("POST", root+"/servers", map[string]any{"version": snap.Version, "server": in}, http.StatusCreated)
	assertProposal(snap)
	id := snap.Servers[0].ID
	path := root + "/servers/" + id
	snap = do("POST", path+"/test", map[string]any{"version": snap.Version}, http.StatusOK)
	if snap.Servers[0].Probe.State != "ok" {
		t.Fatal("catalogue did not test successfully")
	}
	assertProposal(snap, "read_echo", "readonly_other_hint")
	if f.calls.Load() != 0 {
		t.Fatal("Test executed a tool while collecting declarations")
	}
	in["enabled"] = true
	snap = do("PUT", path, map[string]any{"version": snap.Version, "server": in}, http.StatusOK)
	assertProposal(snap, "read_echo", "readonly_other_hint")
	snap = do("GET", root, nil, http.StatusOK)
	assertProposal(snap, "read_echo", "readonly_other_hint")
	for _, name := range []string{"read_echo", "readonly_other_hint", "write_echo", "unannotated"} {
		found := false
		for _, p := range snap.Servers[0].AllowedTools {
			if p.Name == name {
				found = true
				if !p.Destructive || p.RequiredScope != "tools:call" {
					t.Errorf("default %s ask=%v scope=%q, want true/tools:call", name, p.Destructive, p.RequiredScope)
				}
			}
		}
		if !found {
			t.Errorf("tested tool %s did not acquire a default", name)
		}
	}
	// Discover public names before deliberately withdrawing a tool later.
	req := httptest.NewRequest("POST", "/session/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
	req.Header.Set("Authorization", "Bearer "+f.token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.management.ServeSessionHTTP(w, req)
	var listing struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if json.Unmarshal(w.Body.Bytes(), &listing) != nil {
		t.Fatal("session catalogue unavailable")
	}
	aliases := map[string]string{}
	for _, tool := range listing.Result.Tools {
		for _, name := range []string{"read_echo", "readonly_other_hint", "write_echo", "unannotated"} {
			if strings.HasSuffix(tool.Name, "_"+name) {
				aliases[name] = tool.Name
			}
		}
	}
	if len(aliases) != 4 {
		t.Fatal("session did not see all tested defaults")
	}
	call := func(name, disposition string, wantEffects int32) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": aliases[name], "arguments": map[string]any{"text": "exact input"}}})
		req := httptest.NewRequest("POST", "/session/mcp", bytes.NewReader(payload)).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+f.token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		done := make(chan struct{})
		go func() { defer close(done); f.management.ServeSessionHTTP(w, req) }()
		defer func() { cancel(); <-done }()
		if disposition == "ask" || disposition == "approve" {
			ref := f.pendingApproval(ctx, done, w)
			select {
			case <-done:
				t.Fatal("ask returned before a decision")
			default:
			}
			beforeDecision, decision := wantEffects, "reject"
			if disposition == "approve" {
				beforeDecision, decision = wantEffects-1, "approve"
			}
			if f.calls.Load() != beforeDecision {
				t.Fatal("tool ran before the administrator's decision")
			}
			if code, _ := f.h.req("POST", "/v1/m/governance/approvals/"+ref+"/decisions", f.h.adminToken, f.h.tenantA, map[string]any{"decision": decision}); code != http.StatusOK && code != http.StatusCreated {
				t.Fatalf("%s = %d", decision, code)
			}
		}
		select {
		case <-done:
		case <-ctx.Done():
			t.Fatal("tool policy did not finish the call")
		}
		var response struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if json.Unmarshal(w.Body.Bytes(), &response) != nil || f.calls.Load() != wantEffects {
			t.Fatalf("%s/%s effect count=%d want=%d", name, disposition, f.calls.Load(), wantEffects)
		}
		if disposition == "allow" || disposition == "approve" {
			if len(response.Error) != 0 || !bytes.Contains(response.Result, []byte("exact input")) {
				t.Fatalf("authorized call failed: %s", w.Body.String())
			}
		} else if len(response.Error) == 0 {
			t.Fatal("refused tool succeeded")
		}
		var pending struct {
			Items []json.RawMessage `json:"items"`
		}
		if code := f.h.reqInto("GET", "/v1/m/governance/approvals?status=pending", f.h.adminToken, f.h.tenantA, nil, &pending); code != http.StatusOK || len(pending.Items) != 0 {
			t.Fatal("completed call left a pending approval")
		}
	}
	call("read_echo", "approve", 1)
	call("readonly_other_hint", "ask", 1)
	call("write_echo", "ask", 1)
	call("unannotated", "ask", 1)
	in["allowed_tools"] = []auth.MCPGatewayToolPolicy{{Name: "read_echo", RequiredScope: "tools:call", Destructive: true}, {Name: "write_echo", RequiredScope: "tools:call", Destructive: false}}
	snap = do("PUT", path, map[string]any{"version": snap.Version, "server": in}, http.StatusOK)
	snap = do("POST", path+"/test", map[string]any{"version": snap.Version}, http.StatusOK)
	snap = do("PUT", path, map[string]any{"version": snap.Version, "server": in}, http.StatusOK)
	assertProposal(snap, "read_echo", "readonly_other_hint")
	call("write_echo", "allow", 2)
	call("read_echo", "ask", 2)
	call("unannotated", "deny", 2)
	in["allowed_tools"] = []auth.MCPGatewayToolPolicy{}
	snap = do("PUT", path, map[string]any{"version": snap.Version, "server": in}, http.StatusOK)
	if len(snap.Servers[0].AllowedTools) != 0 {
		t.Fatal("explicit empty policy widened")
	}
	assertProposal(snap, "read_echo", "readonly_other_hint")
	call("write_echo", "deny", 2)
	call("read_echo", "deny", 2)
}
