// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Agent identity event types.
const (
	evtAgentRegistered     = "agent_registered"
	evtAgentSponsorChanged = "agent_sponsor_changed"
)

// ErrAgentRequiresSponsor is returned when an agent identity is registered or
// updated without a human sponsor (deny-closed — an agent without a sponsor IS
// the gap this product exists to close).
var ErrAgentRequiresSponsor = errors.New("governance: agent identity requires a human sponsor (deny-closed)")

// AgentRegistration is the input for registering an agent identity.
type AgentRegistration struct {
	IdentityRef string `json:"identity_ref"`
	Source      string `json:"source"`
	SponsorRef  string `json:"sponsor_ref"`
	Criticality string `json:"criticality,omitempty"`
}

// CheckAgentForExchange validates that the named agent is eligible for a
// token-exchange delegation (agent-OBO). It satisfies the auth.AgentLifecycleChecker
// interface (defined in core/auth) and is wired by the composition root.
//
// Returns nil iff the agent:
//   - exists in the NHI lifecycle store with kind=agent
//   - is not orphaned (sponsor disabled or missing)
//   - is not blocked by an enforcement policy
//   - has a sponsor_ref that matches sponsorRef (the exchange subject's externalId)
//
// Deny-closed: a missing, orphaned, blocked, or sponsor-mismatched agent is refused.
func (m *Module) CheckAgentForExchange(ctx context.Context, tenant model.TenantID, agentRef, sponsorRef string) error {
	rec, found, err := m.AgentLifecycleRecord(ctx, tenant, agentRef)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("agent %q not found or not registered as an agent identity", agentRef)
	}
	if rec.Bool(colNHIOrphaned) {
		return fmt.Errorf("agent %q is orphaned (sponsor disabled or missing)", agentRef)
	}
	if rec.String(colNHIEnforce) == enforceBlocked {
		return fmt.Errorf("agent %q is blocked by enforcement policy", agentRef)
	}
	storedSponsor := rec.String(colNHISponsorRef)
	if sponsorRef == "" || storedSponsor != sponsorRef {
		return fmt.Errorf("sponsor mismatch: subject is not the registered sponsor of agent %q", agentRef)
	}
	return nil
}

// AgentEligibleForWork reports whether identityRef is an active first-class
// agent identity whose human sponsor is still live. It is the narrow read seam
// used by composition roots that need lifecycle eligibility without importing
// governance records or their private column vocabulary.
//
// A negative answer is authoritative (missing lifecycle row, non-agent row,
// orphaned/blocked/offboarded agent, or an absent/non-human/disabled sponsor
// or unknown sponsor account state).
// An error means the lifecycle or roster could not be read, so callers can fail
// closed without misreporting an outage as an ineligible participant.
func (m *Module) AgentEligibleForWork(ctx context.Context, tenant model.TenantID, identityRef string) (eligible bool, err error) {
	if m == nil || m.data == nil {
		return false, errors.New("governance: agent lifecycle is unavailable")
	}
	identityRef = strings.TrimSpace(identityRef)
	if identityRef == "" {
		return false, nil
	}
	err = m.data.View(ctx, tenant, func(sc store.Scope) error {
		var inner error
		eligible, inner = agentEligibleForWorkInScope(ctx, sc, identityRef)
		return inner
	})
	return eligible, err
}

// AgentWorkAuthorityFactsInScope observes the exact lifecycle and sponsor rows behind
// a positive work-eligibility decision. The composition root adds core identity
// and agent facts, hashes the opaque set, and later asks the store to pin their
// versions on the WorkItem transaction. No governance row vocabulary crosses
// into sessions.
func (m *Module) AgentWorkAuthorityFactsInScope(
	ctx context.Context,
	sc store.Scope,
	identityRef string,
) (bool, []store.AuthorizationFactRef, error) {
	if m == nil || sc == nil {
		return false, nil, errors.New("governance: agent lifecycle scope is unavailable")
	}
	return agentWorkAuthorityFactsInScope(ctx, sc, strings.TrimSpace(identityRef))
}

func agentEligibleForWorkInScope(
	ctx context.Context,
	sc store.Scope,
	identityRef string,
) (bool, error) {
	eligible, _, err := agentWorkAuthorityFactsInScope(ctx, sc, identityRef)
	return eligible, err
}

func agentWorkAuthorityFactsInScope(
	ctx context.Context,
	sc store.Scope,
	identityRef string,
) (bool, []store.AuthorizationFactRef, error) {
	if identityRef == "" {
		return false, nil, nil
	}
	repo, err := sc.Ext(nhiLifecycleKind)
	if err != nil {
		return false, nil, err
	}
	rec, found, err := findOne(ctx, repo, eq(colNHIIdentityRef, identityRef))
	if err != nil || !found {
		return false, nil, err
	}
	if rec.String(colNHIIdentityRef) != identityRef || rec.String(colNHIKind) != NHIKindAgent {
		return false, nil, nil
	}
	sponsorRef := strings.TrimSpace(rec.String(colNHISponsorRef))
	if sponsorRef == "" {
		return false, nil, nil
	}
	sponsor, found, err := exactWorkSponsorIdentity(ctx, sc, sponsorRef)
	if err != nil || !found {
		return false, nil, err
	}
	if rec.String(colNHIKind) != NHIKindAgent || rec.Bool(colNHIOrphaned) ||
		rec.String(colNHIEnforce) == enforceBlocked ||
		rec.String(colNHIOffboard) != offboardNone {
		return false, nil, nil
	}
	if sponsor.ExternalID != sponsorRef {
		return false, nil, nil
	}
	principalType, _ := sponsor.Metadata["principal_type"].(string)
	disabled, reported := sponsor.Metadata["disabled"].(bool)
	if principalType != "human" || !reported || disabled {
		return false, nil, nil
	}
	return true, []store.AuthorizationFactRef{
		{Kind: "core.identity", ID: sponsor.ID, Version: sponsor.Version},
		{Kind: nhiLifecycleKind, ID: model.ID(rec.String(model.ColID)), Version: rec.Int(model.ColVersion)},
	}, nil
}

// exactWorkSponsorIdentity is deliberately narrower than
// identityByExternalID. The latter tolerates duplicates because its callers are
// idempotent roster UPSERTs. A sponsor is an authorization subject, so choosing
// either of two rows would assign accountability arbitrarily.
func exactWorkSponsorIdentity(
	ctx context.Context,
	sc store.Scope,
	ref string,
) (model.Identity, bool, error) {
	list, _, err := sc.Identities().List(ctx, model.Query{
		Filters: []model.Filter{eq("external_id", ref)}, Limit: 2,
	})
	if err != nil {
		return model.Identity{}, false, err
	}
	if len(list) != 1 {
		return model.Identity{}, false, nil
	}
	return list[0], true, nil
}

// AgentLifecycleRecord returns the NHI lifecycle record for an agent identity.
// Returns (record, true, nil) if found with kind=agent; (nil, false, nil) if not
// found or not an agent. Downstream tasks use this to validate agent existence and
// lifecycle status without going through HTTP.
func (m *Module) AgentLifecycleRecord(ctx context.Context, tenant model.TenantID, identityRef string) (model.Record, bool, error) {
	var (
		rec   model.Record
		found bool
	)
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(nhiLifecycleKind)
		if err != nil {
			return err
		}
		r, ok, err := findOne(ctx, repo, eq(colNHIIdentityRef, identityRef))
		if err != nil {
			return err
		}
		if ok && r.String(colNHIKind) == NHIKindAgent {
			rec, found = r, true
		}
		return nil
	}); err != nil {
		return nil, false, err
	}
	return rec, found, nil
}
