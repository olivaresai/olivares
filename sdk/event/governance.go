// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package event

import "time"

// The governance lifecycle event types. Like guardrail.observed they are
// module-defined types — NOT part of the sealed model.Observation sum type — so
// the S02 §3 wire contract stays frozen; they travel the unversioned JSON
// fallback if they ever cross the out-of-process module boundary. They are
// defined here (not module-side like voice.telemetry.observed) because they are
// EXTERNALLY SUBSCRIBABLE through the eventing platform: an Apache-side consumer
// or the eventing module can name the type and read the payload without
// importing the AGPL governance module.
const (
	// TypeApprovalRequested wraps an ApprovalRequest: a pending approval was
	// opened and awaits decision. Published by modules/governance post-commit.
	TypeApprovalRequested Type = "approval.requested"
	// TypeApprovalResolved wraps an ApprovalResolution: a pending approval
	// reached a terminal outcome. Published by modules/governance post-commit.
	TypeApprovalResolved Type = "approval.resolved"
	// TypePolicyChanged wraps a PolicyChange: a governance policy (kind
	// abac/approval) was created, updated or deleted. Published by
	// modules/governance post-commit.
	TypePolicyChanged Type = "policy.changed"
)

// ApprovalRequest is the minimal-data payload of a TypeApprovalRequested event:
// identifiers and the approval's decision parameters only. It deliberately
// carries NEITHER the requester's free-text reason NOR the subject reference —
// the approval's create audit Meta omits both for the same minimal-data reason
// (docs/SECURITY-HARDENING.md); a consumer authorized for governance:approval:read fetches the
// full approval by ApprovalID. Field names are the public JSON contract (the
// SDK is the contract, like every payload in this package).
type ApprovalRequest struct {
	// ApprovalID is the approval's id — the reference a consumer uses to fetch,
	// decide or watch the request.
	ApprovalID string
	// Action is the requested action (a bounded short identifier).
	Action string
	// SubjectKind is the kind of subject the action targets (a bounded short
	// identifier; the subject's reference is deliberately not carried).
	SubjectKind string
	// RiskTier is the risk classification under which the request was
	// opened (e.g. "critical"); it determines the dual-control floor.
	RiskTier string
	// RequiredApprovals is the number of distinct approvers needed.
	RequiredApprovals int64
	// PolicyRef is the id of the approval policy that matched, or "" when the
	// request used caller-supplied parameters.
	PolicyRef string
	// ExpiresAt is when the pending request lapses; zero when it never expires.
	// The zero value is omitted on the JSON wire (omitzero) so "never" is field
	// absence, not the year-1 sentinel.
	ExpiresAt time.Time `json:",omitzero"`
	// EscalateAt is when an undecided request escalates; zero when it never
	// does. Omitted on the JSON wire when zero, like ExpiresAt.
	EscalateAt time.Time `json:",omitzero"`
}

// ApprovalRequestOf returns the ApprovalRequest payload of a
// TypeApprovalRequested event, or false when the event is not one or its
// payload is malformed. It accepts a value or pointer payload (a directly built
// event may carry either).
func ApprovalRequestOf(e Event) (ApprovalRequest, bool) {
	if e.Type != TypeApprovalRequested {
		return ApprovalRequest{}, false
	}
	switch o := e.Payload.(type) {
	case ApprovalRequest:
		return o, true
	case *ApprovalRequest:
		return *o, true
	default:
		return ApprovalRequest{}, false
	}
}

// ApprovalRequested builds a TypeApprovalRequested event carrying a, stamped
// with the producing component's source name. The engine assigns the event ID
// at publish; the publisher stamps Time with when the request was opened. It is
// a convenience so producers do not hand-assemble the envelope and risk a wrong
// Type.
func ApprovalRequested(tenant, source string, at time.Time, a ApprovalRequest) Event {
	return Event{
		Type:    TypeApprovalRequested,
		Tenant:  tenant,
		Source:  source,
		Time:    at,
		Payload: a,
	}
}

