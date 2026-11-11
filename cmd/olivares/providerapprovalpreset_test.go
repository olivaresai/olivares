// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
)

func TestProviderSessionPresetsApplyBeforeNativeApproval(t *testing.T) {
	for _, driver := range []string{"codex", "grok", "opencode"} {
		t.Run(driver, func(t *testing.T) {
			for _, preset := range []struct {
				name, mode string
				want       sessions.ProviderApprovalDisposition
			}{
				{"read-only", "plan", sessions.ProviderApprovalDeny},
				{"ask", "default", sessions.ProviderApprovalAsk},
				{"edits-only", "acceptEdits", sessions.ProviderApprovalAsk},
			} {
				t.Run(preset.name, func(t *testing.T) {
					h := newHarness(t)
					tenant := model.TenantID(h.tenantA)
					human, err := h.authr.Authenticate(t.Context(), h.adminToken)
					if err != nil {
						t.Fatal(err)
					}
					credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
					intent := claimHookTestSession(t, h, human, tenant, driver+"-"+preset.name)
					intent.PermissionMode = preset.mode
					if _, err := credentials.mintForPrincipal(human, tenant, intent); err != nil {
						t.Fatal(err)
					}
					principal, scope, err := credentials.ResolveRun(t.Context(), tenant, intent.RunRef)
					if err != nil {
						t.Fatal(err)
					}
					service := h.set.gov.EngineApprovals()
					h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
					h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
					policy := sessionProviderPolicy{credentials: credentials.SessionCredentials, eval: h.set.gov.Evaluator(), authz: harnessAuthz(h), scoped: h.set.gov.ScopedGrants(), approvals: service, store: h.st}
					request := sessions.ProviderApprovalRequest{
						Driver: driver, RunRef: intent.RunRef, SessionRef: scope.SessionRef, Principal: principal,
						TurnID: "preset-turn", Kind: "tool_call_permission", Method: "session/request_permission", Requested: []string{"allow-once"},
					}
					if driver == "codex" {
						request.Kind, request.Method = "command_execution", "item/commandExecution/requestApproval"
						request.CommandLine, request.FactsComplete = "printf preset-proof > preset-proof.txt", true
					}
					decision, err := policy.Decide(t.Context(), tenant, request)
					if err != nil || decision.Disposition != preset.want {
						t.Fatalf("native %s approval bypassed %s preset: decision=%+v error=%v", driver, preset.name, decision, err)
					}
					for _, event := range canonicalLedgerEventsFrom(t, h.st, tenant, 0) {
						if event.event.Action == driver+".approval.allow" {
							t.Fatal("non-read native approval was audited as allowed before preset review")
						}
					}
					if preset.want == sessions.ProviderApprovalDeny {
						pending, _, err := service.List(t.Context(), tenant, "sessions.provider.approval", "pending", "")
						if err != nil || len(pending) != 0 {
							t.Fatal("read-only refusal entered human review")
						}
						return
					}
					bridge := newApprovalBridge(approvalBridgeConfig{}, discardLog())
					bridge.LocalProposer = service
					ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
					defer cancel()
					type answer struct {
						decision sessions.ProviderApprovalDecision
						err      error
					}
					answers := make(chan answer, 1)
					done := make(chan struct{})
					go func() {
						defer close(done)
						decision, err := (providerApprovalAdapter{bridge: bridge}).Approve(ctx, tenant, request)
						answers <- answer{decision: decision, err: err}
					}()
					defer func() { cancel(); <-done }()
					var pending governance.Approval
					for pending.ID == "" {
						items, _, err := service.List(ctx, tenant, "sessions.provider.approval", "pending", "")
						if err != nil {
							t.Fatal(err)
						}
						if len(items) > 0 {
							if len(items) != 1 {
								t.Fatal("preset ask created more than one human request")
							}
							pending = items[0]
							break
						}
						select {
						case early := <-answers:
							t.Fatalf("provider returned before human approval: %+v", early)
						case <-ctx.Done():
							t.Fatal("preset ask did not reach the existing queue")
						case <-time.After(10 * time.Millisecond):
						}
					}
					if pending.SessionRef != scope.SessionRef || pending.RequestedBy != "session:"+scope.SessionRef || pending.RequiredApprovals != 1 {
						t.Fatal("preset ask did not use the one session principal and one-reviewer default")
					}
					select {
					case early := <-answers:
						t.Fatalf("provider granted before a reviewer decided: %+v", early)
					default:
					}
					if _, err := service.Decide(ctx, tenant, human, pending.ID, governance.ApprovalDecisionRequest{Decision: "approve"}); err != nil {
						t.Fatal(err)
					}
					select {
					case answer := <-answers:
						if answer.err != nil || !answer.decision.Allow || answer.decision.SessionScope {
							t.Fatalf("one human approval did not grant this one action: %+v", answer)
						}
					case <-ctx.Done():
						t.Fatal("provider did not continue after approval")
					}
				})
			}
		})
	}
}

