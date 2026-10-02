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
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions"
)

type approvalProjectionProcess struct {
	output chan sessions.OutputFrame
	done   chan struct{}
	once   sync.Once
}

func (p *approvalProjectionProcess) Send(ctx context.Context, _ []byte) error { return ctx.Err() }
func (p *approvalProjectionProcess) Output() <-chan sessions.OutputFrame      { return p.output }
func (p *approvalProjectionProcess) Wait() (int, error)                       { <-p.done; return 0, nil }
func (p *approvalProjectionProcess) Stop(context.Context) error {
	p.once.Do(func() { close(p.output); close(p.done) })
	return nil
}
func (p *approvalProjectionProcess) PID() int { return 0 }

type approvalProjectionRunner struct{}

func (approvalProjectionRunner) Launch(context.Context, sessions.LaunchSpec) (sessions.Process, error) {
	p := &approvalProjectionProcess{output: make(chan sessions.OutputFrame, 1), done: make(chan struct{})}
	p.output <- sessions.OutputFrame{Stream: "stdout", Data: []byte(`{"type":"system","subtype":"init","session_id":"pending-approval-provider"}`)}
	return p, nil
}

type approvalProjectionLaunchGate func(context.Context, model.TenantID, sessions.LaunchIntent) (sessions.LaunchDecision, error)

func (g approvalProjectionLaunchGate) Authorize(ctx context.Context, tenant model.TenantID, in sessions.LaunchIntent) (sessions.LaunchDecision, error) {
	return g(ctx, tenant, in)
}

