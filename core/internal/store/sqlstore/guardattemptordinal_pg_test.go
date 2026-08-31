// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// guardattemptordinal_pg_test.go is the regression for C4-01 and C4-02, and it exists because the
// defect they name is not reachable from a pure fold: it lives in what the WRITER derives.
//
// THE DEFECT. guardAttemptID is a pure hash of (rollout, unit, ordinal, prestate digest), and the
// ordinal came from `for attempt := 1; ; attempt++` in migrationretry.go — a counter rebuilt from
// scratch in every process. After an attempt whose transaction rolled back, the next boot
// re-projects an identical prestate, restarts the counter at 1, and derives a BYTE-IDENTICAL
// attempt id. The `attempt-started` for it commits in its own transaction, so the duplicate is
// durable even though the work is not, and from then on foldGateEvents refuses the whole history
// on every boot: `reconciled` is refused outright by this edition, the three logs are INSERT-only
// behind ALWAYS guards, and there is no repair CLI. The deployment does not start again.
//
// WHY THE ASSERTION IS "THE TWO IDS DIFFER" AND NOT "THE FOLD ACCEPTS". Both are checked, but the
// first is the one that discriminates cheaply and cannot be satisfied by accident: with the
// ordinal taken from the argument, two announcements made at the same process-local attempt number
// over an unchanged database produce the SAME id by construction, on every platform and every run.
// The fold assertion is what says the brick is actually gone.
//
// THE MUTATION THAT MUST TURN THIS RED: in guardUnitRunner.beforeAttempt, pass `attempt` to
// guardAttemptID instead of the ordinal read from the ledger. Measured red on all four majors.
func TestPostgresTheAttemptOrdinalComesFromTheLedgerAndNotFromTheProcess(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}

	// A REAL boot, so the gate table, its CHECK constraints and a real rollout all exist. A
	// hand-built stream is refused by the edition CHECK before any of this could be measured.
	probe, m, rolloutID := reopenableGuardLedger(t, cfg)
	if len(m.Specs) == 0 {
		t.Fatal("the manifest declares no specs, so this test would pass vacuously")
	}
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}

	// The rollout's edition tuple comes from the MANIFEST, exactly as guardRolloutID derived the
	// id the fixture handed back — never invented. A CHECK compares the tuple against the stream,
	// so a fixture of our own numbers would be refused with 23514 before the property under test
	// could be reached.
	empty, err := emptyRetainedDigest()
	if err != nil {
		t.Fatal(err)
	}
	rollout := guardRolloutContext{
		RolloutID: rolloutID, Format: m.Format, CodeEpoch: m.CodeEpoch, CodeSHA256: m.CodeSHA256,
		RetainedRevision: 0, RetainedSHA256: empty,
	}

	spec := m.Specs[0]
	unitID, uerr := guardUnitID(m.Format, spec.Key, intentAdoptLegacy)
	if uerr != nil {
		t.Fatal(uerr)
	}
	plan := guardUnitPlan{UnitID: unitID, Ordinal: 1, Spec: spec, Intent: intentAdoptLegacy}

	// The fixture wipes the gate log, so the opening this unit belongs to is seeded here. Without
	// it the fold would refuse for a reason that has nothing to do with attempt identity, and the
	// test would report the wrong defect.
	seedTx, err := probe.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := appendGateEvent(ctx, seedTx, dia, gateEvent{
		RolloutID: rollout.RolloutID, Kind: gateEventPendingOpened,
		Format: rollout.Format, CodeEpoch: rollout.CodeEpoch, CodeSHA256: rollout.CodeSHA256,
		RetainedRevision: rollout.RetainedRevision, RetainedSHA256: rollout.RetainedSHA256,
		Phase: gatePhasePending, Condition: gateConditionClean,
		ExpectedUnits: []string{unitID},
	}); err != nil {
		t.Fatalf("seed the opening: %v", err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatalf("commit the opening: %v", err)
	}

	// TWO RUNNERS OVER THE SAME DATABASE, each announcing at process-local attempt 1. That pair
	// IS the crash-and-restart, expressed without a crash: the second process cannot know what
	// the first one counted to, which is the whole reason the counter is not an identity.
	first := newOrdinalProbeRunner(t, ctx, dia, dsns.App, rollout, plan)
	second := newOrdinalProbeRunner(t, ctx, dia, dsns.App, rollout, plan)

	if err := first.beforeAttempt(ctx, 1); err != nil {
		t.Fatalf("the first announcement was refused: %v", err)
	}
	if err := second.beforeAttempt(ctx, 1); err != nil {
		t.Fatalf("the second announcement was refused, and the point of the fix is that it is a LEGITIMATE retry: %v", err)
	}

	if first.attemptID == second.attemptID {
		t.Fatalf("two processes both at their own attempt 1 derived the SAME attempt id (%q); the next fold refuses the whole history and the deployment never starts again",
			first.attemptID)
	}

	// AND THE HISTORY THIS PRODUCED ACTUALLY FOLDS, which is the property the operator has.
	// Asserting only that the ids differ would leave room for two ids that are distinct and
	// still illegal in sequence.
	if _, err := foldGateEvents(ctx, probe, dia, rollout.RolloutID); err != nil {
		t.Fatalf("the ledger this build wrote does not fold, which is the brick this fix exists to remove: %v", err)
	}

	// The ledger is now the authority on how many attempts happened, so the NEXT one is the
	// third — not the first, which is what the process-local counter would have said.
	tx, err := probe.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	next, err := gateUnitAttemptOrdinal(ctx, tx, dia, rollout.RolloutID, unitID)
	if err != nil {
		t.Fatalf("count the announced attempts: %v", err)
	}
	if next != 3 {
		t.Fatalf("after two announcements the ledger puts the next attempt at %d, and it must be 3: the count is not reading this unit's own history", next)
	}
}