// ApprovalResolution is the minimal-data payload of a TypeApprovalResolved
// event: identifiers and the approval's final decision parameters only. It
// deliberately carries NEITHER the requester's free-text reason, NOR a decision
// note, NOR the subject reference; a consumer authorized for
// governance:approval:read fetches the full approval by ApprovalID. Field names
// are the public JSON contract (the SDK is the contract).
type ApprovalResolution struct {
	// ApprovalID is the approval's id — the reference a consumer uses to fetch
	// the resolved request.
	ApprovalID string
	// Action is the requested action (a bounded short identifier).
	Action string
	// SubjectKind is the kind of subject the action targets (a bounded short
	// identifier; the subject's reference is deliberately not carried).
	SubjectKind string
	// RiskTier is the live-derived risk classification at resolution time.
	RiskTier string
	// Outcome is the terminal result. The vocabulary is open on the wire (a
	// consumer must tolerate values it does not know); the governance module
	// currently emits "approved", "rejected", "canceled" and "expired".
	Outcome string
	// RequiredApprovals is the number of distinct approvers needed at
	// resolution time.
	RequiredApprovals int64
	// ApproveCount is the number of recorded approve decisions.
	ApproveCount int64
	// RejectCount is the number of recorded reject decisions.
	RejectCount int64
	// PolicyRef is the id of the approval policy that matched, or "" when the
	// request used caller-supplied parameters.
	PolicyRef string
	// DecidedAt is when the request reached the terminal outcome. The zero value
	// is omitted on the JSON wire (omitzero).
	DecidedAt time.Time `json:",omitzero"`
}

// ApprovalResolutionOf returns the ApprovalResolution payload of a
// TypeApprovalResolved event, or false when the event is not one or its payload
// is malformed. It accepts a value or pointer payload (a directly built event
// may carry either).
func ApprovalResolutionOf(e Event) (ApprovalResolution, bool) {
	if e.Type != TypeApprovalResolved {
		return ApprovalResolution{}, false
	}
	switch o := e.Payload.(type) {
	case ApprovalResolution:
		return o, true
	case *ApprovalResolution:
		return *o, true
	default:
		return ApprovalResolution{}, false
	}
}

// ApprovalResolved builds a TypeApprovalResolved event carrying a, stamped with
// the producing component's source name and the resolution publish time.
func ApprovalResolved(tenant, source string, at time.Time, a ApprovalResolution) Event {
	return Event{
		Type:    TypeApprovalResolved,
		Tenant:  tenant,
		Source:  source,
		Time:    at,
		Payload: a,
	}
}

// The PolicyChange operations. The vocabulary is open on the wire (a consumer
// must tolerate values it does not know), but these are the ones the governance
// module emits.
const (
	// PolicyOpCreated is a policy creation.
	PolicyOpCreated = "created"
	// PolicyOpUpdated is an in-place policy update.
	PolicyOpUpdated = "updated"
	// PolicyOpDeleted is a policy deletion.
	PolicyOpDeleted = "deleted"
)

// PolicyChange is the minimal-data payload of a TypePolicyChanged event. It
// mirrors what the policy mutation's audit Meta records — kind and enabled —
// plus the id and operation; it never carries the operator-supplied policy name
// or the policy spec (the sensitive policy content, docs/SECURITY-HARDENING.md). A consumer
// authorized for governance:policy:read fetches the policy by PolicyID.
type PolicyChange struct {
	// PolicyID is the changed policy's id.
	PolicyID string
	// Kind is the policy kind ("abac" or "approval").
	Kind string
	// Op is the mutation: PolicyOpCreated, PolicyOpUpdated or PolicyOpDeleted.
	Op string
	// Enabled is the policy's enabled flag after the change; false for a
	// deletion.
	Enabled bool
}

// PolicyChangeOf returns the PolicyChange payload of a TypePolicyChanged event,
// or false when the event is not one or its payload is malformed. It accepts a
// value or pointer payload.
func PolicyChangeOf(e Event) (PolicyChange, bool) {
	if e.Type != TypePolicyChanged {
		return PolicyChange{}, false
	}
	switch o := e.Payload.(type) {
	case PolicyChange:
		return o, true
	case *PolicyChange:
		return *o, true
	default:
		return PolicyChange{}, false
	}
}

// PolicyChanged builds a TypePolicyChanged event carrying p, stamped with the
// producing component's source name and the mutation time.
func PolicyChanged(tenant, source string, at time.Time, p PolicyChange) Event {
	return Event{
		Type:    TypePolicyChanged,
		Tenant:  tenant,
		Source:  source,
		Time:    at,
		Payload: p,
	}
}
