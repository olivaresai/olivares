// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// The agent a governed hook request acts as is the agent its CREDENTIAL proves. The
// X-Olivares-Hook-Agent header is the hook client's hint: it never selects an agent-scoped
// firewall policy, and when the credential proves no agent (an API token without an agent
// binding, a human session) the firewall is told so (UnbindableAgent), with or without the
// hint, instead of being handed the hint as the actor.

// exemptHookAgent is the one agent the stand-in firewall policy exempts.
const exemptHookAgent = "ci-bot"

// exemptingHookInspector stands in for a hook firewall configured with one agent-scoped
// exemption: the exempted agent's tool-calls forward, every other actor is denied, and a
// credential that cannot bind an agent is refused before any policy is chosen. It records
// the input it was handed, so a test sees which actor the decider named.
type exemptingHookInspector struct {
	exempt string
	got    claudeapi.ContentInspectionInput
	calls  int
}

func (f *exemptingHookInspector) Inspect(_ context.Context, in claudeapi.ContentInspectionInput) claudeapi.ContentInspectionDecision {
	f.calls++
	f.got = in
	deny := func(reason string) claudeapi.ContentInspectionDecision {
		return claudeapi.ContentInspectionDecision{Status: http.StatusForbidden, ErrorType: "permission_error", Reason: reason}
	}
	switch {
	case in.UnbindableAgent:
		return deny("agent-scoped test policy cannot bind to this API token")
	case in.ActorRef == f.exempt:
		return claudeapi.ContentInspectionDecision{Forward: true}
	default:
		return deny("blocked by test hook firewall")
	}
}

// hookAgentFixture drives the real HookPEP and decider over an in-memory signed ledger; the
// test chooses what the bearer credential proves.
type hookAgentFixture struct {
	*hookLedgerFixture
	pep  *claude.HookPEP
	insp *exemptingHookInspector
	logs *bytes.Buffer
}

// newHookAgentFixture builds the fixture with an allow-default policy and the stand-in
// firewall. agent is the agent the API token binds; "" is a token that binds none.
func newHookAgentFixture(t *testing.T, agent string) *hookAgentFixture {
	t.Helper()
	lf := newHookLedgerFixture(t, hookPolicyDoc{Default: "allow"})
	principal := auth.ScopedPrincipal(model.NewID(), "hook agent token", lf.tenant, auth.RoleEditor)
	if agent != "" {
		principal = principal.WithAgentIdentity(agent)
	}
	insp := &exemptingHookInspector{exempt: exemptHookAgent}
	logs := &bytes.Buffer{}
	lf.dec.authr = hookLedgerAuthenticator{principal: principal}
	lf.dec.hookInspector = insp
	lf.dec.clock = time.Now
	lf.dec.log = slog.New(slog.NewTextHandler(logs, nil))
	return &hookAgentFixture{
		hookLedgerFixture: lf,
		pep:               claude.NewHookPEP(lf.dec, nil, time.Now),
		insp:              insp,
		logs:              logs,
	}
}

// hookAgentBearer is the bearer the fixture sends; the stub authenticator ignores its value,
// and the tests check it never reaches a log line.
const hookAgentBearer = "olv_hook_bearer_never_logged"

// call posts one PreToolUse Bash call that carries a secret and a destructive command,
// declaring the given agent (no header when empty).
func (f *hookAgentFixture) call(t *testing.T, declared string) (decision, reason string) {
	t.Helper()
	return hookCallDeclaringAgent(t, f.pep, hookAgentBearer, f.tenant.String(), declared)
}

func hookCallDeclaringAgent(t *testing.T, pep http.Handler, bearer, tenant, declared string) (decision, reason string) {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"session_id": "sess-hook-agent", "hook_event_name": "PreToolUse",
		"tool_name": "Bash", "tool_use_id": "tu-" + model.NewID().String(),
		"tool_input": map[string]any{"command": "export K=AKIAIOSFODNN7EXAMPLE; rm -rf /srv/data"},
	})
	if err != nil {
		t.Fatalf("marshal hook payload: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("X-Olivares-Hook-Tenant", tenant)
	if declared != "" {
		req.Header.Set("X-Olivares-Hook-Agent", declared)
	}
	rec := httptest.NewRecorder()
	pep.ServeHTTP(rec, req)
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("response not JSON: %q", rec.Body.String())
	}
	if hso, ok := m["hookSpecificOutput"].(map[string]any); ok {
		m = hso
	}
	return decisionOf(m), reasonOf(m)
}

// A credential that proves agent-a and declares the exempted agent is inspected as agent-a:
// the header does not borrow the exemption, and the dangerous call is denied.
func TestHookFirewall_DeclaredAgentDoesNotSelectThePolicy(t *testing.T) {
	f := newHookAgentFixture(t, "agent-a")
	decision, reason := f.call(t, exemptHookAgent)
	if f.insp.calls != 1 {
		t.Fatalf("the firewall must inspect the call once; got %d", f.insp.calls)
	}
	if f.insp.got.ActorRef != "agent-a" || f.insp.got.UnbindableAgent {
		t.Fatalf("firewall actor = %q unbindable=%v; want the credential's agent %q, bindable",
			f.insp.got.ActorRef, f.insp.got.UnbindableAgent, "agent-a")
	}
	if decision != claude.DecisionDeny {
		t.Fatalf("agent-a declaring %q must not inherit its exemption; got %q (%s)", exemptHookAgent, decision, reason)
	}
}

