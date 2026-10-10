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
	"os"
	"os/exec"
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

func TestSessionClaudeReviewDescribesEffectiveRewrite(t *testing.T) {
	for _, scenario := range []string{"command between permitted rules", "write path without contents", "oversized effective command", "unreviewable tool rewrite"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			human, err := h.authr.Authenticate(t.Context(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, human, tenant, "effective-review-"+scenario)
			intent.PermissionMode, intent.TemplateBuiltin = "dontAsk", false
			intent.FolderPath = t.TempDir()
			intent.AllowedTools = []string{"Bash(printf *)", "Bash(echo *)", "Write(./src/**)", "WebFetch"}
			tool, field, original, effective := "Bash", "command", "printf original-command", "echo reviewed-command"
			denyReason := ""
			if scenario == "write path without contents" {
				tool, field = "Write", "file_path"
				original, effective = filepath.Join(intent.FolderPath, "src", "original"), filepath.Join(intent.FolderPath, "src", "effective")
			} else if scenario == "oversized effective command" {
				effective = "echo " + strings.Repeat("x", 16384)
				denyReason = "command too long to review (16389 bytes)"
			} else if scenario == "unreviewable tool rewrite" {
				tool, field = "WebFetch", "url"
				original, effective = "https://example.invalid/original", "https://example.invalid/effective"
				denyReason = "rewritten tool input is unavailable for human review"
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
			if tool == "Write" {
				input["content"] = "private-file-content-not-for-review"
			}
			raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": tool, "tool_input": input})
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
				items, _, err := service.List(t.Context(), tenant, hookpep.ActionCapability, "pending", "")
				if err != nil {
					t.Fatal(err)
				}
				if len(items) > 0 {
					pending = items[0]
					break
				}
				select {
				case <-done:
					if denyReason == "" || !strings.Contains(rec.Body.String(), `"permissionDecision":"deny"`) || !strings.Contains(rec.Body.String(), denyReason) {
						t.Fatal("effective command facts were not reviewed or explicitly refused")
					}
					return
				case <-ctx.Done():
					t.Fatal("effective input did not reach its review decision")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if denyReason != "" {
				t.Fatal("unreviewable rewritten input was hidden behind review of the original")
			}
			label := "command: "
			if tool == "Write" {
				label = "path: "
			}
			if pending.Reason != "Claude Code requests "+tool+"\n"+label+effective {
				t.Fatal("reviewer sees different facts from the rewritten action")
			}
			if strings.Contains(pending.Reason, "private-file-content-not-for-review") {
				t.Fatal("review retained file contents")
			}
			if _, err := service.Decide(ctx, tenant, human, pending.ID, governance.ApprovalDecisionRequest{Decision: "approve"}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("reviewed action did not complete")
			}
			var reply struct {
				HookSpecificOutput struct {
					Permission string         `json:"permissionDecision"`
					Input      map[string]any `json:"updatedInput"`
				} `json:"hookSpecificOutput"`
			}
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &reply) != nil || reply.HookSpecificOutput.Permission != "allow" || reply.HookSpecificOutput.Input[field] != effective {
				t.Fatal("approved effective input differs from the review")
			}
			if tool == "Bash" {
				out, err := exec.CommandContext(ctx, "sh", "-c", reply.HookSpecificOutput.Input[field].(string)).Output()
				if err != nil || string(out) != "reviewed-command\n" {
					t.Fatal("execution differs from the exact command that was approved")
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(effective), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(reply.HookSpecificOutput.Input[field].(string), []byte(input["content"].(string)), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(original); !os.IsNotExist(err) {
					t.Fatal("write targeted the original path instead of the approved one")
				}
			}
		})
	}
}
