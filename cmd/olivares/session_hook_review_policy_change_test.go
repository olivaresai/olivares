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
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

func TestSessionClaudeApprovalKeepsEffectiveInputAcrossPolicyChange(t *testing.T) {
	for _, remove := range []bool{false, true} {
		name := "policy remains"
		if remove {
			name = "policy removed during review"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			human, err := h.authr.Authenticate(t.Context(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, human, tenant, "effective-policy-change")
			intent.PermissionMode, intent.TemplateBuiltin = "dontAsk", false
			intent.FolderPath = t.TempDir()
			intent.AllowedTools = []string{"Bash(printf *)"}
			token, err := credentials.mintForPrincipal(human, tenant, intent)
			if err != nil {
				t.Fatal(err)
			}
			service := h.set.gov.EngineApprovals()
			h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
			h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
			var policy struct {
				ID string `json:"id"`
			}
			if code := h.reqInto(http.MethodPost, "/v1/m/governance/policies", h.adminToken, h.tenantA, map[string]any{"name": "review rewrite", "kind": "approval", "enabled": true, "spec": map[string]any{"required_approvals": 1, "match": map[string]any{"action": hookActionCapability, "subject_kind": "claude.tool"}}}, &policy); code != http.StatusCreated || policy.ID == "" {
				t.Fatalf("policy=%d", code)
			}
			const original, rewritten = "printf original", "printf governed"
			d := &claudeHookDecider{defaultPolicy: &hookPolicyDoc{Default: "allow", Rules: []hookPolicyRule{{Tool: "Bash", Decision: "allow", Rewrite: map[string]any{"command": rewritten}}}}, authr: credentials, eval: h.set.gov.Evaluator(), scoped: h.set.gov.ScopedGrants(), approvals: service, store: h.st, clock: time.Now, log: discardLog()}
			raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": original}})
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			req := httptest.NewRequest(http.MethodPost, "/hooks", bytes.NewReader(raw)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); claude.NewHookPEP(d, nil, time.Now).ServeHTTP(rec, req) }()
			defer func() { cancel(); <-done }()
			var pending governance.Approval
			for pending.ID == "" {
				items, _, err := service.List(ctx, tenant, hookActionCapability, "pending", "")
				if err != nil {
					t.Fatal(err)
				}
				if len(items) == 1 {
					pending = items[0]
					break
				}
				select {
				case <-done:
					t.Fatal("request did not reach review")
				case <-ctx.Done():
					t.Fatal("review did not arrive")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if remove {
				if code, _ := h.req(http.MethodDelete, "/v1/m/governance/policies/"+policy.ID, h.adminToken, h.tenantA, nil); code != http.StatusNoContent {
					t.Fatalf("policy delete=%d", code)
				}
			}
			if _, err := service.Decide(ctx, tenant, human, pending.ID, governance.ApprovalDecisionRequest{Decision: "approve"}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("review did not finish")
			}
			var reply struct {
				HookSpecificOutput struct {
					Permission string         `json:"permissionDecision"`
					Input      map[string]any `json:"updatedInput"`
				} `json:"hookSpecificOutput"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			if reply.HookSpecificOutput.Permission != "allow" {
				t.Fatalf("decision=%s", reply.HookSpecificOutput.Permission)
			}
			effective := original
			if command, ok := reply.HookSpecificOutput.Input["command"].(string); ok {
				effective = command
			}
			reviewed := strings.TrimPrefix(pending.Reason, "Claude Code requests Bash\ncommand: ")
			if reviewed != rewritten {
				t.Error("stored review policy discarded the tenant rewrite before review")
			}
			if reviewed != effective {
				t.Fatalf("reviewed %q but permitted %q after the policy change", reviewed, effective)
			}
		})
	}
}
