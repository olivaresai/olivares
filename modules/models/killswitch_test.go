// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/models"
)

// stubStopGate is a programmable StopGate for the execute-path tests
// (models has no agent dimension — only the estate-wide graduation applies).
type stubStopGate struct {
	decision models.StopDecision
	err      error
}

func (g stubStopGate) Check(context.Context, model.TenantID) (models.StopDecision, error) {
	return g.decision, g.err
}

// TestExecuteKillSwitchEstateStops proves an active estate-wide stop denies
// POST /execute with HTTP 423 BEFORE any executor (provider) call — resolve
// stays readable during a stop, spend does not.
func TestExecuteKillSwitchEstateStops(t *testing.T) {
	ex := &stubExecutor{res: models.ExecuteResult{Text: "never"}}
	m := models.New(
		models.WithExecutor(ex),
		models.WithStopGate(stubStopGate{decision: models.StopDecision{Stopped: true, StopRef: "stop-1"}}),
	)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedModel(t, h, tenant, "anthropic", "claude-opus-4-8")
	id := createRoutingPolicy(t, h, admin, tenant)

	r := h.do("POST", "/v1/m/models/routing-policies/"+id+"/execute", admin, map[string]any{"input": "hello"}, tenantHdr(tenant))
	if r.code != http.StatusLocked {
		t.Fatalf("execute under estate stop = %d %s, want 423", r.code, r.raw)
	}
	if !strings.Contains(r.raw, "stop-1") {
		t.Errorf("the denial must carry the operator-facing stop ref, got %s", r.raw)
	}
	if ex.calls != 0 {
		t.Fatalf("executor called %d times; the stop must deny BEFORE any provider call", ex.calls)
	}
}

// TestExecuteKillSwitchGateErrorFailsClosed proves a stop-gate ERROR denies the
// execution with 503 (deny-closed — the inverse of the budget gate's fail-open
// posture): an unreadable stop state never means "go".
func TestExecuteKillSwitchGateErrorFailsClosed(t *testing.T) {
	ex := &stubExecutor{res: models.ExecuteResult{Text: "never"}}
	m := models.New(
		models.WithExecutor(ex),
		models.WithStopGate(stubStopGate{err: errors.New("synthetic kill-switch outage")}),
	)
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedModel(t, h, tenant, "anthropic", "claude-opus-4-8")
	id := createRoutingPolicy(t, h, admin, tenant)

	r := h.do("POST", "/v1/m/models/routing-policies/"+id+"/execute", admin, map[string]any{"input": "hello"}, tenantHdr(tenant))
	if r.code != http.StatusServiceUnavailable || !strings.Contains(r.raw, "kill-switch") {
		t.Fatalf("execute with a stop-gate error = %d %s, want 503 (deny-closed)", r.code, r.raw)
	}
	if ex.calls != 0 {
		t.Fatalf("executor called %d times when the stop state is unreadable", ex.calls)
	}
}

// TestExecuteKillSwitchNoStopExecutes proves a wired gate reporting NO active
// stop leaves the execute path untouched: the resolved chain reaches the
// executor and the result flows back.
func TestExecuteKillSwitchNoStopExecutes(t *testing.T) {
	ex := &stubExecutor{res: models.ExecuteResult{Text: "the answer", InputTokens: 12, OutputTokens: 7}}
	m := models.New(models.WithExecutor(ex), models.WithStopGate(stubStopGate{}))
	h := newHarness(t, m)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	seedModel(t, h, tenant, "anthropic", "claude-opus-4-8")
	id := createRoutingPolicy(t, h, admin, tenant)

	r := h.do("POST", "/v1/m/models/routing-policies/"+id+"/execute", admin, map[string]any{"input": "hello"}, tenantHdr(tenant))
	if r.code != http.StatusOK {
		t.Fatalf("execute with no stop = %d %s, want 200 (unchanged flow)", r.code, r.raw)
	}
	if ex.calls != 1 {
		t.Errorf("executor called %d times, want 1", ex.calls)
	}
	if r.body["output"] != "the answer" {
		t.Errorf("output = %v, want 'the answer'", r.body["output"])
	}
}
