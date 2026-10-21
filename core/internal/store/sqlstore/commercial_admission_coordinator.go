// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
	"log/slog"
	"time"
)

// Post-module admission extends the closed graph with a completed Community
// predecessor. Historical migration callbacks keep their original declarations
// and transitive source hashes; canonical encoders and SQL writers are shared.
func guardAdmissionBridgePredecessor(
	ctx context.Context,
	mdb dialect.Execer,
	dia dialect.Dialect,
	m guardManifest,
	observerDSN string,
	hardened bool,
	session func(ctx context.Context) (rowQuerier, func(), error),
) (bool, error) {
	graph, err := guardAdmissionGraphFor(m)
	if err != nil {
		return false, err
	}
	edges, err := guardAdmissionPredecessorEdges(graph, m.CodeEpoch)
	if err != nil {
		return false, err
	}
	for _, edge := range edges {
		delta, derr := guardAdmissionDelta(guardAdmissionEdgeSource(edge), edge.To.CodeEpoch)
		if derr != nil {
			return false, derr
		}
		if !guardAdmissionModuleDelta(delta) {
			continue
		}
		predecessorHistory, perr := guardAdmissionVerifyHistory(ctx, mdb, dia, edge.From)
		if perr != nil {
			bridged, bridgeErr := guardAdmissionBridgePredecessor(ctx, mdb, dia, edge.From,
				observerDSN, hardened, session)
			if bridgeErr != nil {
				return false, bridgeErr
			}
			if bridged {
				predecessorHistory, perr = guardAdmissionVerifyHistory(ctx, mdb, dia, edge.From)
			}
		}
		if perr != nil || !guardEditionPostModuleSourceReady(edge, predecessorHistory) {
			continue
		}
		predecessorTables := make([]string, 0, len(edge.From.Specs))
		for _, spec := range edge.From.Specs {
			predecessorTables = append(predecessorTables, spec.Key.Relation)
		}
		if _, bridgeErr := guardAdmissionRunUnits(ctx, mdb, dia, predecessorTables,
			observerDSN, hardened, session); bridgeErr != nil {
			return false, fmt.Errorf("sqlstore: complete epoch-%d predecessor before epoch-%d transition: %w",
				edge.From.CodeEpoch, m.CodeEpoch, bridgeErr)
		}
		return true, nil
	}
	return false, nil
}

