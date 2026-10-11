// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/olivaresai/olivares/modules/sessions/hookpep"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// countedStops counts every stop-state read one tool-call makes, through
// either the bearer's stop history or the PEP's live kill-switch state.
type countedStops struct {
	gov   *governance.Module
	reads atomic.Int32
}

func (c *countedStops) KillSwitchState(ctx context.Context, tenant model.TenantID) (governance.StopState, error) {
	c.reads.Add(1)
	return c.gov.KillSwitchState(ctx, tenant)
}

func (c *countedStops) SessionStopEpoch(ctx context.Context, tenant model.TenantID, agentRef string) (string, error) {
	c.reads.Add(1)
	return c.gov.SessionStopEpoch(ctx, tenant, agentRef)
}

// stopSessionFixture is a launched session behind the production hook route,
// its stop reads counted.
type stopSessionFixture struct {
	h      *harness
	tenant model.TenantID
	stops  *countedStops
	call   func(event, agentHint string) (decision, reason, raw string)
}

func newStopSessionFixture(t *testing.T, run, agentRef string) *stopSessionFixture {
	t.Helper()
	h := newHarness(t)
	ctx := context.Background()
	tenant := model.TenantID(h.tenantA)
	p, err := h.authr.Authenticate(ctx, h.adminToken)
	if err != nil {
		t.Fatal(err)
	}
	stops := &countedStops{gov: h.set.gov}
	c := newSessionHookCredentials(h.authr, h.st, h.set.sessions, stops)
	intent := claimHookTestSession(t, h, p, tenant, run)
	intent.AgentRef = agentRef
	intent.LauncherPrincipal = p
	token, err := c.mint(ctx, tenant, intent)
	if err != nil {
		t.Fatal(err)
	}
	dec := newClaudeHookDecider(&hookpep.Decider{Tenants: map[model.TenantID]hookpep.ResolvedTenant{}, DefaultPolicy: &hookpep.PolicyDoc{Default: "allow"}, Authr: c, Eval: h.set.gov.Evaluator(), Authz: harnessAuthz(h), Scoped: h.set.gov.ScopedGrants(), Store: h.st, Stops: stops, StopDeny: newStopDenyRecorder(h.st, discardLog()).record, Clock: time.Now, Log: discardLog()})
	server := httptest.NewServer(dec.Handler(nil))
	t.Cleanup(server.Close)
	call := func(event, agentHint string) (string, string, string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"hook_event_name": event, "session_id": "vendor-session", "tool_name": "Read", "tool_use_id": model.NewID().String(), "tool_input": map[string]any{"file_path": "/tmp/fixture"}})
		var out bytes.Buffer
		if err := claude.RunHookClient(ctx, bytes.NewReader(body), &out, claude.HookClientConfig{Endpoint: server.URL, Token: token, Tenant: tenant.String(), Agent: agentHint}); err != nil {
			t.Fatal(err)
		}
		var wire struct {
			HookSpecificOutput struct {
				Decision string `json:"permissionDecision"`
				Reason   string `json:"permissionDecisionReason"`
			} `json:"hookSpecificOutput"`
		}
		_ = json.Unmarshal(out.Bytes(), &wire) // a non-gating event has no permission output
		return wire.HookSpecificOutput.Decision, wire.HookSpecificOutput.Reason, out.String()
	}
	return &stopSessionFixture{h: h, tenant: tenant, stops: stops, call: call}
}

// engage starts a stop and returns its id and the ledger position after it.
func (f *stopSessionFixture) engage(t *testing.T, kind, ref string) (model.ID, int64) {
	t.Helper()
	code, raw := f.h.req("POST", "/v1/m/governance/killswitch", f.h.adminToken, f.h.tenantA, map[string]any{"scope_kind": kind, "scope_ref": ref, "reason": "stop reason proof"})
	if code != 201 {
		t.Fatalf("engage: %d %s", code, raw)
	}
	var stop struct {
		ID model.ID `json:"id"`
	}
	if err := json.Unmarshal(raw, &stop); err != nil {
		t.Fatal(err)
	}
	from := int64(0)
	if events := canonicalLedgerEventsFrom(t, f.h.st, f.tenant, 0); len(events) > 0 {
		from = events[len(events)-1].event.Seq + 1
	}
	return stop.ID, from
}

