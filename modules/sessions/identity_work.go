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

// The K2 work kernel's SESSION questions, answered by the plane that owns the
// facts instead of by the entity next door.
//
// Both questions used to be answered in the composition root against a core
// model.Session, keyed by the bare UUID inside a canonical sid
// (cmd/olivares/workkernel.go). That could never work: a sid is minted as
// "osn_" + a FRESH uuid (newSID), nothing in the tree ever creates a core
// Session with that primary key, and the SG-00 preamble in identity.go says in
// so many words that the core session is a DIFFERENT notion — which is the
// reason this plane exists. The result was that owner_kind="session" answered
// not-found before any authorization ran, so the half of the ownership model
// that this plane makes possible was unreachable in production.
//
// The hub decided on 2026-08-11 that the plane owns the workspace dimension
// rather than deriving it from the driving agent, and the reasoning generalizes
// to both answers below: a session is not its agent and is not a core Session,
// so asking either of them for a fact about it is how the original defect
// happened. Where a fact genuinely lives elsewhere — which agent drives an
// operated session — the plane RESOLVES it through its own alias rather than
// guessing a key.

// SessionWorkParticipant answers "is this canonical session a live participant
// eligible in this workspace" from the identity plane.
//
// Eligibility: the identity's own workspace, with NULL meaning the tenant's
// default workspace — the same soft-isolation model.Session.WorkspaceID and
// model.Agent.WorkspaceID already use, so an identity minted before the column
// existed reads as the safe default rather than as missing evidence.
//
// Liveness: a LIVE CLAIM. The plane's other liveness signal, last_seen_at, is a
// hint whose freshness window would be invented here (identity.go says so at
// touch), while the claim is the plane's own statement that a process holds this
// session right now — and it is already what admission trusts. A participant
// about to be handed durable execution authority is exactly the caller for whom
// "somebody is running this session" must be a fact and not a heuristic.
//
// It returns a NOT-eligible participant rather than an error when the identity
// is absent: the caller (checkParticipant) turns that into owner_ineligible,
// which is the honest answer for a sid nobody has ever resolved. An unreadable
// store still surfaces as an error, so "could not look" never reads as "no".
func (m *Module) SessionWorkParticipant(
	ctx context.Context,
	tenant model.TenantID,
	workspace model.ID,
	sid string,
) (Participant, error) {
	out := Participant{Kind: "session"}
	if m.data == nil {
		// Contradicting the contract three lines up would be worse than the bug:
		// an unwired plane is "I could not look", which checkParticipant turns into
		// evidence_unavailable, never into owner_ineligible.
		return out, unknown("evidence_unavailable", nil)
	}
	if !validCanonicalSID(sid) {
		// Measured, not unreadable: this is not a canonical sid, so it names no
		// session and is not eligible.
		return out, nil
	}
	var (
		found     bool
		scoped    model.ID
		resolved  string
		defaultWS model.ID
	)
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		// Follow any merge first: a session that was merged away must answer as
		// the surviving identity, or work scoped to it would be orphaned by a
		// bookkeeping operation.
		target, err := resolveMerge(ctx, sc, sid)
		if err != nil {
			return err
		}
		rec, ok, err := findIdentity(ctx, sc, target)
		if err != nil || !ok {
			return err
		}
		found, resolved = true, target
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
		return nil
	}); err != nil {
		return Participant{Kind: "session"}, err
	}
	if !found {
		return out, nil
	}
	effective := scoped
	if effective.IsZero() {
		effective = defaultWS
	}
	out.CanonicalRef = sid
	out.WorkspaceEligible = !effective.IsZero() && effective == workspace
	_, live, err := m.ActiveClaim(ctx, tenant, resolved)
	if err != nil {
		return Participant{Kind: "session"}, err
	}
	out.Active = live
	return out, nil
}

// SessionActsForAgent reports whether the agent identity agentRef is the agent
// driving this canonical session.
//
// It resolves the relation the plane actually records instead of guessing a key.
// An OPERATED session was minted by the runtime with the run's own ref as the
// alias external id (runtime.go admit -> ResolveSession with
// ProviderOperated), so sid -> alias -> run_ref -> sessions_run.agent_ref is a
// chain of facts this module wrote itself, every link under the UNIQUE index
// that makes SG-00 worth having.
//
// A session with no operated alias, or a run with no agent attribution, is NOT
// acting for anybody: false, no error. That is deny-closed and it is also true —
// an observed session the plane never launched has no agent it can vouch for.
func (m *Module) SessionActsForAgent(
	ctx context.Context,
	tenant model.TenantID,
	sid string,
	agentRef string,
) (bool, error) {
	if agentRef == "" || !validCanonicalSID(sid) || m.data == nil {
		return false, nil
	}
	acts := false
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		target, err := resolveMerge(ctx, sc, sid)
		if err != nil {
			return err
		}
		runRef, ok, err := operatedRunRef(ctx, sc, target)
		if err != nil || !ok {
			return err
		}
		runs, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		run, err := findRunRec(ctx, runs, runRef)
		if err != nil {
			var re *runErr
			if errors.As(err, &re) && re.status == http.StatusNotFound {
				return nil // the alias outlived its run row; it vouches for nobody
			}
			return err
		}
		acts = run.String(colRunAgentRef) == agentRef
		return nil
	})
	if err != nil {
		return false, err
	}
	return acts, nil
}

// operatedRunRef reads the run ref an operated session was resolved from, and
// refuses to guess when there is more than one.
//
// An earlier version took the first row with Limit: 1 and justified it with the
// UNIQUE index on (tenant, provider, external_id). That implication is FALSE and
// the direction matters: the index guarantees external_id -> sid, not
// sid + provider -> external_id. BindAlias exists precisely to attach additional
// provider ids to one session (identity.go), so two operated aliases for one sid
// are constructible, and an unordered Limit: 1 over them would answer
// SessionActsForAgent from whichever row came back first — possibly true for the
// agent of the run that was NOT the driver.
//
// No HTTP route can build that today (a run mints its own ref server-side), but
// BindAlias is exported, and a helper must not consume a guarantee the catalog
// does not make. Two rows is ambiguous EVIDENCE, not a false answer: UNKNOWN.
func operatedRunRef(ctx context.Context, sc store.Scope, sid string) (string, bool, error) {
	repo, err := sc.Ext(aliasKind)
	if err != nil {
		return "", false, err
	}
	rows, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{eq(colAliasSID, sid), eq(colProvider, ProviderOperated)},
		Limit:   2,
	})
	if err != nil || len(rows) == 0 {
		return "", false, err
	}
	if len(rows) > 1 {
		return "", false, unknown("evidence_unavailable", nil)
	}
	ref := rows[0].String(colExternalID)
	return ref, ref != "", nil
}
