// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// guardledgerconcurrency_pg_test.go closes the two findings this campaign twice declared
// unreachable without new production seams. They were reachable; the reproductions here are the
// ones the round-five contrast supplied, and neither needs a hook.
//
// THE LESSON IS WORTH MORE THAN THE TESTS. Both were called "owed, and not producible" on the
// strength of a reading of the code rather than an attempt. "Not measured" licenses measuring;
// it never licenses concluding.

// TestPostgresAFailedUnitLeavesItsAttemptFailedEvent is F-13.
//
// WHY IT LOOKED UNREACHABLE. The unit's own statement is `ALTER TABLE ... ENABLE ALWAYS TRIGGER`,
// which the owner is allowed to run, so nothing in the fixture's reach could make it fail without
// also breaking the boot before the runner started — and a runner that never starts writes no
// `attempt-failed`.
//
// WHAT MAKES IT REACHABLE. An EVENT TRIGGER refuses one DDL tag for the length of one boot. The
// unit's statement then fails the way a real one would — server-side, mid-attempt, after the
// prestate was captured — and the production writer takes its own failure path.
//
// WHAT IT PINS: that the failure path is WIRED — a unit that dies mid-attempt leaves a durable
// `attempt-failed` carrying a diagnostic code, and the rollout does not close.
//
// WHAT IT DOES NOT PIN: this fixture's parent context is never canceled, so removing
// `WithoutCancel` from afterFailure leaves it green — round six measured that. The canceled
// parent is covered separately by
// TestPostgresAFailedUnitRecordsItsFailureWhenTheParentIsCancelled.
//
// AND THE REASON THIS COMMENT ONCE GAVE FOR NOT COVERING IT — "there is no deterministic way to
// hit that window from here without a seam" — WAS WRONG, which round seven demonstrated by
// writing the fixture. Holding the shared function parks a unit after its announcement, and the
// parent can be canceled on that state rather than on a timer. It also found a production defect
// while it was there.
func TestPostgresAFailedUnitLeavesItsAttemptFailedEvent(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}

	probe, m, _ := reopenableGuardLedger(t, cfg)
	if len(m.Specs) == 0 {
		t.Fatal("the manifest declares no specs, so this test would pass vacuously")
	}

	// A TARGET THAT ACTUALLY NEEDS THE STATEMENT. Wiping the log alone is not enough: the
	// triggers are still at 'A', so the units find the state they want and emit no DDL at all
	// — the first version of this test refused an event trigger that never fired and reported
	// a serving store. Returning one target to 'O' is what makes a unit issue the ALTER TABLE
	// the trigger is there to refuse.
	victim := m.Specs[0]
	if _, err := probe.ExecContext(ctx, `ALTER TABLE `+quoteIdent(victim.Key.Schema)+`.`+
		quoteIdent(victim.Key.Relation)+` ENABLE TRIGGER `+quoteIdent(victim.Key.Trigger)); err != nil {
		t.Fatalf("return %s to 'O': %v", victim.Key, err)
	}

	// The event trigger needs a superuser, and the isolated fixture provides one pointed at
	// THIS database precisely so an adversarial test does not have to touch another.
	super, err := sql.Open("pgx", dsns.Superuser)
	if err != nil {
		t.Fatalf("open the superuser connection: %v", err)
	}
	t.Cleanup(func() { _ = super.Close() })
	targetDDL := "ALTER TABLE ONLY " + quoteIdent(victim.Key.Schema) + "." +
		quoteIdent(victim.Key.Relation) + " ENABLE ALWAYS TRIGGER " + quoteIdent(victim.Key.Trigger)
	if _, err := super.ExecContext(ctx, `CREATE OR REPLACE FUNCTION public.guard_refuse_alter() RETURNS event_trigger AS $$
BEGIN
  IF pg_catalog.current_query() OPERATOR(pg_catalog.=) `+quoteLiteral(targetDDL)+` THEN
    RAISE EXCEPTION 'guard fixture refuses the target ALTER TABLE';
  END IF;
END;
$$ LANGUAGE plpgsql
SET search_path = pg_catalog`); err != nil {
		t.Fatalf("create the refusing function: %v", err)
	}
	if _, err := super.ExecContext(ctx,
		`CREATE EVENT TRIGGER guard_refuse_alter_trg ON ddl_command_start WHEN TAG IN ('ALTER TABLE') EXECUTE FUNCTION public.guard_refuse_alter()`); err != nil {
		t.Fatalf("create the event trigger: %v", err)
	}
	// Dropped FIRST among the cleanups registered so far, because everything registered
	// earlier — including the fixture's own guard restoration — issues ALTER TABLE.
	t.Cleanup(func() {
		if _, err := super.ExecContext(context.Background(), `DROP EVENT TRIGGER IF EXISTS guard_refuse_alter_trg`); err != nil {
			t.Errorf("drop the event trigger: %v", err)
		}
	})

	st, oerr := Open(ctx, cfg, registerWidget)
	if st != nil {
		_ = st.Close()
	}
	if oerr == nil {
		t.Fatal("a boot whose guard units could not run reached a serving store")
	}

	failed := countGateKind(t, probe, gateEventAttemptFailed)
	started := countGateKind(t, probe, gateEventAttemptStarted)
	ready := countGateKind(t, probe, gateEventReady)
	if failed == 0 {
		t.Errorf("a unit that failed mid-attempt left no `attempt-failed` (started=%d); the failure path writes nothing durable and an operator reading the ledger cannot tell a crash from a refusal",
			started)
	}
	if started == 0 {
		t.Error("no `attempt-started` either, so the runner never reached the statement and this fixture is not exercising the failure path")
	}
	if ready != 0 {
		t.Errorf("the rollout closed with %d `ready` despite a failed unit", ready)
	}
	// The record has to carry a DIAGNOSIS. An `attempt-failed` with no code is a row that
	// says something went wrong and refuses to say what.
	codes := diagnosticCodes(t, probe)
	if len(codes) == 0 {
		t.Error("the failure events carry no diagnostic code")
	}
	t.Logf("GUARD_ATTEMPT_FAILED_DURABLE|started=%d|failed=%d|ready=%d|codes=%v",
		started, failed, ready, keysOf(codes))
}