func TestProviderSessionNoPresetUsesLivePolicy(t *testing.T) {
	for _, driver := range []string{"codex", "grok", "opencode"} {
		t.Run(driver, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			human, err := h.authr.Authenticate(t.Context(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, human, tenant, driver+"-no-choice")
			intent.PermissionMode = ""
			if _, err := credentials.mintForPrincipal(human, tenant, intent); err != nil {
				t.Fatal(err)
			}
			principal, scope, err := credentials.ResolveRun(t.Context(), tenant, intent.RunRef)
			if err != nil || scope.Preset != sessions.PresetNone {
				t.Fatal("fixture did not launch without a preset choice")
			}
			policy := sessionProviderPolicy{credentials: credentials.SessionCredentials, eval: h.set.gov.Evaluator(), authz: harnessAuthz(h), scoped: h.set.gov.ScopedGrants(), approvals: h.set.gov.EngineApprovals(), store: h.st}
			request := sessions.ProviderApprovalRequest{Driver: driver, RunRef: intent.RunRef, SessionRef: scope.SessionRef, Principal: principal, Kind: "tool_call_permission", Method: "session/request_permission"}
			if driver == "codex" {
				request.Kind, request.Method = "command_execution", "item/commandExecution/requestApproval"
				request.CommandLine, request.FactsComplete = "printf policy-proof", true
			}
			decision, err := policy.Decide(t.Context(), tenant, request)
			if err != nil || decision.Disposition != sessions.ProviderApprovalAllow {
				t.Fatalf("no preset must defer to the permitting live policy: %+v, %v", decision, err)
			}
			createSessionReviewPolicy(t, h, "sessions.provider.approval", "session_run")
			decision, err = policy.Decide(t.Context(), tenant, request)
			if err != nil || decision.Disposition != sessions.ProviderApprovalAsk {
				t.Fatalf("no preset must retain authored human review: %+v, %v", decision, err)
			}
		})
	}
}

