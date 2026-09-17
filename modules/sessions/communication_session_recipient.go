// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// CommunicationSessionRecipientWitness is the typed, payload-free projection
// the composition root's K3 directory adapter consumes to decide whether a
// communication-session recipient is a live principal of one workspace. It
// carries the exact identity tuple (SID, Claim fence, workspace, operated run
// and its agent attribution) and nothing else: no holder, no token, no run
// payload. The session plane owns every fact in it; cmd never reads the claim,
// alias or run tables directly.
type CommunicationSessionRecipientWitness struct {
	// SID is the canonical session id the witness describes.
	SID string
	// Found is true when the sid names a canonical identity that was NOT merged
	// away. A merged sid is deliberately reported absent: a K3 recipient is an
	// exact identity, and answering as the surviving session would deliver to a
	// recipient the sender never named.
	Found bool
	// WorkspaceEligible is true when the identity's effective workspace equals
	// the requested one (an unscoped identity resolves to the tenant default).
	WorkspaceEligible bool
	// Active is true when a live Claim exists at observation time.
	Active bool
	// Fence is the live Claim's fence; zero when Active is false.
	Fence int64
	// ClaimExpiresAt is the live lease deadline; zero when Active is false.
	ClaimExpiresAt time.Time
	// RunRef is the operated run this session was resolved from, or empty when
	// the plane did not launch it or the operated alias is ambiguous.
	RunRef string
	// AgentRef is the run's authenticated agent attribution (the identity's
	// stable external id), or empty when RunRef is empty or the run carries none.
	AgentRef string
	// ObservedAt is the module clock instant of the observation.
	ObservedAt time.Time
}

// CommunicationSessionRecipient resolves one canonical sid to its K3 recipient
// witness. It answers "I could not look" (an error) only for store failures;
// an unknown, merged, unscoped-elsewhere or unclaimed session is a measured
// negative answer, never an error, so the caller can distinguish ineligible
// from unavailable.
func (m *Module) CommunicationSessionRecipient(
	ctx context.Context,
	tenant model.TenantID,
	workspace model.ID,
	sid string,
) (CommunicationSessionRecipientWitness, error) {
	out := CommunicationSessionRecipientWitness{SID: sid, ObservedAt: m.now()}
	if m == nil || m.data == nil {
		return out, unknown("evidence_unavailable", errors.New("sessions: session identity plane is not wired"))
	}
	if !validCanonicalSID(sid) || workspace.IsZero() {
		return out, nil
	}
	var (
		found     bool
		scoped    model.ID
		defaultWS model.ID
		runRef    string
		agentRef  string
	)
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		target, err := resolveMerge(ctx, sc, sid)
		if err != nil {
			return err
		}
		if target != sid {
			return nil
		}
		rec, ok, err := findIdentity(ctx, sc, sid)
		if err != nil || !ok {
			return err
		}
		found = true
		if raw := rec.String(colIDWorkspaceID); raw != "" {
			id, perr := model.ParseID(raw)
			if perr != nil {
				return unknown("evidence_unavailable", perr)
			}
			scoped = id
		}
		if scoped.IsZero() {
			ws, err := sc.DefaultWorkspace(ctx)
			if err != nil {
				return err
			}
			defaultWS = ws.ID
		}
		ref, ok, err := operatedRunRef(ctx, sc, sid)
		if err != nil || !ok {
			return err
		}
		runs, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		run, err := findRunRec(ctx, runs, ref)
		if err != nil {
			var re *runErr
			if errors.As(err, &re) && re.status == http.StatusNotFound {
				return nil
			}
			return err
		}
		runRef, agentRef = ref, run.String(colRunAgentRef)
		return nil
	}); err != nil {
		return out, err
	}
	if !found {
		return out, nil
	}
	effective := scoped
	if effective.IsZero() {
		effective = defaultWS
	}
	out.Found = true
	out.WorkspaceEligible = !effective.IsZero() && effective == workspace
	out.RunRef, out.AgentRef = runRef, agentRef
	lease, live, err := m.ActiveClaim(ctx, tenant, sid)
	if err != nil {
		return CommunicationSessionRecipientWitness{SID: sid, ObservedAt: out.ObservedAt}, err
	}
	if live {
		out.Active, out.Fence, out.ClaimExpiresAt = true, lease.Fence, lease.ExpiresAt
	}
	return out, nil
}
