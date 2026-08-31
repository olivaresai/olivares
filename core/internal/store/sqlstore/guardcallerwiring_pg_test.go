// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// guardcallerwiring_pg_test.go holds the regressions round four asked for by name: the ones
// that go through the PRODUCTION CALLER instead of through a helper.
//
// THE CRITICISM THAT PRODUCED THIS FILE WAS CORRECT, and it is worth stating plainly rather
// than paraphrasing. Round three's findings were each answered with a unit test that drove the
// helper directly — verifyGuardReceiptCensus over a hand-built map, verifyGuardControlPlaneObjects
// on a connection of the test's own — and every one of those tests stayed GREEN when the
// corresponding CALL was deleted from the close. A helper that refuses correctly and is never
// consulted refuses nothing, and a suite that cannot tell those two apart is measuring the
// wrong object.
//
// So the rubric here is single and harsh: break the WIRING, not the helper, and the observable
// result of Open must change.
//
// TWO EXCEPTIONS, NAMED because round five caught this header claiming all of them met it:
//
//   - TestPostgresACommitObeysTheContextItsTransactionBeganUnder measures database/sql and
//     PostgreSQL, not this package. It is here because the close's deadline is only correct if
//     that contract holds, and it will stay green if the close's own BeginTx is changed.
//   - TestPostgresTheCloseCannotOutlastItsWorkBudget pins the RE-ARMING and nothing more. Round
//     five measured that it survives unbinding the transaction from the clock
//     (`BeginTx(context.WithoutCancel(ctx))`) and giving every retry a fresh budget.
//
// THOSE TWO EDGES ARE NOW COVERED, by TestPostgresTheCloseCommitIsBoundToTheClockItsTransaction
// BeganUnder and TestPostgresTheAcquisitionBudgetIsTotalAcrossTheCloseRetries respectively. Both
// mutations were run: each reddens its own test and leaves the other two green. Writing the
// second one is also what found the production defect where the close armed `statement_timeout`
// and `lock_timeout` at the same value, so a wait in ITS OWN acquisition phase arrived as a
// terminal 57014 and that retry loop could not be entered.
//
// CORRECTED: this header used to end "armGuardCloseAcquisition is the only place that armed the
// two equal", and that was FALSE against main from the moment it was written — retryUnit's own
// armAcquisition armed `setLocalTimeouts(actx, tx, d, d)` with the identical defect, on the path
// the close is only the last step of. It is fixed there too now, with the same slack; the scope
// sentence stays because these tests still cover the close's acquisition and nothing wider.
//
// All of them use the SINGLE-ROLE topology, where the application role owns its tables. That is
// not convenience: it is the default deployment, it is the one in which the fixture can seed a
// ledger at all, and it is where an operator with the application's own credentials has the
// capability these checks exist to catch.

// wipeGuardLogForFixture returns one of the three logs to the state a database that has never
// opened a rollout is in.
//
// IT NEEDS NO SUPERUSER, AND THAT IS A MEASUREMENT RATHER THAN A CONVENIENCE. An earlier
// version of this campaign claimed the single-role topology made a tail deletion require one,
// on the strength of the append-only reconcile having revoked DELETE from the role. That claim
// was FALSE and round four was right to refute it: PostgreSQL grants a table's owner every
// privilege on it IMPLICITLY, together with the grant option, so revoking DELETE from itself
// removes nothing it cannot immediately restore. Measured here, on every run of this fixture:
//
//	REVOKE DELETE FROM owner  -> has_table_privilege = false
//	GRANT  DELETE TO owner    -> succeeds
//	DELETE                    -> rows removed
//
// So the honest statement of the limit is the narrower one: in the single-role topology the
// ledger is durable against a CRASH and against the runtime traffic of the application, and NOT
// against the role itself. That is exactly what the boot warns about when it recommends
// --owner-dsn, and it is why the hardened split exists.
//
// The privilege is handed back afterwards so the fixture leaves the posture it found. The guard
// is disabled and RE-ENABLED AS ALWAYS, not merely enabled: 'O' is the state a
// logical-replication apply walks straight through, so a fixture that restored the weaker one
// would leave the database in a posture the next boot has to repair, and the test would then be
// measuring that repair.
func wipeGuardLogForFixture(t *testing.T, owner *sql.DB, table, where string) {
	t.Helper()
	ctx := context.Background()
	if _, err := owner.ExecContext(ctx, `ALTER TABLE `+table+` DISABLE TRIGGER USER`); err != nil {
		t.Fatalf("disable the guard on %s: %v", table, err)
	}
	if _, err := owner.ExecContext(ctx, `GRANT DELETE ON `+table+` TO CURRENT_USER`); err != nil {
		t.Fatalf("the owner cannot restore its own DELETE on %s, which would contradict PostgreSQL's implicit grant options: %v", table, err)
	}
	if _, err := owner.ExecContext(ctx, `DELETE FROM `+table+where); err != nil {
		t.Fatalf("clear %s: %v", table, err)
	}
	if _, err := owner.ExecContext(ctx, `REVOKE DELETE ON `+table+` FROM CURRENT_USER`); err != nil {
		t.Fatalf("restore the append-only posture on %s: %v", table, err)
	}
	if _, err := owner.ExecContext(ctx, `ALTER TABLE `+table+` ENABLE ALWAYS TRIGGER `+table+`_immutable`); err != nil {
		t.Fatalf("restore the guard on %s: %v", table, err)
	}
}