// Each permission's one-sentence summary (modules/sessions/permission_modes.go) is what
// every tool gets. Claude Code answers through its hook; Codex, OpenCode, Grok Build and
// Gemini CLI through their native approvals. Codex never asks before a read.
func TestPermissionSummariesHoldForEveryTool(t *testing.T) {
	const allow, ask, refuse = "allow", "ask", "deny"
	words := []struct{ name, read, edit, command, other string }{
		{"supervised", allow, ask, ask, ask},                // Asks before every action that is not a read.
		{"auto-accept-edits", allow, allow, ask, ask},       // Reads and edits files without asking. Everything else asks.
		{"edits-and-commands", allow, allow, allow, refuse}, // ... and runs commands without asking. Other tools are refused.
	}
	var builtin []string
	for _, row := range sessions.PermissionTable() {
		for _, cell := range row.Tools {
			if list, ok := strings.CutPrefix(strings.Join(cell.Receives, "\n"), "--permission-mode dontAsk\n--allowedTools "); ok && cell.Driver == "claude" {
				builtin = strings.Split(list, ",")
			}
		}
	}
	if len(builtin) == 0 {
		t.Fatal("Edit files and run commands hands Claude Code no allowlist")
	}
	launch := func(t *testing.T, h *harness, human auth.Principal, tenant model.TenantID, run, name string) sessions.LaunchIntent {
		n, ok := sessions.ResolvePermission(name)
		if !ok {
			t.Fatalf("no permission %q", name)
		}
		intent := claimHookTestSession(t, h, human, tenant, run)
		intent.LauncherPrincipal = human
		intent.PermissionMode, intent.TemplateBuiltin, intent.AllowedTools = n.Mode, false, nil
		if n.Template {
			intent.PermissionMode, intent.TemplateBuiltin, intent.AllowedTools = "dontAsk", true, builtin
		}
		return intent
	}
	for _, driver := range []string{"claude", "codex", "opencode", "grok", "gemini"} {
		t.Run(driver, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			human, err := h.authr.Authenticate(t.Context(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			credentials := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			service := h.set.gov.EngineApprovals()
			h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
			h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
			for _, w := range words {
				t.Run(w.name, func(t *testing.T) {
					intent := launch(t, h, human, tenant, driver+"-"+w.name, w.name)
					token, err := credentials.mintForPrincipal(human, tenant, intent)
					if err != nil {
						t.Fatal(err)
					}
					if driver == "claude" {
						d := newClaudeHookDecider(&hookpep.Decider{DefaultPolicy: &hookpep.PolicyDoc{Default: "allow"}, Authr: credentials, Eval: h.set.gov.Evaluator(), Authz: harnessAuthz(h), Scoped: h.set.gov.ScopedGrants(), Approvals: service, Store: h.st, Clock: time.Now, Log: discardLog()})
						for _, c := range []struct {
							tool, want string
							input      map[string]any
						}{
							{"Read", w.read, map[string]any{"file_path": "/tmp/preset-proof"}},
							{"Edit", w.edit, map[string]any{"file_path": "/tmp/preset-proof", "old_string": "a", "new_string": "b"}},
							{"Bash", w.command, map[string]any{"command": "printf preset-proof"}},
							{"mcp__fixture__tool", w.other, map[string]any{"query": "preset-proof"}},
						} {
							t.Run(c.tool, func(t *testing.T) {
								want := c.want
								if want == refuse {
									// Claude Code refuses it itself: dontAsk denies a tool the allowlist
									// does not name, and the hook's allow does not skip that.
									if !intent.TemplateBuiltin || intent.PermissionMode != "dontAsk" || slices.Contains(builtin, c.tool) {
										t.Fatalf("Claude Code under %q is not held to an allowlist without %s", w.name, c.tool)
									}
									want = allow
								}
								assertPresetHook(t, d, service, tenant, human, token, intent.ClaimSID, "PreToolUse", c.tool, want, c.input)
							})
						}
						return
					}
					principal, scope, err := credentials.ResolveRun(t.Context(), tenant, intent.RunRef)
					if err != nil {
						t.Fatal(err)
					}
					policy := sessionProviderPolicy{credentials: credentials.SessionCredentials, eval: h.set.gov.Evaluator(), authz: harnessAuthz(h), scoped: h.set.gov.ScopedGrants(), approvals: service, store: h.st}
					acp := func(toolKind, command string, paths ...string) sessions.ProviderApprovalRequest {
						return sessions.ProviderApprovalRequest{Kind: "tool_call_permission", Method: "session/request_permission", ToolKind: toolKind,
							CommandLine: command, FilePaths: paths, FactsComplete: command != "" || len(paths) > 0}
					}
					type approval struct {
						req  sessions.ProviderApprovalRequest
						want string
					}
					cases := map[string]approval{
						"read":    {acp("read", "", "README.md"), w.read},
						"edit":    {acp("edit", "", "README.md"), w.edit},
						"command": {acp("execute", "printf preset-proof"), w.command},
						"other":   {acp("fetch", ""), w.other},
					}
					if driver == "codex" {
						cases = map[string]approval{
							"edit":    {sessions.ProviderApprovalRequest{Kind: "file_change", Method: "item/fileChange/requestApproval", FilePaths: []string{"README.md"}, FactsComplete: true}, w.edit},
							"command": {sessions.ProviderApprovalRequest{Kind: "command_execution", Method: "item/commandExecution/requestApproval", CommandLine: "printf preset-proof", FactsComplete: true}, w.command},
							"other":   {sessions.ProviderApprovalRequest{Kind: "permissions", Method: "item/permissions/requestApproval"}, w.other},
						}
					}
					for action, c := range cases {
						t.Run(action, func(t *testing.T) {
							req := c.req
							req.Driver, req.RunRef, req.SessionRef, req.Principal = driver, intent.RunRef, scope.SessionRef, principal
							req.TurnID, req.Requested = "summary-turn", []string{"allow-once"}
							decision, err := policy.Decide(t.Context(), tenant, req)
							if err != nil || string(decision.Disposition) != c.want {
								t.Errorf("%s under %q: %s = %+v %v, want %s", driver, w.name, action, decision, err, c.want)
							}
						})
					}
				})
			}
		})
	}
}