func (f *stopSessionFixture) evidence(t *testing.T, stopID model.ID, from int64) int {
	t.Helper()
	n := 0
	for _, ev := range canonicalLedgerEventsFrom(t, f.h.st, f.tenant, from) {
		if ev.event.Action == "security.killswitch.deny" && ev.event.TargetID == stopID {
			n++
		}
	}
	return n
}

func stopReasonFor(id model.ID) string {
	return "emergency stop active (kill switch " + id.String() + ")"
}

func TestSessionHookStopDeniesWithStopReasonAndEvidence(t *testing.T) {
	f := newStopSessionFixture(t, "stop-reason-run", "stop-reason-agent")
	// The bearer's resolve reads the stop history before deciding, and again
	// after an allow (a stop during an approval wait must still refuse). The
	// decision itself must not read it a third time.
	f.stops.reads.Store(0)
	if got, reason, _ := f.call("PreToolUse", ""); got != "allow" {
		t.Fatalf("live session Read: %s (%s)", got, reason)
	}
	if got := f.stops.reads.Load(); got != 2 {
		t.Fatalf("stop-state reads per allowed session tool-call: got %d, want 2", got)
	}
	stopID, from := f.engage(t, "agent", "stop-reason-agent")
	// The first call meets the live credential, the second the generation that
	// refusal revoked: both name the stop, never a lost membership.
	for i := range 2 {
		got, reason, _ := f.call("PreToolUse", "")
		if got != "deny" || !strings.Contains(reason, stopReasonFor(stopID)) {
			t.Fatalf("stopped session call %d: %s (%s)", i+1, got, reason)
		}
	}
	if got := f.evidence(t, stopID, from); got != 1 {
		t.Fatalf("stop deny evidence for %s: got %d events, want 1 (throttled)", stopID, got)
	}
}

func TestSessionHookStopScopes(t *testing.T) {
	for _, tc := range []struct {
		name, agentRef, hint, event, stopKind, stopRef string
		denied                                         bool
		evidence                                       int
	}{
		// The bearer's stop history covers the estate for every session.
		{name: "estate stop", agentRef: "scoped-agent", event: "PreToolUse", stopKind: "estate", denied: true, evidence: 1},
		// A credential that proves no agent: only the live gate sees the hinted agent's stop.
		{name: "hinted agent of an unbound session", hint: "hinted-agent", event: "PreToolUse", stopKind: "agent", stopRef: "hinted-agent", denied: true, evidence: 1},
		{name: "another agent's stop", agentRef: "scoped-agent", event: "PreToolUse", stopKind: "agent", stopRef: "another-agent"},
		// A non-gating event never spends the stop path or its evidence.
		{name: "non-gating event", agentRef: "scoped-agent", event: "Stop", stopKind: "agent", stopRef: "scoped-agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStopSessionFixture(t, "stop-scope-"+strings.ReplaceAll(tc.name, " ", "-"), tc.agentRef)
			stopID, from := f.engage(t, tc.stopKind, tc.stopRef)
			got, reason, raw := f.call(tc.event, tc.hint)
			named := strings.Contains(raw, stopReasonFor(stopID))
			if tc.denied && (got != "deny" || !strings.Contains(reason, stopReasonFor(stopID))) {
				t.Fatalf("want the stop deny, got %s (%s)", got, reason)
			}
			if !tc.denied && (named || (tc.event == "PreToolUse" && got != "allow")) {
				t.Fatalf("want no stop deny, got %q", raw)
			}
			if n := f.evidence(t, stopID, from); n != tc.evidence {
				t.Fatalf("stop deny evidence: got %d, want %d", n, tc.evidence)
			}
		})
	}
}
