// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// SessionWorkParticipantWithin is a composition authority query, not a data
// accessor. The engine supplies its tenant-pinned Mutate scope; the module
// returns only eligibility for the requested SID/workspace. Request data stays
// confined. An observed Claim lapse is persisted before a negative answer.
func (m *Module) SessionWorkParticipantWithin(ctx context.Context, sc store.Scope, workspace model.ID, sid string) (Participant, error) {
	witness, err := m.sessionAuthorityWitnessWithin(ctx, sc, workspace, sid, true, false)
	out := Participant{Kind: "session"}
	if err == nil && witness.Found {
		out.CanonicalRef, out.WorkspaceEligible, out.Active = sid, witness.WorkspaceEligible, witness.Active
	}
	return out, err
}

// CommunicationSessionRecipientWithin has the same bounded authority query,
// but an exact recipient never follows a merge and its witness also carries
// the operated run/agent tuple. A crossed workspace returns no private tuple.
func (m *Module) CommunicationSessionRecipientWithin(ctx context.Context, sc store.Scope, workspace model.ID, sid string) (CommunicationSessionRecipientWitness, error) {
	return m.sessionAuthorityWitnessWithin(ctx, sc, workspace, sid, false, true)
}

func (m *Module) sessionAuthorityWitnessWithin(ctx context.Context, sc store.Scope, workspace model.ID, sid string, followMerge, includeRun bool) (CommunicationSessionRecipientWitness, error) {
	out := CommunicationSessionRecipientWitness{SID: sid}
	if m == nil || sc == nil {
		return out, unknown("evidence_unavailable", errors.New("session authority scope is unavailable"))
	}
	out.ObservedAt = m.now()
	if !validCanonicalSID(sid) || workspace.IsZero() {
		return out, nil
	}
	target, err := resolveMerge(ctx, sc, sid)
	if err != nil || (!followMerge && target != sid) {
		return out, err
	}
	identity, found, err := findIdentity(ctx, sc, target)
	if err != nil || !found {
		return out, err
	}
	effective := model.ID(identity.String(colIDWorkspaceID))
	if effective.IsZero() {
		defaultWorkspace, err := sc.DefaultWorkspace(ctx)
		if err != nil {
			return out, err
		}
		effective = defaultWorkspace.ID
	} else if _, err := model.ParseID(effective.String()); err != nil {
		return out, unknown("evidence_unavailable", err)
	}
	if effective != workspace {
		return out, nil
	}
	out.Found, out.WorkspaceEligible = true, true
	claim, found, err := findClaim(ctx, sc, target)
	if err != nil {
		return out, err
	}
	if found && claimIsLive(claim, out.ObservedAt) {
		lease := leaseFrom(claim)
		out.Active, out.Fence, out.ClaimExpiresAt = true, lease.Fence, lease.ExpiresAt
	} else if observedLapse(claim, found, out.ObservedAt).seen {
		// This observation and retirement share a transaction. OCC refuses a
		// concurrent successor; no clock reread can revive the observed fence.
		repo, err := sc.Ext(claimKind)
		if err != nil {
			return out, err
		}
		claim[colClaimState] = claimExpired
		if _, err := repo.Update(ctx, claim); err != nil {
			return out, err
		}
	}
	if !includeRun {
		return out, nil
	}
	runRef, found, err := operatedRunRef(ctx, sc, target)
	if err != nil || !found {
		return out, err
	}
	runs, err := sc.Ext(runKind)
	if err != nil {
		return out, err
	}
	run, err := findRunRec(ctx, runs, runRef)
	if err != nil {
		var re *runErr
		if errors.As(err, &re) && re.status == http.StatusNotFound {
			return out, nil
		}
		return out, err
	}
	out.RunRef, out.AgentRef = runRef, run.String(colRunAgentRef)
	return out, nil
}
