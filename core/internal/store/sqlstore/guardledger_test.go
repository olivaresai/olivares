// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// guardledger_test.go pins the ledger: that a history folds to the projection the coordinator
// acts on, and that a history somebody edited does not fold at all.
//
// The second half is the one worth being precise about. The chain detects a row CHANGED
// WITHOUT recomputing the digests — which is what an UPDATE does — and a row removed or
// reordered. It does NOT detect a coordinated rewrite of the whole tail by somebody who can
// also recompute every digest; that needs an external anchor, which this design explicitly
// does not provide. Claiming otherwise would be exactly the kind of overclaim this ledger
// exists to make impossible about schema changes.

// guardLedgerFixture creates the three control-plane relations on a scratch SQLite database,
// WITHOUT their append-only triggers.
//
// Omitting the triggers is what makes the tamper tests possible: with them installed SQLite
// refuses the UPDATE, so a test could only assert that the guard works — which is a different
// property, covered by the boot test. Here the point is what the FOLD notices when a row has
// been changed by somebody who could change it.
func guardLedgerFixture(t *testing.T) (*sql.DB, dialect.Dialect) {
	t.Helper()
	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("no SQLite dialect")
	}
	db, err := sql.Open("sqlite", t.TempDir()+"/ledger.db")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, stmt := range dia.GuardControlPlaneStmts() {
		if strings.HasPrefix(strings.TrimSpace(stmt), "CREATE TRIGGER") {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("apply %.60s...: %v", stmt, err)
		}
	}
	return db, dia
}

// guardTestRollout is a rollout context with the shape production produces.
func guardTestRollout(t *testing.T) guardRolloutContext {
	t.Helper()
	empty, err := emptyRetainedDigest()
	if err != nil {
		t.Fatal(err)
	}
	m := guardTestManifest(t, "audit_events", "t")
	id, err := guardRolloutID(m.Format, m.CodeEpoch, m.CodeSHA256, 0, empty)
	if err != nil {
		t.Fatal(err)
	}
	return guardRolloutContext{
		RolloutID: id, Format: m.Format, CodeEpoch: m.CodeEpoch,
		CodeSHA256: m.CodeSHA256, RetainedRevision: 0, RetainedSHA256: empty,
	}
}

