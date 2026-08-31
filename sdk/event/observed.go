// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package event

// TypeGuardrailObserved wraps an ObservedText: a redacted excerpt of observed
// agent text routed to the security module's detector chain so guardrail
// findings emit automatically, without a manual POST /guardrails/inspect
// (closes). It is a DETECTIVE input, not a flow fact, so — exactly like
// the identity roster — it travels a typed event payload rather than a
// fourth sealed model.Observation; the S02 §3 wire contract stays frozen. A
// subscriber reads it with ObservedTextOf.
const TypeGuardrailObserved Type = "guardrail.observed"

// ObservedText is the minimal-data payload of a TypeGuardrailObserved event: a
// bounded, ALREADY-REDACTED excerpt of observed agent text plus its
// non-sensitive context, so the security detectors can run on real traffic
// without a raw-payload path on the bus (docs/SECURITY-HARDENING.md). The PRODUCER must redact
// secrets/PII and bound the excerpt before emitting — the engine never widens a
// connector's minimal-data guarantee, and the bus never carries a raw payload.
//
// It is deliberately NOT a model.Observation: the sealed sum type (EdgeObservation
// /CostSample/FindingReport) is the frozen connector→engine wire contract, and a
// SourceConnector still cannot emit text through its Sink. ObservedText is the
// module/ingest-layer counterpart for the detective path; it is published with
// Host.Publish and consumed by a Subscribe, traveling the unversioned JSON
// fallback if it ever crosses the out-of-process module boundary.
type ObservedText struct {
	// Surface is which piece of agent text this is: "input", "output" or
	// "tool_args" (mirrors the security module's Surface vocabulary). A string so
	// the SDK stays free of the AGPL module's types.
	Surface string
	// Text is the already-redacted, bounded excerpt the detectors inspect. The
	// producer guarantees it carries no secret/PII (docs/SECURITY-HARDENING.md); the consumer
	// clamps it again defensively before inspecting.
	Text string
	// AgentRef, SessionRef and ResourceRef are non-sensitive context references
	// that let a resulting finding name its subject (any may be empty).
	AgentRef    string
	SessionRef  string
	ResourceRef string
}

// ObservedTextOf returns the ObservedText payload of a TypeGuardrailObserved
// event, or false when the event is not one or its payload is malformed. It
// accepts a value or pointer payload (a directly built event may carry either).
func ObservedTextOf(e Event) (ObservedText, bool) {
	if e.Type != TypeGuardrailObserved {
		return ObservedText{}, false
	}
	switch o := e.Payload.(type) {
	case ObservedText:
		return o, true
	case *ObservedText:
		return *o, true
	default:
		return ObservedText{}, false
	}
}

// GuardrailObserved builds a TypeGuardrailObserved event carrying t for tenant,
// stamped with the producing component's source name. The engine assigns the
// event ID at publish; Time is left to the publisher (Host.Publish stamps the
// source when empty). It is a convenience so producers do not hand-assemble the
// envelope and risk a wrong Type.
func GuardrailObserved(tenant, source string, t ObservedText) Event {
	return Event{
		Type:    TypeGuardrailObserved,
		Tenant:  tenant,
		Source:  source,
		Payload: t,
	}
}
