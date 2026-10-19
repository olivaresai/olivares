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
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
)

type budgetCall struct {
	denied bool
	reason string
}

// denialBudgetHook is the production hook decider on a live governed run: the real
// credentials, policy and approval queue, with the tenant's budget set by the operator
// config and every counted decision also recorded here.
type denialBudgetHook struct {
	t      *testing.T
	h      *harness
	run    string
	token  string
	dec    *hookpep.Decider
	mu     sync.Mutex
	counts []budgetCall
	errs   []error // what the module answered to each counted decision
}

func newDenialBudgetHook(t *testing.T, budget sessions.DenialBudget) *denialBudgetHook {
	t.Helper()
	return newDenialBudgetHookWith(t, budget, nil, nil)
}

// newDenialBudgetHookWith is newDenialBudgetHook with two seams a test may need before the
// run exists: beforeLaunch sees the module and the session credentials ahead of the launch,
// and decider adjusts the hook decider before it is wrapped.
func newDenialBudgetHookWith(t *testing.T, budget sessions.DenialBudget, beforeLaunch func(*sessions.Module, *sessionHookCredentials), decider func(*hookpep.Decider)) *denialBudgetHook {
	t.Helper()
	h := newHarness(t)
	tenant := model.TenantID(h.tenantA)
	m := h.set.sessions
	sessions.WithRunner(approvalProjectionRunner{})(m)
	m.UseExecutionEnvironmentRef("denial-budget-test")
	credentials := newSessionHookCredentials(h.authr, h.st, m, h.set.gov)
	f := &denialBudgetHook{t: t, h: h}
	if beforeLaunch != nil {
		beforeLaunch(m, credentials)
	}
	sessions.WithLaunchGate(approvalProjectionLaunchGate(func(ctx context.Context, tenant model.TenantID, in sessions.LaunchIntent) (sessions.LaunchDecision, error) {
		var err error
		f.token, err = credentials.mint(ctx, tenant, in)
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
	f.run = run.Ref
	t.Cleanup(func() { h.req(http.MethodPost, "/v1/m/sessions/runs/"+f.run+"/stop", h.adminToken, h.tenantA, nil) })
	h.set.gov.UseApprovalCapacity(h.authr.ApprovalCapacity)
	h.set.gov.UseApprovalAuthority(h.authr, auth.NewAuthorizer(h.set.gov.RequestEvaluator(), auth.WithScopedGrants(h.set.gov.ScopedGrants())))
	resolved, err := hookpep.ResolveTenant(tenant, hookpep.TenantConfig{
		Tenant: h.tenantA, DenialBudget: budget,
		Policy: hookpep.PolicyDoc{Default: "allow", Rules: []hookpep.PolicyRule{
			{Tool: "Write", Decision: "deny", Reason: "writes are not allowed here"},
			{Tool: "Bash", Decision: "ask", Reason: "shell needs review"},
		}},
	}, time.Now(), discardLog())
	if err != nil {
		t.Fatal(err)
	}
	hd := &hookpep.Decider{
		Tenants: map[model.TenantID]hookpep.ResolvedTenant{tenant: resolved},
		Authr:   credentials, Eval: h.set.gov.Evaluator(), Authz: harnessAuthz(h), Scoped: h.set.gov.ScopedGrants(),
		Approvals: h.set.gov.EngineApprovals(), ApprovalWait: m.BeginApprovalWait, Store: h.st, Clock: time.Now, Log: discardLog(),
		SessionDecision: func(p auth.Principal, denied bool, reason string, b sessions.DenialBudget) error {
			err := m.RecordToolDecision(p, denied, reason, b)
			f.mu.Lock()
			f.counts = append(f.counts, budgetCall{denied, reason})
			f.errs = append(f.errs, err)
			f.mu.Unlock()
			return err
		},
	}
	if decider != nil {
		decider(hd)
	}
	f.dec = newClaudeHookDecider(hd)
	return f
}

func (f *denialBudgetHook) counted() []budgetCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]budgetCall(nil), f.counts...)
}

// fire sends one hook event the way Claude Code does and returns the verdict.
func (f *denialBudgetHook) fire(ctx context.Context, body string) (string, string) {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+f.token)
	rec := httptest.NewRecorder()
	claude.NewHookPEP(f.dec, nil, time.Now).ServeHTTP(rec, req)
	var reply struct {
		HookSpecificOutput struct {
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		f.t.Fatalf("hook reply %q: %v", rec.Body.String(), err)
	}
	return reply.HookSpecificOutput.Decision, reply.HookSpecificOutput.Reason
}

const (
	hookWriteDenied = `{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"/tmp/budget","content":"x"}}`
	hookReadAllowed = `{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/tmp/budget"}}`
	hookBashAsk     = `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"printf budget"}}`
)

// askAnd sends a gated Bash call and ends its review with outcome: approve, reject or cancel.
func (f *denialBudgetHook) askAnd(outcome string) (string, string) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.t.Context(), 10*time.Second)
	defer cancel()
	var decision, reason string
	done := make(chan struct{})
	go func() { defer close(done); decision, reason = f.fire(ctx, hookBashAsk) }()
	var ref string
	for ref == "" {
		var list struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		if code := f.h.reqInto(http.MethodGet, "/v1/m/governance/approvals?status=pending", f.h.adminToken, f.h.tenantA, nil, &list); code != http.StatusOK {
			f.t.Fatalf("approvals=%d", code)
		}
		if len(list.Items) > 0 {
			ref = list.Items[0].ID
			break
		}
		select {
		case <-done:
			f.t.Fatal("the hook ended without queueing a review")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if outcome == "cancel" {
		cancel()
	} else if code, _ := f.h.req(http.MethodPost, "/v1/m/governance/approvals/"+ref+"/decisions", f.h.adminToken, f.h.tenantA, map[string]any{"decision": outcome}); code != http.StatusCreated && code != http.StatusOK {
		f.t.Fatalf("decision=%d", code)
	}
	<-done
	return decision, reason
}

func (f *denialBudgetHook) runState() string {
	f.t.Helper()
	var run map[string]any
	if code := f.h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+f.run, f.h.adminToken, f.h.tenantA, nil, &run); code != http.StatusOK {
		f.t.Fatalf("run=%d", code)
	}
	state, _ := run["state"].(string)
	return state
}

