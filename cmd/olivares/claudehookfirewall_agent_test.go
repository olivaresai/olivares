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
	"testing"

	"github.com/olivaresai/olivares/connectors/claude"
	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
)

// The agent a governed hook request acts as is the agent its CREDENTIAL proves. The
// X-Olivares-Hook-Agent header is the hook client's hint: it never selects an agent-scoped
// firewall policy, and when the credential proves no agent (an API token without an agent
// binding, a human session) the firewall is told so (UnbindableAgent), with or without the
// hint, instead of being handed the hint as the actor.

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

// A human session proves no agent, and every hook request is an agent's tool-call: the
// firewall is told the agent is unbindable whether or not the hook client declares one, and a
// declared agent is never handed over as the actor.
func TestHookFirewall_HumanSessionDeclaringAnAgentIsUnbindable(t *testing.T) {
	h := newHarness(t)
	tok := h.firmAgentToken(t, "declares-another-agent@e2e.test")
	fx := newHookPEPFixture(t, h, hookpep.PolicyDoc{Default: "allow"}, true, fixedEval{allow: true}, false)
	insp := &exemptingHookInspector{exempt: exemptHookAgent}
	fx.dec.Inspector = insp

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
	fx := newHookPEPFixture(t, h, hookpep.PolicyDoc{Default: "allow"}, true, fixedEval{allow: true}, false)
	insp := &strictAgentHookInspector{strict: "agent-a"}
	fx.dec.Inspector = insp

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
		fx.dec.Authr = hookLedgerAuthenticator{principal: auth.ScopedPrincipal(model.NewID(), "hook agent token", fx.tenant, auth.RoleEditor).WithAgentIdentity(agent)}
		if decision, reason := hookCallDeclaringAgent(t, fx.pep, hookAgentBearer, h.tenantA, ""); decision != want {
			t.Fatalf("control %s: got %q (%s); want %q", agent, decision, reason, want)
		}
	}
}

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

// hookAgentBearer is the bearer the fixture sends; the stub authenticator ignores its value,
// and the tests check it never reaches a log line.
const hookAgentBearer = "olv_hook_bearer_never_logged"