func guardAdmissionRunUnits(
	ctx context.Context,
	mdb dialect.Execer,
	dia dialect.Dialect,
	registered []string,
	observerDSN string,
	hardened bool,
	session func(ctx context.Context) (rowQuerier, func(), error),
) (guardRolloutSummary, error) {
	summary := guardRolloutSummary{
		Executed: map[unitIntent]int{},
		Outcomes: map[reconcileOutcome]int{},
	}
	m, err := buildGuardManifest(registered)
	if err != nil {
		return summary, err
	}
	if err := requireCompleteGuardCurrentEdition(m); err != nil {
		return summary, err
	}
	summary.Entries = len(m.Specs)
	history, err := guardAdmissionVerifyHistory(ctx, mdb, dia, m)
	if err != nil {
		bridged, bridgeErr := guardAdmissionBridgePredecessor(ctx, mdb, dia, m, observerDSN, hardened, session)
		if bridgeErr != nil {
			return summary, bridgeErr
		}
		if bridged {
			history, err = guardAdmissionVerifyHistory(ctx, mdb, dia, m)
		}
	}
	if err != nil {
		return summary, err
	}
	if history.Kind == guardEditionHistoryPredecessorV7 {
		history, err = guardAdmissionTransitionAfterModules(ctx, mdb, dia, m, history)
		if err != nil {
			return summary, err
		}
	}

	if dia.Name() != store.EnginePostgres {
		if !guardEditionHistoryCompletesV7(history.Kind) {
			return summary, fmt.Errorf("%w: SQLite guard history %q has no completed core v7 witness",
				ErrGuardManifestNoEdge, history.Kind)
		}
		return summary, nil
	}
	if !guardEditionHistoryCompletesV7(history.Kind) {
		return summary, fmt.Errorf("%w: PostgreSQL guard history %q has no completed core v7 witness",
			ErrGuardManifestNoEdge, history.Kind)
	}
	conn, ok := mdb.(*sql.Conn)
	if !ok {
		return summary, fmt.Errorf(
			"sqlstore: append-only guard runner: the PostgreSQL migration executor is %T, want *sql.Conn; every guarantee this runner makes depends on one session holding the coordination lock", mdb)
	}
	if session == nil {
		return summary, errors.New("sqlstore: append-only guard runner: no reconcile-session provider, so a commit whose acknowledgement is lost could never be resolved")
	}
	if err := verifyGuardFenceCapability(ctx, conn); err != nil {
		return summary, err
	}

	keys := make([]guardKey, 0, len(m.Specs))
	for _, spec := range m.Specs {
		keys = append(keys, spec.Key)
	}
	observed, err := projectGuardCatalogBatch(ctx, conn, keys)
	if err != nil {
		return summary, err
	}
	plans, refusals, err := buildGuardUnitPlans(m, observed)
	if err != nil {
		return summary, err
	}
	summary.PlannedUnits, summary.Refusals = len(plans), refusals

	rollout, gate, opened, err := guardAdmissionOpenOrVerifyGuardRollout(ctx, mdb, dia, m, history, plans, len(refusals) == 0)
	if err != nil {
		return summary, err
	}
	summary.RolloutID, summary.Phase, summary.Condition = rollout.RolloutID, gate.Phase, gate.Condition

	derived := plans
	plans, err = guardPlanFromEnumeration(m, gate.ExpectedUnits, gate.Units, observed)
	if err != nil {
		return summary, err
	}
	if opened {
		if aerr := guardPlansAgree(derived, plans); aerr != nil {
			return summary, fmt.Errorf("sqlstore: the guard rollout's durable enumeration does not rebuild the plan it recorded: %w", aerr)
		}
	}
	summary.PlannedUnits = len(plans)

	if err := guardAdmissionVerifyControlPlane(ctx, conn, dia, m); err != nil {
		return summary, err
	}

	receipts, err := guardRolloutReceipts(ctx, mdb, dia, rollout.RolloutID)
	if err != nil {
		return summary, err
	}

	if gate.Phase == gatePhaseReady {
		summary.FastPath = true
		if gate.Condition != gateConditionVerified && gate.Condition != gateConditionBlocked {
			summary.Diagnosis = "READY_CONDITION_UNKNOWN"
			summary.log()
			return summary, fmt.Errorf("%w: rollout %s is closed with condition %q, which this engine never writes under %q",
				ErrGuardGateIllegalTransition, rollout.RolloutID, gate.Condition, gatePhaseReady)
		}
		if err := verifyGuardCheckpoint(ctx, mdb, dia, rollout.RolloutID, gate); err != nil {
			summary.Diagnosis = "CHECKPOINT_MISMATCH"
			summary.log()
			return summary, err
		}
		if gate.Condition == gateConditionBlocked {
			summary.Diagnosis = gate.FirstBlocking.Code
			summary.log()
			return summary, fmt.Errorf("%w: rollout %s closed and then recorded drift (%s: %s). The objects are NOT re-verified before this refusal on purpose: the drift is the event, and a guard that looks correct again does not unmake it. Its durable diagnostic carries unblock_policy=operator, so clearing it is a deliberate human decision — inspect %s for this rollout",
				ErrGuardGateBlocked, rollout.RolloutID,
				gate.FirstBlocking.Code, gate.FirstBlocking.Details, guardGateEventsTable)
		}
		refusal, verr := verifyGuardTerminals(ctx, conn, dia, rollout, gate, plans, receipts, observed, &summary)
		if refusal != nil {
			if rerr := recordGuardVerificationFailure(ctx, conn, dia, rollout, refusal.UnitID, refusal.Refusal, gate.Phase); rerr != nil {
				slog.Warn("could not record a guard verification failure durably", "err", rerr)
			}
		}
		if verr != nil {
			summary.log()
			return summary, verr
		}
		summary.log()
		return summary, nil
	}

	if !gate.mayMutate() {
		summary.Diagnosis = gate.FirstBlocking.Code
		summary.log()
		return summary, fmt.Errorf("%w: rollout %s is %s/%s (%s: %s); this boot may read but not change anything",
			ErrGuardGateBlocked, rollout.RolloutID, gate.Phase, gate.Condition,
			gate.FirstBlocking.Code, gate.FirstBlocking.Details)
	}
	if len(refusals) > 0 {
		refusedUnit, uerr := guardUnitID(m.Format, refusals[0].Key, intentAdoptLegacy)
		if uerr != nil {
			slog.Warn("could not name the unit for a guard plan refusal", "err", uerr)
		} else if rerr := recordGuardVerificationFailure(ctx, conn, dia, rollout, refusedUnit, refusals[0], gate.Phase); rerr != nil {
			slog.Warn("could not record a guard plan refusal durably", "err", rerr)
		}
		summary.Diagnosis = refusals[0].Code
		summary.log()
		return summary, fmt.Errorf("sqlstore: the append-only guard rollout cannot proceed: %d of %d targets are unusable; the first is %s",
			len(refusals), len(m.Specs), refusals[0])
	}

	for _, p := range plans {
		if done, have := receipts[receiptLookupKey(p.UnitID, guardReceiptKindUnit)]; have {
			if verr := validateGuardReceipt(p, rollout, gate.Units[p.UnitID], done); verr != nil {
				summary.Diagnosis = "RECEIPT_NOT_THIS_UNIT"
				summary.log()
				return summary, fmt.Errorf("sqlstore: refusing to skip unit %s for %s: %w",
					p.Intent, p.Spec.Key, verr)
			}
			summary.Outcomes[outcomeApplied]++
			continue
		}
		if fold, ran := gate.Units[p.UnitID]; ran && fold.JudgedReadingValid {
			summary.Diagnosis = "JUDGED_WITHOUT_RECEIPT"
			summary.log()
			return summary, fmt.Errorf("%w: unit %s for %s has a judged reading and no receipt; those commit together, so this is a truncated history and re-running the unit would rebuild the attribution instead of reporting its absence",
				ErrGuardGateChainBroken, p.UnitID, p.Spec.Key)
		}
		predecessor := optDigest{}
		if p.Predecessor != "" {
			pred, ok := receipts[receiptLookupKey(p.Predecessor, guardReceiptKindUnit)]
			if !ok {
				return summary, fmt.Errorf("sqlstore: unit %s for %s requires the receipt of %s, which is not in the ledger; a lineage is verified, never assumed",
					p.UnitID, p.Spec.Key, p.Predecessor)
			}
			predecessor = someDigest(pred.ReceiptID)
		}
		r := &guardUnitRunner{dia: dia, conn: conn, rollout: rollout, plan: p, predecessor: predecessor, session: session}
		unit := retryUnit{
			Spec:             p.unitSpec(),
			Plan:             guardLockPlan(p.Spec.Key.Relation, p.Intent),
			ObserverDSN:      observerDSN,
			Fence:            r.fenceSharedGuardFunction,
			Project:          r.project,
			Execute:          r.execute,
			Receipt:          r.receipt,
			ProjectReceipt:   r.projectReceipt,
			ProjectObject:    r.projectObject,
			ReconcileSession: session,
			BeforeAttempt:    r.beforeAttempt,
			AfterFailure:     r.afterFailure,
		}
		budget := newMigrationUnitBudget()
		if err := unit.run(ctx, conn, budget); err != nil {
			return summary, fmt.Errorf("sqlstore: append-only guard unit %s on %s: %w", p.Intent, p.Spec.Key, err)
		}
		summary.Executed[p.Intent]++
		receipts, err = guardRolloutReceipts(ctx, mdb, dia, rollout.RolloutID)
		if err != nil {
			return summary, err
		}
	}

	refusal, err := guardAdmissionCloseUnderFence(ctx, conn, dia, m, rollout, plans, keys, hardened, &summary)
	if refusal != nil {
		if rerr := recordGuardVerificationFailure(ctx, conn, dia, rollout, refusal.UnitID, refusal.Refusal, gate.Phase); rerr != nil {
			slog.Warn("could not record a guard verification failure durably", "err", rerr)
		}
	}
	if err != nil {
		summary.log()
		return summary, err
	}
	summary.Phase, summary.Condition = gatePhaseReady, gateConditionVerified
	summary.log()
	return summary, nil
}