// TestPostgresTwoWritersCannotShareAGateOrdinal is F-11.
//
// WHY IT LOOKED UNREACHABLE. appendGateEvent reads the stream head and then inserts, and the two
// statements are microseconds apart, so "make two writers read the same head" read like a
// scheduling wish rather than a fixture.
//
// WHAT MAKES IT REACHABLE — and it is a lock-mode argument, not a timing one. An external SHARE
// lock on the gate CONFLICTS WITH ROW EXCLUSIVE, which is what INSERT takes, and PERMITS ACCESS
// SHARE, which is what the head SELECT takes. So both writers read the head, both queue on their
// insert, and the interleaving stops being a race the fixture has to win.
//
// WHAT IT PINS: the ordinal's uniqueness is enforced by the DATABASE and not by the arithmetic.
// One writer commits; the other must be REFUSED with 23505 rather than landing a second event at
// the same position — which would fork the chain, since every successor digest is computed over
// a predecessor selected by ordinal.
func TestPostgresTwoWritersCannotShareAGateOrdinal(t *testing.T) {
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
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}

	// The rollout AND ITS EDITION TUPLE, read from the ledger the boot just wrote rather than
	// rebuilt. A CHECK constraint compares the tuple against the stream, so an event carrying a
	// tuple of the fixture's own invention is refused with 23514 before its ordinal can collide
	// — measured, on the version of this test that only read the rollout id.
	var (
		rolloutID                  string
		format, epoch, retainedRev int64
		codeSHA, retainedSHA       []byte
	)
	if err := probe.QueryRowContext(ctx,
		"SELECT rollout_id, manifest_format, code_epoch, code_sha256, retained_revision, retained_sha256 FROM ONLY "+
			dialect.GuardGateEventsTable+" ORDER BY event_ordinal DESC LIMIT 1").
		Scan(&rolloutID, &format, &epoch, &codeSHA, &retainedRev, &retainedSHA); err != nil {
		t.Fatalf("read the stream's edition: %v", err)
	}
	var codeDigest, retainedDigest [32]byte
	copy(codeDigest[:], codeSHA)
	copy(retainedDigest[:], retainedSHA)
	before := countRows(t, probe, "SELECT COUNT(*) FROM ONLY "+dialect.GuardGateEventsTable)

	// THE FENCE THAT MAKES THE INTERLEAVING DETERMINISTIC.
	//
	// It runs as the SUPERUSER, and that is a privilege fact rather than a convenience: PostgreSQL
	// requires UPDATE, DELETE or TRUNCATE for every LOCK TABLE mode above ROW EXCLUSIVE, and the
	// append-only reconcile revokes exactly those from the application role. Measured: the app
	// role gets `permission denied for table olivares_guard_gate_events (SQLSTATE 42501)`. The
	// holder is scenery — the writers under test are the application's own.
	holder, err := sql.Open("pgx", dsns.Superuser)
	if err != nil {
		t.Fatalf("open the holder: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	htx, err := holder.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin the holder: %v", err)
	}
	released := false
	defer func() {
		if !released {
			_ = htx.Rollback()
		}
	}()
	if _, err := htx.ExecContext(ctx, `LOCK TABLE `+dialect.GuardGateEventsTable+` IN SHARE MODE`); err != nil {
		t.Fatalf("take the SHARE lock: %v", err)
	}

	// THE TWO EVENTS MUST BE ABLE TO COLLIDE ON THE ORDINAL AND ON NOTHING ELSE, and the first
	// version of this fixture got that wrong in a way that made it a FALSE POSITIVE. It gave
	// both writers the same event, and the gate carries two uniquenesses:
	//
	//	UNIQUE (rollout_id, event_ordinal)
	//	UNIQUE (rollout_id, unit_id, diagnostic_fingerprint)
	//
	// so the 23505 it observed came from the SECOND one. Round six deleted the ordinal
	// uniqueness from the DDL and the test stayed GREEN — it had been measuring the wrong
	// constraint all along. 23505 means `unique_violation` and names nothing by itself.
	//
	// Different units, therefore different diagnostic fingerprints, and the same ordinal is the
	// only thing left for them to fight over. And the refusal is checked by CONSTRAINT NAME, not
	// by SQLSTATE, so a future uniqueness cannot quietly take over this test's job.
	m, err := buildGuardManifest(registryWithWidget(t).appendOnlyTables())
	if err != nil {
		t.Fatalf("build the manifest: %v", err)
	}
	if len(m.Specs) < 2 {
		t.Fatalf("the manifest declares %d spec(s); this fixture needs two distinct units so the writers cannot collide on the diagnostic uniqueness", len(m.Specs))
	}
	units := make([]string, 2)
	for i := range units {
		id, uerr := guardUnitID(m.Format, m.Specs[i].Key, intentAdoptLegacy)
		if uerr != nil {
			t.Fatalf("derive the unit id for %s: %v", m.Specs[i].Key, uerr)
		}
		units[i] = id
	}
	// The constraint name is DISCOVERED from the catalog, because PostgreSQL generates it and
	// truncates at NAMEDATALEN-1; spelling it by hand is how an earlier fixture in this campaign
	// failed with 42704.
	// BY COLUMNS, NOT BY ARITY. Round seven planted a different two-column UNIQUE and this
	// query happily called it the ordinal one, so the fixture went on to accept ITS refusal —
	// the same false positive as before, one level down. The set of columns is the identity.
	var ordinalConstraint string
	switch err := probe.QueryRowContext(ctx, `SELECT c.conname
FROM pg_catalog.pg_constraint c
WHERE c.conrelid = $1::regclass AND c.contype = 'u'
  AND (SELECT array_agg(a.attname::text ORDER BY a.attname::text)
         FROM pg_catalog.pg_attribute a
        WHERE a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey))
      = ARRAY['event_ordinal','rollout_id']`,
		dialect.GuardGateEventsTable).Scan(&ordinalConstraint); {
	case errors.Is(err, sql.ErrNoRows):
		// Fail FAST and say why. Without this the fixture would fall through to a race whose
		// only possible refusal comes from another constraint — which is exactly the false
		// positive round six found.
		t.Fatalf("the gate carries no UNIQUE over exactly (rollout_id, event_ordinal), so nothing in the database enforces one event per position; the chain's predecessor digest selects by that column and a duplicate would fork it")
	case err != nil:
		t.Fatalf("discover the ordinal uniqueness: %v", err)
	}
	event := func(i int) gateEvent {
		return gateEvent{
			RolloutID:        rolloutID,
			Kind:             gateEventVerificationFailed,
			UnitID:           units[i],
			Key:              m.Specs[i].Key,
			Format:           format,
			CodeEpoch:        epoch,
			CodeSHA256:       codeDigest,
			RetainedRevision: retainedRev,
			RetainedSHA256:   retainedDigest,
			Phase:            gatePhasePending,
			Condition:        gateConditionBlocked,
			Diagnostic: guardDiagnostic{
				Code: "FIXTURE_ORDINAL_RACE", RetryClass: guardRetryClassPermanent,
				UnblockPolicy: "operator", Details: "two writers, one ordinal",
			},
		}
	}

	type outcome struct {
		err error
	}
	results := make([]outcome, 2)
	var wg sync.WaitGroup
	var ready sync.WaitGroup
	ready.Add(2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conn, cerr := probe.Conn(ctx)
			if cerr != nil {
				results[i] = outcome{err: cerr}
				ready.Done()
				return
			}
			defer func() { _ = conn.Close() }()
			tx, terr := conn.BeginTx(ctx, nil)
			if terr != nil {
				results[i] = outcome{err: terr}
				ready.Done()
				return
			}
			defer func() { _ = tx.Rollback() }()
			// The head SELECT happens inside appendGateEvent and takes ACCESS SHARE, which
			// the holder's SHARE permits; the INSERT that follows takes ROW EXCLUSIVE, which
			// it does not. Both writers therefore park on the same ordinal.
			ready.Done()
			if _, aerr := appendGateEvent(ctx, tx, dia, event(i)); aerr != nil {
				results[i] = outcome{err: aerr}
				return
			}
			results[i] = outcome{err: tx.Commit()}
		}(i)
	}

	ready.Wait()
	// Wait for BOTH writers to be parked on a lock before releasing, so the fixture is not
	// racing the goroutines it just started. Asserting on the server's own view of who is
	// waiting is what makes this deterministic rather than a sleep.
	waitForLockWaiters(t, probe, dialect.GuardGateEventsTable, 2)
	released = true
	if err := htx.Rollback(); err != nil {
		t.Fatalf("release the SHARE lock: %v", err)
	}
	wg.Wait()

	var committed, refused int
	for _, r := range results {
		var pgErr *pgconn.PgError
		switch {
		case r.err == nil:
			committed++
		case errors.As(r.err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == ordinalConstraint:
			refused++
		case errors.As(r.err, &pgErr) && pgErr.Code == "23505":
			t.Errorf("a writer was refused by %q rather than by the ordinal uniqueness %q, so this fixture is measuring a different constraint: %v",
				pgErr.ConstraintName, ordinalConstraint, r.err)
		default:
			t.Errorf("a writer failed with something other than a unique violation: %v (sqlstate %q)", r.err, sqlStateOf(r.err))
		}
	}
	if committed != 1 || refused != 1 {
		t.Errorf("of two writers racing for one ordinal, %d committed and %d were refused by %s; exactly one of each is the only outcome that keeps the chain single",
			committed, refused, ordinalConstraint)
	}
	// MEASURED, then reported. The previous version asserted against a query and then logged
	// `before+1` regardless — so a run where two events landed printed `10->11` while the
	// assertion above had just read 12. Same defect as the fence log 37862b88 fixed, three
	// commits later; the count is read once and feeds both.
	after := countRows(t, probe, "SELECT COUNT(*) FROM ONLY "+dialect.GuardGateEventsTable)
	if after != before+1 {
		t.Errorf("the gate holds %d events, up from %d; two writers landed at one ordinal and the chain has forked", after, before)
	}
	// And the ordinals are still a sequence with no repetition, which is the property the
	// predecessor digest depends on.
	if dup := countRows(t, probe, "SELECT COUNT(*) FROM (SELECT event_ordinal FROM ONLY "+
		dialect.GuardGateEventsTable+" GROUP BY rollout_id, event_ordinal HAVING COUNT(*) > 1) d"); dup != 0 {
		t.Errorf("%d ordinals are held by more than one event", dup)
	}
	t.Logf("GUARD_ORDINAL_COLLISION|committed=%d|refused_by=%s|events=%d->%d",
		committed, ordinalConstraint, before, after)
}

