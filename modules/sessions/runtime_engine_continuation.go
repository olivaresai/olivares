// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// engineContinuationMessage is what the engine's own resume tells the tool when the
// stop it resumes from cut a turn: the work was interrupted, not finished, and what it
// started may or may not have happened.
const engineContinuationMessage = "[Engine restart] Your previous turn was cut when the Olivares engine stopped. " +
	"A command it started may have finished or may still be running. Check its effects before you " +
	"repeat anything that changes state, then continue the interrupted work."

// engineContinuationStoppedDetail is the run ledger's line when the chain is bounded.
const engineContinuationStoppedDetail = "the cut turn was not continued: three continuations already ran in the last five minutes"

// A chain of restarts that each cut the continuation stops here, and says so.
const (
	engineContinuationMax    = 3
	engineContinuationWindow = 5 * time.Minute
)

// engineContinuationKey marks the input the engine's resume sends, so its turn row
// records source "continuation" (insertRunTurn).
type engineContinuationKey struct{}

func isEngineContinuation(ctx context.Context) bool {
	_, ok := ctx.Value(engineContinuationKey{}).(struct{})
	return ok
}

// engineContinuationFailedReason is the stop reason of a resume whose cut turn could not
// be continued before anything reached the tool; the uncertain one says it may have.
const (
	engineContinuationFailedReason    = "the cut turn could not be continued after the engine restarted; resume the session to continue"
	engineContinuationUncertainReason = "the cut turn could not be continued after the engine restarted, and the continuation may have reached the tool; check what it did, then resume the session to continue"
)

// continueCutTurn runs in the engine's own resume (never a person's, never the usage
// reset's), after the relaunch and before the queue drains: when the run's last turn
// was cut, the tool gets one continuation, and the queue waits for that turn to end as
// it waits for any other. The caller holds the run's lock. A continuation that cannot
// be read, recorded or sent ends the run stopped with a reason (endUncontinuedRun):
// a run left running with nothing in progress gives the person nothing to act on.
func (m *Module) continueCutTurn(ctx, wctx context.Context, tenant model.TenantID, runRef, actor, actorKind string, rec model.Record) error {
	if limit := storedRunLimit(rec); limit != nil && !m.limitLifted(limit) {
		return nil
	}
	cut, recent, err := m.cutTurnAndRecentContinuations(wctx, tenant, runRef)
	if err != nil {
		return m.endUncontinuedRun(wctx, tenant, runRef, actor, actorKind, rec, false, err)
	}
	if !cut {
		return nil
	}
	if recent >= engineContinuationMax {
		m.warnf("sessions: "+engineContinuationStoppedDetail, "run_ref", runRef)
		if _, err := m.transition(wctx, tenant, runRef, transitionInput{event: "continuation_stopped", actor: actor, actorKind: actorKind, keepReason: true, detail: engineContinuationStoppedDetail}); err != nil {
			m.warnf("sessions: the bounded continuation could not be recorded", "run_ref", runRef, "err", redactErr(err))
		}
		return nil
	}
	if _, err := m.transition(wctx, tenant, runRef, transitionInput{event: "continuation", actor: actor, actorKind: actorKind, keepReason: true, detail: engineContinuationMessage}); err != nil {
		return m.endUncontinuedRun(wctx, tenant, runRef, actor, actorKind, rec, false, err)
	}
	attempted, err := m.sendTextInputLoaded(context.WithValue(ctx, engineContinuationKey{}, struct{}{}), tenant, runRef, engineContinuationMessage, nil, rec)
	if err != nil {
		return m.endUncontinuedRun(wctx, tenant, runRef, actor, actorKind, rec, attempted, err)
	}
	return nil
}

// endUncontinuedRun stops the run through the ordinary stop, so it is recorded stopped
// with the reason, never left running with nothing in progress, and the person can
// resume it. attempted says the continuation may have reached the tool. The ordinary
// stop writes its "stopping" record before it touches the child: when the store keeps
// refusing writes (the same fault that failed the continuation) it never reaches the
// child, so the child is then ended through the effect half of the stop, which needs no
// write. It returns the cause, joined with the stop's own failure when there was one.
func (m *Module) endUncontinuedRun(wctx context.Context, tenant model.TenantID, runRef, actor, actorKind string, rec model.Record, attempted bool, cause error) error {
	m.warnf("sessions: the engine's resume could not continue the cut turn; the run is stopped for a person to resume",
		"run_ref", runRef, "delivery_uncertain", attempted, "err", redactErr(cause))
	reason := engineContinuationFailedReason
	if attempted {
		reason = engineContinuationUncertainReason
	}
	_, _, stopErr := m.stopRunLoaded(wctx, tenant, runRef, actor, actorKind, reason, rec)
	if stopErr != nil {
		stopErr = errors.Join(stopErr, m.reapUnstoppedChild(wctx, tenant, runRef, actor, reason))
		m.warnf("sessions: the run whose cut turn could not be continued was not stopped cleanly", "run_ref", runRef, "err", redactErr(stopErr))
		return errors.Join(cause, stopErr)
	}
	return cause
}

// reapUnstoppedChild ends the resumed child when the ordinary stop could not: the
// runtime reaping its own child is not an act of a holder, needs no ledger write, and
// leaves the bridge to record what it can. It does nothing when the run has no live
// child here or its handle has already finalized.
func (m *Module) reapUnstoppedChild(wctx context.Context, tenant model.TenantID, runRef, actor, reason string) error {
	lr, ok := m.rt.getLive(tenant, runRef)
	if !ok {
		return nil
	}
	select {
	case <-lr.finalizedCh:
		return nil
	default:
	}
	outcome, stopErr, revokeErr := m.stopLiveEffect(wctx, lr, reason, actor, legacyRevocationPhase(wctx))
	err := errors.Join(stopErr, revokeErr)
	if outcome != stopEffectFinalized {
		err = errors.Join(err, errors.New("sessions: the child was signalled but its run did not settle"))
	}
	return err
}

// cutTurnAndRecentContinuations reads whether the run's last turn was cut and how many
// continuations the engine sent this run within the window.
func (m *Module) cutTurnAndRecentContinuations(ctx context.Context, tenant model.TenantID, runRef string) (bool, int, error) {
	var cut bool
	var recent int
	err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
		last, err := lastRunTurn(ctx, sc, runRef)
		if err != nil || last == nil || last.State != "cut" {
			return err
		}
		cut = true
		repo, err := sc.Ext(runTurnKind)
		if err != nil {
			return err
		}
		since := model.NewTimestamp(m.now().Add(-engineContinuationWindow)).String()
		rows, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq("run_ref", runRef), eq("source", "continuation"), {Column: "started_at", Op: model.OpGte, Value: since}}, Limit: engineContinuationMax})
		recent = len(rows)
		return err
	})
	return cut, recent, err
}
