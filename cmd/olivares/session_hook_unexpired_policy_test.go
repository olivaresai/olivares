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
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
)

// An authored quorum does not need an expiry. The actual hook and managed run
// still share a bounded live wait and finish with an explicit terminal verdict.
func TestSessionClaudeApprovalWithoutPolicyExpiryHasBoundedLiveWait(t *testing.T) {
	for _, outcome := range []string{"approve", "deadline"} {
		t.Run(outcome, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			m := h.set.sessions
			sessions.WithRunner(approvalProjectionRunner{})(m)
			m.UseExecutionEnvironmentRef("unexpired-policy-test")
			credentials := newSessionHookCredentials(h.authr, h.st, m, h.set.gov)
			var token string
			sessions.WithLaunchGate(approvalProjectionLaunchGate(func(ctx context.Context, tenant model.TenantID, intent sessions.LaunchIntent) (sessions.LaunchDecision, error) {
				var err error
				token, err = credentials.mint(ctx, tenant, intent)
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
			createSessionReviewPolicy(t, h, hookpep.ActionCapability, "claude.tool")
			registered := make(chan time.Time, 1)
			d := newClaudeHookDecider(&hookpep.Decider{DefaultPolicy: &hookpep.PolicyDoc{Default: "allow"}, Authr: credentials, Eval: h.set.gov.Evaluator(), Authz: harnessAuthz(h), Scoped: h.set.gov.ScopedGrants(), Approvals: service, Store: h.st, Clock: time.Now, Log: discardLog()})
			d.ApprovalWait = func(ctx context.Context, p auth.Principal, ref string, expires time.Time) (func(), error) {
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
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"printf unexpired-policy"}}`)).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { defer close(done); claude.NewHookPEP(d, nil, time.Now).ServeHTTP(rec, req) }()
			defer func() { cancel(); <-done }()
			var liveUntil time.Time
			select {
			case liveUntil = <-registered:
			case <-done:
				t.Fatalf("ordinary policy refused its live wait: %s", rec.Body.String())
			case <-ctx.Done():
				t.Fatal("live wait was not registered")
			}
			deadline, _ := ctx.Deadline()
			if liveUntil.IsZero() || liveUntil.After(deadline) {
				t.Fatal("live wait is not bounded by the hook deadline")
			}
			items, _, err := service.List(t.Context(), tenant, hookpep.ActionCapability, "pending", "")
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
				t.Fatal("hook did not finish its bounded wait")
			}
			var reply struct {
				HookSpecificOutput struct {
					Decision string `json:"permissionDecision"`
					Reason   string `json:"permissionDecisionReason"`
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
				t.Fatalf("decision=%s want=%s", reply.HookSpecificOutput.Decision, want)
			}
			if outcome == "deadline" && !strings.Contains(reply.HookSpecificOutput.Reason, "deadline") {
				t.Fatal("deadline refusal did not explain the bounded wait")
			}
			view = nil
			h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+run.Ref, h.adminToken, h.tenantA, nil, &view)
			if _, exists := view["pending_approval_ref"]; exists {
				t.Fatal("terminal hook retained its live wait")
			}
		})
	}
}
