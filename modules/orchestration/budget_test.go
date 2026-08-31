// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package orchestration

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// errTestBudget is a synthetic budget-gate failure for the fail-open test.
var errTestBudget = errors.New("synthetic finops outage")

// TestFireBudgetBlocked proves an APPROVED fire is denied (deny-closed) when an
// enforcing budget at its cap (action=block) scopes it, the denial is HTTP 402 with a
// distinct op_status, the dispatcher is NEVER reached, and the ledger records it.
func TestFireBudgetBlocked(t *testing.T) {
	fired := false
	h, _ := newHarness(t,
		WithApprovalGate(fakeGate{status: StatusApproved}),
		WithDispatcher(recordingDispatcher{fired: &fired}),
		WithBudgetGate(fakeBudgetGate{decision: BudgetDecision{
			Action: budgetActionBlock, BudgetRef: "bud-1", Reason: `budget "eng-cap" block cap reached (monthly)`,
		}}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	id := h.createSchedule(tok, tenant, "nightly", "agent", "batch-agent", "cron", "0 0 * * *", 0)

	r := h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok, map[string]any{"approval_ref": "appr-1"}, tenantHdr(tenant))
	if r.code != http.StatusPaymentRequired || r.body["op_status"] != opStatusBudgetBlocked {
		t.Fatalf("budget block must deny with 402/budget_blocked, got %d %s", r.code, r.raw)
	}
	if r.body["dispatch_ref"] != nil && r.body["dispatch_ref"] != "" {
		t.Fatalf("a budget-denied fire must carry no dispatch_ref, got %v", r.body["dispatch_ref"])
	}
	if fired {
		t.Fatal("the dispatcher must NOT be reached when the budget denies the fire")
	}
	// The denial is recorded in the append-only ledger with the budget op_status.
	dr := h.do("GET", "/v1/m/orchestration/schedules/"+id+"/decisions", tok, nil, tenantHdr(tenant))
	var ledger listResponse[decisionDTO]
	_ = json.Unmarshal([]byte(dr.raw), &ledger)
	fireRow := findDecision(ledger.Items, opFire)
	if fireRow == nil || fireRow.OpStatus != opStatusBudgetBlocked {
		t.Fatalf("ledger must record a budget_blocked fire, got %s", dr.raw)
	}
	// The denial is also emitted to the tamper-evident audit ledger.
	ar := h.do("GET", "/v1/audit", tok, nil, tenantHdr(tenant))
	if ar.code != http.StatusOK || !strings.Contains(ar.raw, "orchestration.schedule.fire.budget_denied") {
		t.Fatalf("audit ledger must record the budget denial, got %d %s", ar.code, ar.raw)
	}
	// Minimal data (docs/SECURITY-HARDENING.md): no USD amount leaks into the response.
	if strings.Contains(r.raw, "$") {
		t.Fatalf("a budget denial response must carry no USD amount, got %s", r.raw)
	}
}

// TestFireBudgetThrottled proves a soft cap (action=throttle) denies with HTTP 429 and
// the throttled op_status.
func TestFireBudgetThrottled(t *testing.T) {
	fired := false
	h, _ := newHarness(t,
		WithApprovalGate(fakeGate{status: StatusApproved}),
		WithDispatcher(recordingDispatcher{fired: &fired}),
		WithBudgetGate(fakeBudgetGate{decision: BudgetDecision{Action: budgetActionThrottle, BudgetRef: "bud-2"}}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	id := h.createSchedule(tok, tenant, "nightly", "agent", "batch-agent", "cron", "0 0 * * *", 0)

	r := h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok, map[string]any{"approval_ref": "appr-1"}, tenantHdr(tenant))
	if r.code != http.StatusTooManyRequests || r.body["op_status"] != opStatusBudgetThrottled {
		t.Fatalf("budget throttle must deny with 429/budget_throttled, got %d %s", r.code, r.raw)
	}
	if fired {
		t.Fatal("the dispatcher must NOT be reached when the budget throttles the fire")
	}
	// The throttle is recorded in the ledger with its own op_status.
	dr := h.do("GET", "/v1/m/orchestration/schedules/"+id+"/decisions", tok, nil, tenantHdr(tenant))
	var ledger listResponse[decisionDTO]
	_ = json.Unmarshal([]byte(dr.raw), &ledger)
	if fireRow := findDecision(ledger.Items, opFire); fireRow == nil || fireRow.OpStatus != opStatusBudgetThrottled {
		t.Fatalf("ledger must record a budget_throttled fire, got %s", dr.raw)
	}
}

// TestFireBudgetAllowedDispatches proves an enforcing budget WITHIN its limit (Allowed)
// lets an approved fire dispatch unchanged — the gate is orthogonal to approval.
func TestFireBudgetAllowedDispatches(t *testing.T) {
	h, _ := newHarness(t,
		WithApprovalGate(fakeGate{status: StatusApproved}),
		WithDispatcher(fakeDispatcher{ref: "run-9"}),
		WithBudgetGate(fakeBudgetGate{decision: BudgetDecision{Allowed: true}}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	id := h.createSchedule(tok, tenant, "nightly", "agent", "batch-agent", "cron", "0 0 * * *", 0)

	r := h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok, map[string]any{"approval_ref": "appr-1"}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["op_status"] != opStatusDispatched || r.body["dispatch_ref"] != "run-9" {
		t.Fatalf("an allowed budget must let the fire dispatch, got %d %s", r.code, r.raw)
	}
}

// TestFireBudgetGateErrorFailsOpen proves a budget-gate ERROR never blocks an approved
// fire (fail-open: a FinOps outage must not take down actuation; the hard-cap finding
// is the backstop).
func TestFireBudgetGateErrorFailsOpen(t *testing.T) {
	h, _ := newHarness(t,
		WithApprovalGate(fakeGate{status: StatusApproved}),
		WithDispatcher(fakeDispatcher{ref: "run-open"}),
		WithBudgetGate(fakeBudgetGate{err: errTestBudget}),
	)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "acme")
	tok := h.roleToken(admin, tenant, "op@acme.io", "admin")
	id := h.createSchedule(tok, tenant, "nightly", "agent", "batch-agent", "cron", "0 0 * * *", 0)

	r := h.do("POST", "/v1/m/orchestration/schedules/"+id+"/fire", tok, map[string]any{"approval_ref": "appr-1"}, tenantHdr(tenant))
	if r.code != http.StatusOK || r.body["op_status"] != opStatusDispatched {
		t.Fatalf("a budget-gate error must fail OPEN (fire dispatches), got %d %s", r.code, r.raw)
	}
}
