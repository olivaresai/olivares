// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// circuit-breaker gate for the inference PEP: checks whether the
// acting agent's circuit breaker is tripped (open) and denies the request
// if so. A nil engine (the open build) skips the gate entirely.

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
)

// circuitBreakerGateCheck returns (denied, reason). A nil engine returns
// (false, "") — no circuit-breaker in the open build.
func circuitBreakerGateCheck(ctx context.Context, engine circuitBreakerEngine, tenant model.TenantID, agentRef string) (bool, string) {
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