// reopenableGuardLedger drives one clean boot and then returns the ledger to the state that
// makes the NEXT boot open a rollout again.
//
// The gate and the UNIT receipts, and nothing else. Not the inventory and not the BOOTSTRAP
// receipts: v6 writes those once and never again, so wiping them makes the next boot fail for a
// reason unrelated to whatever is being tested. And not the unit receipts alone either —
// leaving them beside an empty gate is a state no history produces.
func reopenableGuardLedger(t *testing.T, cfg store.Config) (*sql.DB, guardManifest, string) {
	t.Helper()
	ctx := context.Background()

	st, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("the first boot failed: %v", err)
	}
	if cerr := st.Close(); cerr != nil {
		t.Fatalf("close the first store: %v", cerr)
	}
	probe := guardPGProbe(t, cfg.DSN)

	m, err := buildGuardManifest(registryWithWidget(t).appendOnlyTables())
	if err != nil {
		t.Fatalf("build the manifest: %v", err)
	}
	empty, err := emptyRetainedDigest()
	if err != nil {
		t.Fatal(err)
	}
	rolloutID, err := guardRolloutID(m.Format, m.CodeEpoch, m.CodeSHA256, 0, empty)
	if err != nil {
		t.Fatal(err)
	}
	wipeGuardLogForFixture(t, probe, dialect.GuardGateEventsTable, "")
	wipeGuardLogForFixture(t, probe, dialect.GuardReceiptsTable, ` WHERE receipt_kind = 'unit'`)
	return probe, m, rolloutID
}

// countGateKind counts the events of one kind, with ONLY, so an inherited child cannot make
// this fixture's own assertions lie.
func countGateKind(t *testing.T, probe *sql.DB, kind gateEventKind) int {
	t.Helper()
	return countRows(t, probe, "SELECT COUNT(*) FROM ONLY "+dialect.GuardGateEventsTable+" WHERE kind = $1", string(kind))
}

