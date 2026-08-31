// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import "context"

// caeptransmitgate.go is the AGPL composition-root seam for the OPTIONAL commercial
// CAEP transmitter add-on (caeptransmit_enterprise.go, phase 2). It defines the
// interface and event type the bus subscription and boot wiring depend on, and names
// the environment variable the enterprise implementation reads for its config.
//
// The OPEN (AGPL) binary wires nil (no transmitter — the community edition does not
// emit CAEP events to external SSF receivers). The ENTERPRISE build injects the
// closed implementation that signs SETs with an Ed25519 key and HTTP-pushes them to
// configured receivers per RFC 8935.
//
// A nil transmitter is the default: any caller must guard on non-nil before calling
// EmitAgentRisk, so the community binary never reaches this code path (no rug-pull).
// The receiver (the security piece: revoking access on an incoming CAEP/RISC signal)
// is open-core (core/auth/caep_events.go) and is unaffected by this seam.

// caepTransmitter emits CAEP agent-risk events to external SSF receivers.
// The enterprise build (caeptransmit_enterprise.go) wires a real implementation;
// the community build supplies nil (wire_noenterprise.go).
type caepTransmitter interface {
	// EmitAgentRisk signs a CAEP agent-risk SET and HTTP-pushes it to all
	// configured receivers (RFC 8935). Errors from individual receivers are
	// collected and returned; a nil transmitter is always a no-op.
	EmitAgentRisk(ctx context.Context, evt CAEPAgentRiskEvent) error
}

// CAEPAgentRiskEvent describes an agent-risk event the engine wants to communicate
// to external SSF receivers. The transmitter signs it as a CAEP SET and delivers
// it via RFC 8935 HTTP push to each configured receiver endpoint.
type CAEPAgentRiskEvent struct {
	// AgentID is the engine's internal reference for the acting agent or session.
	AgentID string
	// TenantID identifies the tenant the agent was acting on behalf of.
	TenantID string
	// EventType distinguishes the risk trigger:
	//   "circuit_breaker_triggered" — a cost/rate circuit-breaker fired
	//   "agent_session_revoked"     — a CAEP-inbound revoke cut the agent's session
	EventType string
	// Severity is either "high" or "critical".
	Severity string
	// Description is a human-readable summary of the risk event. It must not carry
	// user data or credentials (minimal-data principle, docs/SECURITY-HARDENING.md).
	Description string
}

// envCAEPTransmitterConfig is the environment variable holding the JSON-encoded
// CAEP transmitter configuration. The enterprise implementation reads this at boot.
// Unset in the community build (transmitter = nil).
//
// `unused` reports this constant, and here the reason not to delete it is DATED: boot.go
// carries `_ = caepTx // bus subscription wired in follow-up (Task 7)`, which is row C08-08 of
// an internal design note (not shipped) Deleting this to make C08-07 green would take away
// the variable C08-08 has to read — two rows of the same backlog pulling opposite ways, with
// the gate green and nobody the wiser until someone tried the second one.
//
//nolint:unused // read by the enterprise build, and C08-08 wires the community side
const envCAEPTransmitterConfig = "OLIVARES_CAEP_TRANSMITTER_CONFIG"