// The contradiction is visible to the operator: one log line names both agents and carries
// neither the bearer nor the tool input.
func TestHookFirewall_ContradictedAgentHeaderIsLogged(t *testing.T) {
	f := newHookAgentFixture(t, "agent-a")
	f.call(t, exemptHookAgent)
	logs := f.logs.String()
	if strings.Count(logs, "declared_agent=") != 1 ||
		!strings.Contains(logs, "declared_agent="+exemptHookAgent) || !strings.Contains(logs, " agent=agent-a") {
		t.Fatalf("the contradicted header must be logged once with both agents; got %q", logs)
	}
	for _, secret := range []string{hookAgentBearer, "AKIAIOSFODNN7EXAMPLE"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("the mismatch log must not carry %q; got %q", secret, logs)
		}
	}
}

// The exempted agent keeps its exemption when its OWN credential proves it, whether its hook
// client declares it or not; an agreeing or absent hint is not a mismatch.
func TestHookFirewall_ExemptAgentKeepsItsExemptionWithItsOwnCredential(t *testing.T) {
	for _, declared := range []string{exemptHookAgent, ""} {
		f := newHookAgentFixture(t, exemptHookAgent)
		if decision, reason := f.call(t, declared); decision != claude.DecisionAllow {
			t.Fatalf("declared %q: the exempted agent's own credential must keep its exemption; got %q (%s)",
				declared, decision, reason)
		}
		if strings.Contains(f.logs.String(), "declared_agent=") {
			t.Fatalf("declared %q: an agreeing or absent hint must not be logged as a mismatch; got %q",
				declared, f.logs.String())
		}
	}
}

// An API token that binds no agent cannot be matched to an agent-scoped policy: the firewall
// is told the agent is unbindable, and the declared hint is not handed over as the actor.
func TestHookFirewall_TokenWithoutAgentBindingIsUnbindable(t *testing.T) {
	f := newHookAgentFixture(t, "")
	decision, reason := f.call(t, exemptHookAgent)
	if !f.insp.got.UnbindableAgent || f.insp.got.ActorRef != "" {
		t.Fatalf("firewall actor = %q unbindable=%v; want no actor and unbindable",
			f.insp.got.ActorRef, f.insp.got.UnbindableAgent)
	}
	if decision != claude.DecisionDeny {
		t.Fatalf("an unbindable token declaring %q must be denied; got %q (%s)", exemptHookAgent, decision, reason)
	}
}

// A human session proves no agent, and every hook request is an agent's tool-call: the
// firewall is told the agent is unbindable whether or not the hook client declares one, and a
// declared agent is never handed over as the actor.
func TestHookFirewall_HumanSessionDeclaringAnAgentIsUnbindable(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "declares-another-agent@e2e.test")
	fx := newHookPEPFixture(t, h, hookPolicyDoc{Default: "allow"}, true, fixedEval{allow: true}, false)
	insp := &exemptingHookInspector{exempt: exemptHookAgent}
	fx.dec.hookInspector = insp

	for _, declared := range []string{"", exemptHookAgent} {
		decision, reason := hookCallDeclaringAgent(t, fx.pep, tok, h.tenantA, declared)
		if insp.got.ActorRef != "" || !insp.got.UnbindableAgent {
			t.Fatalf("declared %q: firewall actor = %q unbindable=%v; want no actor, unbindable",
				declared, insp.got.ActorRef, insp.got.UnbindableAgent)
		}
		if decision != claude.DecisionDeny {
			t.Fatalf("declared %q: a session must not inherit %q's exemption; got %q (%s)",
				declared, exemptHookAgent, decision, reason)
		}
	}
}

// strictAgentHookInspector stands in for a hook firewall whose tenant-wide policy PERMITS every
// call while one agent's agent-scoped policy is stricter and denies it. Like the commercial
// firewall, it refuses a request whose agent cannot be bound before it chooses any policy. It
// records every input it is handed.
type strictAgentHookInspector struct {
	strict string
	got    []claudeapi.ContentInspectionInput
}

// reasonStrictAgentUnbindable is the stand-in's binding refusal.
const reasonStrictAgentUnbindable = "agent-scoped test policy cannot bind to this credential"

func (f *strictAgentHookInspector) Inspect(_ context.Context, in claudeapi.ContentInspectionInput) claudeapi.ContentInspectionDecision {
	f.got = append(f.got, in)
	deny := func(reason string) claudeapi.ContentInspectionDecision {
		return claudeapi.ContentInspectionDecision{Status: http.StatusForbidden, ErrorType: "permission_error", Reason: reason}
	}
	switch {
	case in.UnbindableAgent:
		return deny(reasonStrictAgentUnbindable)
	case in.ActorRef == f.strict:
		return deny("blocked by the stricter agent-scoped test policy")
	default:
		return claudeapi.ContentInspectionDecision{Forward: true} // the permissive tenant policy
	}
}

