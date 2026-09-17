// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// WorkEventFamily classifies a durable sessions event by the plane that owns
// its effect. The outbox aggregate kind alone cannot decide it: a handoff
// offer or a decision request is written against the work_item aggregate and
// is nevertheless a K3 communication effect, so the local pump classifies by
// the event type family before it decides whether a candidate may be claimed.
type WorkEventFamily string

const (
	// WorkEventFamilyWork is the K1/K2 kernel: items, leases, ownership,
	// dependencies, acceptance, recorded decisions and protocol bindings' work
	// facts. It recovers independently of K3 readiness.
	WorkEventFamilyWork WorkEventFamily = "work"
	// WorkEventFamilyCommunication is the K3 plane: messages, deliveries,
	// handoff carriers and decision requests, whatever aggregate carries them.
	WorkEventFamilyCommunication WorkEventFamily = "communication"
	// WorkEventFamilyProtocol is the K5 binding plane (protocol replies and
	// received peer messages). It is its own capability with its own gate.
	WorkEventFamilyProtocol WorkEventFamily = "protocol"
	// WorkEventFamilyUnknown is an event type no family claims. A policy must
	// treat it deny-closed; the pump logs it so a new writer cannot publish
	// under a family nobody classified.
	WorkEventFamilyUnknown WorkEventFamily = "unknown"
)

// ClassifyWorkEventFamily maps one durable event type to its family. The
// prefixes are the exact vocabularies written by this module's apply paths;
// anything else is Unknown, never Work by default.
func ClassifyWorkEventFamily(eventType string) WorkEventFamily {
	switch {
	case strings.HasPrefix(eventType, "work.message."),
		strings.HasPrefix(eventType, "work.handoff."),
		strings.HasPrefix(eventType, "work.decision.request."):
		return WorkEventFamilyCommunication
	case strings.HasPrefix(eventType, "work.protocol."):
		return WorkEventFamilyProtocol
	case strings.HasPrefix(eventType, "work.item."),
		strings.HasPrefix(eventType, "work.lease."),
		strings.HasPrefix(eventType, "work.owner."),
		strings.HasPrefix(eventType, "work.dependency."),
		strings.HasPrefix(eventType, "work.acceptance."),
		strings.HasPrefix(eventType, "work.binding."),
		eventType == "work.decision.recorded":
		return WorkEventFamilyWork
	default:
		return WorkEventFamilyUnknown
	}
}

// WorkOutboxCandidate is the payload-free projection of one claimable outbox
// row the policy sees before the claim transaction touches it.
type WorkOutboxCandidate struct {
	TenantID      model.TenantID
	WorkspaceID   model.ID
	EventID       model.ID
	AggregateKind string
	AggregateID   model.ID
	Sequence      int64
	Type          string
	Family        WorkEventFamily
}

// WorkOutboxClaimPolicy is consulted inside the claim transaction for every
// candidate that is otherwise ready. false skips the candidate without
// touching its row; an error aborts the drain as "could not look" so a policy
// that cannot decide never lets a gated effect through by accident.
type WorkOutboxClaimPolicy interface {
	AllowWorkOutboxClaim(context.Context, WorkOutboxCandidate) (bool, error)
}

// WorkOutboxClaimPolicyFunc adapts a function to WorkOutboxClaimPolicy.
type WorkOutboxClaimPolicyFunc func(context.Context, WorkOutboxCandidate) (bool, error)

// AllowWorkOutboxClaim implements WorkOutboxClaimPolicy.
func (f WorkOutboxClaimPolicyFunc) AllowWorkOutboxClaim(
	ctx context.Context, candidate WorkOutboxCandidate,
) (bool, error) {
	return f(ctx, candidate)
}

// WorkOutboxEffectPolicy is the optional second boundary of a claim policy.
// The claim boundary runs inside the claim transaction; the effect boundary
// runs after that transaction committed and immediately before the sink is
// invoked, so an authority withdrawn between the two (a pump stopped, a
// leadership fence lost) still holds the effect. A refusal returns the claim
// to the outbox through the ordinary settlement path without a delivery
// attempt; an error is "could not look" and is treated the same way.
type WorkOutboxEffectPolicy interface {
	AllowWorkOutboxEffect(context.Context, WorkOutboxCandidate) (bool, error)
}

// ErrWorkOutboxAuthorityWithdrawn is the settlement cause recorded when the
// composed authority refused a claimed candidate at the effect boundary. No
// bytes reached the sink; the row returns to pending under the ordinary
// backoff and is retried once the authority is present again.
var ErrWorkOutboxAuthorityWithdrawn = errors.New("sessions: work outbox authority withdrawn between claim and effect")

// UseWorkOutboxClaimAuthority late-binds the composition root's MANDATORY
// outbox authority. Every production drain (the post-commit nudge in Apply,
// the public DrainWorkOutbox and the periodic pump) consults it for the
// communication and unknown families before any caller-supplied restriction;
// a caller policy can only narrow the result further. Nil or a typed nil
// unbinds it, which on a composed module holds those families deny-closed.
func (m *Module) UseWorkOutboxClaimAuthority(authority WorkOutboxClaimPolicy) {
	if !communicationPortBound(authority) {
		m.workOutboxAuthority = nil
		return
	}
	m.workOutboxAuthority = authority
}