// diagnosticCodes is the set of durable diagnostic codes the gate holds, read with ONLY.
func diagnosticCodes(t *testing.T, probe *sql.DB) map[string]bool {
	t.Helper()
	rows, err := probe.QueryContext(context.Background(),
		"SELECT DISTINCT diagnostic_code FROM ONLY "+dialect.GuardGateEventsTable+" WHERE diagnostic_code <> ''")
	if err != nil {
		t.Fatalf("read the durable diagnostics: %v", err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			t.Fatalf("scan a diagnostic code: %v", err)
		}
		out[code] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the durable diagnostics: %v", err)
	}
	return out
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestPostgresAnExtraReceiptRefusesTheCloseThroughOpen is round four's literal closure
// condition for F-04, and the previous test for it was a helper test.
//
// THE SHAPE OF THE ATTACK. A receipt is not a free-standing row: it carries an ordinal, a
// predecessor digest, an id its own body must produce and a chain digest over the three. So the
// interesting forgery is not a corrupt one — guardRolloutReceipts rejects those while reading,
// before any census runs, and a test built on one would pass for the wrong reason. The
// interesting forgery is a SELF-CONSISTENT one: correctly chained, correctly digested,
// counted by the checkpoint, and attributing a unit the plan never enumerated.
//
// insertGuardReceipt is the real writer and it computes the chain fields itself, so the fixture
// hands it the semantic content and the row it lands is exactly as well-formed as a genuine one.
//
// WHAT IT PINS: the close CONSULTS the census, and does so BEFORE it can append `ready`.
//
// MUTATION VERIFIED: deleting the verifyGuardReceiptCensus call from attemptGuardClose (the
// call at guardcoordinator.go, in the block that re-reads the receipts under the locks) makes
// this test FAIL — Open succeeds and a `ready` appears. The helper's own unit test is untouched
// by that mutation, which is precisely why this file exists.
func TestPostgresAnExtraReceiptRefusesTheCloseThroughOpen(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}

	probe, m, rolloutID := reopenableGuardLedger(t, cfg)
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	if len(m.Specs) == 0 {
		t.Fatal("the manifest declares no specs, so this test would pass vacuously")
	}

	// A key that is REAL in its relation and fictitious in its trigger. Borrowing a registered
	// relation is what makes the row credible — it is the shape of a receipt an older edition
	// might have left — while the trigger name guarantees the unit id is one no plan of this
	// edition derives.
	victim := m.Specs[0]
	stray := guardKey{Schema: victim.Key.Schema, Relation: victim.Key.Relation, Trigger: victim.Key.Trigger + "_v5"}
	strayUnit, err := guardUnitID(m.Format, stray, intentAdoptLegacy)
	if err != nil {
		t.Fatalf("derive the stray unit id: %v", err)
	}
	empty, err := emptyRetainedDigest()
	if err != nil {
		t.Fatal(err)
	}

	tx, err := probe.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	written, err := insertGuardReceipt(ctx, tx, dia, guardReceipt{
		RolloutID:        rolloutID,
		UnitID:           strayUnit,
		Kind:             guardReceiptKindUnit,
		Intent:           intentAdoptLegacy,
		Key:              stray,
		Epoch:            m.CodeEpoch,
		Format:           m.Format,
		CodeSHA256:       m.CodeSHA256,
		RetainedRevision: 0,
		RetainedSHA256:   empty,
		SpecSHA256:       victim.SpecSHA256,
		DefinitionSHA256: victim.DefinitionSHA256,
		PrestateSHA256:   victim.SpecSHA256,
		ToEnableState:    guardStateAlways,
		AttemptID:        "fixture-stray-receipt",
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("plant the stray receipt: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit the stray receipt: %v", err)
	}

	// The planted row is self-consistent by the ledger's OWN reader, which is the property that
	// makes this a census test rather than a chain test. Asserting it here means a later failure
	// cannot be explained away as a badly built fixture.
	if _, err := guardRolloutReceipts(ctx, probe, dia, rolloutID); err != nil {
		t.Fatalf("the planted receipt does not read back as a valid chain, so this fixture would test the chain rather than the census: %v", err)
	}
	t.Logf("GUARD_STRAY_RECEIPT|unit=%s|ordinal=%d|chain_valid=true", written.UnitID, written.EventOrdinal)

	st, err := Open(ctx, cfg, registerWidget)
	if err == nil {
		_ = st.Close()
		t.Fatal("a boot whose ledger holds a receipt its plan does not enumerate reached a serving store; the close either did not take the census or took it after authorizing")
	}
	// The refusal must be the census's, and it must name the intruder. An Open that failed for
	// some other reason would satisfy "returns an error" while proving nothing.
	if !strings.Contains(err.Error(), strayUnit) {
		t.Errorf("the refusal does not name the unattributable unit %s, so it is probably a different failure: %v", strayUnit, err)
	}
	if !strings.Contains(err.Error(), "does not enumerate") {
		t.Errorf("the refusal is not the receipt census's: %v", err)
	}
	if ready := countGateKind(t, probe, gateEventReady); ready != 0 {
		t.Errorf("the refused rollout left %d `ready` events; the census ran after the close had already authorized", ready)
	}
	t.Logf("GUARD_RECEIPT_CENSUS_THROUGH_OPEN|refused=true|ready=0|named_unit=true")
}

// TestPostgresAnAdoptionOnlyLineageRefusesThroughOpen is round four's literal closure condition
// for F-03: the TOCTOU that LINEAGE_INCOMPLETE closes, driven through Open, with the control
// that must still close.
//
// THE STATE IT BUILDS, which is the one no earlier test constructed. A durable `pending-opened`
// whose enumeration lists ONLY the adoption of every target — no transition anywhere — while
// one target's guard sits at 'O'. That enumeration is legal at the moment it is read: the rule
// requiring a transition compares against the adoption's DURABLE JUDGED state, and there is no
// judged reading yet. The judged reading arrives DURING this same Open, when the adoption runs
// and lands 'O'. From that instant the enumeration is incomplete — a terminal adoption whose
// judged state is not the desired one — and the close must refuse rather than attest 'A'.
//
// The control is the identical fixture with the same target left at 'A'. There the adoption IS
// the whole lineage, its judged state is the desired one, and the boot must close normally.
// Without it, a refusal that simply rejected every adoption-only enumeration would pass.
//
// MUTATION VERIFIED: removing the LINEAGE_INCOMPLETE block from verifyGuardTerminals makes the
// divergent case FAIL — Open succeeds and `ready` appears while a target the ledger attests as
// 'A' is sitting at 'O'.
func TestPostgresAnAdoptionOnlyLineageRefusesThroughOpen(t *testing.T) {
	for _, tc := range []struct {
		name       string
		leaveAtO   bool
		wantRefuse bool
	}{
		{name: "target_at_O_refuses", leaveAtO: true, wantRefuse: true},
		{name: "control_target_at_A_closes", leaveAtO: false, wantRefuse: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsns := isolatedPG(t)
			ctx := context.Background()
			cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}

			probe, m, rolloutID := reopenableGuardLedger(t, cfg)
			dia, ok := dialect.New(store.EnginePostgres)
			if !ok {
				t.Fatal("no PostgreSQL dialect")
			}
			if len(m.Specs) == 0 {
				t.Fatal("the manifest declares no specs, so this test would pass vacuously")
			}

			// THE ENUMERATION: one adoption per target, no transitions at all.
			adoptions := make([]string, 0, len(m.Specs))
			for _, spec := range m.Specs {
				id, err := guardUnitID(m.Format, spec.Key, intentAdoptLegacy)
				if err != nil {
					t.Fatalf("derive the adoption unit id for %s: %v", spec.Key, err)
				}
				adoptions = append(adoptions, id)
			}

			victim := m.Specs[0]
			if tc.leaveAtO {
				// ENABLE, not ENABLE ALWAYS: 'O' is the legacy state the adoption is allowed to
				// find and NOT allowed to leave behind as final.
				if _, err := probe.ExecContext(ctx, `ALTER TABLE `+quoteIdent(victim.Key.Schema)+`.`+
					quoteIdent(victim.Key.Relation)+` ENABLE TRIGGER `+quoteIdent(victim.Key.Trigger)); err != nil {
					t.Fatalf("return %s to 'O': %v", victim.Key, err)
				}
			}

			empty, err := emptyRetainedDigest()
			if err != nil {
				t.Fatal(err)
			}
			tx, err := probe.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			if _, err := appendGateEvent(ctx, tx, dia, gateEvent{
				RolloutID:        rolloutID,
				Kind:             gateEventPendingOpened,
				Format:           m.Format,
				CodeEpoch:        m.CodeEpoch,
				CodeSHA256:       m.CodeSHA256,
				RetainedRevision: 0,
				RetainedSHA256:   empty,
				Phase:            gatePhasePending,
				Condition:        gateConditionClean,
				ExpectedUnits:    adoptions,
			}); err != nil {
				_ = tx.Rollback()
				t.Fatalf("seed the adoption-only opening: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit the seeded opening: %v", err)
			}

			st, oerr := Open(ctx, cfg, registerWidget)
			if st != nil {
				_ = st.Close()
			}
			ready := countGateKind(t, probe, gateEventReady)
			state := guardEnableStates(t, probe)[victim.Key.Relation]

			if tc.wantRefuse {
				if oerr == nil {
					t.Fatalf("a rollout whose enumeration ends at an adoption that landed 'O' reached a serving store; %s is %q and the gate holds %d `ready`",
						victim.Key, state, ready)
				}
				// The MESSAGE must be the incomplete-lineage one. Asserting on a substring of the
				// prose is deliberate: "returns an error" is satisfied by every other way a boot
				// can fail, and this fixture disturbs enough state to produce several of them.
				if !strings.Contains(oerr.Error(), "enumerates no transition for it") {
					t.Errorf("the refusal is not the incomplete-lineage one, so this test may be passing for another reason: %v", oerr)
				}
				// And the DURABLE diagnostic must carry the code, because an operator reads the
				// ledger rather than the process that exited.
				codes := diagnosticCodes(t, probe)
				if !codes["LINEAGE_INCOMPLETE"] {
					t.Errorf("the ledger records %v, not LINEAGE_INCOMPLETE; the refusal reached the caller but not the history", keysOf(codes))
				}
				if ready != 0 {
					t.Errorf("the refused rollout left %d `ready` events", ready)
				}
				t.Logf("GUARD_LINEAGE_INCOMPLETE_THROUGH_OPEN|refused=true|ready=0|victim_state=%s|durable_code=LINEAGE_INCOMPLETE", state)
				return
			}
			if oerr != nil {
				t.Fatalf("the control boot — the same adoption-only enumeration over targets already at 'A' — was refused, so the divergent case above may be refusing every adoption-only history rather than the incomplete one: %v", oerr)
			}
			if ready == 0 {
				t.Error("the control boot left no `ready`, so it did not actually close")
			}
			t.Logf("GUARD_LINEAGE_CONTROL_THROUGH_OPEN|closed=true|ready=%d|victim_state=%s", ready, state)
		})
	}
}