// Whether a human session's request can be matched to an agent-scoped policy is decided by the
// credential, not by the optional header. The stand-in firewall's tenant policy permits the
// call and only agent-a's policy is stricter, so a session that were NOT flagged unbindable
// would be allowed by the permissive tenant policy. With the hint and without it, the session
// is refused for the binding. The two agent tokens show what the stand-in's policies do with
// this very call: a proven agent without a policy of its own is allowed by the tenant policy,
// agent-a is denied by its own.
func TestHookFirewall_HumanSessionIsUnbindableWithOrWithoutTheHint(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "session-without-agent@e2e.test")
	fx := newHookPEPFixture(t, h, hookPolicyDoc{Default: "allow"}, true, fixedEval{allow: true}, false)
	insp := &strictAgentHookInspector{strict: "agent-a"}
	fx.dec.hookInspector = insp

	for _, declared := range []string{"agent-a", ""} {
		calls := len(insp.got)
		decision, reason := hookCallDeclaringAgent(t, fx.pep, tok, h.tenantA, declared)
		if len(insp.got) != calls+1 {
			t.Fatalf("declared %q: the firewall must inspect the session's call once; got %d", declared, len(insp.got)-calls)
		}
		if got := insp.got[calls]; got.ActorRef != "" || !got.UnbindableAgent {
			t.Fatalf("declared %q: firewall actor = %q unbindable=%v; want no actor, unbindable",
				declared, got.ActorRef, got.UnbindableAgent)
		}
		if decision != claude.DecisionDeny || reason != reasonStrictAgentUnbindable {
			t.Fatalf("declared %q: got %q (%s); want the binding refusal %q",
				declared, decision, reason, reasonStrictAgentUnbindable)
		}
	}

	for agent, want := range map[string]string{"agent-b": claude.DecisionAllow, "agent-a": claude.DecisionDeny} {
		fx.dec.authr = hookLedgerAuthenticator{principal: auth.ScopedPrincipal(model.NewID(), "hook agent token", fx.tenant, auth.RoleEditor).WithAgentIdentity(agent)}
		if decision, reason := hookCallDeclaringAgent(t, fx.pep, hookAgentBearer, h.tenantA, ""); decision != want {
			t.Fatalf("control %s: got %q (%s); want %q", agent, decision, reason, want)
		}
	}
}

// agentStopGuard reports one active agent-scoped emergency stop.
type agentStopGuard struct{ stopped string }

func (g agentStopGuard) KillSwitchState(context.Context, model.TenantID) (governance.StopState, error) {
	return governance.StopState{AgentRefs: map[string]model.ID{g.stopped: model.ID("stop-agent")}}, nil
}

// agentBlockingNHI reports one agent as blocked by its lifecycle.
type agentBlockingNHI struct{ blocked string }

func (n agentBlockingNHI) NHIEnforcementForAgentRef(_ context.Context, _ model.TenantID, agentRef string) (bool, string, error) {
	return agentRef == n.blocked, "offboarded", nil
}

// The checks that can only add a denial — the agent-scoped emergency stop and the NHI
// lifecycle block — consult the credential's agent whatever the header says, and fall back to
// the declared hint only when the credential binds no agent (naming an agent there can add a
// deny, never remove one).
func TestHookRestrictOnlyChecksFollowTheProvenAgentElseTheHint(t *testing.T) {
	cases := []struct {
		name, tokenAgent, declared, want string
	}{
		{"stop: proven agent declares another", "agent-a", "agent-b", "emergency stop active"},
		{"stop: proven agent declares none", "agent-a", "", "emergency stop active"},
		{"stop: unbound token declares the stopped agent", "", "agent-a", "emergency stop active"},
		{"nhi: proven agent declares another", "agent-a", "agent-b", "NHI blocked"},
		{"nhi: unbound token declares the blocked agent", "", "agent-a", "NHI blocked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newHookAgentFixture(t, tc.tokenAgent)
			f.dec.hookInspector = nil
			if strings.HasPrefix(tc.name, "stop:") {
				f.dec.stops = agentStopGuard{stopped: "agent-a"}
			} else {
				rt := f.dec.tenants[f.tenant]
				rt.enforceNHI = true
				f.dec.tenants[f.tenant] = rt
				f.dec.nhiEnforcer = agentBlockingNHI{blocked: "agent-a"}
			}
			decision, reason := f.call(t, tc.declared)
			if decision != claude.DecisionDeny || !strings.Contains(reason, tc.want) {
				t.Fatalf("got %q (%s); want deny naming %q", decision, reason, tc.want)
			}
		})
	}
	t.Run("a proven agent is not denied by another agent's stop it declares", func(t *testing.T) {
		f := newHookAgentFixture(t, "agent-b")
		f.dec.hookInspector = nil
		f.dec.stops = agentStopGuard{stopped: "agent-a"}
		if decision, reason := f.call(t, "agent-a"); decision != claude.DecisionAllow {
			t.Fatalf("agent-b is not under agent-a's stop; got %q (%s)", decision, reason)
		}
	})
}
