// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// errTestStop is a synthetic stop-gate failure for the fail-closed test.
var errTestStop = errors.New("synthetic kill-switch outage")

// fakeStopGate is a programmable StopGate for the kill-switch tests. With
// agentRef set it stops ONLY an open whose StopDims carry that agent (the
// agent-scoped graduation); empty agentRef applies the decision to everything.
type fakeStopGate struct {
	decision StopDecision
	err      error
	agentRef string
}

func (g fakeStopGate) Check(_ context.Context, _ model.TenantID, dims StopDims) (StopDecision, error) {
	if g.err != nil {
		return StopDecision{}, g.err
	}
	if g.agentRef != "" && dims.AgentRef != g.agentRef {
		return StopDecision{}, nil
	}
	return g.decision, nil
}

// findVoiceDecision returns the first ledger row with the given op, or nil.
func findVoiceDecision(items []decisionDTO, op string) *decisionDTO {
	for i := range items {
		if items[i].Op == op {
			return &items[i]
		}
	}
	return nil
}

// TestOpenKillSwitchEstateBlocksBothPhases proves an estate-wide emergency stop
// freezes BOTH open phases with HTTP 423: no new approval request queues
// (phase 1) and no already-approved open dispatches (phase 2). Each denial is
// recorded in the append-only ledger with op_status blocked, and the dispatcher
// is never reached.
func TestOpenKillSwitchEstateBlocksBothPhases(t *testing.T) {
	opened := false
	h, _ := newHarness(t,
		WithApprovalGate(fakeGate{status: StatusApproved}),
		WithDispatcher(recordingDispatcher{opened: &opened}),
		WithStopGate(fakeStopGate{decision: StopDecision{Stopped: true, StopRef: "stop-1", Scope: "estate"}}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	h.setPolicy(tok, tenant, "voice-agent", "gpt-realtime", "openai", 0)

	// Phase 1 (no approval_ref): the stop outranks even asking for an approval.
	r1 := h.open(tok, tenant, "s1", "voice-agent", "gpt-realtime", "openai", "")
	if r1.code != http.StatusLocked || r1.body["op"] != opOpenRequest || r1.body["op_status"] != opStatusBlocked {
		t.Fatalf("phase 1 under estate stop = %d %s, want 423 %s/%s", r1.code, r1.raw, opOpenRequest, opStatusBlocked)
	}
	// Phase 2 (approval_ref present): an approved open still does not dispatch.
	r2 := h.open(tok, tenant, "s1", "voice-agent", "gpt-realtime", "openai", "appr-1")
	if r2.code != http.StatusLocked || r2.body["op"] != opOpen || r2.body["op_status"] != opStatusBlocked {
		t.Fatalf("phase 2 under estate stop = %d %s, want 423 %s/%s", r2.code, r2.raw, opOpen, opStatusBlocked)
	}
	if opened {
		t.Fatal("the dispatcher must NEVER be reached while an estate stop is active")
	}
	// Both denials land in the append-only decision ledger with op_status blocked.
	dr := h.do("GET", "/v1/m/voice/decisions", tok, nil, tenantHdr(tenant))
	var ledger listResponse[decisionDTO]
	_ = json.Unmarshal([]byte(dr.raw), &ledger)
	if row := findVoiceDecision(ledger.Items, opOpenRequest); row == nil || row.OpStatus != opStatusBlocked {
		t.Fatalf("ledger must record a blocked %s, got %s", opOpenRequest, dr.raw)
	}
	if row := findVoiceDecision(ledger.Items, opOpen); row == nil || row.OpStatus != opStatusBlocked {
		t.Fatalf("ledger must record a blocked %s, got %s", opOpen, dr.raw)
	}
}

// TestOpenKillSwitchAgentScoped proves an agent-scoped stop freezes only an
// open for THAT agent: another agent's approved open proceeds to its normal
// dispatch path unchanged.
func TestOpenKillSwitchAgentScoped(t *testing.T) {
	h, _ := newHarness(t,
		WithApprovalGate(fakeGate{status: StatusApproved}),
		WithDispatcher(fakeDispatcher{ref: "rt-7"}),
		WithStopGate(fakeStopGate{agentRef: "frozen-agent", decision: StopDecision{Stopped: true, StopRef: "stop-2", Scope: "agent"}}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	h.setPolicy(tok, tenant, "frozen-agent", "gpt-realtime", "openai", 0)
	h.setPolicy(tok, tenant, "voice-agent", "gpt-realtime", "openai", 0)

	if r := h.open(tok, tenant, "s1", "frozen-agent", "gpt-realtime", "openai", "appr-1"); r.code != http.StatusLocked || r.body["op_status"] != opStatusBlocked {
		t.Fatalf("stopped agent's open = %d %s, want 423/blocked", r.code, r.raw)
	}
	r := h.open(tok, tenant, "s2", "voice-agent", "gpt-realtime", "openai", "appr-1")
	if r.code != http.StatusOK || r.body["op_status"] != opStatusDispatched || r.body["dispatch_ref"] != "rt-7" {
		t.Fatalf("another agent's open must proceed under an agent-scoped stop, got %d %s", r.code, r.raw)
	}
}

// TestOpenKillSwitchGateErrorFailsClosed proves a stop-gate ERROR denies the
// open with 503 (deny-closed — the inverse of the budget gate's fail-open
// posture): an unreadable stop state never means "go".
func TestOpenKillSwitchGateErrorFailsClosed(t *testing.T) {
	opened := false
	h, _ := newHarness(t,
		WithApprovalGate(fakeGate{status: StatusApproved}),
		WithDispatcher(recordingDispatcher{opened: &opened}),
		WithStopGate(fakeStopGate{err: errTestStop}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	h.setPolicy(tok, tenant, "voice-agent", "gpt-realtime", "openai", 0)

	if r := h.open(tok, tenant, "s1", "voice-agent", "gpt-realtime", "openai", ""); r.code != http.StatusServiceUnavailable {
		t.Fatalf("phase 1 with a stop-gate error = %d %s, want 503 (deny-closed)", r.code, r.raw)
	}
	if r := h.open(tok, tenant, "s1", "voice-agent", "gpt-realtime", "openai", "appr-1"); r.code != http.StatusServiceUnavailable {
		t.Fatalf("phase 2 with a stop-gate error = %d %s, want 503 (deny-closed)", r.code, r.raw)
	}
	if opened {
		t.Fatal("the dispatcher must NOT be reached when the stop state is unreadable")
	}
}

// TestOpenKillSwitchNoStopUnchanged proves a wired gate reporting NO active
// stop leaves the existing two-phase flow untouched: phase 1 still requests an
// approval and an approved phase 2 still dispatches.
func TestOpenKillSwitchNoStopUnchanged(t *testing.T) {
	h, _ := newHarness(t,
		WithApprovalGate(fakeGate{status: StatusApproved}),
		WithDispatcher(fakeDispatcher{ref: "rt-9"}),
		WithStopGate(fakeStopGate{}), // wired, nothing stopped
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	h.setPolicy(tok, tenant, "voice-agent", "gpt-realtime", "openai", 0)

	if r := h.open(tok, tenant, "s1", "voice-agent", "gpt-realtime", "openai", ""); r.code != http.StatusAccepted || r.body["op_status"] != opStatusRequested {
		t.Fatalf("phase 1 with no stop = %d %s, want 202/requested (unchanged flow)", r.code, r.raw)
	}
	r := h.open(tok, tenant, "s1", "voice-agent", "gpt-realtime", "openai", "appr-1")
	if r.code != http.StatusOK || r.body["op_status"] != opStatusDispatched || r.body["dispatch_ref"] != "rt-9" {
		t.Fatalf("phase 2 with no stop = %d %s, want 200/dispatched (unchanged flow)", r.code, r.raw)
	}
}