func guardAdmissionTransitionAfterModules(
	ctx context.Context,
	mdb dialect.Execer,
	dia dialect.Dialect,
	current guardManifest,
	history guardEditionHistory,
) (guardEditionHistory, error) {
	if history.Kind != guardEditionHistoryPredecessorV7 {
		return history, fmt.Errorf("%w: post-module transition needs a completed immediate predecessor, got epoch %d history %q",
			ErrGuardManifestNoEdge, current.CodeEpoch, history.Kind)
	}
	graph, err := guardAdmissionGraphFor(current)
	if err != nil {
		return history, err
	}
	edge, err := guardAdmissionEdge(graph, history.ParentEpoch, current.CodeEpoch)
	if err != nil {
		return history, err
	}
	delta, err := guardAdmissionDelta(guardAdmissionEdgeSource(edge), edge.To.CodeEpoch)
	if err != nil {
		return history, err
	}
	if !guardAdmissionModuleDelta(delta) {
		return history, fmt.Errorf("%w: the post-module seam cannot cross the %s edge %d -> %d; that delta belongs to a core migration",
			ErrGuardManifestNoEdge, delta, edge.From.CodeEpoch, edge.To.CodeEpoch)
	}
	if !guardEditionPostModuleSourceReady(edge, history) {
		return history, fmt.Errorf("%w: edition %d cannot cross the %s edge from %d: its source history %q has not completed the core migration that establishes it",
			ErrGuardManifestNoEdge, current.CodeEpoch, delta, edge.From.CodeEpoch, history.Kind)
	}
	tx, err := mdb.BeginTx(ctx, nil)
	if err != nil {
		return history, fmt.Errorf("sqlstore: begin guard edition %d transition: %w", current.CodeEpoch, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit

	var plans []guardUnitPlan
	openCurrent := dia.Name() == store.EnginePostgres
	if openCurrent {
		if err := verifyGuardFenceCapability(ctx, tx); err != nil {
			return history, err
		}
		keys := make([]guardKey, 0, len(current.Specs))
		for _, spec := range current.Specs {
			keys = append(keys, spec.Key)
		}
		observed, err := projectGuardCatalogBatch(ctx, tx, keys)
		if err != nil {
			return history, err
		}
		var refusals []guardPlanRefusal
		plans, refusals, err = buildGuardUnitPlans(current, observed)
		if err != nil {
			return history, err
		}
		if len(refusals) != 0 {
			return history, fmt.Errorf("sqlstore: guard edition post-module transition cannot open epoch %d: %d of %d targets have no authorized plan (first: %s)",
				current.CodeEpoch, len(refusals), len(current.Specs), refusals[0])
		}
	}
	if err := guardAdmissionTransitionInTx(ctx, tx, dia, current, edge, history, plans, openCurrent); err != nil {
		return history, err
	}
	if err := tx.Commit(); err != nil {
		return history, fmt.Errorf("sqlstore: commit guard edition %d transition: %w", current.CodeEpoch, err)
	}
	post, err := guardAdmissionVerifyHistory(ctx, mdb, dia, current)
	if err != nil {
		return history, fmt.Errorf("sqlstore: verify committed guard edition %d transition: %w", current.CodeEpoch, err)
	}
	if post.Kind != guardEditionHistoryTransitioned {
		return history, fmt.Errorf("%w: committed epoch-%d transition produced history %q",
			ErrGuardManifestNoEdge, current.CodeEpoch, post.Kind)
	}
	return post, nil
}

func guardAdmissionCloseUnderFence(
	ctx context.Context,
	conn *sql.Conn,
	dia dialect.Dialect,
	m guardManifest,
	rollout guardRolloutContext,
	plans []guardUnitPlan,
	keys []guardKey,
	hardened bool,
	summary *guardRolloutSummary,
) (*guardTerminalRefusal, error) {
	budgets := &guardCloseBudgets{
		acquire: newLockBudget(guardCloseAcquisitionBudget, time.Now, sleepCtx, jitterFloat),
	}
	if obs := guardCloseWorkSpentObserver; obs != nil {
		defer func() { obs(budgets.workSpent()) }()
	}
	b := budgets.acquire
	for attempt := 1; ; attempt++ {
		refusal, err := guardAdmissionAttemptGuardClose(ctx, conn, dia, m, rollout, plans, keys, hardened, budgets, summary)
		if err == nil || refusal != nil {
			return refusal, err
		}
		if attempt >= guardCloseMaxAttempts || !guardCloseRetryable(err) {
			return nil, err
		}
		slog.Warn("the guard rollout's close lost a lock race and will be retried",
			"rollout", rollout.RolloutID, "attempt", attempt, "sqlstate", sqlStateOf(err), "err", err)
		waited, berr := b.backoff(ctx, attempt, guardCloseBackoffBase, guardCloseBackoffMax)
		if berr != nil {
			return nil, berr
		}
		if !waited {
			return nil, err
		}
	}
}

func guardAdmissionAttemptGuardClose(
	ctx context.Context,
	conn *sql.Conn,
	dia dialect.Dialect,
	m guardManifest,
	rollout guardRolloutContext,
	plans []guardUnitPlan,
	keys []guardKey,
	hardened bool,
	budgets *guardCloseBudgets,
	summary *guardRolloutSummary,
) (_ *guardTerminalRefusal, err error) {
	b := budgets.acquire
	ctx, clock := newGuardCloseTxClock(ctx, b.remaining(), errGuardCloseAcquisitionDeadline)
	defer clock.stop()
	defer func() { err = clock.attribute(ctx, err) }()
	tx, berr := conn.BeginTx(ctx, nil)
	if berr != nil {
		return nil, fmt.Errorf("sqlstore: close the guard rollout: begin: %w", berr)
	}
	defer func() { _ = tx.Rollback() }()

	acquire := func(what, stmt string) error {
		if aerr := armGuardCloseAcquisition(ctx, tx, b, what); aerr != nil {
			return aerr
		}
		if _, eerr := tx.ExecContext(ctx, stmt); eerr != nil {
			return fmt.Errorf("sqlstore: close the guard rollout: stabilize %s: %w", what, eerr)
		}
		return nil
	}

	for _, l := range guardCloseMetadataLocks(guardCloseMetadataMode(hardened)) {
		if err := acquire(l.displayRelation(), l.lockStatement()); err != nil {
			return nil, err
		}
	}
	fn := canonicalGuardDefinition().Function
	if err := acquire(fn.Schema+"."+fn.Name+"()", fenceSharedFunctionStatement(fn.Schema, fn.Name)); err != nil {
		return nil, err
	}
	for _, l := range guardCloseLocks(keys) {
		if err := acquire(l.displayRelation(), l.lockStatement()); err != nil {
			return nil, err
		}
	}
	work := budgets.workBudget()
	if work.expired() {
		return nil, fmt.Errorf("%w: the guard rollout's close ran out of work budget before it could read anything under the fence",
			ErrMigrationLockBudgetExceeded)
	}
	clock.rearm(work.remaining(), errGuardCloseWorkDeadline)
	if _, err := setLocalTimeouts(ctx, tx, guardCloseLockTimeout, guardCloseWorkTimeout); err != nil {
		return nil, fmt.Errorf("sqlstore: close the guard rollout: arm the work timeout: %w", err)
	}

	if err := guardAdmissionVerifyControlPlane(ctx, tx, dia, m); err != nil {
		return nil, err
	}
	fresh, err := projectGuardCatalogBatch(ctx, tx, keys)
	if err != nil {
		return nil, err
	}
	revision, retained, err := guardAdmissionVerifyInventoryChain(ctx, tx, dia, m)
	if err != nil {
		return nil, err
	}
	if revision != rollout.RetainedRevision || retained != rollout.RetainedSHA256 {
		return nil, fmt.Errorf("%w: rollout %s was opened over inventory revision %d/%s and the inventory now folds to %d/%s",
			ErrGuardGateChainBroken, rollout.RolloutID, rollout.RetainedRevision,
			hexDigest(rollout.RetainedSHA256), revision, hexDigest(retained))
	}
	gate, err := foldGateEvents(ctx, tx, dia, rollout.RolloutID)
	if err != nil {
		return nil, err
	}
	if gate.Phase != gatePhasePending || !gate.mayMutate() {
		return nil, fmt.Errorf("%w: rollout %s folds to %s/%s under the closing fence, so the reading that authorized this close no longer holds",
			ErrGuardGateIllegalTransition, rollout.RolloutID, gate.Phase, gate.Condition)
	}
	receipts, err := guardRolloutReceipts(ctx, tx, dia, rollout.RolloutID)
	if err != nil {
		return nil, err
	}
	if err := verifyGuardReceiptCensus(rollout, plans, receipts); err != nil {
		summary.Diagnosis = "RECEIPT_CENSUS"
		return nil, err
	}
	refusal, verr := verifyGuardTerminals(ctx, tx, dia, rollout, gate, plans, receipts, fresh, summary)
	if verr != nil {
		return refusal, verr
	}
	checkpoint, err := computeGuardCheckpoint(ctx, tx, dia, rollout.RolloutID)
	if err != nil {
		return nil, err
	}
	if _, err := appendGateEvent(ctx, tx, dia, gateEvent{
		RolloutID:         rollout.RolloutID,
		Kind:              gateEventReady,
		Format:            rollout.Format,
		CodeEpoch:         rollout.CodeEpoch,
		CodeSHA256:        rollout.CodeSHA256,
		RetainedRevision:  rollout.RetainedRevision,
		RetainedSHA256:    rollout.RetainedSHA256,
		Phase:             gatePhaseReady,
		Condition:         gateConditionVerified,
		ExpectedUnits:     guardPlanUnitIDs(plans),
		Checkpoint:        checkpoint,
		CheckpointPresent: true,
	}); err != nil {
		return nil, err
	}
	if hook := guardCloseBeforeCommitTestHook; hook != nil {
		hook(ctx, clock.fire)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("sqlstore: close the guard rollout: commit: %w", err)
	}
	return nil, nil
}
