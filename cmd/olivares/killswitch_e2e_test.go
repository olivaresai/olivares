// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

// killswitch_e2e_test.go is the proof at the composition root, against the
// REAL engine the binary boots: the one-click estate stop bites at the hooks
// PEP and the MCP gateway seams, outranks an ACTIVE break-glass grant, leaves
// throttled tamper-evident deny evidence, is lifted only by the
// dual-control re-enable (two distinct AAL3 humans), and exports an evidence
// pack whose timeline carries the whole story on a verified chain.

// fakeMCPUpstream counts forwards so the kill-switch wrap can prove it never
// reached the backend.
type fakeMCPUpstream struct{ calls int }

func (f *fakeMCPUpstream) Forward(context.Context, mcpc.UpstreamRequest) (mcpc.UpstreamResult, error) {
	f.calls++
	return mcpc.UpstreamResult{Result: json.RawMessage(`{"ok":true}`), State: mcpc.DispatchCompleted}, nil
}

func TestKillSwitchEstateStoryE2E(t *testing.T) {
	h := newHarness(t)
	tid := tenantAID(t, h)
	ctx := context.Background()

	// A governed PEP with an allow-default policy: before the stop, tool-calls flow.
	fix := newHookPEPFixture(t, h, hookPolicyDoc{Default: "allow"}, false, nil, false)
	fix.dec.stops = h.set.gov
	fix.dec.stopRec = newStopDenyRecorder(h.st, discardLog())

	out := fix.call(t, "Bash", map[string]any{"command": "ls"}, h.adminToken, h.tenantA)
	if out["permissionDecision"] != "allow" {
		t.Fatalf("pre-stop PEP = %v", out)
	}

	// An ACTIVE break-glass grant covering EVERY action is the strongest
	// emergency credential the plane has — the stop must outrank it.
	grant := h.activateBreakGlassE2E(t, "", "unrelated emergency window")

	// ONE CLICK: the estate stops.
	var stop struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if code := h.reqInto("POST", "/v1/m/governance/killswitch", h.adminToken, h.tenantA, map[string]any{
		"scope_kind": "estate", "reason": "agent swarm exfiltrating data (INC-9000)",
	}, &stop); code != http.StatusCreated || stop.Status != "active" {
		t.Fatalf("engage = %d %+v", code, stop)
	}

	// The PEP denies EVERY governed tool-call — the check runs before the
	// disposition AND before the HITL/break-glass path, so the active grant
	// cannot re-authorize anything.
	out = fix.call(t, "Bash", map[string]any{"command": "ls"}, h.adminToken, h.tenantA)
	if out["permissionDecision"] != "deny" {
		t.Fatalf("under-stop PEP = %v", out)
	}
	if reason, _ := out["permissionDecisionReason"].(string); !strings.Contains(reason, stop.ID) {
		t.Fatalf("deny reason must point at the stop: %q", reason)
	}

	// The MCP destructive-tool gate refuses despite the active grant…
	rec := newStopDenyRecorder(h.st, discardLog())
	gate := mcpToolGate{bridge: buildBridge(t, h, h.adminToken), tenant: tid, guard: h.set.gov, rec: rec}
	d, err := gate.Authorize(ctx, mcpc.ToolApprovalRequest{Tenant: h.tenantA, Tool: "db.drop_table", PlanHash: "p1", RequestedBy: "agent"})
	if err != nil || d.Status != mcpc.StatusRejected || !strings.HasPrefix(d.ApprovalRef, "killswitch:") {
		t.Fatalf("mcp gate under stop = %+v err=%v", d, err)
	}
	// …and the upstream wrap freezes EVERY forwarded method without touching
	// the backend.
	inner := &fakeMCPUpstream{}
	wrapped := killSwitchUpstream{guard: h.set.gov, tenant: tid, rec: rec, inner: inner}
	if _, err := wrapped.Forward(ctx, mcpc.UpstreamRequest{Method: "tools/list"}); err == nil || !strings.Contains(err.Error(), "kill switch") {
		t.Fatalf("mcp forward under stop = %v", err)
	}
	if inner.calls != 0 {
		t.Fatalf("the backend must never be reached under a stop")
	}

	// The grant is closed before recovery (hygiene; also proves the stop's deny
	// was the switch, not a missing grant).
	if code, body := h.req("POST", "/v1/m/governance/breakglass/"+grant+"/revoke", h.adminToken, h.tenantA, nil); code != http.StatusOK {
		t.Fatalf("revoke grant = %d: %s", code, body)
	}

	// RE-ENABLE is never unilateral: 202 + a CRITICAL approval; two distinct
	// AAL3 humans decide; the flip lifts the stop.
	var pending struct {
		Status   string `json:"status"`
		Approval struct {
			ID                string  `json:"id"`
			RiskTier          string  `json:"risk_tier"`
			RequiredApprovals float64 `json:"required_approvals"`
		} `json:"approval"`
	}
	if code := h.reqInto("POST", "/v1/m/governance/killswitch/"+stop.ID+"/reenable", h.adminToken, h.tenantA, map[string]any{}, &pending); code != http.StatusAccepted {
		t.Fatalf("reenable phase 1 = %d", code)
	}
	if pending.Approval.RiskTier != "critical" || pending.Approval.RequiredApprovals != 2 {
		t.Fatalf("re-enable approval = %+v", pending.Approval)
	}
	_, approverB := h.createApprover(t, "ks-b@e2e.test")
	_, approverC := h.createApprover(t, "ks-c@e2e.test")
	if code, body := h.decide(t, approverB, pending.Approval.ID, "approve"); code != http.StatusOK {
		t.Fatalf("approve B = %d: %s", code, body)
	}
	// One human is not enough — the PEP still denies.
	if out = fix.call(t, "Bash", map[string]any{"command": "ls"}, h.adminToken, h.tenantA); out["permissionDecision"] != "deny" {
		t.Fatalf("PEP with 1/2 approvals = %v", out)
	}
	if code, body := h.decide(t, approverC, pending.Approval.ID, "approve"); code != http.StatusOK {
		t.Fatalf("approve C = %d: %s", code, body)
	}
	if code, body := h.req("POST", "/v1/m/governance/killswitch/"+stop.ID+"/reenable", h.adminToken, h.tenantA, map[string]any{}); code != http.StatusOK {
		t.Fatalf("reenable flip = %d: %s", code, body)
	}

	// Actuation resumes.
	if out = fix.call(t, "Bash", map[string]any{"command": "ls"}, h.adminToken, h.tenantA); out["permissionDecision"] != "allow" {
		t.Fatalf("post-reenable PEP = %v", out)
	}

	// The denials left tamper-evident, throttled evidence on the ledger.
	var denyEvents int
	if err := h.st.View(ctx, tid, func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 1, func(e model.AuditEvent) error {
			if e.Action == "security.killswitch.deny" {
				denyEvents++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if denyEvents == 0 {
		t.Fatalf("kill-switch denials must reach the ledger")
	}

	// Mandatory post-review by an uninvolved human closes the incident.
	_, reviewer := h.createApprover(t, "ks-rev@e2e.test")
	if code, body := h.req("POST", "/v1/m/governance/killswitch/"+stop.ID+"/review", reviewer, h.tenantA, map[string]any{
		"note": "stop justified; swarm contained; exfil path closed",
	}); code != http.StatusOK {
		t.Fatalf("review = %d: %s", code, body)
	}

	// The evidence pack carries the whole story on a verified chain: the engage,
	// the PEP/MCP denials, the two-human approval, the re-enable, the review.
	pack := h.getJSON(h.adminToken, h.tenantA, "/v1/m/governance/killswitch/"+stop.ID+"/evidence")
	integ := pack["integrity"].(map[string]any)
	if integ["chain_verified"] != true {
		t.Fatalf("pack chain must verify: %v", integ)
	}
	if decs, _ := pack["reenable_decisions"].([]any); len(decs) != 2 {
		t.Fatalf("pack must carry the two-human proof, got %d decisions", len(decs))
	}
	want := map[string]bool{
		"governance.killswitch.engage": false, "security.killswitch.deny": false,
		"governance.killswitch.reenable": false, "governance.killswitch.review": false,
	}
	for _, e := range pack["timeline"].([]any) {
		if a, ok := e.(map[string]any)["action"].(string); ok {
			if _, tracked := want[a]; tracked {
				want[a] = true
			}
		}
	}
	for a, seen := range want {
		if !seen {
			t.Fatalf("pack timeline missing %s", a)
		}
	}
}

// An AGENT-scoped stop freezes that agent's WHOLE MCP surface — not just its
// destructive tools/call (the connector gates those) but every forwarded method
// (tools/list recon, resources/read exfil, non-destructive calls), via the
// upstream wrap that carries the caller subject. A different agent still forwards.
func TestKillSwitchMCPForwardAgentScope(t *testing.T) {
	h := newHarness(t)
	tid := tenantAID(t, h)
	ctx := context.Background()
	rec := newStopDenyRecorder(h.st, discardLog())

	// Stop just one agent (by external ref — the MCP token subject convention).
	var stop struct {
		ID string `json:"id"`
	}
	if code := h.reqInto("POST", "/v1/m/governance/killswitch", h.adminToken, h.tenantA, map[string]any{
		"scope_kind": "agent", "scope_ref": "agent-rogue", "reason": "agent recon detected",
	}, &stop); code != http.StatusCreated {
		t.Fatalf("agent engage = %d", code)
	}

	inner := &fakeMCPUpstream{}
	wrapped := killSwitchUpstream{guard: h.set.gov, tenant: tid, rec: rec, inner: inner}

	// The stopped agent: a NON-destructive method (tools/list) is frozen.
	if _, err := wrapped.Forward(ctx, mcpc.UpstreamRequest{Method: "tools/list", Subject: "agent-rogue"}); err == nil || !strings.Contains(err.Error(), "kill switch") {
		t.Fatalf("stopped agent tools/list forward = %v, want deny", err)
	}
	if _, err := wrapped.Forward(ctx, mcpc.UpstreamRequest{Method: "resources/read", Subject: "agent-rogue"}); err == nil {
		t.Fatalf("stopped agent resources/read forward must be denied")
	}
	if inner.calls != 0 {
		t.Fatalf("the backend must never be reached for a stopped agent, got %d calls", inner.calls)
	}

	// A DIFFERENT agent forwards normally (the stop is graduated, not estate-wide).
	if _, err := wrapped.Forward(ctx, mcpc.UpstreamRequest{Method: "tools/list", Subject: "agent-ok"}); err != nil {
		t.Fatalf("a non-stopped agent must forward, got %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("the non-stopped agent's forward must reach the backend, got %d calls", inner.calls)
	}
}

// The stop-state consult is DENY-CLOSED at the PEP: when the guard errors, every
// tool-call denies rather than proceeding on unknown state.
func TestKillSwitchPEPFailsClosedOnStateError(t *testing.T) {
	h := newHarness(t)
	fix := newHookPEPFixture(t, h, hookPolicyDoc{Default: "allow"}, false, nil, false)
	fix.dec.stops = failingKillSwitchGuard{}
	fix.dec.stopRec = newStopDenyRecorder(h.st, discardLog())
	out := fix.call(t, "Bash", map[string]any{"command": "ls"}, h.adminToken, h.tenantA)
	if out["permissionDecision"] != "deny" {
		t.Fatalf("PEP must fail closed on a stop-state error, got %v", out)
	}
}

type failingKillSwitchGuard struct{}

func (failingKillSwitchGuard) KillSwitchState(context.Context, model.TenantID) (governance.StopState, error) {
	return governance.StopState{}, context.DeadlineExceeded
}

// The deny recorder throttles per (tenant, surface, subject): a hammering agent
// cannot flood the ledger, and the suppressed count rides the next append.
func TestStopDenyRecorderThrottles(t *testing.T) {
	h := newHarness(t)
	tid := tenantAID(t, h)
	ctx := context.Background()
	rec := newStopDenyRecorder(h.st, discardLog())
	for i := 0; i < 25; i++ {
		rec.record(ctx, tid, model.ID("019ebc00-0000-7000-8000-000000000001"), "hooks-pep", "agent-x", "")
	}
	count := 0
	if err := h.st.View(ctx, tid, func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 1, func(e model.AuditEvent) error {
			if e.Action == "security.killswitch.deny" {
				count++
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("25 denials in one window must append exactly 1 event, got %d", count)
	}
}