// waitForLockWaiters blocks until at least n backends are waiting on a lock for the relation.
func waitForLockWaiters(t *testing.T, db *sql.DB, relation string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*)
FROM pg_catalog.pg_locks l
JOIN pg_catalog.pg_stat_activity a ON a.pid = l.pid
WHERE l.granted = false AND l.relation = $1::regclass`, relation).Scan(&waiting); err != nil {
			t.Fatalf("read the lock waiters: %v", err)
		}
		if waiting >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d writers ever queued on %s, so the fixture never built the collision it exists to build",
				waiting, n, relation)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestPostgresTheWorkBudgetIsTotalAcrossTheCloseRetries is the edge I declared unreachable, and
// the declaration was wrong.
//
// WHAT I CLAIMED, in code and in the amendment: "after the locks are held nothing waits, so no
// 40P01/55P03 can arrive to send the close back to the loop with work budget already spent."
// The code contradicts it. In the single-role topology the close can only take ROW EXCLUSIVE on
// the gate (the stronger modes need privileges the append-only reconcile revokes), and ROW
// EXCLUSIVE DOES NOT CONFLICT WITH ITSELF — so another session's INSERT is not excluded at all.
// Table-level compatibility is not the whole story either: two inserts aiming at the same key of
// a UNIQUE index wait on each other at the row level, whatever the table lock says.
//
// So the wait exists, and it lands on the `ready` append, which is the LAST thing the work phase
// does — after the projection, the two folds, the census and the checkpoint have all spent
// budget. That is precisely the shape the sharing exists for.
//
// THE FIXTURE. An external transaction of the application role inserts a well-formed event at the
// ordinal the close is about to use and never commits. The close does its work, queues on the
// unique index, and `lock_timeout` cuts it with 55P03 — retryable — with the work budget already
// part-spent. The holder stays for every attempt.
//
// MUTATION VERIFIED: removing the `if g.work == nil` guard from guardCloseBudgets.workBudget, so
// each attempt builds a fresh budget, makes this FAIL — three attempts buy roughly a full
// lock_timeout each.
//
// ⛔ RE-VERIFICADO EL 2026-08-24 CONTRA LA CONTABILIDAD NUEVA, y no por cortesia: cuando el
// sujeto de la asercion dejo de ser el reloj de pared, esta linea paso a AFIRMAR una
// verificacion que el cambio podia haber invalidado en silencio. Una afirmacion falsa sobre lo
// que esta verificado, escrita como hecho, no la audita nadie. Medido, dos mutantes distintos:
//
//	B · quitar la guarda `if g.work == nil`   work_spent=2,279 / 2,259 s   FAIL
//	A · `budgets.work = nil` en el bucle      work_spent=2,229 / 2,190 s   FAIL
//	    sin mutante                           work_spent=900 ms clavados   PASS
//
// A es el que NO estaba escrito y es el que se escribe de verdad: no necesita conocer la
// contabilidad. La primera version de este cambio lo dejaba VIVO con 715 ms.
//
// I concluded this was impossible by reading the lock table rather than attempting the fixture,
// which is the error this whole round wrote a lesson about. The lesson survives the test.
func TestPostgresTheWorkBudgetIsTotalAcrossTheCloseRetries(t *testing.T) {
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
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("no PostgreSQL dialect")
	}
	// The `ready` alone: the rollout stays open, every unit keeps its receipt and takes its
	// shortcut, and the next boot walks straight into the close.
	wipeGuardLogForFixture(t, probe, dialect.GuardGateEventsTable, ` WHERE kind = 'ready'`)

	var (
		rolloutID                  string
		format, epoch, retainedRev int64
		codeSHA, retainedSHA       []byte
	)
	if err := probe.QueryRowContext(ctx,
		"SELECT rollout_id, manifest_format, code_epoch, code_sha256, retained_revision, retained_sha256 FROM ONLY "+
			dialect.GuardGateEventsTable+" ORDER BY event_ordinal DESC LIMIT 1").
		Scan(&rolloutID, &format, &epoch, &codeSHA, &retainedRev, &retainedSHA); err != nil {
		t.Fatalf("read the stream's edition: %v", err)
	}
	var codeDigest, retainedDigest [32]byte
	copy(codeDigest[:], codeSHA)
	copy(retainedDigest[:], retainedSHA)

	m, err := buildGuardManifest(registryWithWidget(t).appendOnlyTables())
	if err != nil {
		t.Fatalf("build the manifest: %v", err)
	}
	if len(m.Specs) == 0 {
		t.Fatal("the manifest declares no specs")
	}
	victim := m.Specs[0]
	unitID, err := guardUnitID(m.Format, victim.Key, intentAdoptLegacy)
	if err != nil {
		t.Fatalf("derive the unit id: %v", err)
	}

	// THE UNCOMMITTED SQUATTER, on the ordinal the close's `ready` will compute for itself.
	// appendGateEvent derives that ordinal from the head, so it lands on exactly the row the
	// close is about to try.
	holder, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("open the holder: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	htx, err := holder.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin the holder: %v", err)
	}
	defer func() { _ = htx.Rollback() }()
	squatter, err := appendGateEvent(ctx, htx, dia, gateEvent{
		RolloutID: rolloutID, Kind: gateEventVerificationFailed,
		UnitID: unitID, Key: victim.Key,
		Format: format, CodeEpoch: epoch, CodeSHA256: codeDigest,
		RetainedRevision: retainedRev, RetainedSHA256: retainedDigest,
		Phase: gatePhasePending, Condition: gateConditionBlocked,
		Diagnostic: guardDiagnostic{
			Code: "FIXTURE_WORK_BUDGET", RetryClass: guardRetryClassPermanent,
			UnblockPolicy: "operator", Details: "squatting on the ready ordinal",
		},
	})
	if err != nil {
		t.Fatalf("plant the uncommitted squatter: %v", err)
	}

	budget := 900 * time.Millisecond
	oldWork, oldLock := guardCloseWorkBudget, guardCloseLockTimeout
	guardCloseWorkBudget = budget
	guardCloseLockTimeout = 700 * time.Millisecond
	t.Cleanup(func() { guardCloseWorkBudget, guardCloseLockTimeout = oldWork, oldLock })

	var logs strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// EL SUJETO ES EL PRESUPUESTO, NO EL RELOJ DE `Open`. Ver guardCloseWorkSpentObserver.
	//
	// ⛔ Y SE CUENTAN LAS OBSERVACIONES, PORQUE EL OBSERVADOR ES DE PROCESO. Un contraste
	// externo señaló el camino y lo medí: `guardCloseWorkSpentObserver` es una variable de
	// paquete, asi que CUALQUIER close de guardia que corra mientras este test lo tiene
	// instalado escribe en `workSpent`, y la asercion juzgaria el gasto de OTRO close sin que
	// nada lo diga. Hoy es inalcanzable —este test y el otro llamante
	// (`guardfence_pg_test.go`) son seriales, y Go no solapa un test serial con los paralelos—
	// pero el paquete tiene 171 llamadas a `t.Parallel()` y basta con que alguien anada una
	// aqui para que pase a ser alcanzable EN SILENCIO.
	//
	// Contar y exigir exactamente una convierte esa atribucion cruzada en un fallo que se
	// NOMBRA. No quita la global —eso es un rediseno— pero quita el silencio, que es la mitad
	// cara: un numero ajeno se parece exactamente a uno propio.
	var (
		obsMu        sync.Mutex
		workSpent    time.Duration
		observations int
	)
	oldObs := guardCloseWorkSpentObserver
	guardCloseWorkSpentObserver = func(d time.Duration) {
		obsMu.Lock()
		defer obsMu.Unlock()
		observations++
		workSpent = d
	}
	t.Cleanup(func() { guardCloseWorkSpentObserver = oldObs })

	start := time.Now()
	st2, oerr := Open(ctx, cfg, registerWidget)
	elapsed := time.Since(start)
	// La foto se toma AQUI, antes de cerrar `st2`: un cierre nuestro anadiria su propia
	// observacion y el conteo dejaria de discriminar lo que existe para discriminar.
	obsMu.Lock()
	spent, seen := workSpent, observations
	obsMu.Unlock()
	if st2 != nil {
		_ = st2.Close()
	}
	if seen != 1 {
		t.Fatalf("el observador vio %d gasto(s) donde este close produce UNO: la cifra puede ser de otro close y la asercion no estaria juzgando su sujeto (elapsed %s)",
			seen, elapsed)
	}
	if oerr == nil {
		t.Fatal("a close whose `ready` could never be inserted reached a serving store")
	}
	retries := strings.Count(logs.String(), "lost a lock race and will be retried")
	if retries == 0 {
		t.Errorf("the close never retried, so it never returned to the loop with work budget spent and this fixture is not exercising the sharing: %v", oerr)
	}
	// THE CEILING. Two budgets' worth is impossible for one shared budget however many attempts
	// divide it, and is the first thing a per-attempt budget buys.
	//
	// ⛔ SOBRE `workSpent` Y NO SOBRE `elapsed`, Y EL TECHO NO SUBE. Este mismo `2 * budget`
	// se comparaba antes con el reloj de pared de `Open` —montaje, migraciones, adquisicion y
	// trabajo—, que es mas ancho que el sujeto. Medido el 2026-08-24 con Postgres: 1,101-1,124 s
	// en caja tranquila (sigma 20 ms) contra 1,8 s, o sea 38 % de margen, y en CI se paso DOS
	// veces en SHAs distintos con la caja cargada (+2,4 % y +15,8 %). Lo que cruzaba el techo
	// no era el presupuesto: era todo lo demas.
	//
	// Con el gasto del presupuesto a la vista la separacion es la que el codigo ya documenta
	// (guardcoordinator.go, sobre workBudget): 951 ms compartido contra 2,23 s por intento.
	// Margen 1,28 s en vez de 0,69 s, y la carga de la caja fuera de la cuenta.
	if spent <= 0 {
		t.Fatalf("el observador no vio gasto alguno: la asercion de abajo no estaria midiendo nada (elapsed %s)", elapsed)
	}
	if ceiling := 2 * budget; spent >= ceiling {
		t.Errorf("the shared work budget spent %s across %d attempt(s) against one %s budget; a shared work budget cannot buy two full budgets (wall clock of Open was %s, which is NOT the subject)",
			spent, retries+1, budget, elapsed)
	}
	if ready := countGateKind(t, probe, gateEventReady); ready != 0 {
		t.Errorf("a close that never landed its `ready` left %d of them", ready)
	}
	// ⛔ LOS DOS NUMEROS EN LA MISMA LINEA, Y EL SUJETO PRIMERO. La asercion de arriba juzga
	// `workSpent`, pero esta linea publicaba solo `elapsed`: en una corrida VERDE la cantidad
	// juzgada no aparecia por ningun sitio —solo salia al fallar, dentro del mensaje de error—
	// y quien leyera el log veia el reloj de pared creyendo que veia el techo.
	//
	// Y hay una lectura concreta que sin esto no se puede hacer de UNA sola corrida: bajo carga,
	// si `elapsed` se dispara y `work_spent` no, el techo esta midiendo lo que dice medir. Esa
	// comparacion es la razon de ser del cambio, asi que tiene que caber en la misma linea.
	t.Logf("GUARD_WORK_BUDGET_TOTAL|blocked_ordinal=%d|work_spent=%s|elapsed=%s|budget=%s|retries=%d|err=%v",
		squatter.EventOrdinal, spent, elapsed, budget, retries, oerr)
}

// TestPostgresAFailedUnitRecordsItsFailureWhenTheParentIsCancelled is the case I declared
// unreachable without a seam, one round after declaring another one unreachable for a reason that
// was also wrong.
//
// IT NEEDS NO SEAM. An external transaction holds the shared guard function, a unit blocks taking
// the same fence AFTER announcing its attempt, and the boot's parent context is canceled while
// it waits. That is the incident the whole failure path exists for: the boot is going away and
// the ledger's note of WHY is the only thing that will outlive it.
//
// WHAT IT CAUGHT — a production defect, not a test one. afterFailure derived an independent
// CONTEXT with WithoutCancel and then used the runner's own CONNECTION. Canceling an operation
// in flight poisons that connection, so BeginTx returned `driver: bad connection`, the warning
// "could not record a failed guard attempt durably" was logged, and the run left an
// `attempt-started` with no `attempt-failed`. A live context on a dead socket is not a session.
//
// MUTATION VERIFIED: pointing failureWriter back at r.conn makes this FAIL with started >= 1 and
// failed == 0.
func TestPostgresAFailedUnitRecordsItsFailureWhenTheParentIsCanceled(t *testing.T) {
	dsns := isolatedPG(t)
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4}

	probe, _, _ := reopenableGuardLedger(t, cfg)

	// THE FENCE, HELD. Every unit takes the shared function before it acts, so this stops the
	// first one mid-attempt — after its `attempt-started` is durable.
	fn := canonicalGuardDefinition().Function
	holder, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("open the holder: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	htx, err := holder.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin the holder: %v", err)
	}
	defer func() { _ = htx.Rollback() }()
	if _, err := htx.ExecContext(context.Background(), `ALTER FUNCTION `+quoteIdent(fn.Schema)+`.`+
		quoteIdent(fn.Name)+`() RESET ALL`); err != nil {
		t.Fatalf("hold the shared function: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		st, oerr := Open(ctx, cfg, registerWidget)
		if st != nil {
			_ = st.Close()
		}
		done <- oerr
	}()

	// Wait for the STATE, not for a duration: an attempt announced durably and a backend
	// parked on a lock. Canceling before both is true would measure a boot that never
	// reached the failure path.
	deadline := time.Now().Add(20 * time.Second)
	for {
		started := countGateKind(t, probe, gateEventAttemptStarted)
		var blocked int
		if err := probe.QueryRowContext(context.Background(), `SELECT COUNT(*)
FROM pg_catalog.pg_stat_activity
WHERE wait_event_type = 'Lock' AND query LIKE '%guardfence%'`).Scan(&blocked); err != nil {
			t.Fatalf("read the blocked backends: %v", err)
		}
		if started > 0 && blocked > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no unit ever announced an attempt and parked on the fence (started=%d, blocked=%d), so this fixture never reached the window it exists for",
				started, blocked)
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	oerr := <-done
	if oerr == nil {
		t.Fatal("a boot canceled mid-attempt reached a serving store")
	}

	started := countGateKind(t, probe, gateEventAttemptStarted)
	failed := countGateKind(t, probe, gateEventAttemptFailed)
	if failed == 0 {
		t.Errorf("a parent canceled while an announced attempt was blocked left %d `attempt-started` and NO `attempt-failed`; the ledger records that something began and never says what happened to it, which is the one case this write exists for",
			started)
	}
	if ready := countGateKind(t, probe, gateEventReady); ready != 0 {
		t.Errorf("the canceled rollout closed with %d `ready`", ready)
	}
	t.Logf("GUARD_ATTEMPT_FAILED_ON_CANCEL|started=%d|failed=%d|err=%v", started, failed, oerr)
}
