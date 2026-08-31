// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package voice

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
)

// errTestBudget is a synthetic budget-gate failure for the fail-open test.
var errTestBudget = errors.New("synthetic finops outage")

// TestOpenBudgetBlocked proves an APPROVED voice open is denied (deny-closed) when an
// enforcing budget at its cap (action=block) scopes it: HTTP 402, distinct op_status,
// and the dispatcher is NEVER reached.
func TestOpenBudgetBlocked(t *testing.T) {
	opened := false
	h, _ := newHarness(t,
		WithApprovalGate(fakeGate{status: StatusApproved}),
		WithDispatcher(recordingDispatcher{opened: &opened}),
		WithBudgetGate(fakeBudgetGate{decision: BudgetDecision{
			Action: budgetActionBlock, BudgetRef: "bud-1", Reason: `budget "voice-cap" block cap reached (monthly)`,
		}}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	h.setPolicy(tok, tenant, "voice-agent", "gpt-realtime", "openai", 0)

	r := h.open(tok, tenant, "s1", "voice-agent", "gpt-realtime", "openai", "appr-1")
	if r.code != http.StatusPaymentRequired || r.body["op_status"] != opStatusBudgetBlocked {
		t.Fatalf("budget block must deny with 402/budget_blocked, got %d %s", r.code, r.raw)
	}
	if opened {
		t.Fatal("the dispatcher must NOT be reached when the budget denies the open")
	}
	// The denial is recorded in the open ledger with its own op_status.
	if got := h.ledgerOpStatus(tok, tenant); got != opStatusBudgetBlocked {
		t.Fatalf("ledger must record a budget_blocked open, got op_status=%q", got)
	}
	// ...and emitted to the tamper-evident audit ledger, money-free (docs/SECURITY-HARDENING.md).
	ar := h.do("GET", "/v1/audit", tok, nil, tenantHdr(tenant))
	if ar.code != http.StatusOK || !strings.Contains(ar.raw, "voice.session.open.budget_denied") {
		t.Fatalf("audit ledger must record the budget denial, got %d %s", ar.code, ar.raw)
	}
	if strings.Contains(r.raw, "$") {
		t.Fatalf("a budget denial response must carry no USD amount, got %s", r.raw)
	}
}

// ledgerOpStatus returns the op_status of the most recent open decision row.
func (h *harness) ledgerOpStatus(tok string, tenant model.TenantID) string {
	h.t.Helper()
	dr := h.do("GET", "/v1/m/voice/decisions", tok, nil, tenantHdr(tenant))
	var ledger listResponse[decisionDTO]
	_ = json.Unmarshal([]byte(dr.raw), &ledger)
	for _, d := range ledger.Items {
		if d.Op == opOpen {
			return d.OpStatus
		}
	}
	return ""
}

// TestOpenBudgetThrottled proves a soft cap (action=throttle) denies with HTTP 429.
func TestOpenBudgetThrottled(t *testing.T) {
	opened := false
	h, _ := newHarness(t,
		WithApprovalGate(fakeGate{status: StatusApproved}),
		WithDispatcher(recordingDispatcher{opened: &opened}),
		WithBudgetGate(fakeBudgetGate{decision: BudgetDecision{Action: budgetActionThrottle, BudgetRef: "bud-2"}}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	h.setPolicy(tok, tenant, "voice-agent", "gpt-realtime", "openai", 0)

	r := h.open(tok, tenant, "s1", "voice-agent", "gpt-realtime", "openai", "appr-1")
	if r.code != http.StatusTooManyRequests || r.body["op_status"] != opStatusBudgetThrottled {
		t.Fatalf("budget throttle must deny with 429/budget_throttled, got %d %s", r.code, r.raw)
	}
	if opened {
		t.Fatal("the dispatcher must NOT be reached when the budget throttles the open")
	}
	if got := h.ledgerOpStatus(tok, tenant); got != opStatusBudgetThrottled {
		t.Fatalf("ledger must record a budget_throttled open, got op_status=%q", got)
	}
}

// TestOpenBudgetAllowedDispatches proves an enforcing budget WITHIN its limit lets an
// approved open dispatch unchanged.
func TestOpenBudgetAllowedDispatches(t *testing.T) {
	h, _ := newHarness(t,
		WithApprovalGate(fakeGate{status: StatusApproved}),
		WithDispatcher(fakeDispatcher{ref: "rt-9"}),
		WithBudgetGate(fakeBudgetGate{decision: BudgetDecision{Allowed: true}}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	h.setPolicy(tok, tenant, "voice-agent", "gpt-realtime", "openai", 0)

	r := h.open(tok, tenant, "s1", "voice-agent", "gpt-realtime", "openai", "appr-1")
	if r.code != http.StatusOK || r.body["op_status"] != opStatusDispatched || r.body["dispatch_ref"] != "rt-9" {
		t.Fatalf("an allowed budget must let the open dispatch, got %d %s", r.code, r.raw)
	}
}

// TestOpenBudgetGateErrorFailsOpen proves a budget-gate ERROR never blocks an approved
// open (fail-open).
func TestOpenBudgetGateErrorFailsOpen(t *testing.T) {
	h, _ := newHarness(t,
		WithApprovalGate(fakeGate{status: StatusApproved}),
		WithDispatcher(fakeDispatcher{ref: "rt-open"}),
		WithBudgetGate(fakeBudgetGate{err: errTestBudget}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	h.setPolicy(tok, tenant, "voice-agent", "gpt-realtime", "openai", 0)

	r := h.open(tok, tenant, "s1", "voice-agent", "gpt-realtime", "openai", "appr-1")
	if r.code != http.StatusOK || r.body["op_status"] != opStatusDispatched {
		t.Fatalf("a budget-gate error must fail OPEN (open dispatches), got %d %s", r.code, r.raw)
	}
}