// TestPostgresDriftInsideTheServeWindowRefusesTheBoot is round four's F-01: the final
// verification must repeat SHAPE and GUARDS, not only the ACL.
//
// THE WINDOW IS REAL AND IT IS NOT REACHABLE ANY OTHER WAY. The rollout verifies the control
// plane inside the migration lock and then releases it; boot continues for several more steps
// before the store is handed out. Drift committed in that gap was carried into service by a
// boot that reported success. Planting the drift BEFORE the boot proves nothing — the
// verification inside the rollout refuses it first, and the test would pass with the final call
// deleted — so the fixture uses the seam that exists for exactly this.
//
// MUTATION VERIFIED: deleting the verifyGuardControlPlaneObjects call from the pre-serve window
// in store.go makes this test FAIL — Open returns a usable store over a ledger whose
// immutability guard is gone.
func TestPostgresDriftInsideTheServeWindowRefusesTheBoot(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}

	st, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("the first boot failed: %v", err)
	}
	if cerr := st.Close(); cerr != nil {
		t.Fatalf("close the first store: %v", cerr)
	}
	probe := guardPGProbe(t, dsns.App)

	gate := dialect.GuardGateEventsTable
	restore := func() {
		if _, err := probe.ExecContext(context.Background(),
			`ALTER TABLE `+gate+` ENABLE ALWAYS TRIGGER `+gate+`_immutable`); err != nil {
			t.Errorf("restore the guard on %s: %v", gate, err)
		}
	}
	// The drift lands at the ONE instant this check exists for: after the rollout released the
	// migration lock, before the store is returned.
	fired := false
	guardPreServeTestHook = func() {
		if fired {
			return
		}
		fired = true
		if _, err := probe.ExecContext(context.Background(),
			`ALTER TABLE `+gate+` DISABLE TRIGGER USER`); err != nil {
			t.Errorf("plant the drift: %v", err)
		}
	}
	t.Cleanup(func() { guardPreServeTestHook = nil; restore() })

	st2, err := Open(ctx, cfg, registerWidget)
	if st2 != nil {
		_ = st2.Close()
	}
	if !fired {
		t.Fatal("the pre-serve hook never ran, so this test measured nothing")
	}
	if err == nil {
		t.Fatal("a boot whose ledger lost its immutability guard between the lock release and the return handed out a store; every later reading of that history assumes the guard is there")
	}
	if !strings.Contains(err.Error(), "no longer matches its own attribution") {
		t.Errorf("the refusal is not the end-of-boot one, so this test may be passing for another reason: %v", err)
	}
	t.Logf("GUARD_SERVE_WINDOW_DRIFT|hook_fired=true|refused=true")
}