// WorkOutboxClaimAuthorityBound reports whether the composition root bound the
// mandatory authority, for boot logs and tests. It does not evaluate it.
func (m *Module) WorkOutboxClaimAuthorityBound() bool {
	return communicationPortBound(m.workOutboxAuthority)
}

// DrainWorkOutboxWithPolicy is DrainWorkOutbox with an additional caller
// restriction. The module's mandatory authority applies first on every drain;
// the caller policy is consulted only for candidates the authority allowed,
// so it can hold more back but never release a family the authority holds.
// A nil policy adds no restriction.
func (m *Module) DrainWorkOutboxWithPolicy(
	ctx context.Context,
	tenant model.TenantID,
	limit int,
	policy WorkOutboxClaimPolicy,
) error {
	return m.drainWorkOutboxWithDataAndPolicy(ctx, m.workData(tenant), tenant, limit, true, policy)
}

// workOutboxPolicy is the policy every drain actually runs: the module's
// baseline (family routing plus the bound authority) AND the caller's optional
// restriction, at both the claim and the effect boundary.
type workOutboxPolicy struct {
	module *Module
	caller WorkOutboxClaimPolicy
}

func (m *Module) composeWorkOutboxPolicy(caller WorkOutboxClaimPolicy) *workOutboxPolicy {
	if !communicationPortBound(caller) {
		caller = nil
	}
	return &workOutboxPolicy{module: m, caller: caller}
}

// AllowWorkOutboxClaim implements WorkOutboxClaimPolicy.
func (p *workOutboxPolicy) AllowWorkOutboxClaim(
	ctx context.Context, candidate WorkOutboxCandidate,
) (bool, error) {
	allow, err := p.module.workOutboxBaselineClaim(ctx, candidate)
	if err != nil || !allow {
		return false, err
	}
	if p.caller != nil {
		return p.caller.AllowWorkOutboxClaim(ctx, candidate)
	}
	return true, nil
}

// AllowWorkOutboxEffect implements WorkOutboxEffectPolicy.
func (p *workOutboxPolicy) AllowWorkOutboxEffect(
	ctx context.Context, candidate WorkOutboxCandidate,
) (bool, error) {
	allow, err := p.module.workOutboxBaselineEffect(ctx, candidate)
	if err != nil || !allow {
		return false, err
	}
	if p.caller != nil {
		if effect, ok := p.caller.(WorkOutboxEffectPolicy); ok {
			return effect.AllowWorkOutboxEffect(ctx, candidate)
		}
	}
	return true, nil
}

// workOutboxBaselineClaim routes a candidate by family. K1/K2 work facts and
// the K5 protocol plane keep their historical unconditional recovery on every
// entry point. The communication family and anything nobody classified go to
// the bound authority; a composed module (one with any K3 readiness witness
// bound, which every production boot is) without an authority holds them
// deny-closed. Only a standalone module with no K3 composition at all keeps
// the historical unconditional drain, which is what its pre-K3 tests exercise.
func (m *Module) workOutboxBaselineClaim(ctx context.Context, candidate WorkOutboxCandidate) (bool, error) {
	switch candidate.Family {
	case WorkEventFamilyWork, WorkEventFamilyProtocol:
		return true, nil
	default:
		if communicationPortBound(m.workOutboxAuthority) {
			return m.workOutboxAuthority.AllowWorkOutboxClaim(ctx, candidate)
		}
		if m.communicationReadinessComposed() {
			return false, nil
		}
		return true, nil
	}
}

// workOutboxBaselineEffect re-asks the authority at the effect boundary for
// the families it governs, when the authority offers that boundary.
func (m *Module) workOutboxBaselineEffect(ctx context.Context, candidate WorkOutboxCandidate) (bool, error) {
	switch candidate.Family {
	case WorkEventFamilyWork, WorkEventFamilyProtocol:
		return true, nil
	default:
		if !communicationPortBound(m.workOutboxAuthority) {
			// The claim boundary already decided for a module without an
			// authority; nothing changed in between that this module can see.
			return true, nil
		}
		effect, ok := m.workOutboxAuthority.(WorkOutboxEffectPolicy)
		if !ok {
			return true, nil
		}
		return effect.AllowWorkOutboxEffect(ctx, candidate)
	}
}

func workOutboxCandidateFromEvent(tenant model.TenantID, event model.Record) WorkOutboxCandidate {
	eventType := event.String(colEventType)
	return WorkOutboxCandidate{
		TenantID: tenant, WorkspaceID: model.ID(event.String(colWorkWorkspaceID)),
		EventID: model.ID(event.String(colEventID)), AggregateKind: event.String(colEventAggregateKind),
		AggregateID: model.ID(event.String(colEventAggregateID)), Sequence: event.Int(colEventSeq),
		Type: eventType, Family: ClassifyWorkEventFamily(eventType),
	}
}
