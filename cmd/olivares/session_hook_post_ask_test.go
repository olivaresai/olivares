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

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func TestSessionClaudeTenantAskDoesNotReviewExecutedToolsOrLifecycle(t *testing.T) {
	h := newHarness(t)
	tenant := model.TenantID(h.tenantA)
	human, err := h.authr.Authenticate(t.Context(), h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
	intent := claimHookTestSession(t, h, human, tenant, "tenant-post-ask")
	token, err := credentials.mintForPrincipal(human, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	service := h.set.gov.EngineApprovals()
	h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
	h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
	d := &claudeHookDecider{defaultPolicy: &hookPolicyDoc{Default: "allow", Rules: []hookPolicyRule{
		{Tool: "Bash", Decision: "ask", Reason: "review commands before execution"},
		{Event: "UserPromptSubmit", Decision: "ask"},
		{Event: "TaskCompleted", Decision: "ask"},
		{Event: "SessionStart", Decision: "ask"},
	}}, authr: credentials, eval: h.set.gov.Evaluator(), scoped: h.set.gov.ScopedGrants(), approvals: service, store: h.st, clock: time.Now, log: discardLog()}
	// This is the tenant's hook rule, not an authored governance ReviewPolicy.
	assertPresetHook(t, d, service, tenant, human, token, intent.ClaimSID, "PreToolUse", "Bash", "ask")
	for _, event := range []string{"PostToolUse", "UserPromptSubmit", "TaskCompleted", "SessionStart"} {
		t.Run(event, func(t *testing.T) {
			assertPresetHook(t, d, service, tenant, human, token, intent.ClaimSID, event, "Bash", "allow")
		})
	}
	approved, _, err := service.List(t.Context(), tenant, hookActionCapability, "approved", "")
	if err != nil || len(approved) != 1 {
		t.Fatalf("approved calls=%d, want=1, err=%v", len(approved), err)
	}
}

func TestSessionClaudePostAskRetainsOutputBlockAndAudit(t *testing.T) {
	h := newHarness(t)
	tenant := model.TenantID(h.tenantA)
	human, err := h.authr.Authenticate(t.Context(), h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
	intent := claimHookTestSession(t, h, human, tenant, "tenant-post-block")
	token, err := credentials.mintForPrincipal(human, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	service := h.set.gov.EngineApprovals()
	h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
	h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
	d := &claudeHookDecider{defaultPolicy: &hookPolicyDoc{Default: "allow", Rules: []hookPolicyRule{{Event: "PostToolUse", Tool: "Bash", Decision: "ask", Block: true, Reason: "output inspection blocked further processing"}}}, authr: credentials, eval: h.set.gov.Evaluator(), approvals: service, store: h.st, clock: time.Now, log: discardLog()}
	raw, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "printf output-proof"}})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	claude.NewHookPEP(d, nil, time.Now).ServeHTTP(rec, req)
	var reply struct {
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &reply) != nil || reply.Decision != "block" {
		t.Fatalf("output block was lost: %d %s", rec.Code, rec.Body.String())
	}
	items, _, err := service.List(t.Context(), tenant, hookActionCapability, "pending", "")
	if err != nil || len(items) != 0 {
		t.Fatalf("post-output approval requests=%d, want=0, err=%v", len(items), err)
	}
	found := 0
	for _, event := range canonicalLedgerEventsFrom(t, h.st, tenant, 0) {
		if event.meta["event"] == "PostToolUse" && event.event.Action == "hook.tool.allow" {
			if event.meta["session_ref"] != intent.ClaimSID || event.meta["run_ref"] != intent.RunRef || event.event.Actor != "session:"+intent.ClaimSID {
				t.Fatal("output decision lost its canonical session audit")
			}
			found++
		}
	}
	if found != 1 {
		t.Fatalf("anchored output decisions=%d, want=1", found)
	}
}