// TestPostgresTheCloseCannotOutlastItsWorkBudget drives the work deadline through a real Open.
//
// WHAT IT PROVES AND WHAT IT DOES NOT. It proves the work phase has a ceiling that a boot
// actually hits, and that hitting it leaves NO `ready` — the close rolled back whole. It does
// not prove a COMMIT already in flight is interrupted, because it is not: see guardCloseTxClock
// for the measurement. The guarantee is that the close cannot ENTER a commit after its phase
// expired, and the durable evidence of that is the absent `ready` this asserts.
func TestPostgresTheCloseCannotOutlastItsWorkBudget(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}

	old := guardCloseWorkBudget
	guardCloseWorkBudget = time.Millisecond
	t.Cleanup(func() { guardCloseWorkBudget = old })

	st, err := Open(ctx, cfg, registerWidget)
	if st != nil {
		_ = st.Close()
	}
	if err == nil {
		t.Fatal("a close with a one-millisecond work budget reached a serving store, so the budget bounds nothing")
	}
	if !strings.Contains(err.Error(), "work budget") {
		t.Errorf("the refusal does not name the work budget: %v", err)
	}
	probe := guardPGProbe(t, dsns.App)
	if ready := countGateKind(t, probe, gateEventReady); ready != 0 {
		t.Errorf("a close that ran out of work budget still left %d `ready` events", ready)
	}
	t.Logf("GUARD_WORK_BUDGET_THROUGH_OPEN|refused=true|ready=0")
}