// newOrdinalProbeRunner is one runner on its OWN pooled connection, which is what makes two of
// them a stand-in for two processes rather than two calls.
func newOrdinalProbeRunner(t *testing.T, ctx context.Context, dia dialect.Dialect, dsn string,
	rollout guardRolloutContext, plan guardUnitPlan) *guardUnitRunner {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open a runner connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("check out a runner connection: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// The reading is the same on both, deliberately: an unchanged database is exactly the case
	// where the old derivation repeated itself, so a fixture that varied the prestate would hide
	// the defect instead of measuring it.
	pre := rollout.bind(prestate{TargetExists: true, GuardPresent: true, GuardEnableState: "A", GuardMatchesCanonical: true}, plan.Spec)
	return &guardUnitRunner{
		dia: dia, conn: conn, rollout: rollout, plan: plan,
		lastProjected: pre, lastProjectedValid: true,
	}
}

// TestTheDuplicateGuardConsultsEveryAttemptAndNotOnlyTheLast is the C4-04 regression, and C4-04 is
// a DIFFERENT defect from the one above — it needs this fix even after the ordinal comes from the
// ledger.
//
// THE DEFECT. The duplicate-announcement guard compared the incoming id against f.AttemptID, the
// LAST folded attempt. A history that had moved on could therefore be re-entered:
// started(A1) → failed(A1) → started(A2), and then A1 again, which is not the last and so was
// accepted. The fold then resets State to started and AttemptID to A1, and the rule that refuses a
// judged reading for an attempt that already failed keys on that same overwritten State — so
// judged(A1) passes too, and the ledger ends up carrying ONE attempt id recorded both as
// terminally failed and as judged with a receipt.
//
// THE MUTATION THAT MUST TURN THIS RED: restore `f.AttemptID == ev.AttemptID` in place of the set
// membership in checkGateEventGrammar.
func TestTheDuplicateGuardConsultsEveryAttemptAndNotOnlyTheLast(t *testing.T) {
	proj, rollout, spec, adoptID, transitionID := grammarFixture(t)
	fx := gateShapeFixture{rollout: rollout, spec: spec, adoptID: adoptID, transitionID: transitionID}

	opened := grammarEvent(rollout, 1, gateEventPendingOpened)
	opened.ExpectedUnits = []string{adoptID, transitionID}
	if err := proj.foldOne(opened); err != nil {
		t.Fatalf("the opening was refused: %v", err)
	}

	a1 := wellFormedGateEvent(t, fx, 2, gateEventAttemptStarted, proj.Phase)
	if err := proj.foldOne(a1); err != nil {
		t.Fatalf("the first announcement was refused: %v", err)
	}
	failed := wellFormedGateEvent(t, fx, 3, gateEventAttemptFailed, proj.Phase)
	if err := proj.foldOne(failed); err != nil {
		t.Fatalf("the failure was refused: %v", err)
	}
	a2 := wellFormedGateEvent(t, fx, 4, gateEventAttemptStarted, proj.Phase)
	a2.AttemptID = "attempt-2"
	if err := proj.foldOne(a2); err != nil {
		t.Fatalf("a NEW attempt after a failure was refused: %v", err)
	}

	// THE REACH-BACK. A1 is on the record but is no longer the last, which is exactly the gap.
	again := wellFormedGateEvent(t, fx, 5, gateEventAttemptStarted, proj.Phase)
	if again.AttemptID != a1.AttemptID {
		t.Fatalf("the fixture built a different attempt (%q vs %q), so this case would be testing a legitimate retry", again.AttemptID, a1.AttemptID)
	}
	err := proj.foldOne(again)
	if err == nil {
		t.Fatal("an attempt already on the record was re-announced after a LATER one and the history was accepted; from here the same id can be recorded as both terminally failed and judged with a receipt")
	}
	if !strings.Contains(err.Error(), "already on the record") {
		t.Errorf("the refusal is not the duplicate-announcement one: %v", err)
	}

	// AND THE HARM IT LED TO IS GONE. With the re-announcement refused the fold is untouched, so
	// the judged reading for that same old attempt is refused for its own reason rather than
	// waved through by a fold the re-announcement had reset.
	judged := wellFormedGateEvent(t, fx, 5, gateEventAttemptJudged, proj.Phase)
	judged.AttemptID = a1.AttemptID
	if err := proj.foldOne(judged); err == nil {
		t.Fatal("a judged reading was accepted for an attempt that already failed, which is the receipted-success-over-a-terminal-failure this rule exists to stop")
	}
}
