// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
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

// handleRegisterAgent creates a lifecycle row with kind=agent and mandatory
// sponsor. Deny-closed: no sponsor → 400. Subsystem G.
func (m *Module) handleRegisterAgent(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	var in AgentRegistration
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&in); err != nil || dec.More() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid JSON body"))
		return
	}
	in.IdentityRef = strings.TrimSpace(in.IdentityRef)
	in.Source = strings.TrimSpace(in.Source)
	in.SponsorRef = strings.TrimSpace(in.SponsorRef)
	in.Criticality = strings.TrimSpace(strings.ToLower(in.Criticality))

	if in.IdentityRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("identity_ref is required"))
		return
	}
	// Deny-closed: a sponsor is mandatory for agent identities.
	if in.SponsorRef == "" {
		writeJSON(w, http.StatusBadRequest, errorBody(ErrAgentRequiresSponsor.Error()))
		return
	}
	if in.Criticality != "" && !validRiskTier(in.Criticality) {
		writeJSON(w, http.StatusBadRequest, errorBody("criticality must be one of low, medium, high, critical"))
		return
	}

	var (
		clientErr  string
		httpStatus int
		promoted   bool // true = promoted existing NHI row; false = created new row
	)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		// Validate the sponsor is a human identity in the roster.
		found, human, _, verr := resolveHumanIdentity(r.Context(), sc, in.SponsorRef)
		if verr != nil {
			return verr
		}
		if !found {
			clientErr = "sponsor identity " + in.SponsorRef + " is not in the roster; sync it first"
			httpStatus = http.StatusBadRequest
			return nil
		}
		if !human {
			clientErr = "sponsor identity " + in.SponsorRef + " is not a human identity (sponsor must be an accountable person)"
			httpStatus = http.StatusBadRequest
			return nil
		}

		// Check if this identity already has a lifecycle row.
		repo, rerr := sc.Ext(nhiLifecycleKind)
		if rerr != nil {
			return rerr
		}
		existing, existFound, ferr := findOne(r.Context(), repo, eq(colNHIIdentityRef, in.IdentityRef))
		if ferr != nil {
			return ferr
		}
		if existFound {
			if existing.String(colNHIKind) == NHIKindAgent {
				clientErr = "agent identity " + in.IdentityRef + " already registered"
				httpStatus = http.StatusConflict
				return nil
			}
			// Existing non-agent NHI: promote to agent by setting kind + sponsor.
			existing[colNHIKind] = NHIKindAgent
			existing[colNHISponsorRef] = in.SponsorRef
			existing[colNHISponsorActor] = mc.Principal.Actor()
			existing[colNHIOrphaned] = false
			if in.Source != "" {
				existing[colNHISource] = in.Source
			}
			if in.Criticality != "" {
				existing[colNHICriticality] = in.Criticality
			}
			if _, uerr := repo.Update(r.Context(), existing); uerr != nil {
				return uerr
			}
			if err := m.recordLifecycleEvent(r.Context(), sc, in.IdentityRef,
				evtAgentRegistered, mc.Principal.Actor(), mc.Principal.UserID.String(),
				fmt.Sprintf("promoted to agent; sponsor=%s", in.SponsorRef)); err != nil {
				return err
			}
			if err := auditEvent(r.Context(), sc, mc, "governance.agent.promote", nhiLifecycleKind, "", map[string]any{
				"identity_ref": in.IdentityRef, "sponsor_ref": in.SponsorRef, "source": in.Source,
			}); err != nil {
				return err
			}
			promoted = true
			return nil
		}

		// Create new lifecycle row with kind=agent.
		rec := newLifecycleRecord(in.IdentityRef, in.Source, NHIKindAgent)
		rec[colNHISponsorRef] = in.SponsorRef
		rec[colNHISponsorActor] = mc.Principal.Actor()
		if in.Criticality != "" {
			rec[colNHICriticality] = in.Criticality
		}
		if _, cerr := repo.Create(r.Context(), rec); cerr != nil {
			if isConflict(cerr) {
				clientErr = "agent identity " + in.IdentityRef + " already registered"
				httpStatus = http.StatusConflict
				return nil
			}
			return cerr
		}
		if err := m.recordLifecycleEvent(r.Context(), sc, in.IdentityRef,
			evtAgentRegistered, mc.Principal.Actor(), mc.Principal.UserID.String(),
			fmt.Sprintf("agent registered; sponsor=%s source=%s", in.SponsorRef, in.Source)); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "governance.agent.register", nhiLifecycleKind, "", map[string]any{
			"identity_ref": in.IdentityRef, "sponsor_ref": in.SponsorRef, "source": in.Source,
		})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if clientErr != "" {
		writeJSON(w, httpStatus, errorBody(clientErr))
		return
	}
	if promoted {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusCreated)
	}
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
// orphaned/blocked/offboarded agent, or an absent/non-human/disabled sponsor).
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
	disabled, _ := sponsor.Metadata["disabled"].(bool)
	if principalType != "human" || disabled {
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