// TestPostgresTheCloseCommitIsBoundToTheClockItsTransactionBeganUnder is the edge round five
// refuted, and the one the previous file could not reach.
//
// WHAT WAS WRONG WITH THE OLD EVIDENCE. Round five replaced `conn.BeginTx(ctx, nil)` with
// `conn.BeginTx(context.WithoutCancel(ctx), nil)` and every budget test stayed GREEN. That is
// not a gap in the assertions, it is a property of where the tests looked: every statement of
// the close is issued with `ctx` passed explicitly, so a statement can never tell the two
// versions apart. Tx.Commit is the only operation that takes no context of its own.
//
// SO THE FIXTURE STOPS THE CLOSE AT THAT EXACT INSTANT — after the `ready` append, before the
// commit — expires the clock, and waits for the context to be done rather than sleeping. What
// happens next is the whole difference:
//
//   - BOUND (production): the commit does not happen, Open fails naming the work phase, and no
//     `ready` is durable. WHICH error surfaces is a race this deliberately does not assert —
//     database/sql's watchdog may have rolled back already (ErrTxDone) or Commit may see the
//     canceled context first. Measured here: `commit: context canceled`. Both are refusals, and
//     depending on either would make the test fragile about something that does not matter.
//   - UNBOUND (the mutation): nothing is watching that transaction, the commit SUCCEEDS, and a
//     close that had run out of its work budget lands `ready` anyway.
//
// MUTATION VERIFIED: with `context.WithoutCancel(ctx)` on the close's BeginTx this test FAILS —
// Open returns a serving store and the gate holds one `ready`.
func TestPostgresTheCloseCommitIsBoundToTheClockItsTransactionBeganUnder(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}

	probe, _, _ := reopenableGuardLedger(t, cfg)

	fired := 0
	guardCloseBeforeCommitTestHook = func(txctx context.Context, expire func()) {
		fired++
		expire()
		// Waiting on the FACT rather than on a duration: there is no sleep to tune and no
		// race to lose, because the only thing this needs is for the cancellation to have
		// happened before the commit is issued.
		<-txctx.Done()
	}
	t.Cleanup(func() { guardCloseBeforeCommitTestHook = nil })

	st, err := Open(ctx, cfg, registerWidget)
	if st != nil {
		_ = st.Close()
	}
	if fired == 0 {
		t.Fatal("the pre-commit hook never ran, so this test measured nothing; the boot did not reach a close")
	}
	if err == nil {
		t.Fatal("a close whose clock expired between its last statement and its commit reached a serving store, so the transaction is not governed by that clock and the work deadline bounds nothing at the one point that matters")
	}
	// The refusal must name the phase, which is what attribute() exists for: an operator
	// reading `transaction has already been committed or rolled back` learns nothing about
	// whose deadline ended it.
	if !strings.Contains(err.Error(), "work budget") {
		t.Errorf("the refusal does not attribute the expiry to the work phase: %v", err)
	}
	if ready := countGateKind(t, probe, gateEventReady); ready != 0 {
		t.Errorf("the close committed %d `ready` events after its clock expired", ready)
	}
	t.Logf("GUARD_CLOSE_COMMIT_BOUND|hook_fired=%d|refused=true|ready=0|err=%v", fired, err)
}

