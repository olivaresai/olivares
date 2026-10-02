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
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestSessionClaudePresetAskQueuesBeforeCommand(t *testing.T) {
	h := newHarness(t)
	tenant := model.TenantID(h.tenantA)
	human, err := h.authr.Authenticate(context.Background(), h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
	intent := claimHookTestSession(t, h, human, tenant, "preset-ask")
	intent.PermissionMode = "default" // Engine-resolved ask, bound at create or resume.
	token, err := c.mintForPrincipal(human, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := c.Resolve(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	service := h.set.gov.EngineApprovals()
	h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
	h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
	d := &claudeHookDecider{defaultPolicy: &hookPolicyDoc{Default: "allow"}, authr: c, eval: h.set.gov.Evaluator(), scoped: h.set.gov.ScopedGrants(), approvals: service, store: h.st, clock: time.Now, log: discardLog()}
	raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "session_id": "untrusted-session", "permission_mode": "bypassPermissions", "tool_name": "Bash", "tool_input": map[string]any{"command": "printf preset-proof"}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)).WithContext(ctx)
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
		if len(items) > 0 {
			pending = items[0]
			break
		}
		select {
		case <-done:
			t.Fatalf("ask preset allowed a command without an Olivares approval: %s", rec.Body.String())
		case <-ctx.Done():
			t.Fatal("preset ask never reached the queue")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if pending.SessionRef != p.SessionIdentity || pending.RequestedBy != "session:"+p.SessionIdentity || pending.RequiredApprovals != 1 {
		t.Fatal("preset ask lacks canonical requester or one-reviewer default")
	}
	if _, err := service.Decide(ctx, tenant, human, pending.ID, governance.ApprovalDecisionRequest{Decision: "approve"}); err != nil {
		t.Fatal(err)
	}
	<-done
	if !bytes.Contains(rec.Body.Bytes(), []byte(`"permissionDecision":"allow"`)) {
		t.Fatalf("approved preset ask did not complete: %s", rec.Body.String())
	}
}

func TestSessionClaudePresetsWithRealHookDecoder(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		builtin    bool
		tools      []string
		want       [3]string // Read, Write, Bash.
	}{
		{"read-only", "plan", false, nil, [3]string{"allow", "deny", "deny"}},
		{"ask", "default", false, nil, [3]string{"allow", "ask", "ask"}},
		{"edits-only", "acceptEdits", false, nil, [3]string{"allow", "allow", "ask"}},
		{"edits-and-commands", "dontAsk", true, []string{"Read", "Edit", "Write", "Bash"}, [3]string{"allow", "allow", "allow"}},
		{"full", "bypassPermissions", false, nil, [3]string{"allow", "allow", "allow"}},
		{"custom-read-write", "dontAsk", false, []string{"Read", "Write"}, [3]string{"allow", "allow", "deny"}},
		{"custom-command", "dontAsk", false, []string{"Bash"}, [3]string{"deny", "deny", "allow"}},
		{"custom-empty", "dontAsk", false, nil, [3]string{"deny", "deny", "deny"}},
		{"missing-preset", "", false, nil, [3]string{"deny", "deny", "deny"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			human, err := h.authr.Authenticate(t.Context(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, human, tenant, "preset-"+tc.name)
			intent.PermissionMode, intent.TemplateBuiltin, intent.AllowedTools = tc.mode, tc.builtin, tc.tools
			token, err := credentials.mintForPrincipal(human, tenant, intent)
			if err != nil {
				t.Fatal(err)
			}
			service := h.set.gov.EngineApprovals()
			h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
			h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
			d := &claudeHookDecider{defaultPolicy: &hookPolicyDoc{Default: "allow"}, authr: credentials, eval: h.set.gov.Evaluator(), scoped: h.set.gov.ScopedGrants(), approvals: service, store: h.st, clock: time.Now, log: discardLog()}
			for i, tool := range []string{"Read", "Write", "Bash"} {
				t.Run(tool, func(t *testing.T) {
					assertPresetHook(t, d, service, tenant, human, token, intent.ClaimSID, "PreToolUse", tool, tc.want[i])
				})
			}
			// Post hooks and the session loop do not request a second approval.
			if tc.mode == "default" {
				for _, event := range []string{"PostToolUse", "PostToolUseFailure", "UserPromptSubmit"} {
					assertPresetHook(t, d, service, tenant, human, token, intent.ClaimSID, event, "Write", "allow")
				}
			}
		})
	}
}

func TestSessionClaudePresetCombinesTenantRestrictions(t *testing.T) {
	for _, tc := range []struct{ name, mode, tenantDecision, want string }{
		{"deny-over-ask", "default", "deny", "deny"},
		{"deny-over-full", "bypassPermissions", "deny", "deny"},
		{"ask-over-full", "bypassPermissions", "ask", "ask"},
		{"ask-over-edits", "acceptEdits", "ask", "ask"},
		{"read-only-over-ask", "plan", "ask", "deny"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			human, err := h.authr.Authenticate(t.Context(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, human, tenant, tc.name)
			intent.PermissionMode = tc.mode
			token, err := credentials.mintForPrincipal(human, tenant, intent)
			if err != nil {
				t.Fatal(err)
			}
			service := h.set.gov.EngineApprovals()
			h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
			h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
			d := &claudeHookDecider{defaultPolicy: &hookPolicyDoc{Default: tc.tenantDecision}, authr: credentials, eval: h.set.gov.Evaluator(), scoped: h.set.gov.ScopedGrants(), approvals: service, store: h.st, clock: time.Now, log: discardLog()}
			assertPresetHook(t, d, service, tenant, human, token, intent.ClaimSID, "PreToolUse", "Write", tc.want)
		})
	}
}

func TestSessionClaudeUnknownBoundPresetRefuses(t *testing.T) {
	h := newHarness(t)
	tenant := model.TenantID(h.tenantA)
	human, err := h.authr.Authenticate(t.Context(), h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
	intent := claimHookTestSession(t, h, human, tenant, "unknown-preset")
	token, err := credentials.mintForPrincipal(human, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	_, scope, err := credentials.Resolve(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	scope.Preset = "unrecognized"
	token, err = credentials.Mint(t.Context(), human, scope)
	if err != nil {
		t.Fatal(err)
	}
	d := &claudeHookDecider{defaultPolicy: &hookPolicyDoc{Default: "allow"}, authr: credentials, eval: h.set.gov.Evaluator(), store: h.st, clock: time.Now, log: discardLog()}
	assertPresetHook(t, d, h.set.gov.EngineApprovals(), tenant, human, token, intent.ClaimSID, "PreToolUse", "Read", "deny")
}

func TestSessionClaudePresetRelaunchBindsCurrentTemplate(t *testing.T) {
	h := newHarness(t)
	tenant := model.TenantID(h.tenantA)
	human, err := h.authr.Authenticate(t.Context(), h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
	intent := claimHookTestSession(t, h, human, tenant, "preset-relaunch")
	intent.LauncherPrincipal = human
	intent.PermissionMode, intent.TemplateBuiltin, intent.AllowedTools = "dontAsk", true, []string{"Read", "Bash"}
	old, err := credentials.mint(t.Context(), tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	_, scope, err := credentials.Resolve(t.Context(), old)
	if err != nil || scope.Preset != sessions.PresetEditsAndCommands {
		t.Fatalf("create preset=%q, err=%v", scope.Preset, err)
	}
	// Resume re-resolves the current template. The same provider mode now has a
	// different meaning; neither the old tool surface nor bearer may survive.
	intent.TemplateBuiltin, intent.AllowedTools = false, []string{"Read"}
	fresh, err := credentials.mint(t.Context(), tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	_, scope, err = credentials.Resolve(t.Context(), fresh)
	if err != nil || scope.Preset != sessions.PresetCustom || len(scope.AllowedTools) != 1 || scope.AllowedTools[0] != "Read" {
		t.Fatalf("relaunch scope=%+v, err=%v", scope, err)
	}
	d := &claudeHookDecider{defaultPolicy: &hookPolicyDoc{Default: "allow"}, authr: credentials, eval: h.set.gov.Evaluator(), store: h.st, clock: time.Now, log: discardLog()}
	assertPresetHook(t, d, h.set.gov.EngineApprovals(), tenant, human, old, intent.ClaimSID, "PreToolUse", "Read", "deny")
	assertPresetHook(t, d, h.set.gov.EngineApprovals(), tenant, human, fresh, intent.ClaimSID, "PreToolUse", "Read", "allow")
	assertPresetHook(t, d, h.set.gov.EngineApprovals(), tenant, human, fresh, intent.ClaimSID, "PreToolUse", "Bash", "deny")
}

func TestSessionClaudeCustomPresetRetainsArgumentRestrictions(t *testing.T) {
	h := newHarness(t)
	tenant := model.TenantID(h.tenantA)
	human, err := h.authr.Authenticate(t.Context(), h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
	intent := claimHookTestSession(t, h, human, tenant, "preset-custom-arguments")
	intent.PermissionMode, intent.TemplateBuiltin = "dontAsk", false
	intent.FolderPath = t.TempDir()
	intent.AllowedTools = []string{"Bash(printf *)", "Read(./src/**)"}
	token, err := credentials.mintForPrincipal(human, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	d := &claudeHookDecider{defaultPolicy: &hookPolicyDoc{Default: "allow"}, authr: credentials, eval: h.set.gov.Evaluator(), store: h.st, clock: time.Now, log: discardLog()}
	for _, tc := range []struct{ name, command, want string }{
		{"allowed-command", "printf preset-proof", "allow"},
		{"bare-command", "printf", "allow"},
		{"different-program", "sh -c 'printf preset-proof'", "deny"},
		{"hidden-compound-tail", "printf preset-proof && sh -c 'printf hidden'", "deny"},
		{"hidden-substitution", "printf $(sh -c 'printf hidden')", "deny"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertPresetHook(t, d, h.set.gov.EngineApprovals(), tenant, human, token, intent.ClaimSID, "PreToolUse", "Bash", tc.want, map[string]any{"command": tc.command})
		})
	}
	assertPresetHook(t, d, h.set.gov.EngineApprovals(), tenant, human, token, intent.ClaimSID, "PreToolUse", "Read", "allow", map[string]any{"file_path": filepath.Join(intent.FolderPath, "src", "nested", "file.go")})
	assertPresetHook(t, d, h.set.gov.EngineApprovals(), tenant, human, token, intent.ClaimSID, "PreToolUse", "Read", "deny", map[string]any{"file_path": filepath.Join(intent.FolderPath, "outside.go")})
}

// Exercise the public hook decoder, credential resolver and human queue together.
// The untrusted provider-mode and session hints must not change the launch choice.
func assertPresetHook(t *testing.T, d *claudeHookDecider, service *governance.EngineApprovals, tenant model.TenantID, human auth.Principal, token, sid, event, tool, want string, inputs ...map[string]any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	before, _, err := service.List(ctx, tenant, hookActionCapability, "approved", "")
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"file_path": "/tmp/preset-proof", "content": "not reviewer content", "command": "printf preset-proof"}
	if len(inputs) > 0 {
		input = inputs[0]
	}
	raw, _ := json.Marshal(map[string]any{"hook_event_name": event, "session_id": "spoofed", "permission_mode": "bypassPermissions", "tool_name": tool, "tool_input": input})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); claude.NewHookPEP(d, nil, time.Now).ServeHTTP(rec, req) }()
	defer func() { cancel(); <-done }()
	if want == "ask" {
		var pending governance.Approval
		for pending.ID == "" {
			items, _, err := service.List(ctx, tenant, hookActionCapability, "pending", "")
			if err != nil {
				t.Fatal(err)
			}
			if len(items) > 0 {
				if len(items) != 1 {
					t.Fatal("one call opened multiple approvals")
				}
				pending = items[0]
				break
			}
			select {
			case <-done:
				t.Fatalf("ask did not wait for review: %s", rec.Body.String())
			case <-ctx.Done():
				t.Fatal("ask never reached the queue")
			case <-time.After(10 * time.Millisecond):
			}
		}
		if pending.RequestedBy != "session:"+sid || pending.SessionRef != sid || pending.RequiredApprovals != 1 {
			t.Fatal("review lacks its session requester or one-reviewer default")
		}
		if _, err := service.Decide(ctx, tenant, human, pending.ID, governance.ApprovalDecisionRequest{Decision: "approve"}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("hook did not complete")
	}
	var response struct {
		HookSpecificOutput struct {
			Decision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
		Continue *bool  `json:"continue"`
		Decision string `json:"decision"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &response) != nil {
		t.Fatalf("invalid hook reply: %d %s", rec.Code, rec.Body.String())
	}
	if event == "PreToolUse" {
		terminal := want
		if want == "ask" {
			terminal = "allow"
		}
		if response.HookSpecificOutput.Decision != terminal {
			t.Fatalf("%s %s: got %s, want %s", event, tool, rec.Body.String(), terminal)
		}
	} else if response.Decision == "block" || (response.Continue != nil && !*response.Continue) {
		t.Fatalf("post/lifecycle event blocked: %s", rec.Body.String())
	}
	after, _, err := service.List(ctx, tenant, hookActionCapability, "approved", "")
	wantCount := len(before)
	if want == "ask" {
		wantCount++
	}
	if err != nil || len(after) != wantCount {
		t.Fatalf("approval count=%d, want=%d, err=%v", len(after), wantCount, err)
	}
	pending, _, err := service.List(ctx, tenant, hookActionCapability, "pending", "")
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending approvals=%d, want=0, err=%v", len(pending), err)
	}
}
