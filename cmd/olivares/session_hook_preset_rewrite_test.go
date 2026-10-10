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
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
)

// A tenant rewrite must retain the launching person's bound custom tool surface.
// Refusal or an allowed original input is safe; a broad rewrite cannot disable
// useful rewrites that remain inside the selected surface.
func TestSessionClaudeCustomPresetChecksEffectiveRewrite(t *testing.T) {
	h := newHarness(t)
	tenant := model.TenantID(h.tenantA)
	human, err := h.authr.Authenticate(t.Context(), h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
	intent := claimHookTestSession(t, h, human, tenant, "custom-rewrite")
	intent.PermissionMode, intent.TemplateBuiltin = "dontAsk", false
	intent.FolderPath = t.TempDir()
	intent.AllowedTools = []string{"Bash(printf *)", "Bash(echo *)", "Read(./src/**)", "Read(./tests/**)"}
	token, err := credentials.mintForPrincipal(human, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	for _, surface := range []struct{ tool, field, original, inside, other, outside string }{
		{"Bash", "command", "printf original", "printf rewritten", "echo rewritten", "touch forbidden"},
		{"Read", "file_path", filepath.Join(intent.FolderPath, "src", "original"), filepath.Join(intent.FolderPath, "src", "rewritten"), filepath.Join(intent.FolderPath, "tests", "rewritten"), filepath.Join(intent.FolderPath, "private", "outside")},
	} {
		t.Run(surface.tool, func(t *testing.T) {
			for _, tc := range []struct{ name, input, rewrite, want string }{
				{"original positive", surface.original, "", "allow"},
				{"outside original refuses", surface.outside, "", "deny"},
				{"inside rewrite remains useful", surface.original, surface.inside, "allow"},
				{"rewrite into another permitted rule", surface.original, surface.other, "allow"},
				{"outside rewrite cannot widen", surface.original, surface.outside, "ceiling"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var rewrite map[string]any
					if tc.rewrite != "" {
						rewrite = map[string]any{surface.field: tc.rewrite}
					}
					d := newClaudeHookDecider(&hookpep.Decider{
						DefaultPolicy: &hookpep.PolicyDoc{Default: "allow", Rules: []hookpep.PolicyRule{{Tool: surface.tool, Decision: "allow", Rewrite: rewrite}}},
						Authr:         credentials, Eval: h.set.gov.Evaluator(), Authz: harnessAuthz(h), Scoped: h.set.gov.ScopedGrants(), Store: h.st, Clock: time.Now, Log: discardLog(),
					})
					raw, err := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "session_id": "spoofed", "permission_mode": "bypassPermissions", "tool_name": surface.tool, "tool_input": map[string]any{surface.field: tc.input}})
					if err != nil {
						t.Fatal(err)
					}
					req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)).WithContext(t.Context())
					req.Header.Set("Authorization", "Bearer "+token)
					rec := httptest.NewRecorder()
					claude.NewHookPEP(d, nil, time.Now).ServeHTTP(rec, req)
					var reply struct {
						HookSpecificOutput struct {
							Decision     string         `json:"permissionDecision"`
							UpdatedInput map[string]any `json:"updatedInput"`
						} `json:"hookSpecificOutput"`
					}
					if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &reply) != nil {
						t.Fatalf("invalid hook reply: %d %s", rec.Code, rec.Body.String())
					}
					out := reply.HookSpecificOutput
					if tc.want != "ceiling" && out.Decision != tc.want {
						t.Fatalf("decision %s, want %s: %s", out.Decision, tc.want, rec.Body.String())
					}
					if out.Decision == "allow" {
						effective := tc.input
						if value, ok := out.UpdatedInput[surface.field].(string); ok {
							effective = value
						}
						if effective != surface.original && effective != surface.inside && effective != surface.other {
							t.Fatalf("bound custom preset allowed out-of-scope effective %s %q: %s", surface.field, effective, rec.Body.String())
						}
						if (tc.rewrite == surface.inside || tc.rewrite == surface.other) && effective != tc.rewrite {
							t.Fatalf("in-scope rewrite was dropped: %s", rec.Body.String())
						}
					} else if tc.want == "ceiling" && out.Decision != "deny" {
						t.Fatalf("unsafe rewrite has no terminal refusal: %s", rec.Body.String())
					}
				})
			}
		})
	}
}

func TestSessionClaudeCustomRewriteRefusesBeforeReview(t *testing.T) {
	h := newHarness(t)
	tenant := model.TenantID(h.tenantA)
	human, err := h.authr.Authenticate(t.Context(), h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
	intent := claimHookTestSession(t, h, human, tenant, "review-rewrite")
	intent.PermissionMode, intent.TemplateBuiltin = "dontAsk", false
	intent.AllowedTools = []string{"Bash(printf *)"}
	token, err := credentials.mintForPrincipal(human, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	service := h.set.gov.EngineApprovals()
	h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
	h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
	d := newClaudeHookDecider(&hookpep.Decider{DefaultPolicy: &hookpep.PolicyDoc{Default: "allow", Rules: []hookpep.PolicyRule{{Tool: "Bash", Decision: "ask", Rewrite: map[string]any{"command": "touch forbidden"}}}}, Authr: credentials, Eval: h.set.gov.Evaluator(), Authz: harnessAuthz(h), Scoped: h.set.gov.ScopedGrants(), Approvals: service, Store: h.st, Clock: time.Now, Log: discardLog()})
	raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash", "tool_input": map[string]any{"command": "printf original", "description": "preserve this field"}})
	ctx, cancel := context.WithTimeout(t.Context(), 350*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	claude.NewHookPEP(d, nil, time.Now).ServeHTTP(rec, req)
	var out struct {
		HookSpecificOutput struct {
			Permission string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.HookSpecificOutput.Permission != "deny" {
		t.Fatalf("unsafe rewrite did not refuse: %d %s", rec.Code, rec.Body.String())
	}
	pending, _, err := service.List(t.Context(), tenant, hookpep.ActionCapability, "pending", "")
	if err != nil || len(pending) != 0 {
		t.Fatalf("unsafe rewrite entered the human queue: %d %v", len(pending), err)
	}
}