// appendForTest appends one event through a transaction, failing the test on error.
func appendForTest(t *testing.T, db *sql.DB, dia dialect.Dialect, ev gateEvent) gateEvent {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	out, err := appendGateEvent(context.Background(), tx, dia, ev)
	if err != nil {
		t.Fatalf("append %s: %v", ev.Kind, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return out
}

// TestGuardGateFoldsToTheProjectionTheCoordinatorActsOn walks one rollout's whole life.
func TestGuardGateFoldsToTheProjectionTheCoordinatorActsOn(t *testing.T) {
	t.Parallel()
	db, dia := guardLedgerFixture(t)
	ctx := context.Background()
	rollout := guardTestRollout(t)
	m := guardTestManifest(t, "audit_events", "t")
	spec := m.Specs[0]
	unitID, err := guardUnitID(m.Format, spec.Key, intentAdoptLegacy)
	if err != nil {
		t.Fatal(err)
	}

	pre := rollout.bind(prestate{
		TargetExists: true, GuardPresent: true,
		GuardEnableState: guardStateOrigin, GuardMatchesCanonical: true,
	}, spec)
	digest, err := prestateDigest(pre)
	if err != nil {
		t.Fatal(err)
	}
	base := gateEvent{
		RolloutID: rollout.RolloutID, Format: rollout.Format, CodeEpoch: rollout.CodeEpoch,
		CodeSHA256: rollout.CodeSHA256, RetainedRevision: rollout.RetainedRevision,
		RetainedSHA256: rollout.RetainedSHA256,
		Phase:          gatePhasePending, Condition: gateConditionClean,
	}

	opened := base
	opened.Kind, opened.ExpectedUnits = gateEventPendingOpened, []string{unitID}
	appendForTest(t, db, dia, opened)

	proj, err := foldGateEvents(ctx, db, dia, rollout.RolloutID)
	if err != nil {
		t.Fatalf("fold after opening: %v", err)
	}
	if !proj.Found || proj.Phase != gatePhasePending || proj.Condition != gateConditionClean {
		t.Fatalf("after pending-opened the projection is found=%v %s/%s", proj.Found, proj.Phase, proj.Condition)
	}
	if !proj.mayMutate() {
		t.Error("pending/clean does not authorize mutation, so no unit could ever run")
	}
	if got := proj.Units[unitID].State; got != unitGateNever {
		t.Errorf("the enumerated unit starts as %q, want %q", got, unitGateNever)
	}

	started := base
	started.Kind, started.UnitID, started.AttemptID = gateEventAttemptStarted, unitID, "attempt-1"
	started.Intent, started.Key = intentAdoptLegacy, spec.Key
	started.SpecSHA256, started.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
	started.PrestateSHA256, started.PrestatePresent, started.Prestate = someDigest(digest), true, pre
	started.PrestateBytes = prestateRendering(pre)
	appendForTest(t, db, dia, started)

	judged := started
	judged.Kind = gateEventAttemptJudged
	appendForTest(t, db, dia, judged)

	proj, err = foldGateEvents(ctx, db, dia, rollout.RolloutID)
	if err != nil {
		t.Fatalf("fold after judging: %v", err)
	}
	fold := proj.Units[unitID]
	if fold.State != unitGateJudged {
		t.Errorf("the unit folded to %q, want %q", fold.State, unitGateJudged)
	}
	// THE RECONSTRUCTED READING, which is the whole reason the fields are stored beside the
	// digest: after a successful O -> A the catalog says 'A' and the authority must still say
	// 'O'.
	if !fold.JudgedReadingValid {
		t.Fatal("the judged reading was not reconstructed, so a later boot would have nothing to reason from")
	}
	if fold.JudgedReading != pre {
		t.Errorf("the reconstructed reading is %+v, want %+v", fold.JudgedReading, pre)
	}

	ready := base
	ready.Kind, ready.Phase, ready.Condition = gateEventReady, gatePhaseReady, gateConditionVerified
	ready.ExpectedUnits = []string{unitID}
	// A closing event MUST carry the checkpoint of the other two logs, and the table's own CHECK
	// enforces it. Without one, removing the last row of either log would leave a shorter chain
	// that still verified perfectly.
	ready.Checkpoint, ready.CheckpointPresent = guardCheckpoint{
		InventoryHead: digest, InventoryCount: 1, ReceiptHead: digest, ReceiptCount: 1,
	}, true
	appendForTest(t, db, dia, ready)

	proj, err = foldGateEvents(ctx, db, dia, rollout.RolloutID)
	if err != nil {
		t.Fatalf("fold after ready: %v", err)
	}
	if proj.Phase != gatePhaseReady || proj.Condition != gateConditionVerified {
		t.Errorf("after ready the projection is %s/%s", proj.Phase, proj.Condition)
	}
	if !proj.CheckpointPresent || proj.Checkpoint.InventoryCount != 1 || proj.Checkpoint.ReceiptCount != 1 {
		t.Errorf("the closing event's checkpoint did not reach the projection: present=%v %+v",
			proj.CheckpointPresent, proj.Checkpoint)
	}
	if proj.mayMutate() {
		t.Error("ready/verified authorizes mutation; a closed rollout must be VERIFY-only, or re-creating a missing object would launder its removal")
	}
	if proj.Events != 4 {
		t.Errorf("the fold saw %d events, want 4", proj.Events)
	}
}

// TestGuardGateRefusesAHistorySomebodyEdited pins what the chain notices.
//
// Each mutation is a real UPDATE or DELETE against the log, of the kind possible for whoever
// can write it, and each must make the fold refuse rather than return a projection. A
// projection built from an unverifiable history is worse than no projection: it authorizes.
func TestGuardGateRefusesAHistorySomebodyEdited(t *testing.T) {
	t.Parallel()
	rollout := guardTestRollout(t)
	m := guardTestManifest(t, "audit_events", "t")
	spec := m.Specs[0]
	unitID, err := guardUnitID(m.Format, spec.Key, intentAdoptLegacy)
	if err != nil {
		t.Fatal(err)
	}
	pre := rollout.bind(prestate{
		TargetExists: true, GuardPresent: true,
		GuardEnableState: guardStateOrigin, GuardMatchesCanonical: true,
	}, spec)
	digest, err := prestateDigest(pre)
	if err != nil {
		t.Fatal(err)
	}
	second := m.Specs[1]
	secondUnitID, err := guardUnitID(m.Format, second.Key, intentAdoptLegacy)
	if err != nil {
		t.Fatal(err)
	}
	pre2 := rollout.bind(prestate{
		TargetExists: true, GuardPresent: true,
		GuardEnableState: guardStateOrigin, GuardMatchesCanonical: true,
	}, second)
	digest2, err := prestateDigest(pre2)
	if err != nil {
		t.Fatal(err)
	}

	seed := func(t *testing.T) (*sql.DB, dialect.Dialect) {
		t.Helper()
		db, dia := guardLedgerFixture(t)
		base := gateEvent{
			RolloutID: rollout.RolloutID, Format: rollout.Format, CodeEpoch: rollout.CodeEpoch,
			CodeSHA256: rollout.CodeSHA256, RetainedSHA256: rollout.RetainedSHA256,
			Phase: gatePhasePending, Condition: gateConditionClean,
		}
		opened := base
		opened.Kind, opened.ExpectedUnits = gateEventPendingOpened, []string{unitID, secondUnitID}
		appendForTest(t, db, dia, opened)
		// ANNOUNCED BEFORE IT IS JUDGED, because that is the only history this engine can
		// produce: `attempt-started` commits in its own transaction before the unit's. The
		// fixture used to jump straight to `attempt-judged`, which the gate's grammar now
		// refuses — so every case below was tampering with a history that was already illegal.
		started1 := base
		started1.Kind, started1.UnitID, started1.AttemptID = gateEventAttemptStarted, unitID, "attempt-1"
		started1.Intent, started1.Key = intentAdoptLegacy, spec.Key
		started1.SpecSHA256, started1.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
		// The announcement carries the reading the runner took BEFORE acting, exactly as
		// guardUnitRunner.gateEvent writes it. The first version of this fixture left it off, so
		// its "legal prefix" was a history production cannot emit — caught by the shape table
		// once that table covered the prestate.
		started1.PrestateSHA256, started1.PrestatePresent, started1.Prestate = someDigest(digest), true, pre
		appendForTest(t, db, dia, started1)
		judged := base
		judged.Kind, judged.UnitID, judged.AttemptID = gateEventAttemptJudged, unitID, "attempt-1"
		judged.Intent, judged.Key = intentAdoptLegacy, spec.Key
		judged.SpecSHA256, judged.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
		judged.PrestateSHA256, judged.PrestatePresent, judged.Prestate = someDigest(digest), true, pre
		appendForTest(t, db, dia, judged)
		// A SECOND UNIT, so the stream is longer than the window a truncated verification would
		// cover. With three events every mutation this table applies lands inside the first
		// three, and limiting the production query to `event_ordinal <= 3` kept all of them
		// green while every real tail went unchecked. The cases below reach ordinals 4 and 5.
		started := base
		started.Kind, started.UnitID, started.AttemptID = gateEventAttemptStarted, secondUnitID, "attempt-1"
		started.Intent, started.Key = intentAdoptLegacy, second.Key
		started.SpecSHA256, started.DefinitionSHA256 = someDigest(second.SpecSHA256), someDigest(second.DefinitionSHA256)
		started.PrestateSHA256, started.PrestatePresent, started.Prestate = someDigest(digest2), true, pre2
		appendForTest(t, db, dia, started)
		judged2 := base
		judged2.Kind, judged2.UnitID, judged2.AttemptID = gateEventAttemptJudged, secondUnitID, "attempt-1"
		judged2.Intent, judged2.Key = intentAdoptLegacy, second.Key
		judged2.SpecSHA256, judged2.DefinitionSHA256 = someDigest(second.SpecSHA256), someDigest(second.DefinitionSHA256)
		judged2.PrestateSHA256, judged2.PrestatePresent, judged2.Prestate = someDigest(digest2), true, pre2
		appendForTest(t, db, dia, judged2)
		ready := base
		ready.Kind, ready.Phase, ready.Condition = gateEventReady, gatePhaseReady, gateConditionVerified
		// DISTINCT heads, deliberately. With the same digest in both, the "repoint one to the
		// other" mutation below changes no bytes and the case would assert nothing — which is
		// exactly how it first behaved.
		otherHead := digest
		otherHead[0] ^= 0xff
		ready.Checkpoint, ready.CheckpointPresent = guardCheckpoint{
			InventoryHead: digest, InventoryCount: 1, ReceiptHead: otherHead, ReceiptCount: 1,
		}, true
		appendForTest(t, db, dia, ready)
		return db, dia
	}

	cases := []struct {
		name string
		sql  string
		args []any
		// tailDeletion marks the case a chain CANNOT catch on its own: the last event has no
		// successor, so what remains is a shorter, valid history. It is listed here so the limit
		// is exercised and stated rather than assumed away.
		tailDeletion bool
	}{{
		// The most valuable one: somebody promotes a pending rollout to ready.
		name: "the phase is promoted",
		sql:  "UPDATE " + dialect.GuardGateEventsTable + " SET phase = 'ready', gate_condition = 'verified' WHERE event_ordinal = 1",
	}, {
		// The judged reading is rewritten to authorize something else.
		name: "the judged state is rewritten",
		sql:  "UPDATE " + dialect.GuardGateEventsTable + " SET prestate_guard_state = 'A' WHERE kind = 'attempt-judged'",
	}, {
		name: "an event is removed from the middle",
		sql:  "DELETE FROM " + dialect.GuardGateEventsTable + " WHERE event_ordinal = 2",
	}, {
		name: "a predecessor link is repointed",
		sql:  "UPDATE " + dialect.GuardGateEventsTable + " SET prev_event_sha256 = NULL WHERE event_ordinal = 4",
	}, {
		name: "the diagnostic detail is edited",
		sql:  "UPDATE " + dialect.GuardGateEventsTable + " SET details = 'nothing to see' WHERE event_ordinal = 1",
	}, {
		name: "half a prestate is left behind",
		sql:  "UPDATE " + dialect.GuardGateEventsTable + " SET prestate_guard_state = NULL WHERE kind = 'attempt-judged'",
	}, {
		// The checkpoint is inside the closing event's canonical bytes, so editing it to match a
		// truncated log breaks the event's own digest. Without that, somebody could delete a
		// receipt AND adjust the count it was attested with.
		name: "the attested receipt count is adjusted",
		sql:  "UPDATE " + dialect.GuardGateEventsTable + " SET checkpoint_receipt_count = 0 WHERE kind = 'ready'",
	}, {
		name: "the attested inventory head is repointed",
		sql:  "UPDATE " + dialect.GuardGateEventsTable + " SET checkpoint_inventory_sha256 = checkpoint_receipt_sha256 WHERE kind = 'ready'",
	}, {
		// THE LAST ROW, which is the case F-02 was about. Deleting the tail of the GATE leaves a
		// prefix with no successor — so this one is caught by the ordinal count rather than by a
		// digest, and the assertion below is what tells the two apart.
		name:         "the closing event itself is deleted",
		sql:          "DELETE FROM " + dialect.GuardGateEventsTable + " WHERE kind = 'ready'",
		tailDeletion: true,
	}, {
		// BEYOND THE THIRD EVENT. These two are what a verification limited to a fixed prefix
		// walks past, and they are ordinary edits to the SECOND unit's judged reading and to its
		// attempt. The history is six events now: opened, started/judged for each of the two
		// units, and the close.
		name: "the fifth event's judged state is rewritten",
		sql:  "UPDATE " + dialect.GuardGateEventsTable + " SET prestate_guard_state = 'A' WHERE event_ordinal = 5",
	}, {
		name: "the fifth event's attempt is renamed",
		sql:  "UPDATE " + dialect.GuardGateEventsTable + " SET attempt_id = 'attempt-forged' WHERE event_ordinal = 5",
	}, {
		name: "the closing event's condition is rewritten",
		sql:  "UPDATE " + dialect.GuardGateEventsTable + " SET gate_condition = 'clean' WHERE event_ordinal = 6",
	}, {
		name: "the fifth event is repointed at the first",
		sql:  "UPDATE " + dialect.GuardGateEventsTable + " SET prev_event_sha256 = (SELECT event_sha256 FROM " + dialect.GuardGateEventsTable + " WHERE event_ordinal = 1) WHERE event_ordinal = 5",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, dia := seed(t)
			if _, err := db.Exec(tc.sql, tc.args...); err != nil {
				t.Fatalf("apply the tampering: %v", err)
			}
			proj, err := foldGateEvents(context.Background(), db, dia, rollout.RolloutID)
			if tc.tailDeletion {
				// THE HONEST HALF. Deleting the closing event leaves a chain that verifies: the
				// fold succeeds and reports the state BEFORE the closure. What stops that being a
				// downgrade is the coordinator, which then finds a pending rollout, re-reads every
				// object and every receipt, and either closes again or refuses — never silently
				// treats it as closed.
				if err != nil {
					t.Fatalf("the fold rejected a chain whose tail was simply removed, which it cannot detect: %v", err)
				}
				if proj.Phase == gatePhaseReady {
					t.Error("the projection still reports ready after its closing event was deleted")
				}
				return
			}
			if err == nil {
				t.Fatal("the fold accepted an edited history")
			}
			if !errors.Is(err, ErrGuardGateChainBroken) {
				t.Errorf("the fold refused with %v, which is not the named chain error", err)
			}
		})
	}
}

// TestGuardGateConditionNeverRelaxesItself pins the fold's one-way rule.
//
// A retryable failure arriving AFTER a permanent one must not relax the condition. Without
// that, a boot loop would eventually find an ordering that lets a permanently blocked rollout
// run again — which is the loop this whole gate exists to prevent.
func TestGuardGateConditionNeverRelaxesItself(t *testing.T) {
	t.Parallel()
	db, dia := guardLedgerFixture(t)
	rollout := guardTestRollout(t)
	// REAL UNIT IDENTITIES, not "unit-a"/"unit-b". A unit id is the digest of (format, key,
	// intent) and the gate now recomputes it from every attempt event's own fields, so a
	// synthetic name is a history this engine could not have written.
	m := guardTestManifest(t, "audit_events", "t")
	spec := m.Specs[0]
	unitA, err := guardUnitID(m.Format, spec.Key, intentAdoptLegacy)
	if err != nil {
		t.Fatal(err)
	}
	unitB, err := guardUnitID(m.Format, spec.Key, intentTransitionLegacyOToA)
	if err != nil {
		t.Fatal(err)
	}
	announceReading := rollout.bind(prestate{
		TargetExists: true, GuardPresent: true,
		GuardEnableState: guardStateOrigin, GuardMatchesCanonical: true,
	}, spec)
	announceDigest, err := prestateDigest(announceReading)
	if err != nil {
		t.Fatal(err)
	}
	base := gateEvent{
		RolloutID: rollout.RolloutID, Format: rollout.Format, CodeEpoch: rollout.CodeEpoch,
		CodeSHA256: rollout.CodeSHA256, RetainedSHA256: rollout.RetainedSHA256,
		Phase: gatePhasePending,
	}
	opened := base
	opened.Kind, opened.Condition = gateEventPendingOpened, gateConditionClean
	opened.ExpectedUnits = []string{unitA, unitB}
	appendForTest(t, db, dia, opened)

	announce := func(unitID string, intent unitIntent) {
		t.Helper()
		ev := base
		ev.Kind, ev.UnitID, ev.AttemptID, ev.Condition = gateEventAttemptStarted, unitID, "attempt-1", gateConditionClean
		ev.Intent, ev.Key = intent, spec.Key
		ev.SpecSHA256, ev.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
		ev.PrestateSHA256, ev.PrestatePresent, ev.Prestate = someDigest(announceDigest), true, announceReading
		appendForTest(t, db, dia, ev)
	}
	announce(unitA, intentAdoptLegacy)
	permanent := base
	permanent.Kind, permanent.UnitID, permanent.Condition = gateEventAttemptFailed, unitA, gateConditionBlocked
	permanent.AttemptID, permanent.Intent, permanent.Key = "attempt-1", intentAdoptLegacy, spec.Key
	permanent.SpecSHA256, permanent.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
	permanent.PrestateSHA256, permanent.PrestatePresent, permanent.Prestate = someDigest(announceDigest), true, announceReading
	permanent.Diagnostic = guardDiagnostic{Code: "PERMANENT", RetryClass: guardRetryClassPermanent, UnblockPolicy: guardUnblockOperator}
	appendForTest(t, db, dia, permanent)

	announce(unitB, intentTransitionLegacyOToA)
	retryable := base
	retryable.Kind, retryable.UnitID, retryable.Condition = gateEventAttemptFailed, unitB, gateConditionRetryable
	retryable.AttemptID, retryable.Intent, retryable.Key = "attempt-1", intentTransitionLegacyOToA, spec.Key
	retryable.SpecSHA256, retryable.DefinitionSHA256 = someDigest(spec.SpecSHA256), someDigest(spec.DefinitionSHA256)
	retryable.PrestateSHA256, retryable.PrestatePresent, retryable.Prestate = someDigest(announceDigest), true, announceReading
	retryable.Diagnostic = guardDiagnostic{Code: "RETRYABLE", RetryClass: guardRetryClassRetryable}
	appendForTest(t, db, dia, retryable)

	proj, err := foldGateEvents(context.Background(), db, dia, rollout.RolloutID)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if proj.Condition != gateConditionBlocked {
		t.Errorf("a retryable failure after a permanent one left the rollout %s; blocked must not relax itself", proj.Condition)
	}
	if proj.mayMutate() {
		t.Error("a blocked rollout authorizes mutation")
	}
	if proj.FirstBlocking.Code != "PERMANENT" {
		t.Errorf("the recorded cause is %q, want the FIRST blocking one", proj.FirstBlocking.Code)
	}
}

// TestGuardDiagnosticFingerprintIdentifiesTheFAILURE pins what the fingerprint includes and
// what it deliberately leaves out.
//
// Excluding the volatile fields is what makes the unique constraint work: with a timestamp,
// an attempt id or a build id inside, a permanent failure would fingerprint differently on
// every boot, the constraint would never fire, and the ledger would accumulate one row per
// restart for a condition that never changed.
func TestGuardDiagnosticFingerprintIdentifiesTheFailure(t *testing.T) {
	t.Parallel()
	rollout := guardTestRollout(t)
	base := guardDiagnostic{
		Code: "UNIT_EXECUTE_PERMANENT", RetryClass: guardRetryClassPermanent,
		UnblockPolicy: guardUnblockOperator, SQLState: "42501",
		ExpectedSHA: "aa", ObservedSHA: "bb", Details: "the first message",
	}
	fp := func(d guardDiagnostic) string {
		got, err := d.fingerprint(rollout.RolloutID, "unit-a", rollout.CodeEpoch, rollout.CodeSHA256, gateConditionBlocked)
		if err != nil {
			t.Fatalf("fingerprint: %v", err)
		}
		return hexDigest(got)
	}
	ref := fp(base)

	// The human message is a snapshot, and rewording it must not make the same failure look
	// new.
	reworded := base
	reworded.Details = "the same failure, said better"
	if fp(reworded) != ref {
		t.Error("rewording the human message changed the fingerprint, so improving an error message would make every old failure look new")
	}
	// Everything the decision reads must move it.
	for _, tc := range []struct {
		name string
		mut  func(d *guardDiagnostic)
	}{
		{"the code", func(d *guardDiagnostic) { d.Code = "OTHER" }},
		{"the retry class", func(d *guardDiagnostic) { d.RetryClass = guardRetryClassRetryable }},
		{"the SQLSTATE", func(d *guardDiagnostic) { d.SQLState = "40P01" }},
		{"the expected hash", func(d *guardDiagnostic) { d.ExpectedSHA = "cc" }},
		{"the observed hash", func(d *guardDiagnostic) { d.ObservedSHA = "dd" }},
	} {
		mutated := base
		tc.mut(&mutated)
		if fp(mutated) == ref {
			t.Errorf("changing %s did not change the fingerprint", tc.name)
		}
	}
	// And so must EVERY component of the identity it is scoped to.
	//
	// The condition was the one this test never varied: every case used `blocked`, so dropping
	// `cond` from the fingerprint entirely left it green while two genuinely different
	// conditions on the same unit collided into one row — and the unique constraint then
	// swallowed the second, which is a diagnostic silently lost rather than recorded.
	for _, tc := range []struct {
		name              string
		rolloutID, unitID string
		epoch             int64
		codeSHA           [32]byte
		cond              gateCondition
	}{
		{name: "a different unit", rolloutID: rollout.RolloutID, unitID: "unit-b", epoch: rollout.CodeEpoch, codeSHA: rollout.CodeSHA256, cond: gateConditionBlocked},
		{name: "a different rollout", rolloutID: rollout.RolloutID + "-other", unitID: "unit-a", epoch: rollout.CodeEpoch, codeSHA: rollout.CodeSHA256, cond: gateConditionBlocked},
		{name: "a different epoch", rolloutID: rollout.RolloutID, unitID: "unit-a", epoch: rollout.CodeEpoch + 1, codeSHA: rollout.CodeSHA256, cond: gateConditionBlocked},
		{name: "a different code digest", rolloutID: rollout.RolloutID, unitID: "unit-a", epoch: rollout.CodeEpoch, codeSHA: [32]byte{9, 9, 9}, cond: gateConditionBlocked},
		{name: "a retryable condition", rolloutID: rollout.RolloutID, unitID: "unit-a", epoch: rollout.CodeEpoch, codeSHA: rollout.CodeSHA256, cond: gateConditionRetryable},
		{name: "a clean condition", rolloutID: rollout.RolloutID, unitID: "unit-a", epoch: rollout.CodeEpoch, codeSHA: rollout.CodeSHA256, cond: gateConditionClean},
		{name: "a verified condition", rolloutID: rollout.RolloutID, unitID: "unit-a", epoch: rollout.CodeEpoch, codeSHA: rollout.CodeSHA256, cond: gateConditionVerified},
	} {
		other, oerr := base.fingerprint(tc.rolloutID, tc.unitID, tc.epoch, tc.codeSHA, tc.cond)
		if oerr != nil {
			t.Fatalf("%s: %v", tc.name, oerr)
		}
		if hexDigest(other) == ref {
			t.Errorf("the same failure under %s produced the same fingerprint, so the two would collide under the unique constraint and the second would be lost", tc.name)
		}
	}
}

// TestGuardReceiptIsIdempotentAndRefusesADifferentClaim pins the insert's two halves.
//
// The same attribution twice is idempotence — a retried boot, a lost acknowledgement — and a
// DIFFERENT attribution under the same key is two attempts disagreeing about what happened,
// which neither may win silently. A bare ON CONFLICT DO NOTHING makes those the same
// outcome, which is how a receipt written under one edition comes to attest a unit executed
// under another.
func TestGuardReceiptIsIdempotentAndRefusesADifferentClaim(t *testing.T) {
	t.Parallel()
	db, dia := guardLedgerFixture(t)
	ctx := context.Background()
	rollout := guardTestRollout(t)
	m := guardTestManifest(t, "audit_events", "t")
	spec := m.Specs[0]
	unitID, err := guardUnitID(m.Format, spec.Key, intentAdoptLegacy)
	if err != nil {
		t.Fatal(err)
	}
	pre := rollout.bind(prestate{
		TargetExists: true, GuardPresent: true,
		GuardEnableState: guardStateOrigin, GuardMatchesCanonical: true,
	}, spec)
	digest, err := prestateDigest(pre)
	if err != nil {
		t.Fatal(err)
	}
	receipt := guardReceipt{
		RolloutID: rollout.RolloutID, UnitID: unitID, Kind: guardReceiptKindUnit,
		Intent: intentAdoptLegacy, Key: spec.Key,
		Epoch: rollout.CodeEpoch, Format: rollout.Format, CodeSHA256: rollout.CodeSHA256,
		RetainedSHA256: rollout.RetainedSHA256,
		SpecSHA256:     spec.SpecSHA256, DefinitionSHA256: spec.DefinitionSHA256,
		PrestateSHA256: digest, ToEnableState: guardStateOrigin, AttemptID: "attempt-1",
	}

	insert := func(r guardReceipt) (guardReceipt, error) {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		out, ierr := insertGuardReceipt(ctx, tx, dia, r)
		if ierr != nil {
			return out, ierr
		}
		if cerr := tx.Commit(); cerr != nil {
			t.Fatalf("commit: %v", cerr)
		}
		return out, nil
	}

	first, err := insert(receipt)
	if err != nil {
		t.Fatalf("the first insert failed: %v", err)
	}
	again, err := insert(receipt)
	if err != nil {
		t.Fatalf("re-inserting the same attribution was refused: %v", err)
	}
	if again.ReceiptID != first.ReceiptID {
		t.Errorf("the idempotent re-insert returned receipt %s, want the stored %s",
			hexDigest(again.ReceiptID), hexDigest(first.ReceiptID))
	}

	// The unique key is (rollout, unit, kind), so a change to ANY other field is a different
	// claim under the same key — which is exactly the conflict.
	for _, tc := range []struct {
		name string
		mut  func(r *guardReceipt)
	}{
		{"a different epoch", func(r *guardReceipt) { r.Epoch = rollout.CodeEpoch + 1 }},
		{"a different spec digest", func(r *guardReceipt) { r.SpecSHA256[0] ^= 0xff }},
		{"a different prestate digest", func(r *guardReceipt) { r.PrestateSHA256[0] ^= 0xff }},
		{"a different poststate", func(r *guardReceipt) { r.ToEnableState = guardStateAlways }},
		{"a different attempt", func(r *guardReceipt) { r.AttemptID = "attempt-2" }},
		{"a different intent", func(r *guardReceipt) { r.Intent = intentTransitionLegacyOToA }},
		// The FROM state is the fact the catalog no longer shows once a transition has run, so
		// it is the one field a forger has the most to gain from. No case here varied it, and
		// dropping it from the body digest AND from receiptDifference therefore went unnoticed:
		// two claims about different origins became the same claim.
		{"a from-state where the stored receipt has none", func(r *guardReceipt) { r.FromEnableState = someText(guardStateOrigin) }},
		{"a different key", func(r *guardReceipt) { r.Key.Relation = "somebody_elses_table" }},
		{"a different trigger", func(r *guardReceipt) { r.Key.Trigger = "somebody_elses_trigger" }},
		{"a different definition digest", func(r *guardReceipt) { r.DefinitionSHA256[0] ^= 0xff }},
		{"a different retained revision", func(r *guardReceipt) { r.RetainedRevision++ }},
		{"a different retained digest", func(r *guardReceipt) { r.RetainedSHA256[0] ^= 0xff }},
		{"a different code digest", func(r *guardReceipt) { r.CodeSHA256[0] ^= 0xff }},
		{"a different manifest format", func(r *guardReceipt) { r.Format++ }},
		{"a predecessor the stored receipt does not have", func(r *guardReceipt) { r.PredecessorReceiptID = someDigest(first.ReceiptID) }},
	} {
		mutated := receipt
		tc.mut(&mutated)
		_, err := insert(mutated)
		if err == nil {
			t.Errorf("%s was accepted under the same key", tc.name)
			continue
		}
		if !errors.Is(err, ErrGuardReceiptConflict) {
			t.Errorf("%s was refused with %v, which is not the named conflict", tc.name, err)
		}
	}

	// And the stream verifies: one receipt, its body digest reproducible, its chain intact.
	got, err := guardRolloutReceipts(ctx, db, dia, rollout.RolloutID)
	if err != nil {
		t.Fatalf("the receipt stream does not verify: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("the ledger holds %d receipts after one attribution and %d refusals", len(got), 15)
	}
}

// TestGuardReceiptStreamRefusesAnEditedRow is the receipt half of the chain property.
func TestGuardReceiptStreamRefusesAnEditedRow(t *testing.T) {
	t.Parallel()
	db, dia := guardLedgerFixture(t)
	ctx := context.Background()
	rollout := guardTestRollout(t)
	m := guardTestManifest(t, "audit_events", "t")
	spec := m.Specs[0]
	unitID, err := guardUnitID(m.Format, spec.Key, intentAdoptLegacy)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insertGuardReceipt(ctx, tx, dia, guardReceipt{
		RolloutID: rollout.RolloutID, UnitID: unitID, Kind: guardReceiptKindUnit,
		Intent: intentAdoptLegacy, Key: spec.Key,
		Epoch: rollout.CodeEpoch, Format: rollout.Format, CodeSHA256: rollout.CodeSHA256,
		RetainedSHA256: rollout.RetainedSHA256,
		SpecSHA256:     spec.SpecSHA256, DefinitionSHA256: spec.DefinitionSHA256,
		PrestateSHA256: spec.SpecSHA256, ToEnableState: guardStateOrigin, AttemptID: "a",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Promote the recorded poststate without touching the digests: the row now claims the
	// guard was left at ALWAYS.
	if _, err := db.Exec("UPDATE " + dialect.GuardReceiptsTable + " SET to_enable_state = 'A'"); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if _, err := guardRolloutReceipts(ctx, db, dia, rollout.RolloutID); err == nil {
		t.Fatal("the receipt stream accepted a row whose contents no longer produce its id")
	} else if !errors.Is(err, ErrGuardGateChainBroken) {
		t.Errorf("refused with %v, which is not the named chain error", err)
	}
}
