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
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
)

func TestSessionClaudeApprovalPublishesStructuredReview(t *testing.T) {
	f := newHookSecretTestRun(t, hookSecretCanary, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	command := "PASSWORD=" + hookSecretCanary + " printf visible"
	payload, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": command}})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(payload)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+f.token)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); f.server.Handler.ServeHTTP(rec, req) }()
	defer func() { cancel(); <-done }()
	service := f.h.set.gov.EngineApprovals()
	var pending governance.Approval
	for pending.ID == "" {
		items, _, err := service.List(ctx, f.tenant, hookpep.ActionCapability, "pending", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(items) > 0 {
			pending = items[0]
			break
		}
		select {
		case <-done:
			t.Fatal("reviewable command never reached the queue")
		case <-ctx.Done():
			t.Fatal("approval did not arrive")
		case <-time.After(10 * time.Millisecond):
		}
	}
	type review struct {
		Tool string `json:"tool"`
		Text string `json:"text"`
	}
	type view struct {
		ID     string  `json:"id"`
		Review *review `json:"review"`
	}
	want := "PASSWORD=[secret env/test] printf visible"
	var detail view
	if code := f.h.reqInto(http.MethodGet, "/v1/m/governance/approvals/"+pending.ID, f.h.adminToken, f.h.tenantA, nil, &detail); code != http.StatusOK {
		t.Fatalf("approval detail=%d", code)
	}
	if detail.Review == nil || detail.Review.Tool != "Bash" || detail.Review.Text != want {
		t.Fatalf("approval detail has no exact structured review: %+v", detail.Review)
	}
	var list struct {
		Items []view `json:"items"`
	}
	if code := f.h.reqInto(http.MethodGet, "/v1/m/governance/approvals?status=pending", f.h.adminToken, f.h.tenantA, nil, &list); code != http.StatusOK || len(list.Items) != 1 || list.Items[0].Review == nil || *list.Items[0].Review != *detail.Review {
		t.Fatal("approval list does not carry the same structured review")
	}
	if pending.Reason != "Claude Code requests Bash\ncommand: "+want {
		t.Fatal("structured review changed the existing proven reason")
	}
	human, err := f.h.authr.Authenticate(ctx, f.h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Decide(ctx, f.tenant, human, pending.ID, governance.ApprovalDecisionRequest{Decision: "approve"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("approved hook did not finish")
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"permissionDecision":"allow"`)) {
		t.Fatal("reviewed command did not receive allow")
	}
}

func TestSessionClaudeStructuredReviewMatchesEffectiveCommandAndPaths(t *testing.T) {
	for _, tool := range []string{"Bash", "Edit", "Write"} {
		t.Run(tool, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			human, err := h.authr.Authenticate(t.Context(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, human, tenant, "structured-effective-"+tool)
			intent.PermissionMode, intent.TemplateBuiltin = "dontAsk", false
			intent.FolderPath = t.TempDir()
			intent.AllowedTools = []string{"Bash(printf *)", "Bash(echo *)", "Edit(./src/**)", "Write(./src/**)"}
			field, original, effective := "command", "printf original", "echo reviewed"
			if tool != "Bash" {
				field = "file_path"
				original, effective = filepath.Join(intent.FolderPath, "src", "original"), filepath.Join(intent.FolderPath, "src", "effective")
			}
			token, err := credentials.mintForPrincipal(human, tenant, intent)
			if err != nil {
				t.Fatal(err)
			}
			service := h.set.gov.EngineApprovals()
			h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
			h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
			d := newClaudeHookDecider(&hookpep.Decider{DefaultPolicy: &hookpep.PolicyDoc{Default: "allow", Rules: []hookpep.PolicyRule{{Tool: tool, Decision: "ask", Rewrite: map[string]any{field: effective}}}}, Authr: credentials, Eval: h.set.gov.Evaluator(), Authz: harnessAuthz(h), Scoped: h.set.gov.ScopedGrants(), Approvals: service, Store: h.st, Clock: time.Now, Log: discardLog()})
			input := map[string]any{field: original}
			const privateContent = "private-file-content-must-never-be-reviewed"
			if tool != "Bash" {
				input["content"], input["old_string"], input["new_string"] = privateContent, privateContent, privateContent
			}
			raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": tool, "tool_input": input})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			req := httptest.NewRequest(http.MethodPost, "/hooks", bytes.NewReader(raw)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); claude.NewHookPEP(d, nil, time.Now).ServeHTTP(rec, req) }()
			defer func() { cancel(); <-done }()
			var pending governance.Approval
			for pending.ID == "" {
				items, _, err := service.List(ctx, tenant, hookpep.ActionCapability, "pending", "")
				if err != nil {
					t.Fatal(err)
				}
				if len(items) > 0 {
					pending = items[0]
					break
				}
				select {
				case <-done:
					t.Fatal("effective action did not reach review")
				case <-ctx.Done():
					t.Fatal("approval did not arrive")
				case <-time.After(10 * time.Millisecond):
				}
			}
			want := governance.ApprovalReview{Tool: tool, Text: effective}
			if pending.Review == nil || *pending.Review != want {
				t.Fatal("structured review differs from effective action")
			}
			var detail struct {
				Review *governance.ApprovalReview `json:"review"`
			}
			if code := h.reqInto(http.MethodGet, "/v1/m/governance/approvals/"+pending.ID, h.adminToken, h.tenantA, nil, &detail); code != http.StatusOK || detail.Review == nil || *detail.Review != want {
				t.Fatal("persisted review differs from effective action")
			}
			if strings.Contains(pending.Review.Text, privateContent) || strings.Contains(pending.Reason, privateContent) {
				t.Fatal("file contents escaped into review")
			}
			if _, err := service.Decide(ctx, tenant, human, pending.ID, governance.ApprovalDecisionRequest{Decision: "approve"}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("approved action did not finish")
			}
			var reply struct {
				HookSpecificOutput struct {
					Permission string         `json:"permissionDecision"`
					Input      map[string]any `json:"updatedInput"`
				} `json:"hookSpecificOutput"`
			}
			if json.Unmarshal(rec.Body.Bytes(), &reply) != nil || reply.HookSpecificOutput.Permission != "allow" || reply.HookSpecificOutput.Input[field] != effective {
				t.Fatal("allowed action differs from structured review")
			}
		})
	}
}
