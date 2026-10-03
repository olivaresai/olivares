// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestSessionClaudeApprovalWaitsAndRechecksLiveAuthority(t *testing.T) {
	for _, scenario := range []string{"approve", "reject", "policy-deny", "revoke"} {
		t.Run(scenario, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			human, err := h.authr.Authenticate(context.Background(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, human, tenant, "wait-"+scenario)
			token, err := c.mintForPrincipal(human, tenant, intent)
			if err != nil {
				t.Fatal(err)
			}
			service := h.set.gov.EngineApprovals()
			h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
			h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
			createSessionReviewPolicy(t, h, hookActionCapability, "claude.tool")
			d := &claudeHookDecider{defaultPolicy: &hookPolicyDoc{Default: "allow"}, authr: c, eval: h.set.gov.Evaluator(), scoped: h.set.gov.ScopedGrants(), approvals: service, stops: h.set.gov, stopRec: newStopDenyRecorder(h.st, discardLog()), store: h.st, clock: time.Now, log: discardLog()}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			results := make(chan claude.HookDecisionResult, 1)
			go func() {
				out, _ := d.Decide(ctx, claude.HookDecisionInput{Event: "PreToolUse", Tool: "Write", ResourceKind: "file", ResourceRef: "/tmp/proof", Mode: "write", PlanHash: hexSHA("write-proof")}, token)
				results <- out
			}()
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
				case early := <-results:
					t.Fatalf("ask returned before human review: %+v", early)
				case <-ctx.Done():
					t.Fatal("request did not reach the queue")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if pending.SessionRef != intent.ClaimSID || pending.RequestedBy != "session:"+intent.ClaimSID {
				t.Fatalf("requester is not the one session: %+v", pending)
			}
			if scenario == "policy-deny" {
				code, raw := h.req("POST", "/v1/m/governance/policies", h.adminToken, h.tenantA, map[string]any{"name": "deny-after-ask", "kind": "abac", "enabled": true, "spec": map[string]any{"rules": []any{map[string]any{"deny": true, "permission": "claude.tool.use:write"}}}})
				if code != 201 {
					t.Fatalf("policy: %d %s", code, raw)
				}
			}
			if scenario == "revoke" {
				c.Revoke(tenant, intent.RunRef)
			}
			decision := "approve"
			if scenario == "reject" {
				decision = "reject"
			}
			if _, err := service.Decide(ctx, tenant, human, pending.ID, governance.ApprovalDecisionRequest{Decision: decision}); err != nil {
				t.Fatal(err)
			}
			select {
			case out := <-results:
				want := "deny"
				if scenario == "approve" {
					want = "allow"
				}
				if out.Permission != want {
					t.Fatalf("verdict=%+v scenario=%s", out, scenario)
				}
			case <-ctx.Done():
				t.Fatal("hook did not finish after human review")
			}
		})
	}
}

