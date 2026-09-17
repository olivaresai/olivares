// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The EFFECT-BOUNDARY authority check.
//
// ⛔ WHY THIS EXISTS, measured and not theorised. An independent review drove a
// real child through the real store and proved that after a successor took the
// durable Claim (fence 1 → 2), the superseded launch could still write a turn,
// interrupt one, end the process, and answer a provider approval with a GRANT.
// Every one of those crossed the process boundary. The reason was the same in all
// four: the checks that existed were about the RUN — its state, its live handle,
// its launch generation on a row read moments earlier — and none of them put the
// CLAIM row in the write set of the same transaction that authorised the effect.
//
// A read of the current fence is not a check. Comparing the live fence against a
// fresh read of the live fence compares a value with itself (claim.go says so at
// length); what proves authority is presenting the holder and fence THIS launch
// was started under and letting the store refuse if they have moved. That is
// exactly what authorizedMutate + fenceWithin already do for governed writes, so
// this adds no new authority mechanism: it puts the existing one in front of the
// external effects that were reaching the child without it.
//
// It is deliberately NOT applied to the runtime's own cleanup. stopAllRuns, the
// kill-switch sweep, the duration ceiling and teardownLiveWithContext are acts of
// the RUNTIME upon a process it owns, not acts of a holder; fencing those would
// leave a child alive precisely when its authority is gone. Operator controls are
// fenced; reaping our own children is not. The two are different powers and the
// split is the point.

// assertRunAuthority re-proves, against the durable store and in ONE transaction,
// that this owned launch is still the authority for its session.
//
// Four things are checked as a unit, because any one of them alone is a hole:
//
//  1. the registry still holds THIS liveRun for the run (a successor incarnation
//     installs a new handle at the same key);
//  2. the durable Claim still answers to the holder and fence this launch was
//     started under — and the claim row joins this transaction's write set, so a
//     takeover committing concurrently makes this refuse rather than race;
//  3. the run row still carries this launch generation (`runtime_launch_id`);
//  4. the run row still carries the profile this process was launched under.
//
// A refusal is a decision, not an outage: authorizedMutate maps a moved or lapsed
// claim to 403 and a moved generation to 409, and the caller must NOT perform its
// effect afterwards.
func (m *Module) assertRunAuthority(ctx context.Context, lr *liveRun) error {
	if lr == nil {
		return conflictErr("no supervised launch to authorise")
	}
	if current, ok := m.rt.getLive(lr.tenant, lr.runRef); !ok || current != lr {
		// Cheap and first: a handle that is no longer the registered one cannot be
		// rescued by any durable check, and asking the store about it would only
		// tell us about its successor.
		return conflictErr("this launch is no longer the runtime's registered owner of the session")
	}
	return m.authorizedMutate(ctx, lr.tenant, lr.claim, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(ctx, repo, lr.runRef)
		if err != nil {
			return err
		}
		if err := assertLaunchOwnsRow(lr, rec); err != nil {
			return err
		}
		return assertCurrentProfileHolds(ctx, sc, rec)
	})
}

// assertCurrentProfileHolds reads the profile row the run names, IN THIS
// TRANSACTION, and refuses if it is gone or retired.
//
// ⛔ WHY IT IS A SECOND READ AND NOT A COMPARISON. assertLaunchOwnsRow already
// proves the run still names the profile id this process was launched under —
// but that is an IDENTITY check, and retirement does not change the id. It flips
// `state` on the profile row and frees the home slot for a NEW profile id, so
// after `POST /provider-profiles/{ref}/retire` every identity comparison in this
// file still succeeded and every holder effect still crossed to the child: an
// independent review drove a real child through the real store and measured a
// pending approval answering `approved`, a fenced interrupt sending
// turn/interrupt, a fenced text sending turn/start, and a fenced stop ending the
// process — all after the profile had been retired. Retirement was enforced when
// a process was STARTED (resolveLaunchableProfile) or CONTINUED
// (revalidateStoredProfile) and never while one was already running.
//
// Authorization linearizes at the fresh current-profile read inside this
// authorizedMutate attempt, provided the transaction later commits successfully.
// A retirement committed before that read is observed and refused. A retirement
// committed after that read is ordered after this authorization, even if it
// commits before the external effect. The profile read does not lock the profile
// row, the Claim write does not conflict with a profile update, and this check
// cannot recall an already authorized operation. External I/O occurs only after
// the authority transaction commits, so no database lock is held across provider
// I/O.
//
// ⛔ AND IT CHECKS EXACTLY ONE STATE. The neighbouring lifecycle facts are
// deliberately NOT revocation directions, and turning them into ones would be a
// silent product change rather than a fix:
//
//   - `disabled` blocks a NEW launch or resume and leaves a live child alone
//     (provider_profile.go). An operator disabling a profile is closing a door,
//     not killing what is already through it.
//   - the display name and `auth_source` are re-decided for the NEXT launch; the
//     running child keeps the source its own launch was authorised under.
//   - driver, environment, config home and user home are immutable identity, so
//     they cannot move under a live run at all.
//
// It is also NOT on the runtime's own cleanup path: stopAllRuns, the kill-switch
// sweep, the duration ceiling and teardownLiveWithContext never call
// assertRunAuthority, so reaping a child whose profile was retired still works.
// Revoking the holder and stranding the process would be the worst of both.
func assertCurrentProfileHolds(ctx context.Context, sc store.Scope, rec model.Record) error {
	ref := rec.String(colRunProfileID)
	if ref == "" {
		// A legacy/unprofiled run answers to no profile. Refusing here would revoke
		// every historical Claude run in the estate.
		return nil
	}
	prof, err := findProfileRec(ctx, sc, ref)
	if err != nil {
		if errors.Is(err, ErrProfileNotFound) {
			// Deny-closed on ABSENCE, and it is a refusal rather than a 404: the run
			// exists and is supervised; what is gone is the authority behind it.
			return conflictErr("the provider profile this process was launched under no longer exists")
		}
		// Anything else is "I could not look", and it must stay that way: acting on
		// the identity we cached would be exactly the check this function replaces.
		return err
	}
	if prof.String(colPPState) == ProfileRetired {
		return conflictErr("the provider profile this process was launched under has been retired")
	}
	return nil
}

// assertLaunchOwnsRow compares the durable row against what this launch believes
// it owns. It is the in-transaction half of assertRunAuthority and is shared with
// the alias capture, which has always made the same comparison.
func assertLaunchOwnsRow(lr *liveRun, rec model.Record) error {
	if err := guardRuntimeLaunch(lr.launchID)(rec); err != nil {
		return err
	}
	if lr.claim.SID != "" && rec.String(colRunClaimSID) != lr.claim.SID {
		return conflictErr("the run's claim is not the one this process was launched under")
	}
	if lr.profile != nil && rec.String(colRunProfileID) != lr.profile.ProfileID {
		return conflictErr("the run's persisted profile is not the one this process was launched under")
	}
	return nil
}
