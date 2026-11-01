// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

// A human decision crosses the real REST queue and provider adapter before the
// Codex codec answers the owned turn. Neither the adapter nor its result is mocked.
func TestProviderApprovalSessionScopeReachesCodexAndLedger(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		scopes     []string
	}{
		{"once", "accept", []string{""}},
		{"session", "acceptForSession", []string{"session"}},
		{"mixed quorum", "accept", []string{"session", ""}},
		{"session quorum", "acceptForSession", []string{"session", "session"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			tokens := []string{h.adminToken}
			if len(tc.scopes) == 2 {
				tokens = append(tokens, h.newUser("scope-reviewer@example.test", "scope-review-fixture-password", h.tenantA, "admin"))
				// Stored raising policies remain enforced on Community upgrades.
				if err := h.st.Mutate(t.Context(), tenant, func(sc store.Scope) error {
					_, err := sc.Policies().Create(t.Context(), model.Policy{Name: "Two reviewers", Kind: "approval", Enabled: true, Spec: map[string]any{"required_approvals": 2, "match": map[string]any{"action": "sessions.provider.approval"}}})
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			bridge := newApprovalBridge(approvalBridgeConfig{}, discardLog())
			bridge.LocalProposer = h.set.gov.EngineApprovals()
			adapter := providerApprovalAdapter{bridge: bridge}
			asked := make(chan struct{}, 1)
			stub := newCodexStub(t, func(ctx context.Context, req sessions.ProviderApprovalRequest) (sessions.ProviderApprovalDecision, error) {
				req.SessionRef = "osn_scope_test"
				req.Principal = auth.Principal{SessionIdentity: req.SessionRef}
				asked <- struct{}{}
				return adapter.Approve(ctx, tenant, req)
			})
			t.Cleanup(func() { stub.session.Close(nil) })
			handshake := make(chan error, 1)
			go func() { _, err := stub.session.Handshake(t.Context()); handshake <- err }()
			id, _ := stub.request("initialize")
			stub.deliver(map[string]any{"id": id, "result": map[string]any{"userAgent": "stub", "codexHome": "/stub", "platformFamily": "unix", "platformOs": "linux"}})
			id, _ = stub.request("account/read")
			stub.deliver(map[string]any{"id": id, "result": map[string]any{"account": map[string]any{"type": "apiKey"}, "requiresOpenaiAuth": false}})
			id, _ = stub.request("thread/start")
			stub.deliver(map[string]any{"id": id, "result": map[string]any{"thread": map[string]any{"id": "thread-1", "parentThreadId": nil, "agentRole": nil}, "model": "gpt-5.6-sol", "modelProvider": "openai"}})
			if err := <-handshake; err != nil {
				t.Fatal(err)
			}
			go func() { _, _ = stub.session.Input(t.Context(), "list files") }()
			id, _ = stub.request("turn/start")
			stub.deliver(map[string]any{"id": id, "result": map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}}})
			until := time.Now().Add(3 * time.Second)
			for stub.session.ActiveTurn() != "turn-1" && time.Now().Before(until) {
				time.Sleep(10 * time.Millisecond)
			}
			stub.deliver(map[string]any{"id": "scope-request", "method": "item/commandExecution/requestApproval", "params": map[string]any{"itemId": "item-1", "startedAtMs": 1, "threadId": "thread-1", "turnId": "turn-1", "command": "ls -la"}})
			select {
			case <-asked:
			case <-time.After(3 * time.Second):
				t.Fatal("the provider request did not reach the adapter")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			var ref string
			for ref == "" && ctx.Err() == nil {
				items, _, err := bridge.LocalProposer.List(ctx, tenant, "sessions.provider.approval", nbPending, "")
				if err != nil {
					t.Fatal(err)
				}
				if len(items) != 0 {
					ref = items[0].ID
				} else {
					time.Sleep(10 * time.Millisecond)
				}
			}
			if ref == "" {
				t.Fatal("the provider request did not enter the queue")
			}
			approval := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+ref)
			if approval["session_scope_allowed"] != true {
				t.Fatalf("supported request does not advertise session scope: %v", approval)
			}
			path := "/v1/m/governance/approvals/" + ref + "/decisions"
			for _, input := range []map[string]any{{"decision": "approve", "scope": "always"}, {"decision": "reject", "scope": "session"}} {
				if code, body := h.req("POST", path, h.adminToken, h.tenantA, input); code != http.StatusBadRequest {
					t.Fatalf("invalid scoped decision = %d %s", code, body)
				}
			}
			if code, body := h.req("POST", path, h.adminToken, h.tenantB, map[string]any{"decision": "approve", "scope": "session"}); code != http.StatusNotFound {
				t.Fatalf("foreign tenant scoped decision = %d %s", code, body)
			}
			wantScopes := map[string]int{}
			for i, scope := range tc.scopes {
				input := map[string]any{"decision": "approve"}
				if scope != "" {
					input["scope"] = scope
				}
				if code, body := h.req("POST", path, tokens[i], h.tenantA, input); code != http.StatusOK {
					t.Fatalf("scoped approval = %d %s", code, body)
				} else if i+1 < len(tc.scopes) {
					var pending map[string]any
					if err := json.Unmarshal(body, &pending); err != nil || pending["status"] != "pending" {
						t.Fatalf("first reviewer crossed a two-person quorum: %s (%v)", body, err)
					}
				}
				wantScopes[scope]++
			}
			if got := stub.replyTo("scope-request"); got["decision"] != tc.want {
				t.Fatalf("Codex answer = %v, want %s", got, tc.want)
			}
			trail := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/approvals/"+ref+"/decisions")
			rows, _ := trail["items"].([]any)
			if len(rows) != len(tc.scopes) {
				t.Fatalf("decision trail = %v", trail)
			}
			trailScopes := map[string]int{}
			for _, raw := range rows {
				row := raw.(map[string]any)
				scope, _ := row["scope"].(string)
				trailScopes[scope]++
			}
			if !maps.Equal(trailScopes, wantScopes) {
				t.Fatalf("decision scopes = %v, want %v", trailScopes, wantScopes)
			}
			ledgerScopes := map[string]int{}
			if err := h.st.View(t.Context(), tenant, func(sc store.Scope) error {
				return sc.Audit().(store.CanonicalWalker).WalkCanonical(t.Context(), 1, func(e model.AuditEvent, raw string, _ []byte) error {
					if e.Action != "governance.approval.decision" || e.TargetID.String() != ref {
						return nil
					}
					var meta map[string]any
					if err := json.Unmarshal([]byte(raw), &meta); err != nil {
						return err
					}
					scope, _ := meta["scope"].(string)
					ledgerScopes[scope]++
					return nil
				})
			}); err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(ledgerScopes, wantScopes) {
				t.Fatalf("ledger scopes = %v, want %v", ledgerScopes, wantScopes)
			}
		})
	}
}