func TestSessionClaudeApprovalDeadlineReturnsExplicitDeny(t *testing.T) {
	h := newHarness(t)
	tenant := model.TenantID(h.tenantA)
	human, err := h.authr.Authenticate(context.Background(), h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
	intent := claimHookTestSession(t, h, human, tenant, "ask-timeout")
	token, err := c.mintForPrincipal(human, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	createSessionReviewPolicy(t, h, hookActionCapability, "claude.tool")
	d := &claudeHookDecider{defaultPolicy: &hookPolicyDoc{Default: "allow"}, authr: c, eval: h.set.gov.Evaluator(), approvals: h.set.gov.EngineApprovals(), store: h.st, clock: time.Now, log: discardLog()}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	out, err := d.Decide(ctx, claude.HookDecisionInput{Event: "PreToolUse", Tool: "Read", ResourceKind: "file", ResourceRef: "/tmp/proof", Mode: "read", PlanHash: hexSHA("timeout")}, token)
	if err != nil || out.Permission != "deny" || !strings.Contains(out.Reason, "deadline") {
		t.Fatalf("timeout decision=%+v err=%v", out, err)
	}
}

func TestProviderSessionPolicyUsesLivePDPFactsAndAudit(t *testing.T) {
	h := newHarness(t)
	tenant := model.TenantID(h.tenantA)
	human, err := h.authr.Authenticate(context.Background(), h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
	intent := claimHookTestSession(t, h, human, tenant, "codex-policy")
	if _, err := c.mintForPrincipal(human, tenant, intent); err != nil {
		t.Fatal(err)
	}
	p, scope, err := c.ResolveRun(context.Background(), tenant, intent.RunRef)
	if err != nil {
		t.Fatal(err)
	}
	observed := &providerFactEvaluator{next: h.set.gov.Evaluator()}
	g := sessionProviderPolicy{credentials: c.SessionCredentials, eval: observed, scoped: h.set.gov.ScopedGrants(), approvals: h.set.gov.EngineApprovals(), store: h.st}
	req := sessions.ProviderApprovalRequest{Driver: "codex", RunRef: intent.RunRef, SessionRef: scope.SessionRef, Principal: p, Method: "item/commandExecution/requestApproval", Kind: "command_execution", CommandLine: "curl https://user:fixture-secret@host/proof", FactsComplete: true}
	out, err := g.Decide(context.Background(), tenant, req)
	if err != nil || out.Disposition != sessions.ProviderApprovalAllow {
		t.Fatalf("default=%+v %v", out, err)
	}
	if observed.question.Resource.WorkspaceID != scope.WorkspaceID || observed.question.Resource.Extra["command_line"] != req.CommandLine {
		t.Fatal("policy did not receive scoped, original command facts")
	}
	createSessionReviewPolicy(t, h, "sessions.provider.approval", "session_run")
	out, err = g.Decide(context.Background(), tenant, req)
	if err != nil || out.Disposition != sessions.ProviderApprovalAsk {
		t.Fatalf("authored ask=%+v %v", out, err)
	}
	code, raw := h.req("POST", "/v1/m/governance/policies", h.adminToken, h.tenantA, map[string]any{"name": "deny-codex", "kind": "abac", "enabled": true, "spec": map[string]any{"rules": []any{map[string]any{"deny": true, "permission": "codex.tool.use:use"}}}})
	if code != 201 {
		t.Fatalf("policy: %d %s", code, raw)
	}
	out, err = g.Decide(context.Background(), tenant, req)
	if err != nil || out.Disposition != sessions.ProviderApprovalDeny {
		t.Fatalf("deny must outrank ask=%+v %v", out, err)
	}
	seen := map[string]bool{}
	for _, ev := range canonicalLedgerEventsFrom(t, h.st, tenant, 0) {
		if strings.HasPrefix(ev.event.Action, "codex.approval.") {
			seen[ev.event.Action] = true
			if ev.meta["session_ref"] != scope.SessionRef || strings.Contains(ev.meta["command_line"].(string), "fixture-secret") {
				t.Fatal("audit lacks scoped redacted facts")
			}
		}
	}
	for _, verdict := range []string{"allow", "ask", "deny"} {
		if !seen["codex.approval."+verdict] {
			t.Fatalf("missing %s audit", verdict)
		}
	}
}

func TestProviderSessionPolicyRefusesUnreviewableCommandWithoutPartialEvidence(t *testing.T) {
	for _, complete := range []bool{true, false} {
		name := "decoder incomplete"
		if complete {
			name = "boundary recheck"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			tenant := model.TenantID(h.tenantA)
			human, err := h.authr.Authenticate(context.Background(), h.adminToken)
			if err != nil {
				t.Fatal(err)
			}
			c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, h.set.gov)
			intent := claimHookTestSession(t, h, human, tenant, "unreviewable-codex")
			if _, err := c.mintForPrincipal(human, tenant, intent); err != nil {
				t.Fatal(err)
			}
			p, scope, err := c.ResolveRun(context.Background(), tenant, intent.RunRef)
			if err != nil {
				t.Fatal(err)
			}
			observed := &providerFactEvaluator{next: h.set.gov.Evaluator()}
			g := sessionProviderPolicy{credentials: c.SessionCredentials, eval: observed, scoped: h.set.gov.ScopedGrants(), approvals: h.set.gov.EngineApprovals(), store: h.st}
			out, err := g.Decide(context.Background(), tenant, sessions.ProviderApprovalRequest{
				Driver: "codex", RunRef: intent.RunRef, SessionRef: scope.SessionRef, Principal: p,
				Method: "item/commandExecution/requestApproval", Kind: "command_execution",
				CommandLine: "password=$(printf codex-review-proof)", FactsComplete: complete,
			})
			if err != nil || out.Disposition != sessions.ProviderApprovalDeny || observed.question.Permission != "" {
				t.Fatalf("unreviewable command reached the PDP: decision=%+v error=%v", out, err)
			}
			found := false
			for _, ev := range canonicalLedgerEventsFrom(t, h.st, tenant, 0) {
				if ev.event.Action == "codex.approval.deny" {
					found = true
					if ev.meta["facts_complete"] != false || ev.meta["command_line"] != "command not reviewable" {
						t.Fatal("denial recorded a partial command instead of an incomplete raw-free fact")
					}
				}
			}
			if !found {
				t.Fatal("unreviewable command refusal was not audited")
			}
		})
	}
}

type providerFactEvaluator struct {
	next     auth.PolicyEvaluator
	question auth.Request
}

func (p *providerFactEvaluator) Evaluate(ctx context.Context, req auth.Request) (auth.Decision, error) {
	p.question = req
	return p.next.Evaluate(ctx, req)
}

func createSessionReviewPolicy(t *testing.T, h *harness, action, kind string) {
	t.Helper()
	code, raw := h.req("POST", "/v1/m/governance/policies", h.adminToken, h.tenantA, map[string]any{"name": "review-" + action, "kind": "approval", "enabled": true, "spec": map[string]any{"required_approvals": 1, "match": map[string]any{"action": action, "subject_kind": kind}}})
	if code != 201 {
		t.Fatalf("review policy: %d %s", code, raw)
	}
}
