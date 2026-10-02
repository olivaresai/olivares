// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sessions"
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
					policy := sessionProviderPolicy{credentials: credentials.SessionCredentials, eval: h.set.gov.Evaluator(), scoped: h.set.gov.ScopedGrants(), approvals: service, store: h.st}
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
					bridge.localProposer = service
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
			policy := sessionProviderPolicy{credentials: credentials.SessionCredentials, eval: h.set.gov.Evaluator(), scoped: h.set.gov.ScopedGrants(), approvals: h.set.gov.EngineApprovals(), store: h.st}
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