// TestPostgresTheAcquisitionBudgetIsTotalAcrossTheCloseRetries is the second edge: a REAL 55P03,
// consumed budget, and a retry loop whose ceiling is the budget rather than the budget times the
// attempt count.
//
// THE CONTENTION IS REAL AND EXTERNAL. A second session holds the shared trigger function under
// `ALTER FUNCTION ... RESET ALL` in an open transaction, which is the strongest lock a
// non-superuser owner can hold on that row. The close's step 2 is the same statement, so it
// queues, and `lock_timeout` cancels it with 55P03 — the retryable state the loop is built for.
// Nothing about this is injected: the error comes from the server.
//
// ONLY THE `ready` EVENT IS WIPED, and the precision matters: the first version of this fixture
// wiped the whole gate and MEASURED NOTHING. Removing the opening as well makes the next boot
// derive a fresh plan against targets that are already at 'A', so the surviving `adopt-legacy`
// receipt — which records to_enable_state='O' — no longer matches, and the boot refuses long
// before any close:
//
//	refusing to skip unit adopt-legacy for "public"."audit_events" ... records to_enable_state="O"
//	where the plan and rollout require "A"   (elapsed 56ms, 0 retries, and a green test)
//
// Deleting the `ready` alone leaves the opening, the attempt history and every receipt intact, so
// the rollout is still the one that was open, every unit takes its shortcut, and the boot walks
// straight into the close with nothing else to do.
//
// THE OBSERVABLE IS THE CEILING, AND TWO EARLIER SHAPES OF IT MEASURED NOTHING. Both are recorded
// because each looks right until it is mutated:
//
//  1. `guardCloseLockTimeout` ABOVE the budget. Then the per-statement value is clamped to the
//     whole budget, the transaction's own clock expires at the same instant, and the close dies
//     with a canceled context instead of 55P03. A canceled context is not retryable, so there
//     is exactly ONE attempt and a fresh-per-attempt budget is indistinguishable. Measured:
//     elapsed 1.048s and 0 retries under the mutation, green.
//  2. Timing the whole Open. Boot does migrations, unit shortcuts and two folds before the close
//     is even reached, and that fixed cost sits inside any threshold generous enough to be
//     stable.
//
// So `lockTimeout` is kept BELOW the budget — the wait ends in a real 55P03 and the loop actually
// retries — and the boot's fixed cost is SUBTRACTED by timing a control boot that closes normally
// over the same fixture. What is left is the time the close spent waiting, and the budget is the
// ceiling of that number no matter how many attempts it is divided into.
//
// MUTATION VERIFIED: giving each attempt its own acquisition budget — a fresh
// `newLockBudget(guardCloseAcquisitionBudget, ...)` per iteration, with the loop's backoff reading
// it too — makes this test FAIL, because each attempt buys a full guardCloseLockTimeout wait — three of
// them, against a budget the fixture sets below their sum. The numbers live at the fixture, not
// here, because a header that repeats them drifts the moment they are tuned; round twelve caught
// this one still saying 800ms after the fixture had moved to 4s.
func TestPostgresTheAcquisitionBudgetIsTotalAcrossTheCloseRetries(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}

	st, err := Open(ctx, cfg, registerWidget)
	if err != nil {
		t.Fatalf("the first boot failed: %v", err)
	}
	if cerr := st.Close(); cerr != nil {
		t.Fatalf("close the first store: %v", cerr)
	}
	probe := guardPGProbe(t, dsns.App)
	// The `ready` ALONE — see the header for what wiping more than this measured instead.
	reopen := func() {
		t.Helper()
		wipeGuardLogForFixture(t, probe, dialect.GuardGateEventsTable, ` WHERE kind = 'ready'`)
		if ready := countGateKind(t, probe, gateEventReady); ready != 0 {
			t.Fatalf("the fixture left %d `ready` events, so the next boot would take the fast path and never reach a close", ready)
		}
	}

	// THE TWO CEILINGS ARE SEPARATED BY SECONDS, NOT BY MILLISECONDS, and that is a fix.
	//
	// This used to run a 1s budget against an 800ms lock_timeout, leaving 200ms for PostgreSQL,
	// the driver and the Go runtime to surface and classify 55P03 before the transaction's own
	// clock expired. Round ten measured the ordering inverting under a full-package run —
	// `context canceled`, zero retries — while five isolated controls got the intended one. The
	// prose called that ordering guaranteed; it was scheduled by the host.
	//
	// THE ARITHMETIC HAS TO KEEP BOTH ENDS, and I lost one twice before getting it right.
	//
	// The ceiling this test asserts is 2*budget, not budget, so the per-attempt-budget mutant is
	// only caught when guardCloseMaxAttempts waits EXCEED TWICE the budget. Widening to 3s/800ms
	// fixed the premise and let the mutant through at 2.4s; narrowing to 1.5s/600ms let it
	// through again at 1.8s against a 3s ceiling. Both times I checked one end and reported the
	// fixture fixed.
	//
	// Both ends, written as the inequality they are, with guardCloseMaxAttempts = 3:
	//
	//   mutant caught   3 * lockTimeout > 2 * budget      12s > 10s
	//   wait is a 55P03 lockTimeout     < budget          4s  <  5s
	//   classification margin budget - lockTimeout        1s, five times the 200ms that
	//                                                     made this fixture race the host
	budget := 5 * time.Second
	oldBudget, oldTimeout := guardCloseAcquisitionBudget, guardCloseLockTimeout
	guardCloseAcquisitionBudget = budget
	// BELOW the budget by a full second, so the wait ends in a real 55P03 that the loop can
	// retry — see the header for what happens when it is above. A SECOND is not a proof that the
	// scheduler cannot close it, and an earlier wording here said exactly that: the margin is
	// five times the one that was measured failing, which is a reason to expect it to hold, not a
	// bound on the host.
	guardCloseLockTimeout = 4 * time.Second
	t.Cleanup(func() { guardCloseAcquisitionBudget, guardCloseLockTimeout = oldBudget, oldTimeout })

	// THE CONTROL: the same fixture, the same close, nothing holding the function. Its duration
	// is the boot's fixed cost, and subtracting it is what leaves a number about the WAITING.
	reopen()
	controlStart := time.Now()
	stc, cerr := Open(ctx, cfg, registerWidget)
	control := time.Since(controlStart)
	if stc != nil {
		_ = stc.Close()
	}
	if cerr != nil {
		t.Fatalf("the control boot — same fixture, no external holder — failed, so the contended one below would prove nothing: %v", cerr)
	}
	if ready := countGateKind(t, probe, gateEventReady); ready == 0 {
		t.Fatal("the control boot left no `ready`, so it never reached a close and is not a baseline for one")
	}

	// THE EXTERNAL HOLDER, on its own connection, for the whole contended boot. `ALTER FUNCTION
	// ... RESET ALL` is the strongest lock a non-superuser owner can hold on that pg_proc row,
	// and it is the SAME statement the close's step 2 issues, so the close queues behind it.
	reopen()
	fn := canonicalGuardDefinition().Function
	holder, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("open the holder connection: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	htx, err := holder.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin the holder transaction: %v", err)
	}
	defer func() { _ = htx.Rollback() }()
	if _, err := htx.ExecContext(ctx, `ALTER FUNCTION `+quoteIdent(fn.Schema)+`.`+
		quoteIdent(fn.Name)+`() RESET ALL`); err != nil {
		t.Fatalf("take the external hold on the shared function: %v", err)
	}

	var logs strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	start := time.Now()
	st2, oerr := Open(ctx, cfg, registerWidget)
	elapsed := time.Since(start)
	if st2 != nil {
		_ = st2.Close()
	}
	if oerr == nil {
		t.Fatal("a close that never got the shared function reached a serving store")
	}
	if !guardCloseRetryable(oerr) && !strings.Contains(oerr.Error(), "acquisition budget") {
		t.Errorf("the close did not end in a lock-race state or a spent acquisition budget, so it may not have contended at all: %v", oerr)
	}
	// THE CEILING, on the waiting rather than on the boot. Two budgets' worth is impossible for
	// one shared budget however many attempts divide it, and is the first thing a per-attempt
	// budget buys.
	waiting := elapsed - control
	retries := strings.Count(logs.String(), "lost a lock race and will be retried")
	// THE SHARING IS ONLY UNDER TEST IF THERE IS SOMETHING TO SHARE. Round seven set
	// guardCloseMaxAttempts to 1 and this stayed green at retries=0: a single 55P03 says
	// nothing about a budget divided between attempts, and the ceiling below is satisfied
	// trivially by one wait. The loop must actually have gone round.
	if retries == 0 {
		t.Errorf("the close never retried, so the budget was never divided and the ceiling below proves nothing about sharing it: %v", oerr)
	}
	if ceiling := 2 * budget; waiting >= ceiling {
		t.Errorf("the close spent %s waiting (boot %s minus the %s control) against an acquisition budget of %s, over %d attempts; a shared budget cannot be exceeded that way",
			waiting, elapsed, control, budget, retries+1)
	}
	if ready := countGateKind(t, probe, gateEventReady); ready != 0 {
		t.Errorf("a close that never took the function's lock left %d `ready` events", ready)
	}
	t.Logf("GUARD_ACQUISITION_BUDGET_TOTAL|waiting=%s|boot=%s|control=%s|budget=%s|retries=%d|ready=0|err=%v",
		waiting, elapsed, control, budget, retries, oerr)
}