// Policy refusals count and allows count; a person's answer to a review does not, and
// neither does an event that gates no tool-call.
func TestSessionHookReportsOnlyPolicyDecisionsToTheDenialBudget(t *testing.T) {
	f := newDenialBudgetHook(t, sessions.DenialBudget{Consecutive: 50, Total: 50})
	ctx := t.Context()
	if d, r := f.fire(ctx, hookWriteDenied); d != "deny" || r != "writes are not allowed here" {
		t.Fatalf("write = %s %q", d, r)
	}
	if d, _ := f.fire(ctx, hookReadAllowed); d != "allow" {
		t.Fatalf("read = %s", d)
	}
	f.fire(ctx, `{"hook_event_name":"SessionStart","source":"startup"}`)
	f.fire(ctx, `{"hook_event_name":"PostToolUse","tool_name":"Read","tool_input":{"file_path":"/tmp/budget"},"tool_response":{"content":"x"}}`)
	f.mu.Lock()
	for i, err := range f.errs {
		if err != nil {
			t.Fatalf("the module refused counted decision %d: %v", i, err)
		}
	}
	f.mu.Unlock()
	got := f.counted()
	if len(got) != 2 || got[0] != (budgetCall{true, "writes are not allowed here"}) || got[1].denied {
		t.Fatalf("counted %+v, want a refusal then an allow (and nothing for lifecycle or output events)", got)
	}
	for _, outcome := range []string{"reject", "cancel"} {
		before := len(f.counted())
		if d, _ := f.askAnd(outcome); d != "deny" {
			t.Fatalf("%s = %s, want deny", outcome, d)
		}
		if got := f.counted(); len(got) != before {
			t.Fatalf("a %sed review was counted as a policy denial: %+v", outcome, got[before:])
		}
	}
	before := len(f.counted())
	if d, _ := f.askAnd("approve"); d != "allow" {
		t.Fatalf("approved review = %s", d)
	}
	if got := f.counted(); len(got) != before+1 || got[before].denied {
		t.Fatalf("an approved review is an allow: %+v", got[before:])
	}
}

// With the operator's budget of 3 in a row, two policy denials each followed by a person's
// rejected review never reach it; a third policy denial in a row does, and nobody is
// watching, so the run stops.
func TestSessionHookStopsAnUnwatchedRunAtTheTenantsDenialBudget(t *testing.T) {
	f := newDenialBudgetHook(t, sessions.DenialBudget{Consecutive: 3})
	ctx := t.Context()
	f.fire(ctx, hookWriteDenied)
	f.askAnd("reject")
	f.fire(ctx, hookWriteDenied)
	f.askAnd("reject")
	time.Sleep(200 * time.Millisecond)
	if state := f.runState(); state != "running" {
		t.Fatalf("run is %s after 2 policy denials and 2 rejected reviews, want running", state)
	}
	f.fire(ctx, hookWriteDenied)
	deadline := time.Now().Add(5 * time.Second)
	for f.runState() != "stopped" {
		if time.Now().After(deadline) {
			t.Fatal("the run was not stopped after 3 policy denials in a row")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// buildClaudeHookPEPServer is the only place the engine's sessions module reaches the hook
// PEP's denial budget: left unset there, no session would ever be handed to a person and
// nothing else would fail. So this drives the server the builder returns: a live policy
// forbid refuses three tool-calls in a row and, with nobody watching, the run stops.
func TestClaudeHookPEPServerWiresTheDenialBudget(t *testing.T) {
	fixture := newHookSecretTestRun(t, hookSecretCanary, func(auth.PolicyEvaluator) auth.PolicyEvaluator {
		return hookSecretEvaluator(func(context.Context, auth.Request) (auth.Decision, error) {
			return auth.Decision{Allow: false, Reason: "test forbids reads", Class: auth.ClassPolicy}, nil
		})
	})
	call := func() string {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Read", "tool_input": map[string]any{"file_path": "/tmp/budget"}})
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+fixture.token)
		rec := httptest.NewRecorder()
		fixture.server.Handler.ServeHTTP(rec, req)
		var out struct {
			HookSpecificOutput struct {
				Decision string `json:"permissionDecision"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("response not JSON: %q", rec.Body.String())
		}
		return out.HookSpecificOutput.Decision
	}
	state := func() string {
		var run map[string]any
		if code := fixture.h.reqInto(http.MethodGet, "/v1/m/sessions/runs/"+fixture.runRef, fixture.h.adminToken, fixture.h.tenantA, nil, &run); code != http.StatusOK {
			t.Fatalf("run=%d", code)
		}
		s, _ := run["state"].(string)
		return s
	}
	for i := 1; i <= 2; i++ {
		if d := call(); d != "deny" {
			t.Fatalf("call %d = %s, want deny", i, d)
		}
	}
	if s := state(); s != "running" {
		t.Fatalf("run is %s after 2 denials, want running", s)
	}
	if d := call(); d != "deny" {
		t.Fatalf("call 3 = %s, want deny", d)
	}
	deadline := time.Now().Add(5 * time.Second)
	for state() != "stopped" {
		if time.Now().After(deadline) {
			t.Fatal("the built hook server did not hand the run to a person after 3 policy denials")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
