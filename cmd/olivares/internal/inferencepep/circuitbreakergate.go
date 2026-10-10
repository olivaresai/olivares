// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferencepep

// circuit-breaker gate for the inference PEP: checks whether the
// acting agent's circuit breaker is tripped (open) and denies the request
// if so. A nil engine (the open build) skips the gate entirely.

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// CircuitBreakerState is the state of a circuit breaker for one agent.
type CircuitBreakerState struct {
	State    string // closed | open | half_open
	RuleRef  string // the rule that tripped it
	ResetsAt string // when auto-reset fires (open+suspend only)
}

// CircuitBreaker is the part of the enterprise circuit-breaker engine the gate consults.
// nil = no circuit-breaker (the open build). State returns closed for an unknown agent; an
// error fails open (the kill-switch is the hard stop; the circuit-breaker is a softer layer).
type CircuitBreaker interface {
	State(ctx context.Context, tenant model.TenantID, agentRef string) (CircuitBreakerState, error)
}

// CircuitBreakerGateCheck returns (denied, reason). A nil engine returns
// (false, "") — no circuit-breaker in the open build.
func CircuitBreakerGateCheck(ctx context.Context, engine CircuitBreaker, tenant model.TenantID, agentRef string) (bool, string) {
	if engine == nil || agentRef == "" {
		return false, ""
	}
	st, err := engine.State(ctx, tenant, agentRef)
	if err != nil {
		return false, "" // fail open — the kill-switch is the hard stop
	}
	if st.State == "open" {
		return true, "circuit breaker tripped for this agent (rule " + st.RuleRef + "); request denied until cooldown resets or the breaker is manually reset"
	}
	return false, ""
}
