// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sessions"
)

// An authored quorum does not need an expiry. The provider adapter and managed run
// still share a bounded live wait and finish with an explicit terminal verdict.
func TestSessionProviderApprovalWithoutPolicyExpiryHasBoundedLiveWait(t *testing.T) {
	for _, outcome := range []string{"approve", "deadline"} {
		t.Run(outcome, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			m := h.set.sessions
			sessions.WithRunner(approvalProjectionRunner{})(m)
			m.UseExecutionEnvironmentRef("unexpired-policy-test")
			credentials := newSessionHookCredentials(h.authr, h.st, m, h.set.gov)
			sessions.WithLaunchGate(approvalProjectionLaunchGate(func(ctx context.Context, tenant model.TenantID, intent sessions.LaunchIntent) (sessions.LaunchDecision, error) {
				var err error
				_, err = credentials.mint(ctx, tenant, intent)
				return sessions.LaunchDecision{Allowed: err == nil}, err
			}))(m)
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
			if code, _ := h.req(http.MethodPost, "/v1/m/governance/policies", h.adminToken, h.tenantA, map[string]any{"name": "provider-unexpired", "kind": "approval", "enabled": true, "spec": map[string]any{"required_approvals": 1, "match": map[string]any{"action": "sessions.provider.approval", "subject_kind": "session_run"}}}); code != http.StatusCreated {
				t.Fatalf("review policy=%d", code)
			}
			registered := make(chan time.Time, 1)
			bridge := newApprovalBridge(approvalBridgeConfig{}, discardLog())
			bridge.LocalProposer = service
			adapter := providerApprovalAdapter{bridge: bridge}
			adapter.approvalWait = func(ctx context.Context, p auth.Principal, ref string, expires time.Time) (func(), error) {
				end, err := m.BeginApprovalWait(ctx, p, ref, expires)
				if err == nil {
					registered <- expires
				}
				return end, err
			}
			duration := 3 * time.Second
			if outcome == "deadline" {
				duration = 750 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(t.Context(), duration)
			defer cancel()
			p, scope, err := credentials.ResolveRun(ctx, tenant, run.Ref)
			if err != nil {
				t.Fatal(err)
			}
			requestFacts := sessions.ProviderApprovalRequest{Driver: "codex", RunRef: run.Ref, SessionRef: scope.SessionRef, Principal: p, TurnID: "unexpired-turn", Method: "item/commandExecution/requestApproval", Kind: "command_execution", CommandLine: "printf unexpired-policy", FactsComplete: true}
			done := make(chan struct{})
			var answer sessions.ProviderApprovalDecision
			var answerErr error
			go func() { defer close(done); answer, answerErr = adapter.Approve(ctx, tenant, requestFacts) }()
			defer func() { cancel(); <-done }()
			var liveUntil time.Time
			select {
			case liveUntil = <-registered:
			case <-done:
				t.Fatalf("ordinary policy refused its live wait: %q (%v)", answer.Reason, answerErr)
			case <-ctx.Done():
				t.Fatal("live wait was not registered")
			}
			deadline, _ := ctx.Deadline()
			if liveUntil.IsZero() || liveUntil.After(deadline) {
				t.Fatal("live wait is not bounded by the provider deadline")
			}
			items, _, err := service.List(t.Context(), tenant, "sessions.provider.approval", "pending", "")
			if err != nil || len(items) != 1 {
				t.Fatalf("pending=%d err=%v", len(items), err)
			}
			request := items[0]
			if request.ExpiresAt != "" {
				t.Fatal("no-expiry policy gained an authored expiry")
			}
			var view map[string]any
			if code := h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+run.Ref, h.adminToken, h.tenantA, nil, &view); code != http.StatusOK || view["pending_approval_ref"] != request.ID {
				t.Fatal("managed run did not expose its live review")
			}
			if outcome == "approve" {
				human, err := h.authr.Authenticate(ctx, h.adminToken)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = service.Decide(ctx, tenant, human, request.ID, governance.ApprovalDecisionRequest{Decision: "approve"}); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				t.Fatal("provider did not finish its bounded wait")
			}
			if answer.Allow != (outcome == "approve") || answerErr != nil && outcome == "approve" {
				t.Fatalf("decision=%+v err=%v outcome=%s", answer, answerErr, outcome)
			}
			if outcome == "deadline" && !errors.Is(answerErr, context.DeadlineExceeded) {
				t.Fatal("provider wait did not end at its context deadline")
			}
			stored, err := service.Read(t.Context(), tenant, request.ID)
			if err != nil || stored.ExpiresAt != "" {
				t.Fatal("live wait changed the authored expiry")
			}
			if outcome == "deadline" && stored.Status != "expired" {
				t.Fatalf("live deadline did not persist its terminal verdict: status=%s", stored.Status)
			}
			view = nil
			if code := h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+run.Ref, h.adminToken, h.tenantA, nil, &view); code != http.StatusOK || view == nil {
				t.Fatalf("terminal managed run could not be observed: status=%d", code)
			}
			if _, exists := view["pending_approval_ref"]; exists {
				t.Fatal("terminal provider retained its live wait")
			}
		})
	}
}
