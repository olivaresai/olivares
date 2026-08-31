// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

// circuit-breaker interface: the enterprise add-on implements the
// runtime enforcement circuit-breaker (threshold-based automatic suspension,
// auto-reset, escalation to kill-switch). The open build returns nil
// (no circuit-breaker, byte-identical behavior to pre). The interface
// is defined here (build-independent) so both wire files can reference it.

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk/event"
)

// circuitBreakerState is the state of a circuit breaker for one agent.
type circuitBreakerState struct {
	State    string // closed | open | half_open
	RuleRef  string // the rule that tripped it
	ResetsAt string // when auto-reset fires (open+suspend only)
}

// circuitBreakerEngine is the interface the inference PEP and the composition
// root use to consult the circuit-breaker. nil = no circuit-breaker (the open
// build). The enterprise build wires the real engine.
type circuitBreakerEngine interface {
	// State returns the circuit-breaker state for an agent. An unknown agent
	// returns closed (no trip). An error fails open (the kill-switch is the
	// hard stop; the circuit-breaker is a softer layer).
	State(ctx context.Context, tenant model.TenantID, agentRef string) (circuitBreakerState, error)

	// NOTE: RegisterSchema is deliberately NOT here. The ext schema is registered by
	// the OPEN tree in every edition (circuitbreakerwiring.go), because
	// `task lint:schema-parity` compares community against enterprise and an
	// enterprise-only table would fail it.

	// OnFinding is the bus handler that DRIVES the state machine: findings are what
	// trip a breaker, so without this subscription State() would answer "closed"
	// forever and the whole add-on would be decoration.
	//
	// It is on the interface rather than left to the closed side because the
	// subscription is composition, and composition is the open tree's job. The
	// signature matches event.Handler so it binds straight to bus.Subscribe, exactly
	// like incidentCloseLoop.OnFinding.
	OnFinding(ctx context.Context, e event.Event) error
}
