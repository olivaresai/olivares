// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

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
// agentRef set it stops ONLY a mutation whose StopDims carry that agent (the
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

// createMCPDef declares an mcp_server-subject definition (a subject with no
// agent dimension, so an agent-scoped stop never matches it).
func (h *harness) createMCPDef(token string, tenant model.TenantID, name string) string {
	h.t.Helper()
	body := map[string]any{
		"subject_kind": "mcp_server", "subject_ref": "mcp-files", "name": name,
		"environment": "prod", "target": "docker.host/node1", "runtime": "docker",
		"spec": map[string]any{"image": "mcp:1"},
	}
	r := h.do("POST", "/v1/m/deploy/definitions", token, body, tenantHdr(tenant))
	if r.code != http.StatusCreated {
		h.t.Fatalf("create mcp definition = %d %s", r.code, r.raw)
	}
	return r.body["id"].(string)
}

// findOperation returns the first operation row matching (op, status), or nil.
func findOperation(items []operationDTO, op, status string) *operationDTO {
	for i := range items {
		if items[i].Op == op && items[i].Status == status {
			return &items[i]
		}
	}
	return nil
}

// TestKillSwitchEstateBlocksApplyAndRetire proves an estate-wide emergency stop
// freezes BOTH governed mutations with HTTP 423 — apply (both phases) and
// retire — before any executor work, with the denial recorded in the
// append-only operation log and audited.
func TestKillSwitchEstateBlocksApplyAndRetire(t *testing.T) {
	ex := newMockExecutor()
	h := newHarnessWith(t,
		WithApprovalGate(newFakeGate()), WithExecutor(ex), WithIdentityBinder(&fakeBinder{firm: true}),
		WithStopGate(fakeStopGate{decision: StopDecision{Stopped: true, StopRef: "stop-1", Scope: "estate"}}),
	)
	root := h.adminLogin()
	tid := h.createOrg(root, "acme")
	tok := h.roleToken(root, tid, "ops@acme.io", "admin")
	defID := h.createDef(tok, tid, "billing-agent", agentSpec("img:1", "agent:billing"))

	// Apply phase 1: even asking for an approval is frozen.
	r := h.applyPhase1(tok, tid, defID)
	if r.code != http.StatusLocked || r.body["op"] != opApply || r.body["status"] != opStatusBlocked {
		t.Fatalf("apply phase 1 under estate stop = %d %s, want 423 apply/blocked", r.code, r.raw)
	}
	// Apply phase 2: an already-presented approval does not mutate either.
	if r := h.applyPhase2(tok, tid, defID, "appr-any"); r.code != http.StatusLocked || r.body["status"] != opStatusBlocked {
		t.Fatalf("apply phase 2 under estate stop = %d %s, want 423/blocked", r.code, r.raw)
	}
	// Retire: a teardown is an infrastructure mutation too — frozen.
	rr := h.do("POST", "/v1/m/deploy/definitions/"+defID+"/retire", tok, map[string]any{}, tenantHdr(tid))
	if rr.code != http.StatusLocked || rr.body["op"] != opRetire || rr.body["status"] != opStatusBlocked {
		t.Fatalf("retire under estate stop = %d %s, want 423 retire/blocked", rr.code, rr.raw)
	}
	if ex.applyCalls != 0 {
		t.Fatalf("executor.Apply called %d times; a stopped estate must never reach the runtime", ex.applyCalls)
	}
	// The denials are recorded in the append-only operation log.
	or := h.do("GET", "/v1/m/deploy/operations", tok, nil, tenantHdr(tid))
	var ops listResponse[operationDTO]
	_ = json.Unmarshal([]byte(or.raw), &ops)
	if findOperation(ops.Items, opApply, opStatusBlocked) == nil {
		t.Fatalf("operation log must record a blocked apply, got %s", or.raw)
	}
	if findOperation(ops.Items, opRetire, opStatusBlocked) == nil {
		t.Fatalf("operation log must record a blocked retire, got %s", or.raw)
	}
	// ...and emitted to the tamper-evident audit ledger.
	ar := h.do("GET", "/v1/audit", tok, nil, tenantHdr(tid))
	if ar.code != http.StatusOK ||
		!strings.Contains(ar.raw, "deploy.apply.killswitch_denied") ||
		!strings.Contains(ar.raw, "deploy.retire.killswitch_denied") {
		t.Fatalf("audit ledger must record both kill-switch denials, got %d %s", ar.code, ar.raw)
	}
}

// TestKillSwitchAgentScopedSparesMCPServer proves an agent-scoped stop freezes
// a definition whose subject IS that agent, while an mcp_server-subject
// definition (no agent dimension) proceeds to its normal governed path.
func TestKillSwitchAgentScopedSparesMCPServer(t *testing.T) {
	ex := newMockExecutor()
	h := newHarnessWith(t,
		WithApprovalGate(newFakeGate()), WithExecutor(ex), WithIdentityBinder(&fakeBinder{firm: true}),
		// createDef declares its agent definitions with subject_ref "acme-bot".
		WithStopGate(fakeStopGate{agentRef: "acme-bot", decision: StopDecision{Stopped: true, StopRef: "stop-2", Scope: "agent"}}),
	)
	root := h.adminLogin()
	tid := h.createOrg(root, "acme")
	tok := h.roleToken(root, tid, "ops@acme.io", "admin")
	agentDef := h.createDef(tok, tid, "billing-agent", agentSpec("img:1", "agent:billing"))
	mcpDef := h.createMCPDef(tok, tid, "files-server")

	if r := h.applyPhase1(tok, tid, agentDef); r.code != http.StatusLocked || r.body["status"] != opStatusBlocked {
		t.Fatalf("stopped agent's apply = %d %s, want 423/blocked", r.code, r.raw)
	}
	r := h.applyPhase1(tok, tid, mcpDef)
	if r.code != http.StatusAccepted || r.body["status"] != opStatusRequested {
		t.Fatalf("mcp_server apply must proceed to its normal phase-1 path under an agent-scoped stop, got %d %s", r.code, r.raw)
	}
}

// TestKillSwitchGateErrorFailsClosed proves a stop-gate ERROR denies apply and
// retire with 503 (deny-closed): an unreadable stop state never means "go".
func TestKillSwitchGateErrorFailsClosed(t *testing.T) {
	ex := newMockExecutor()
	h := newHarnessWith(t,
		WithApprovalGate(newFakeGate()), WithExecutor(ex), WithIdentityBinder(&fakeBinder{firm: true}),
		WithStopGate(fakeStopGate{err: errTestStop}),
	)
	root := h.adminLogin()
	tid := h.createOrg(root, "acme")
	tok := h.roleToken(root, tid, "ops@acme.io", "admin")
	defID := h.createDef(tok, tid, "billing-agent", agentSpec("img:1", "agent:billing"))

	r := h.applyPhase1(tok, tid, defID)
	if r.code != http.StatusServiceUnavailable || !strings.Contains(r.raw, "kill-switch") {
		t.Fatalf("apply with a stop-gate error = %d %s, want 503 (deny-closed)", r.code, r.raw)
	}
	rr := h.do("POST", "/v1/m/deploy/definitions/"+defID+"/retire", tok, map[string]any{}, tenantHdr(tid))
	if rr.code != http.StatusServiceUnavailable || !strings.Contains(rr.raw, "kill-switch") {
		t.Fatalf("retire with a stop-gate error = %d %s, want 503 (deny-closed)", rr.code, rr.raw)
	}
	if ex.applyCalls != 0 {
		t.Fatalf("executor.Apply called %d times when the stop state is unreadable", ex.applyCalls)
	}
}