// TestPostgresACommitObeysTheContextItsTransactionBeganUnder measures the property the whole
// F-12 fix rests on, against a real server and a real driver.
//
// It is a measurement of database/sql and PostgreSQL rather than of this package, and it is
// here because the fix is only correct if it holds: the close moves its deadline onto the
// context handed to BeginTx precisely because that is the context the transaction's LIFETIME is
// bound to. If this ever stopped being true, the close would silently lose its ceiling and
// nothing else would notice.
//
// WHAT THE MECHANISM TURNS OUT TO BE, measured here rather than assumed: database/sql runs a
// watchdog per transaction that rolls back as soon as that context is done, so by the time
// Commit is called the transaction is already finished and the error is ErrTxDone rather than
// the context's. The assertion is therefore on the OUTCOME — no commit, no durable row — and
// not on which of the two errors surfaces, because both are correct and the race between them
// is not something this package should depend on.
func TestPostgresACommitObeysTheContextItsTransactionBeganUnder(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	db, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `CREATE TABLE commit_ctx_probe (n INT PRIMARY KEY)`); err != nil {
		t.Fatalf("create the probe table: %v", err)
	}

	txctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	tx, err := db.BeginTx(txctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.ExecContext(txctx, `INSERT INTO commit_ctx_probe VALUES (1)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	<-txctx.Done()
	cerr := tx.Commit()
	if cerr == nil {
		t.Fatal("a commit issued after its transaction's context expired SUCCEEDED, so a deadline on BeginTx does not govern the commit and the close's ceiling is imaginary")
	}
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commit_ctx_probe`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Errorf("the refused commit left %d durable rows", rows)
	}
	t.Logf("GUARD_COMMIT_CONTEXT|commit_err=%v|durable_rows=%d", cerr, rows)
}
