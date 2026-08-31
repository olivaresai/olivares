// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// errTestStop is a synthetic stop-gate failure for the fail-closed test.
var errTestStop = errors.New("synthetic kill-switch outage")

// fakeStopGate is a programmable StopGate for the kill-switch tests. With
// agentRef set it stops ONLY a fire whose StopDims carry that agent (the
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

// TestFireKillSwitchEstateBlocksBothPhases proves an estate-wide emergency stop
// freezes BOTH fire phases with HTTP 423: no new fire request queues (phase 1)
// and no already-approved fire dispatches (phase 2). Each denial is recorded in
// the append-only ledger with op_status blocked and audited, and the dispatcher
// is never reached.
func TestFireKillSwitchEstateBlocksBothPhases(t *testing.T) {
	fired := false
	h, _ := newHarness(t,
		WithApprovalGate(fakeGate{status: StatusApproved}),
		WithDispatcher(recordingDispatcher{fired: &fired}),
		WithStopGate(fakeStopGate{decision: StopDecision{Stopped: true, StopRef: "stop-1", Scope: "estate"}}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	id := h.createSchedule(tok, tenant, "nightly", "agent", "batch-agent", "cron", "0 0 * * *", 0)

	// Phase 1 (no approval_ref): the stop outranks even asking for an approval.
	r1 := h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok, nil, tenantHdr(tenant))
	if r1.code != http.StatusLocked || r1.body["op"] != opFireRequest || r1.body["op_status"] != opStatusBlocked {
		t.Fatalf("phase 1 under estate stop = %d %s, want 423 %s/%s", r1.code, r1.raw, opFireRequest, opStatusBlocked)
	}
	// Phase 2 (approval_ref present): an approved fire still does not dispatch.
	r2 := h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok, map[string]any{"approval_ref": "appr-1"}, tenantHdr(tenant))
	if r2.code != http.StatusLocked || r2.body["op"] != opFire || r2.body["op_status"] != opStatusBlocked {
		t.Fatalf("phase 2 under estate stop = %d %s, want 423 %s/%s", r2.code, r2.raw, opFire, opStatusBlocked)
	}
	if fired {
		t.Fatal("the dispatcher must NEVER be reached while an estate stop is active")
	}
	// Both denials land in the append-only decision ledger with op_status blocked.
	dr := h.do("GET", "/v1/m/orchestration/schedules/"+id+"/decisions", tok, nil, tenantHdr(tenant))
	var ledger listResponse[decisionDTO]
	_ = json.Unmarshal([]byte(dr.raw), &ledger)
	if row := findDecision(ledger.Items, opFireRequest); row == nil || row.OpStatus != opStatusBlocked {
		t.Fatalf("ledger must record a blocked %s, got %s", opFireRequest, dr.raw)
	}
	if row := findDecision(ledger.Items, opFire); row == nil || row.OpStatus != opStatusBlocked {
		t.Fatalf("ledger must record a blocked %s, got %s", opFire, dr.raw)
	}
	// The denial is also emitted to the tamper-evident audit ledger.
	ar := h.do("GET", "/v1/audit", tok, nil, tenantHdr(tenant))
	if ar.code != http.StatusOK || !strings.Contains(ar.raw, "orchestration.schedule.fire.killswitch_denied") {
		t.Fatalf("audit ledger must record the kill-switch denial, got %d %s", ar.code, ar.raw)
	}
}

// TestFireKillSwitchAgentScoped proves an agent-scoped stop freezes only a
// schedule whose subject IS that agent: another agent's approved fire proceeds
// to its normal dispatch path unchanged.
func TestFireKillSwitchAgentScoped(t *testing.T) {
	h, _ := newHarness(t,
		WithApprovalGate(fakeGate{status: StatusApproved}),
		WithDispatcher(fakeDispatcher{ref: "run-7"}),
		WithStopGate(fakeStopGate{agentRef: "frozen-bot", decision: StopDecision{Stopped: true, StopRef: "stop-2", Scope: "agent"}}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	frozen := h.createSchedule(tok, tenant, "frozen", "agent", "frozen-bot", "cron", "0 0 * * *", 0)
	free := h.createSchedule(tok, tenant, "free", "agent", "free-bot", "cron", "0 0 * * *", 0)

	if r := h.do("POST", "/v1/m/orchestration/schedules/"+frozen+"/fire", tok, map[string]any{"approval_ref": "appr-1"}, tenantHdr(tenant)); r.code != http.StatusLocked || r.body["op_status"] != opStatusBlocked {
		t.Fatalf("stopped agent's fire = %d %s, want 423/blocked", r.code, r.raw)
	}
	r := h.do("POST", "/v1/m/orchestration/schedules/"+free+"/fire", tok, map[string]any{"approval_ref": "appr-1"}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["op_status"] != opStatusDispatched || r.body["dispatch_ref"] != "run-7" {
		t.Fatalf("another agent's fire must proceed under an agent-scoped stop, got %d %s", r.code, r.raw)
	}
}

// TestFireKillSwitchGateErrorFailsClosed proves a stop-gate ERROR denies the
// fire with 503 (deny-closed — the exact inverse of the budget gate's fail-open
// posture): an unreadable stop state never means "go".
func TestFireKillSwitchGateErrorFailsClosed(t *testing.T) {
	fired := false
	h, _ := newHarness(t,
		WithApprovalGate(fakeGate{status: StatusApproved}),
		WithDispatcher(recordingDispatcher{fired: &fired}),
		WithStopGate(fakeStopGate{err: errTestStop}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	id := h.createSchedule(tok, tenant, "nightly", "agent", "batch-agent", "cron", "0 0 * * *", 0)

	if r := h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok, nil, tenantHdr(tenant)); r.code != http.StatusServiceUnavailable {
		t.Fatalf("phase 1 with a stop-gate error = %d %s, want 503 (deny-closed)", r.code, r.raw)
	}
	if r := h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok, map[string]any{"approval_ref": "appr-1"}, tenantHdr(tenant)); r.code != http.StatusServiceUnavailable {
		t.Fatalf("phase 2 with a stop-gate error = %d %s, want 503 (deny-closed)", r.code, r.raw)
	}
	if fired {
		t.Fatal("the dispatcher must NOT be reached when the stop state is unreadable")
	}
}

// TestFireKillSwitchNoStopUnchanged proves a wired gate reporting NO active stop
// leaves the existing two-phase flow untouched: phase 1 still requests an
// approval and an approved phase 2 still dispatches.
func TestFireKillSwitchNoStopUnchanged(t *testing.T) {
	h, _ := newHarness(t,
		WithApprovalGate(fakeGate{status: StatusApproved}),
		WithDispatcher(fakeDispatcher{ref: "run-9"}),
		WithStopGate(fakeStopGate{}), // wired, nothing stopped
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	id := h.createSchedule(tok, tenant, "nightly", "agent", "batch-agent", "cron", "0 0 * * *", 0)

	if r := h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok, nil, tenantHdr(tenant)); r.code != http.StatusAccepted || r.body["op_status"] != opStatusRequested {
		t.Fatalf("phase 1 with no stop = %d %s, want 202/requested (unchanged flow)", r.code, r.raw)
	}
	r := h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok, map[string]any{"approval_ref": "appr-1"}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["op_status"] != opStatusDispatched || r.body["dispatch_ref"] != "run-9" {
		t.Fatalf("phase 2 with no stop = %d %s, want 200/dispatched (unchanged flow)", r.code, r.raw)
	}
}