// Observe the existing human queue and both public run views while the real hook
// decoder waits. The runner is the public process seam; policy, identity, queue
// and session projection are the production implementations.
func TestSessionToolApprovalAppearsAndClearsOnTerminalWait(t *testing.T) {
	for _, family := range []string{"claude", "provider"} {
		for _, outcome := range []string{"approve", "reject", "cancel"} {
			t.Run(family+"/"+outcome, func(t *testing.T) {
				h := newHarness(t)
				tenant := model.TenantID(h.tenantA)
				m := h.set.sessions
				sessions.WithRunner(approvalProjectionRunner{})(m)
				m.EnableProfiledLaunches()
				m.UseExecutionEnvironmentRef("pending-approval-test")
				credentials := newSessionHookCredentials(h.authr, h.st, m, h.set.gov)
				var token string
				m.UseLaunchGate(approvalProjectionLaunchGate(func(ctx context.Context, tenant model.TenantID, in sessions.LaunchIntent) (sessions.LaunchDecision, error) {
					var err error
					token, err = credentials.mint(ctx, tenant, in)
					return sessions.LaunchDecision{Allowed: err == nil}, err
				}))
				var profile struct {
					Ref string `json:"profile_ref"`
				}
				if code := h.reqInto(http.MethodPost, "/v1/m/sessions/provider-profiles", h.adminToken, h.tenantA, map[string]any{"driver": "claude", "auth_source": "provider_account_home", "config_home": t.TempDir(), "user_home": t.TempDir()}, &profile); code != http.StatusCreated {
					t.Fatalf("profile=%d", code)
				}
				var run struct {
					Ref string `json:"run_ref"`
				}
				if code := h.reqInto(http.MethodPost, "/v1/m/sessions/runs", h.adminToken, h.tenantA, map[string]any{"transport": "stream-json", "permission_mode": "default", "isolation": "native", "provider_profile_ref": profile.Ref}, &run); code != http.StatusCreated {
					t.Fatalf("launch=%d", code)
				}
				t.Cleanup(func() { h.req(http.MethodPost, "/v1/m/sessions/runs/"+run.Ref+"/stop", h.adminToken, h.tenantA, nil) })
				service := h.set.gov.EngineApprovals()
				h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
				h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
				d := &claudeHookDecider{defaultPolicy: &hookPolicyDoc{Default: "allow"}, authr: credentials, eval: h.set.gov.Evaluator(), scoped: h.set.gov.ScopedGrants(), approvals: service, approvalWait: m.BeginApprovalWait, store: h.st, clock: time.Now, log: discardLog()}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"printf pending-projection"}}`)).WithContext(ctx)
				req.Header.Set("Authorization", "Bearer "+token)
				rec := httptest.NewRecorder()
				done := make(chan struct{})
				var providerAnswer sessions.ProviderApprovalDecision
				var providerErr error
				go func() {
					defer close(done)
					if family == "claude" {
						claude.NewHookPEP(d, nil, time.Now).ServeHTTP(rec, req)
						return
					}
					p, scope, err := credentials.ResolveRun(ctx, tenant, run.Ref)
					if err != nil {
						providerErr = err
						return
					}
					b := newApprovalBridge(approvalBridgeConfig{}, discardLog())
					b.localProposer = service
					providerAnswer, providerErr = (providerApprovalAdapter{bridge: b, approvalWait: m.BeginApprovalWait}).Approve(ctx, tenant, sessions.ProviderApprovalRequest{
						Driver: "codex", RunRef: run.Ref, SessionRef: scope.SessionRef, Principal: p,
						TurnID: "turn-wait", Method: "item/commandExecution/requestApproval", Kind: "command_execution",
						CommandLine: "printf pending-projection", FactsComplete: true,
					})
				}()
				defer func() { cancel(); <-done }()
				assertViews := func(want string) {
					t.Helper()
					var detail map[string]any
					if code := h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+run.Ref, h.adminToken, h.tenantA, nil, &detail); code != http.StatusOK {
						t.Fatalf("detail=%d", code)
					}
					var list struct {
						Items []map[string]any `json:"items"`
					}
					if code := h.reqInto(http.MethodGet, "/v1/m/sessions/runs", h.adminToken, h.tenantA, nil, &list); code != http.StatusOK || len(list.Items) != 1 {
						t.Fatalf("list=%d count=%d", code, len(list.Items))
					}
					for _, row := range []map[string]any{detail, list.Items[0]} {
						got, exists := row["pending_approval_ref"]
						if want == "" && exists || want != "" && got != want {
							t.Fatalf("pending_approval_ref=%v, want=%q", got, want)
						}
						if row["process_state"] != "running" {
							t.Fatalf("process state=%v", row["process_state"])
						}
					}
				}
				var ref string
				for ref == "" {
					var list struct {
						Items []struct {
							ID string `json:"id"`
						} `json:"items"`
					}
					if code := h.reqInto(http.MethodGet, "/v1/m/governance/approvals?status=pending", h.adminToken, h.tenantA, nil, &list); code != http.StatusOK {
						t.Fatalf("approvals=%d", code)
					}
					if len(list.Items) > 0 {
						ref = list.Items[0].ID
						break
					}
					select {
					case <-done:
						t.Fatalf("hook ended without review: %s", rec.Body.String())
					case <-ctx.Done():
						t.Fatal("review was not queued")
					case <-time.After(10 * time.Millisecond):
					}
				}
				// Publication follows Request, so allow one scheduler turn for binding.
				for {
					var row map[string]any
					h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+run.Ref, h.adminToken, h.tenantA, nil, &row)
					if row["pending_approval_ref"] == ref {
						break
					}
					select {
					case <-done:
						t.Fatalf("wait was not projected: %s", rec.Body.String())
					case <-ctx.Done():
						t.Fatal("pending ref was not projected")
					case <-time.After(10 * time.Millisecond):
					}
				}
				assertViews(ref)
				if outcome == "cancel" {
					cancel()
				} else if code, _ := h.req(http.MethodPost, "/v1/m/governance/approvals/"+ref+"/decisions", h.adminToken, h.tenantA, map[string]any{"decision": outcome}); code != http.StatusCreated && code != http.StatusOK {
					t.Fatalf("decision=%d", code)
				}
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("terminal wait did not finish")
				}
				assertViews("")
				if family == "provider" {
					if providerAnswer.Allow != (outcome == "approve") || providerErr != nil && outcome != "cancel" {
						t.Fatalf("provider answer=%+v err=%v outcome=%s", providerAnswer, providerErr, outcome)
					}
					return
				}
				var reply struct {
					HookSpecificOutput struct {
						Decision string `json:"permissionDecision"`
					} `json:"hookSpecificOutput"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
					t.Fatal(err)
				}
				want := "deny"
				if outcome == "approve" {
					want = "allow"
				}
				if reply.HookSpecificOutput.Decision != want {
					t.Fatalf("decision=%s, want=%s", reply.HookSpecificOutput.Decision, want)
				}
			})
		}
	}
}
