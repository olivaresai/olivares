// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/olivaresai/olivares/core/store"
)

// TestMillisTextNeverRoundsANearlySpentBudgetToUnlimited is small and nasty.
//
// PostgreSQL reads 0 in lock_timeout and statement_timeout as NO LIMIT. A budget
// with 400 microseconds left is the opposite of unlimited, and rendering it by
// truncating to milliseconds produces exactly that inversion: the moment the
// deadline is nearly spent is the moment the statement would be granted forever.
func TestMillisTextNeverRoundsANearlySpentBudgetToUnlimited(t *testing.T) {
	t.Parallel()
	if got := millisText(400 * time.Microsecond); got == "0" {
		t.Error("a sub-millisecond budget rendered as 0, which PostgreSQL reads as UNLIMITED: the nearly-spent deadline would become no deadline at all")
	}
	if got := millisText(400 * time.Microsecond); got != "1ms" {
		t.Errorf("millisText(400µs) = %q, want %q as the smallest honest expression of almost none left", got, "1ms")
	}
	if got := millisText(0); got != "0" {
		t.Errorf("millisText(0) = %q, want %q — a deliberate zero is how acquisition hands the floor to execution", got, "0")
	}
	if got := millisText(-time.Second); got != "0" {
		t.Errorf("millisText(negative) = %q, want %q", got, "0")
	}
	if got := millisText(2500 * time.Millisecond); got != "2500ms" {
		t.Errorf("millisText(2.5s) = %q, want %q", got, "2500ms")
	}
}

// retryUnitFixture builds a target table, a metadata table and a unit over them.
func retryUnitFixture(t *testing.T, ctx context.Context, db *sql.DB) retryUnit {
	t.Helper()
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS olv_ru_target(id integer)`,
		`CREATE TABLE IF NOT EXISTS olv_ru_receipts(note text)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("fixture %q: %v", stmt, err)
		}
	}
	return retryUnit{
		Spec: unitSpec{Intent: intentCreateGuard, CanonicalEnableState: guardStateOrigin},
		Plan: lockPlan{
			Metadata:        []plannedLock{{Schema: "public", Name: "olv_ru_receipts", Mode: lockModeRowExclusive}},
			Target:          plannedLock{Schema: "public", Name: "olv_ru_target", Mode: lockModeRowExclusive},
			TargetStatement: `LOCK TABLE ONLY "public"."olv_ru_target" IN ROW EXCLUSIVE MODE`,
		},
		Project: func(ctx context.Context, dbx rowQuerier) (prestate, error) {
			var exists bool
			err := dbx.QueryRowContext(ctx,
				`SELECT pg_catalog.to_regclass('olv_ru_target') IS NOT NULL`).Scan(&exists)
			return prestate{TargetExists: exists}, err
		},
		Execute: func(ctx context.Context, tx *sql.Tx, _ prestate) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO olv_ru_target VALUES (1)`)
			return err
		},
		Receipt: func(ctx context.Context, tx *sql.Tx, _ prestate) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO olv_ru_receipts VALUES ('done')`)
			return err
		},
		ProjectReceipt: func(ctx context.Context, dbx rowQuerier) (receiptProjection, error) {
			var n int
			err := dbx.QueryRowContext(ctx, `SELECT count(*) FROM olv_ru_receipts`).Scan(&n)
			return receiptProjection{Present: n > 0}, err
		},
		// The fixture's target carries no real guard, so the poststate projection
		// reports the canonical shape the spec declares. The postcondition check runs
		// on the happy path now, and a fixture that could never satisfy it would make
		// every test here fail for a reason none of them is about.
		ProjectObject: func(ctx context.Context, dbx rowQuerier) (objectProjection, error) {
			var exists bool
			err := dbx.QueryRowContext(ctx,
				`SELECT pg_catalog.to_regclass('olv_ru_target') IS NOT NULL`).Scan(&exists)
			return objectProjection{
				Exists: exists, GuardPresent: exists, MatchesCanonical: exists,
				GuardEnableState: guardStateOrigin,
			}, err
		},
		// A session that is NOT the one the unit runs on. Checked out from the same pool
		// on purpose: that is what production has, and it is why the store already
		// refuses to open PostgreSQL with MaxConns below 2 — reconciliation needs a
		// second connection at precisely the moment the first one has died.
		ReconcileSession: func(ctx context.Context) (rowQuerier, func(), error) {
			c, err := db.Conn(ctx)
			if err != nil {
				return nil, func() {}, err
			}
			return c, func() { _ = c.Close() }, nil //nolint:errcheck // best effort release
		},
	}
}

// armAmbiguousCommit makes the NEXT commit on this database die inside COMMIT itself.
//
// A DEFERRED constraint trigger fires at the end of the transaction, during the
// commit, so terminating the backend from inside it produces exactly the failure the
// whole reconciliation path exists for: the client never learns whether the
// transaction committed. Measured:
//
//	BEFORE_COMMIT|3809203
//	FATAL:  terminating connection due to administrator command
//	CONTEXT:  SQL statement "SELECT pg_terminate_backend(pg_backend_pid())"
//	server closed the connection unexpectedly
//
// Nothing else reproduces it deterministically. Killing the backend from another
// session is a race against a commit that takes microseconds, and a constraint
// violation is answered by the SERVER — which is not ambiguous at all, and correctly
// does not reconcile.
func armAmbiguousCommit(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, ddl := range []string{
		`CREATE OR REPLACE FUNCTION olv_ru_kill() RETURNS trigger LANGUAGE plpgsql AS $f$
		 BEGIN PERFORM pg_catalog.pg_terminate_backend(pg_catalog.pg_backend_pid()); RETURN NULL; END $f$`,
		`DROP TRIGGER IF EXISTS olv_ru_kill_t ON olv_ru_receipts`,
		`CREATE CONSTRAINT TRIGGER olv_ru_kill_t AFTER INSERT ON olv_ru_receipts
		 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION olv_ru_kill()`,
	} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("arm the ambiguous commit: %v", err)
		}
	}
}

// TestRetryUnitCommitsWorkAndReceiptTogether pins the property that makes a
// receipt evidence rather than a claim: it commits with the work it attributes, so
// there is no interval in which one exists without the other.
//
// Mutation that must turn this red: commit after Execute and write the receipt in
// its own transaction.
func TestRetryUnitCommitsWorkAndReceiptTogether(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 3})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	u := retryUnitFixture(t, ctx, db)

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	b := newLockBudget(30*time.Second, time.Now, sleepCtx, jitterFloat)
	if err := u.run(ctx, conn, b); err != nil {
		t.Fatalf("run: %v", err)
	}

	var work, receipts int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM olv_ru_target`).Scan(&work); err != nil {
		t.Fatalf("count work: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM olv_ru_receipts`).Scan(&receipts); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if work != 1 || receipts != 1 {
		t.Errorf("work=%d receipts=%d, want 1 and 1", work, receipts)
	}
}

// TestRetryUnitLeavesNoTimeoutOnTheConnection is the regression for a leak that a
// happy-path test cannot see.
//
// The unit runs on the connection that holds the coordination lock, and that
// connection is handed back when boot finishes. SET LOCAL scopes both knobs to the
// transaction; plain SET would leave a 60-second lock_timeout on a pooled session
// and the next user of it would inherit a limit nobody configured.
//
// Mutation that must turn this red: use SET instead of set_config's is_local flag.
func TestRetryUnitLeavesNoTimeoutOnTheConnection(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 3})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	u := retryUnitFixture(t, ctx, db)

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	// BOTH knobs, and that is not thoroughness for its own sake. A first version of
	// this test read only lock_timeout and stayed GREEN against the very mutation it
	// exists to catch: the last thing acquisition does is set lock_timeout to 0 to
	// hand the floor to execution, and 0 is also the server default, so the leak is
	// invisible there. statement_timeout is where it shows, because the unit leaves
	// it at the execution budget.
	knobs := []string{"lock_timeout", "statement_timeout"}
	before := make(map[string]string, len(knobs))
	for _, k := range knobs {
		var v string
		if err := conn.QueryRowContext(ctx, `SELECT pg_catalog.current_setting($1)`, k).Scan(&v); err != nil {
			t.Fatalf("read %s: %v", k, err)
		}
		before[k] = v
	}

	b := newLockBudget(30*time.Second, time.Now, sleepCtx, jitterFloat)
	if err := u.run(ctx, conn, b); err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, k := range knobs {
		var after string
		if err := conn.QueryRowContext(ctx, `SELECT pg_catalog.current_setting($1)`, k).Scan(&after); err != nil {
			t.Fatalf("read %s: %v", k, err)
		}
		if after != before[k] {
			t.Errorf("%s is %q after the unit, was %q: the session keeps a limit the unit set for itself, and this connection goes back to the pool for whatever runs next", k, after, before[k])
		}
	}
}

// TestRetryUnitAsksReconcileBeforeRetrying drives the ONE genuinely ambiguous
// failure — a COMMIT the client never gets an answer to — and pins what the runner
// does with each verdict the matrix can return.
//
// THE STRUCTURAL LIMIT THIS USED TO DECLARE HAS SINCE BEEN CLOSED, and leaving the old
// note standing would misdescribe the contract. It said the runner reconciles on the SAME
// connection whose COMMIT went unanswered — which is a connection that has, in practice,
// just died — so the projectors would fail, fold to Readable=false, and the matrix could
// only ever answer outcomeUnknown. That was true, and it was the finding that made
// ReconcileSession a required callback: reconciliation now opens a session of its own,
// owned by the goroutine that reads through it.
//
// The projectors below are Go callbacks that ignore the handle, which is what lets this
// test exercise the ROUTING — ambiguous commit, matrix, decision — independently of where
// the session comes from. The session itself is pinned by
// TestRetryUnitCannotReconcileThroughTheSessionThatDied and by the durable lost-ACK test.
func TestRetryUnitAsksReconcileBeforeRetrying(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 6})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown

	// A FRESH connection per subtest: the backend is terminated on purpose, so a
	// shared one would be dead for everything after the first case.
	fresh := func(t *testing.T) *sql.Conn {
		t.Helper()
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("conn: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	t.Run("applied stops the retry", func(t *testing.T) {
		u := retryUnitFixture(t, ctx, db)
		var executes, reconciles int
		u.Execute = func(context.Context, *sql.Tx, prestate) error { executes++; return nil }
		armAmbiguousCommit(t, ctx, db)
		// Feed the matrix the readings that mean "done": the receipt is there for this
		// epoch and the object is canonical. The verdict is reconcileOutcomeFor's, not
		// the test's — which is the point of injecting projections rather than an
		// outcome.
		u.ProjectReceipt = func(context.Context, rowQuerier) (receiptProjection, error) {
			reconciles++
			return receiptProjection{Present: true}, nil
		}
		u.ProjectObject = func(context.Context, rowQuerier) (objectProjection, error) {
			return objectProjection{Exists: true, GuardPresent: true, MatchesCanonical: true,
				GuardEnableState: guardStateOrigin}, nil
		}

		b := newLockBudget(5*time.Second, time.Now, sleepCtx, jitterFloat)
		if err := u.run(ctx, fresh(t), b); err != nil {
			t.Fatalf("run: %v", err)
		}
		if reconciles != 1 {
			t.Errorf("reconcile consulted %d times, want exactly 1", reconciles)
		}
		if executes != 1 {
			t.Errorf("execute ran %d times: reconciliation said the unit was already applied, so a second attempt would apply it twice", executes)
		}
	})

	t.Run("divergent is fail-closed", func(t *testing.T) {
		u := retryUnitFixture(t, ctx, db)
		armAmbiguousCommit(t, ctx, db)
		// A receipt whose object is not there. They commit together, so this is not a
		// state this runner can produce — which is exactly why it must be refused
		// rather than resolved.
		//
		// The object projection is consulted TWICE now: once as the postcondition,
		// under the lock, and once during reconciliation. It has to satisfy the first
		// and be gone by the second, which is precisely the sequence this case
		// describes — the object was there when the unit finished and had vanished by
		// the time anyone asked again.
		u.ProjectReceipt = func(context.Context, rowQuerier) (receiptProjection, error) {
			return receiptProjection{Present: true}, nil
		}
		var objectReads int
		u.ProjectObject = func(context.Context, rowQuerier) (objectProjection, error) {
			objectReads++
			if objectReads == 1 {
				return objectProjection{Exists: true, GuardPresent: true,
					MatchesCanonical: true, GuardEnableState: guardStateOrigin}, nil
			}
			return objectProjection{Exists: true}, nil
		}

		b := newLockBudget(5*time.Second, time.Now, sleepCtx, jitterFloat)
		err := u.run(ctx, fresh(t), b)
		if !errors.Is(err, ErrMigrationOutcomeUnknown) {
			t.Errorf("run = %v, want ErrMigrationOutcomeUnknown: a state that is neither done nor untouched must never be retried around", err)
		}
	})

	t.Run("not-applied does not report success", func(t *testing.T) {
		u := retryUnitFixture(t, ctx, db)
		var executes int
		u.Execute = func(context.Context, *sql.Tx, prestate) error { executes++; return nil }
		armAmbiguousCommit(t, ctx, db)
		// No receipt, and the object is bit-identical to its prestate: nothing of the
		// unit survived, so the runner may try again.
		u.ProjectReceipt = func(context.Context, rowQuerier) (receiptProjection, error) {
			return receiptProjection{}, nil
		}
		u.ProjectObject = func(context.Context, rowQuerier) (objectProjection, error) {
			return objectProjection{Exists: true}, nil
		}

		b := newLockBudget(5*time.Second, time.Now, sleepCtx, jitterFloat)
		err := u.run(ctx, fresh(t), b)
		// The retry happens on a connection the server has just terminated, so it
		// cannot succeed — and MUST NOT be reported as success. This is the structural
		// limit above, observed: not-applied means "try again", the try is impossible
		// on this session, and the honest result is an error rather than a nil.
		if err == nil {
			t.Fatal("run reported success after a commit whose outcome was unknown and whose reconciliation said nothing had been applied")
		}
		if executes != 1 {
			t.Errorf("execute ran %d times, want 1: the second attempt cannot run on a terminated backend", executes)
		}
	})
}

// TestRetryUnitStopsAtItsBudget pins that a unit cannot outlive its deadline, and
// that the error names what it was waiting for.
func TestRetryUnitStopsAtItsBudget(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 3})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	u := retryUnitFixture(t, ctx, db)
	// A deadlock, not a wire failure: the server aborted the transaction itself, so
	// nothing committed and the unit retries on this same session — which is what
	// makes this a test of the BUDGET rather than of the classification.
	u.Execute = func(context.Context, *sql.Tx, prestate) error {
		return &pgconn.PgError{Code: sqlStateDeadlockDetected}
	}

	start := time.Now()
	b := newLockBudget(300*time.Millisecond, time.Now, sleepCtx, jitterFloat) //nolint:gomnd // paired with the ceiling below
	err = u.run(ctx, conn, b)
	elapsed := time.Since(start)

	if !errors.Is(err, ErrMigrationLockBudgetExceeded) {
		t.Errorf("run = %v, want ErrMigrationLockBudgetExceeded", err)
	}
	// A PROPORTIONAL ceiling. Five seconds for a 300ms budget detects a hang and
	// nothing else: a run taking 4.9s would have overrun the deadline sixteenfold and
	// still validated the word "hard". The allowance here covers backoff jitter and
	// the roundtrips of a real server, not an order of magnitude.
	const budget = 300 * time.Millisecond
	if ceiling := 4 * budget; elapsed > ceiling {
		t.Errorf("the unit ran for %v against a %v budget (ceiling %v): the deadline is not hard, and a generous ceiling would have called this a pass",
			elapsed, budget, ceiling)
	}
}

// TestRetryUnitRefusesToRetryABrokenTransportOnTheSameSession is the regression for
// the classification change that this file's other tests had to move for.
//
// A transport failure BEFORE the commit boundary is not an ambiguous outcome: the
// transaction aborts and nothing of the unit survives, so there is nothing to
// reconcile. What IS in doubt is the connection — it just failed at the wire — and a
// fresh transaction on a session that has already failed at the transport is not a
// remedy, it is the same attempt with a new name.
//
// The runner cannot open that session itself: this connection holds the cluster-wide
// coordination lock, so replacing it is a decision about the whole migration. It
// therefore surfaces fail-closed and NAMED rather than degrading to a same-session
// retry, which is precisely what the classification exists to prevent.
//
// Mutation that must turn this red: classify a pre-boundary non-server error as
// retryNewTransaction or retryAfterReconcile.
func TestRetryUnitRefusesToRetryABrokenTransportOnTheSameSession(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 3})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	u := retryUnitFixture(t, ctx, db)
	var executes, reconciles int
	u.Execute = func(context.Context, *sql.Tx, prestate) error {
		executes++
		return errors.New("connection reset by peer")
	}
	u.ProjectReceipt = func(context.Context, rowQuerier) (receiptProjection, error) {
		reconciles++
		return receiptProjection{}, nil
	}

	b := newLockBudget(5*time.Second, time.Now, sleepCtx, jitterFloat)
	err = u.run(ctx, conn, b)

	if !errors.Is(err, ErrMigrationNeedsNewSession) {
		t.Fatalf("run = %v, want ErrMigrationNeedsNewSession: the transport failed before anything committed, so the connection is what is in doubt", err)
	}
	if executes != 1 {
		t.Errorf("execute ran %d times, want 1: retrying on a session that just failed at the wire is the same attempt with a new name", executes)
	}
	if reconciles != 0 {
		t.Errorf("reconciliation was consulted %d times: nothing committed, so there is nothing to reconcile against", reconciles)
	}
}

// TestRetryUnitValidatesItsPlanBeforeIssuingAnything pins that the plan's own
// invariants are checked before the first statement.
//
// Every property validate() checks is about statements that are about to run — a
// duplicated relation, a prefix out of order, a metadata lock stronger than the
// target — and each produces a deadlock or a lock-ordering violation at runtime. A
// check that runs after the first statement is a check that runs after the damage.
//
// Mutation that must turn this red: drop the validate() call from run().
func TestRetryUnitValidatesItsPlanBeforeIssuingAnything(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 3})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	u := retryUnitFixture(t, ctx, db)
	// Metadata stronger than the target: the unit would escalate on its own target,
	// which is the documented deadlock recipe the ordering exists to avoid.
	u.Plan.Metadata = []plannedLock{{Schema: "public", Name: "olv_ru_receipts", Mode: lockModeAccessExclusive}}

	var projected, executed int
	inner := u.Project
	u.Project = func(ctx context.Context, dbx rowQuerier) (prestate, error) {
		projected++
		return inner(ctx, dbx)
	}
	u.Execute = func(context.Context, *sql.Tx, prestate) error { executed++; return nil }

	b := newLockBudget(5*time.Second, time.Now, sleepCtx, jitterFloat)
	err = u.run(ctx, conn, b)
	if err == nil {
		t.Fatal("an unsafe plan was executed: the unit escalates on its own target and would deadlock against any concurrent unit")
	}
	if !strings.Contains(err.Error(), "escalates") {
		t.Errorf("refused with %v, want the diagnosis to name the property violated", err)
	}
	if projected != 0 || executed != 0 {
		t.Errorf("projected %d times and executed %d: the plan must be refused before the prestate is even read, let alone before a statement is issued", projected, executed)
	}
}

// TestRetryUnitRefusesBeforeAnyCallbackWhenUnauthorised is the regression for the
// deepest finding of the fourth round: the semantic rules governed only
// RECONCILIATION, which is a path that runs after a failure and therefore never on the
// run that succeeds.
//
// A unit could project, execute, write a receipt and COMMIT with an unrecognized
// intent, a manifest declaring a disabled guard as canonical, or a precondition the
// intent does not authorize — and be judged afterwards, or never.
//
// Mutation that must turn this red: move the spec and precondition checks back inside
// reconcileOutcomeFor only.
func TestRetryUnitRefusesBeforeAnyCallbackWhenUnauthorised(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 3})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	cases := []struct {
		name          string
		spec          unitSpec
		preGuardState string
		wantProjects  int
		why           string
	}{
		{
			name: "an unrecognized intent", spec: unitSpec{Intent: unitIntent("nonsense"), CanonicalEnableState: guardStateOrigin},
			wantProjects: 0,
			why:          "an intent nothing can reason about must not reach a callback, let alone a COMMIT",
		},
		{
			name: "a manifest declaring DISABLED as canonical", spec: unitSpec{Intent: intentCreateGuard, CanonicalEnableState: guardStateDisable},
			wantProjects: 0,
			why:          "a spec that authorizes a disabled guard authorizes the absence of the protection this machinery exists to provide",
		},
		{
			name:          "an O->A transition from a DISABLED guard",
			spec:          unitSpec{Intent: intentTransitionLegacyOToA, CanonicalEnableState: guardStateAlways},
			preGuardState: guardStateDisable, wantProjects: 1,
			why: "the precondition is half the intent's meaning, and it must be checked against a fresh projection BEFORE the statement that destroys it",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := retryUnitFixture(t, ctx, db)
			u.Spec = tc.spec
			var projects, targets, receipts int
			inner := u.Project
			u.Project = func(ctx context.Context, dbx rowQuerier) (prestate, error) {
				projects++
				pre, err := inner(ctx, dbx)
				pre.GuardPresent = true
				pre.GuardMatchesCanonical = true
				pre.GuardEnableState = tc.preGuardState
				return pre, err
			}
			u.Execute = func(context.Context, *sql.Tx, prestate) error { targets++; return nil }
			u.Receipt = func(context.Context, *sql.Tx, prestate) error { receipts++; return nil }

			b := newLockBudget(5*time.Second, time.Now, sleepCtx, jitterFloat)
			err := u.run(ctx, conn, b)
			if !errors.Is(err, ErrMigrationUnauthorised) {
				t.Fatalf("run = %v, want ErrMigrationUnauthorised — %s", err, tc.why)
			}
			if projects != tc.wantProjects {
				t.Errorf("projected %d times, want %d", projects, tc.wantProjects)
			}
			if targets != 0 || receipts != 0 {
				t.Errorf("executed %d and wrote %d receipts on an unauthorized unit: the refusal must precede any change", targets, receipts)
			}
		})
	}
}

// TestRetryUnitVerifiesThePoststateUnderTheLock pins that the manifest's declared
// poststate is checked on the path that COMMITS, while the locks are still held.
//
// Reading it inside the lock and before the receipt is the only window where the
// answer is both current and stable: nothing can change the object, and the receipt
// has not yet claimed anything about it.
//
// Mutation that must turn this red: remove the postcondition block from attempt().
func TestRetryUnitVerifiesThePoststateUnderTheLock(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 3})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	u := retryUnitFixture(t, ctx, db)
	var receipts int
	u.Receipt = func(context.Context, *sql.Tx, prestate) error { receipts++; return nil }
	// The unit "succeeds" but leaves the guard DISABLED — which is exactly the state
	// no intent may end in, and which nothing on the happy path used to look at.
	u.ProjectObject = func(context.Context, rowQuerier) (objectProjection, error) {
		return objectProjection{
			Exists: true, GuardPresent: true, MatchesCanonical: true,
			GuardEnableState: guardStateDisable,
		}, nil
	}

	b := newLockBudget(5*time.Second, time.Now, sleepCtx, jitterFloat)
	err = u.run(ctx, conn, b)
	if !errors.Is(err, ErrMigrationPostconditionFailed) {
		t.Fatalf("run = %v, want ErrMigrationPostconditionFailed: the unit left the guard disabled and would have committed a receipt saying it had done its job", err)
	}
	if receipts != 0 {
		t.Errorf("wrote %d receipts for work that did not reach its declared poststate", receipts)
	}
}

// armSlowCommit makes the next COMMIT on this database take about d, without aborting
// it, so a client-side socket close can land while the server is still working.
//
// A DEFERRED constraint trigger fires inside COMMIT. Sleeping there — rather than
// terminating the backend, as armAmbiguousCommit does — lets the transaction reach
// durability on the server while the client is denied the acknowledgement. That is the
// case the contrast round asked for and the previous seam did not reach: a commit that
// SUCCEEDED whose answer was lost, as opposed to one the trigger aborted.
func armSlowCommit(t *testing.T, ctx context.Context, db *sql.DB, d time.Duration) {
	t.Helper()
	for _, ddl := range []string{
		fmt.Sprintf(`CREATE OR REPLACE FUNCTION olv_ru_slow() RETURNS trigger LANGUAGE plpgsql AS $f$
		 BEGIN PERFORM pg_catalog.pg_sleep(%f); RETURN NULL; END $f$`, d.Seconds()),
		`DROP TRIGGER IF EXISTS olv_ru_slow_t ON olv_ru_receipts`,
		`CREATE CONSTRAINT TRIGGER olv_ru_slow_t AFTER INSERT ON olv_ru_receipts
		 DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION olv_ru_slow()`,
	} {
		if _, err := db.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("arm the slow commit: %v", err)
		}
	}
}

// closeClientSocket severs the connection from the CLIENT side, leaving the server
// untouched.
//
// This is what distinguishes a durable-but-unacknowledged commit from an aborted one.
// pg_terminate_backend tells the server to give up; closing the socket tells nobody.
// The server finishes the commit and writes it; the client never hears.
// captureClientSocket takes the underlying net.Conn WHILE THE CONNECTION IS IDLE, so it
// can be closed later without going through database/sql.
//
// This is not a refinement, it is the correction of a test that measured nothing for
// three rounds. sql.Conn.Raw serializes with the connection's other users: called while a
// COMMIT is in flight it BLOCKS until that COMMIT finishes, and then closes a socket
// whose transaction has already been acknowledged. So the "lost acknowledgement" seam was
// severing the connection after the very event it was supposed to interrupt — the commit
// succeeded normally, run() returned nil without ever reaching reconciliation, and every
// assertion passed for the wrong reason.
//
// Measured: DEAD_SESSION_RECONCILE|receipts=1|run_err=<nil> on a unit whose reconciliation
// was deliberately wired to a connection that was supposed to be dead. It could not have
// read anything — and it never had to.
//
// Holding the net.Conn from beforehand makes the close independent of database/sql's
// locking, which is the only way to cut the wire mid-COMMIT.
func captureClientSocket(t *testing.T, conn *sql.Conn) net.Conn {
	t.Helper()
	var nc net.Conn
	err := conn.Raw(func(dc any) error {
		c, ok := dc.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("driver conn is %T, not *stdlib.Conn", dc)
		}
		nc = c.Conn().PgConn().Conn()
		if nc == nil {
			return errors.New("no underlying net.Conn")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("capturing the client socket: %v (without it this test cannot sever the connection mid-commit, and would pass vacuously)", err)
	}
	return nc
}

// TestRetryUnitWireCutNeverDuplicatesAndNeverClaimsSuccess is the wire-faithful seam, and
// its name is now the property it can actually guarantee.
//
// It used to be called ...TreatsADurableCommitWithNoAcknowledgementAsUnknown, which
// asserted a premise the test cannot arrange. The commit is slowed from inside itself and
// the CLIENT's socket is cut mid-flight, but WHICH SIDE of the durability boundary the cut
// lands on is not controllable: the window between "the server made it durable" and "the
// client heard" is microseconds wide. In practice a 700ms cut into a 2s commit lands
// before it, and the server aborts on noticing the disconnect.
//
// So what is pinned here is what holds on EITHER side, and it is the pair that costs a
// ledger if it fails: never more than one receipt, and never success reported for work
// that is not there. Plus the premise that the wire really was cut, so a close that
// silently failed cannot read as a pass — which is exactly how this test spent three
// rounds measuring nothing.
//
// The durable side is covered where it can be guaranteed rather than raced for, by the
// commitTx seam in the two tests below.
func TestRetryUnitWireCutNeverDuplicatesAndNeverClaimsSuccess(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	u := retryUnitFixture(t, ctx, db)
	armSlowCommit(t, ctx, db, 2*time.Second)

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	// EVERY PROJECTOR READS THROUGH THE HANDLE THE RUNNER GIVES IT. That is the property
	// under test, and the previous version of this test quietly destroyed it: it captured
	// a side pool in the closure and ignored the rowQuerier argument entirely, so it
	// passed whether or not the runner could supply a usable session. The runner could
	// have handed over the corpse of the connection whose COMMIT was just lost and this
	// test would not have noticed — which is exactly what it was doing.
	//
	// Only the count is read here; the fixture's own projectors are replaced so the
	// receipt reading is unambiguous.
	u.ProjectReceipt = func(ctx context.Context, dbx rowQuerier) (receiptProjection, error) {
		var n int
		if err := dbx.QueryRowContext(ctx, `SELECT count(*) FROM olv_ru_receipts`).Scan(&n); err != nil {
			return receiptProjection{}, err
		}
		return receiptProjection{Present: n > 0}, nil
	}
	u.ProjectObject = func(ctx context.Context, dbx rowQuerier) (objectProjection, error) {
		var exists bool
		if err := dbx.QueryRowContext(ctx,
			`SELECT pg_catalog.to_regclass('olv_ru_target') IS NOT NULL`).Scan(&exists); err != nil {
			return objectProjection{}, err
		}
		return objectProjection{Exists: exists, GuardPresent: exists, MatchesCanonical: exists,
			GuardEnableState: guardStateOrigin}, nil
	}

	// An untouched pool, used ONLY to state the premise afterwards. It is never handed
	// to the runner, so it cannot stand in for the session the runner has to provide.
	side, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open side pool: %v", err)
	}
	defer side.Close() //nolint:errcheck // test teardown

	// Sever the socket while the commit is sleeping inside the server. The handle is
	// taken NOW, while the connection is idle: going through sql.Conn.Raw at cut time
	// would block until the COMMIT finished and close a socket whose transaction had
	// already been acknowledged, which is exactly how this seam measured nothing.
	sock := captureClientSocket(t, conn)
	go func() {
		time.Sleep(700 * time.Millisecond)
		_ = sock.Close() //nolint:errcheck // the assertions below report the real outcome
	}()

	b := newLockBudget(30*time.Second, time.Now, sleepCtx, jitterFloat)
	runErr := u.run(ctx, conn, b)

	// THE PREMISE, checked from a connection that was never touched: the commit
	// really did become durable. Without this the test could pass against a commit
	// that never happened, which is the very confusion it exists to remove.
	var receipts int
	if err := side.QueryRowContext(ctx, `SELECT count(*) FROM olv_ru_receipts`).Scan(&receipts); err != nil {
		t.Fatalf("read the receipt table from a separate connection: %v", err)
	}
	t.Logf("WIRE_CUT|receipts=%d|run_err=%v", receipts, runErr)

	// WHICH SIDE OF THE BOUNDARY THE CUT LANDS ON IS NOT CONTROLLABLE, and pretending
	// otherwise is what made this test vacuous twice. The window between "durable" and
	// "the client heard" is microseconds wide. Cut before it — which is what a 700ms cut
	// into a 2s commit reliably does — and the server notices the disconnect and ABORTS.
	//
	// So this test asserts the property that holds on EITHER side, which is the property
	// that actually matters: the ledger must never end up with more than one row, and the
	// runner must never report success for work that is not there. The durable side is
	// covered deterministically by the commitTx seam in the two tests below, where the
	// state can be guaranteed instead of raced for.
	if receipts > 1 {
		t.Errorf("the receipt table holds %d rows: the unit was applied more than once after its wire was cut", receipts)
	}
	if receipts == 0 && runErr == nil {
		t.Error("run succeeded with nothing in the ledger: the commit was aborted by the disconnect, so success is a claim about work that does not exist")
	}
	if receipts == 1 && runErr != nil && !errors.Is(runErr, ErrMigrationOutcomeUnknown) {
		t.Errorf("run = %v with the receipt on disk; anything other than success or a fail-closed unknown invites a second application", runErr)
	}
	// Either way the session is gone, and the runner must have said so rather than
	// quietly retrying on it.
	if err := conn.PingContext(ctx); err == nil {
		t.Error("the connection survived a socket close, so this run did not exercise a severed wire at all")
	}
}

// TestRetryUnitCannotReconcileThroughTheSessionThatDied is R6-01 stated as its own
// property, because the test above can only show the good path.
//
// The runner hands reconciliation whatever ReconcileSession returns. If a caller wires
// that to the same connection the unit ran on — the obvious, wrong thing, and what the
// runner did unconditionally before this — then after a lost acknowledgement both
// projections fail on a dead socket, both fold to Readable=false, and the matrix answers
// outcomeUnknown. Nothing is corrupted, and that is the point: the failure is invisible
// in every safety assertion and shows up only as a boot that will not finish.
//
// So it is pinned directly: the same durable commit, the same lost acknowledgement, and
// the ONLY difference is which session reconciliation reads through.
func TestRetryUnitCannotReconcileThroughTheSessionThatDied(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	u := retryUnitFixture(t, ctx, db)

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	// The wrong wiring, on purpose: reconcile through the very connection whose COMMIT
	// is about to go unanswered.
	u.ReconcileSession = func(context.Context) (rowQuerier, func(), error) {
		return conn, func() {}, nil
	}

	// The socket handle, taken while the connection is idle.
	sock := captureClientSocket(t, conn)

	// A DETERMINISTIC lost acknowledgement, which the socket race cannot deliver.
	//
	// Cutting the wire mid-COMMIT is faithful but unlandable: cut too early and the server
	// notices the disconnect and ABORTS, cut too late and the acknowledgement has already
	// arrived. Measured both ways on this seam — first it always cut late (Raw waits for
	// the connection) and every assertion passed vacuously; with that fixed it always cuts
	// early and the test skips. The window between "durable" and "the client heard" is
	// microseconds wide and not addressable from here.
	//
	// So the commit is REAL and succeeds, the socket is severed immediately afterwards,
	// and only then is a transport error handed back. That is precisely the state under
	// test: work on disk, session dead, client told nothing.
	restore := commitTx
	commitTx = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		_ = sock.Close() //nolint:errcheck // the session is meant to die here
		return errors.New("synthetic transport failure: the acknowledgement was lost")
	}
	defer func() { commitTx = restore }()
	u.ProjectReceipt = func(ctx context.Context, dbx rowQuerier) (receiptProjection, error) {
		var n int
		if err := dbx.QueryRowContext(ctx, `SELECT count(*) FROM olv_ru_receipts`).Scan(&n); err != nil {
			return receiptProjection{}, err
		}
		return receiptProjection{Present: n > 0}, nil
	}

	side, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open side pool: %v", err)
	}
	defer side.Close() //nolint:errcheck // test teardown

	b := newLockBudget(30*time.Second, time.Now, sleepCtx, jitterFloat)
	runErr := u.run(ctx, conn, b)

	var receipts int
	if err := side.QueryRowContext(ctx, `SELECT count(*) FROM olv_ru_receipts`).Scan(&receipts); err != nil {
		t.Fatalf("read the receipt table from a separate connection: %v", err)
	}
	t.Logf("DEAD_SESSION_RECONCILE|receipts=%d|run_err=%v", receipts, runErr)
	// THE PREMISE, and it is no longer a race: the commit really did become durable, and
	// the session really is dead. No skip, because nothing here can go either way.
	if receipts != 1 {
		t.Fatalf("the receipt table holds %d rows, want exactly 1; the commit did not become durable and this run shows nothing", receipts)
	}
	if err := conn.PingContext(ctx); err == nil {
		t.Fatal("the unit's connection is still usable, so it was never the dead session this test is about")
	}

	// Fail-closed is the REQUIRED behavior here, not an acceptable one: the runner could
	// not read, so it must not claim anything. What this documents is the cost — the work
	// is on disk and the boot still cannot proceed.
	if runErr == nil {
		t.Error("run succeeded while reconciling through a dead session; it cannot have read anything, so success is a claim about state nobody observed")
	} else if !errors.Is(runErr, ErrMigrationOutcomeUnknown) {
		t.Errorf("run = %v; reconciling through a dead session must fail closed as %v", runErr, ErrMigrationOutcomeUnknown)
	}
}

// TestRetryUnitRefusesWithoutAReconcileSession keeps the callback on the same footing as
// the other five: checked BEFORE any lock, not at the moment it is needed.
//
// The moment it is needed is after a COMMIT whose outcome is unknown — the worst possible
// place to discover a nil field, because the alternative to reading is guessing and both
// guesses corrupt the ledger.
func TestRetryUnitRefusesWithoutAReconcileSession(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	u := retryUnitFixture(t, ctx, db)
	var executes int
	u.Execute = func(ctx context.Context, tx *sql.Tx, _ prestate) error {
		executes++
		_, err := tx.ExecContext(ctx, `INSERT INTO olv_ru_target VALUES (1)`)
		return err
	}
	u.ReconcileSession = nil

	b := newLockBudget(30*time.Second, time.Now, sleepCtx, jitterFloat)
	err = u.run(ctx, conn, b)
	if !errors.Is(err, ErrMigrationUnauthorised) {
		t.Errorf("run = %v with no reconcile session, want %v", err, ErrMigrationUnauthorised)
	}
	// And it refused BEFORE doing anything, which is the half that matters.
	if executes != 0 {
		t.Errorf("the unit executed %d times before the missing callback was noticed", executes)
	}
}

// TestRetryUnitDoesNotApplyTwiceAfterADurableCommitLosesItsAcknowledgement is the
// deterministic half of the lost-ACK seam, and it is the one that proves the property
// that matters.
//
// The socket-close variant above is faithful to the wire but cannot choose which side of
// the durability boundary its cut lands on, so it asserts only what holds on both. This
// one cannot race at all: the commit is REAL and succeeds, and only then
// is a transport error handed back — exactly the shape of an acknowledgement lost on
// the way home. The session stays alive, so the projectors read the receipt that is
// genuinely on disk rather than constants.
//
// The property under test is the one that costs a ledger if it fails: after an
// unacknowledged commit, the unit must NOT be applied a second time.
//
// Mutation that must turn this red: route a non-PgError at phaseCommit anywhere other
// than reconciliation.
func TestRetryUnitDoesNotApplyTwiceAfterADurableCommitLosesItsAcknowledgement(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 3})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	u := retryUnitFixture(t, ctx, db)
	var executes int
	u.Execute = func(ctx context.Context, tx *sql.Tx, _ prestate) error {
		executes++
		_, err := tx.ExecContext(ctx, `INSERT INTO olv_ru_target VALUES (1)`)
		return err
	}
	// REAL readings, on the live session, of state the commit really made durable.
	u.ProjectReceipt = func(ctx context.Context, dbx rowQuerier) (receiptProjection, error) {
		var n int
		err := dbx.QueryRowContext(ctx, `SELECT count(*) FROM olv_ru_receipts`).Scan(&n)
		return receiptProjection{Present: n > 0}, err
	}

	// The commit succeeds; its acknowledgement does not arrive.
	var commits int
	prev := commitTx
	commitTx = func(tx *sql.Tx) error {
		commits++
		if err := prev(tx); err != nil {
			return err
		}
		if commits == 1 {
			return errors.New("write tcp 127.0.0.1:5432: broken pipe")
		}
		return nil
	}
	t.Cleanup(func() { commitTx = prev })

	b := newLockBudget(30*time.Second, time.Now, sleepCtx, jitterFloat)
	runErr := u.run(ctx, conn, b)
	if runErr != nil {
		t.Fatalf("run = %v: reconciliation could read a receipt that is genuinely on disk, so the unit is applied and must be reported as such", runErr)
	}

	// The premise and the property, both read from the database.
	var receipts, work int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM olv_ru_receipts`).Scan(&receipts); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM olv_ru_target`).Scan(&work); err != nil {
		t.Fatalf("count work: %v", err)
	}
	if receipts != 1 {
		t.Errorf("the receipt table holds %d rows, want exactly 1: the unit was applied more than once after its acknowledgement was lost", receipts)
	}
	if work != 1 {
		t.Errorf("the target holds %d rows, want exactly 1", work)
	}
	if executes != 1 {
		t.Errorf("Execute ran %d times, want 1: the work was already durable, so a second run is a second application", executes)
	}
	if commits != 1 {
		t.Errorf("committed %d times, want 1", commits)
	}
}

// TestRetryUnitRefusesAPlanThatLeavesItsTargetToExecute pins the ordering the final photo
// cannot see — and it now pins it EARLIER than it used to, which is the point.
//
// A footprint read only before COMMIT proves WHAT was held, never WHEN. A unit that leaves
// its target unlocked through acquisition and takes it inside Execute ends with exactly the
// declared footprint and passes, while having done the one thing the declared order exists
// to prevent: acquiring the relation with real concurrent writers after other locks are
// already held, which is how two units deadlock against each other.
//
// THE GUARANTEE MOVED FROM MEASUREMENT TO CONSTRUCTION, and this test moved with it.
// Previously the only way to express "the target is left for Execute" was to point
// TargetStatement at a DIFFERENT relation, and the post-acquisition footprint check caught
// it against pg_locks. That expression is now refused by validate(), because a
// TargetStatement that is not exactly the statement the plan generates for its own target
// and mode is precisely how a mutating second statement smuggled durable work in ahead of
// the precondition (see TestLockPlanRefusesAnythingButTheGeneratedLockStatement).
//
// So the property is now enforced upstream: the plan cannot describe this shape at all, and
// the refusal lands before a transaction is even opened. That is strictly stronger than
// catching it mid-flight, and it is what this test asserts.
//
// The post-acquisition footprint check STAYS, and its purpose is now honestly narrower: it
// is the net for locks the SERVER adds that nobody declared, which a plan cannot prevent by
// construction. Its unreachable-by-plan status is recorded next to it in the runner rather
// than pretended away with a test that cannot reach it either.
//
// Mutation that must turn this red: accept a TargetStatement that merely starts with
// LOCK TABLE.
func TestRetryUnitRefusesAPlanThatLeavesItsTargetToExecute(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 3})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	u := retryUnitFixture(t, ctx, db)
	// Acquisition would touch only the metadata relation; the TARGET is left for Execute.
	// The end state would be identical to a correct run — same relations, same modes — and
	// only the order would differ.
	u.Plan.TargetStatement = `LOCK TABLE ONLY "public"."olv_ru_receipts" IN ROW EXCLUSIVE MODE`
	u.Plan.Metadata = nil
	var executes, projects int
	u.Execute = func(ctx context.Context, tx *sql.Tx, _ prestate) error {
		executes++
		_, err := tx.ExecContext(ctx, `LOCK TABLE ONLY "public"."olv_ru_target" IN ROW EXCLUSIVE MODE`)
		return err
	}
	inner := u.Project
	u.Project = func(pctx context.Context, dbx rowQuerier) (prestate, error) {
		projects++
		return inner(pctx, dbx)
	}

	b := newLockBudget(10*time.Second, time.Now, sleepCtx, jitterFloat)
	err = u.run(ctx, conn, b)
	t.Logf("TARGET_LEFT_TO_EXECUTE|err=%v|projects=%d|executes=%d", err, projects, executes)

	if err == nil {
		t.Fatal("run succeeded on a plan whose acquisition never touches its own target")
	}
	// The plan is refused by VALIDATION, which is upstream of everything: no transaction,
	// no projection, no statement.
	if !strings.Contains(err.Error(), "the only statement it may issue") {
		t.Errorf("run = %v; the plan should be refused for declaring an acquisition statement that is not its own target lock", err)
	}
	if executes != 0 {
		t.Errorf("Execute ran %d times: the refusal must land before any unit work", executes)
	}
	// AND BEFORE THE PRESTATE IS EVEN READ. run() validates the spec and the plan before
	// projecting, so a plan this broken costs no roundtrip at all.
	if projects != 0 {
		t.Errorf("the prestate was projected %d times before the plan was refused; plan validation runs before any roundtrip", projects)
	}
}

// TestRetryUnitRefusesAUnitThatCannotVerifyItself pins that the postcondition cannot
// be opted out of.
//
// The check used to be conditional on ProjectObject being non-nil, so a caller who
// simply left the field unset got no poststate verification at all — the guarantee
// silently absent rather than loudly refused. A unit that cannot READ its poststate
// cannot be held to it, and the only safe answer is to decline to run.
//
// Mutation that must turn this red: make the postcondition conditional again.
func TestRetryUnitRefusesAUnitThatCannotVerifyItself(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 3})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	for _, tc := range []struct {
		name string
		drop func(*retryUnit)
	}{
		{"no object projector", func(u *retryUnit) { u.ProjectObject = nil }},
		{"no receipt projector", func(u *retryUnit) { u.ProjectReceipt = nil }},
		{"no prestate projector", func(u *retryUnit) { u.Project = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := retryUnitFixture(t, ctx, db)
			var executes int
			u.Execute = func(context.Context, *sql.Tx, prestate) error { executes++; return nil }
			tc.drop(&u)

			b := newLockBudget(5*time.Second, time.Now, sleepCtx, jitterFloat)
			if err := u.run(ctx, conn, b); !errors.Is(err, ErrMigrationUnauthorised) {
				t.Fatalf("run = %v, want ErrMigrationUnauthorised: a unit that cannot verify itself must decline to run rather than run unverified", err)
			}
			if executes != 0 {
				t.Errorf("executed %d times without the means to check its own result", executes)
			}
		})
	}
}

// TestRetryUnitRejectsAPreconditionThatChangedBeforeTheLock is the TOCTOU regression.
//
// The projection that authorizes the change used to be taken on the connection, before
// the transaction existed and long before the target was locked. That is a decision
// made without protection, and the advisory lock only serializes conforming NODES — it
// does not stop a DBA with psql. The contrast measured the window:
//
//	PRECONDITION_TOCTOU|projected=O|intervening=D|final=A|receipts=1|err=<nil>
//
// A guard moved O -> D in that window and the unit committed as though it had performed
// the authorized O -> A.
//
// The fix re-reads the precondition on the transaction that already holds the strongest
// mode, where nothing can move it. This drives exactly that: the first projection
// reports the authorized state, the second — the one under the lock — reports the state
// an intervening session left behind.
//
// Mutation that must turn this red: remove the re-projection under the lock.
func TestRetryUnitRejectsAPreconditionThatChangedBeforeTheLock(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 3})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	u := retryUnitFixture(t, ctx, db)
	u.Spec = unitSpec{Intent: intentTransitionLegacyOToA, CanonicalEnableState: guardStateAlways}

	var projections, lockedProjections, executes int
	u.Project = func(ctx context.Context, dbx rowQuerier) (prestate, error) {
		projections++
		// The SECOND projection is the one taken under the lock. Prove it really is
		// under the lock rather than trusting the call order: ask the catalog whether
		// this backend already holds a relation lock on the target.
		if projections > 1 {
			var held int
			if err := dbx.QueryRowContext(ctx, `
SELECT count(*) FROM pg_catalog.pg_locks l
JOIN pg_catalog.pg_class c ON c.oid = l.relation
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE l.pid = pg_catalog.pg_backend_pid() AND l.locktype = 'relation' AND l.granted
  AND n.nspname = 'public' AND c.relname = 'olv_ru_target'`).Scan(&held); err != nil {
				return prestate{}, err
			}
			if held == 0 {
				t.Error("the second projection ran without the target lock held, so it is not the atomic re-read this test is about")
			}
			lockedProjections++
			// What an intervening session left behind: the guard was DISABLED.
			return prestate{TargetExists: true, GuardPresent: true,
				GuardEnableState: guardStateDisable, GuardMatchesCanonical: true}, nil
		}
		// What the runner saw before it had any protection.
		return prestate{TargetExists: true, GuardPresent: true,
			GuardEnableState: guardStateOrigin, GuardMatchesCanonical: true}, nil
	}
	u.Execute = func(context.Context, *sql.Tx, prestate) error { executes++; return nil }

	b := newLockBudget(10*time.Second, time.Now, sleepCtx, jitterFloat)
	err = u.run(ctx, conn, b)

	if !errors.Is(err, ErrMigrationUnauthorised) {
		t.Fatalf("run = %v, want ErrMigrationUnauthorised: the guard was DISABLED by the time the lock was held, and D -> A is not the authorized O -> A transition", err)
	}
	if lockedProjections == 0 {
		t.Error("no projection ran under the lock, so the precondition was still decided without protection")
	}
	if executes != 0 {
		t.Errorf("Execute ran %d times on a precondition that no longer held", executes)
	}
	if !strings.Contains(err.Error(), "once locked") {
		t.Errorf("the refusal does not distinguish the locked reading from the earlier one, so an operator cannot tell a race from a bad request: %v", err)
	}
}

// TestSetLocalTimeoutsReportsNothingArmedWhenTheKnobDidNotTake closes the gap between
// what this runner BELIEVES it armed and what the server actually received.
//
// setLocalTimeouts used to return the nominal duration alongside its error, which said
// "a statement_timeout of this length is armed" about a SET that failed. The classifier
// reads exactly that field to decide whether a 57014 was its own doing, so the next
// cancellation — an operator's pg_cancel_backend, say — would have been filed as this
// runner's own timeout and RETRIED, on the strength of a timeout the server never had.
//
// The failure is forced the only way it can be forced deterministically: PostgreSQL
// refuses a statement_timeout outside 0..2147483647 ms, so the first SET succeeds and
// the second is rejected by the server. Nothing here mocks anything.
//
// Mutation that must turn this red: return effectiveMillis(statement) with the error.
func TestSetLocalTimeoutsReportsNothingArmedWhenTheKnobDidNotTake(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // test teardown

	// Just past the documented maximum of 2147483647 ms.
	const overRange = 2147483648 * time.Millisecond
	armed, err := setLocalTimeouts(ctx, tx, 0, overRange)
	if err == nil {
		t.Fatalf("PostgreSQL accepted a statement_timeout of %s; this test's premise is that it refuses it", overRange)
	}
	t.Logf("SET_REFUSED|armed=%s|err=%v", armed, err)
	if armed != 0 {
		t.Errorf("setLocalTimeouts reported %s armed alongside an error; the server rejected the SET, so nothing is armed and a later 57014 must not be attributed to this runner",
			armed)
	}

	// And the property that makes it matter: with Armed=0 the classifier refuses to call
	// an external cancellation its own.
	f := unitFailure{Phase: phaseExecute, Armed: armed, Elapsed: 5 * time.Millisecond,
		Err: &pgconn.PgError{Code: sqlStateQueryCanceled}}
	if got := classifyCancel(ctx, f); got != cancelUnknown {
		t.Errorf("classifyCancel = %v with nothing armed, want cancelUnknown: a timeout that was never set cannot have fired", got)
	}
}

// TestRetryUnitAttributesNoTimeoutAtTheCommitBoundary pins what the runner may claim
// about a cancellation delivered during COMMIT.
//
// Its previous name and header said the runner reports "the timeout PostgreSQL actually
// received", and its declared mutation was "pass exec instead of execArmed". Both outlived
// the property: the commit path now passes a literal zero, so overwriting execArmed with
// the nominal value leaves this green — verified by mutation, which reported
// reported=0s|exec_effective=2.987s|exec_nominal=2.987654321s.
//
// What it does discriminate, and what it is now named for, is the property that matters:
// nothing is attributable at the commit boundary, so an unexplained 57014 there routes to
// reconciliation instead of being retried as if it were this runner's own timeout.
//
// Mutation that must turn this red: report any non-zero Armed at phaseCommit.
func TestRetryUnitAttributesNoTimeoutAtTheCommitBoundary(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 3})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	u := retryUnitFixture(t, ctx, db)

	// A commit that fails AFTER the transaction is settled, so the failure lands at
	// phaseCommit with execArmed already computed. Rolling back keeps the fixture clean.
	restore := commitTx
	commitTx = func(tx *sql.Tx) error {
		_ = tx.Rollback() //nolint:errcheck // deliberate: this attempt must not commit
		return errors.New("synthetic transport failure after the commit was attempted")
	}
	defer func() { commitTx = restore }()

	// A clock that does not move, so remaining() is this exact value on every reading —
	// and it is deliberately NOT a whole number of milliseconds.
	const budgetTotal = 2987654321 * time.Nanosecond
	frozen := time.Now()
	b := newLockBudget(budgetTotal, func() time.Time { return frozen }, sleepCtx, jitterFloat)

	f, _, _ := u.attempt(ctx, conn, b, prestate{TargetExists: true})
	if f.Phase != phaseCommit {
		t.Fatalf("the attempt failed at %s, not at the commit: %v", f.Phase, f.Err)
	}
	t.Logf("COMMIT_ARMED|reported=%s|exec_effective=%s|exec_nominal=%s",
		f.Armed, effectiveMillis(budgetTotal), budgetTotal)

	// NOTHING IS SAFELY ATTRIBUTABLE AT THE COMMIT — note the narrower claim.
	//
	// statement_timeout is enabled in start_xact_command and covers parse, PortalRun and
	// any ProcessUtility hook; finish_xact_command disables it before
	// CommitTransactionCommand, so only end-of-transaction work is provably past the
	// disarm (measured by TestPostgresDisarmsStatementTimeoutBeforeCommitTransactionCommand).
	// A client cannot tell which side a 57014 landed on, so the runner vouches for
	// nothing. Reporting a non-zero value would let an external cancellation arriving near
	// it be filed as this runner's own timeout and retried — the runner arguing with a
	// human who is actively intervening.
	if f.Armed != 0 {
		t.Errorf("the commit failure reported Armed=%s; PostgreSQL disarms statement_timeout before COMMIT, so no timeout this runner set could have fired there",
			f.Armed)
	}

	// And the consequence, stated where it bites: with nothing armed, an unexplained
	// cancellation at COMMIT cannot be classified as our own.
	canceled := unitFailure{Phase: phaseCommit, Armed: f.Armed, Elapsed: 737774652 * time.Nanosecond,
		Err: &pgconn.PgError{Code: sqlStateQueryCanceled}}
	if got := classifyCancel(ctx, canceled); got != cancelUnknown {
		t.Errorf("classifyCancel at COMMIT = %v, want cancelUnknown", got)
	}
	if decision, _ := classifyFailure(ctx, canceled); decision != retryAfterReconcile {
		t.Errorf("an unexplained cancellation at COMMIT classified as %v, want retryAfterReconcile: the unit's fate is the urgent question, and only the database knows it",
			decision)
	}
}

// TestPostgresDisarmsStatementTimeoutBeforeCommitTransactionCommand measures the premise
// the assertion above rests on, instead of citing it — and its name is deliberately the
// narrow claim.
//
// The general documentation describes statement_timeout as a per-statement ceiling, and
// reading only that, arming it before Execute and then judging a COMMIT failure against
// it looks correct. It is not: the backend disables the timeout in finish_xact_command
// before CommitTransactionCommand, so END-OF-TRANSACTION work runs unbounded by it.
//
// What this does NOT show, and what an earlier name of it wrongly implied: that no
// timeout is live during a COMMIT at all. statement_timeout is enabled in
// start_xact_command and covers parse, PortalRun and any ProcessUtility hook. Only the
// deferred work measured here is provably past the disarm.
//
// A DEFERRABLE INITIALLY DEFERRED constraint trigger is what makes this observable —
// its work runs inside the COMMIT — so the commit here takes far longer than the armed
// timeout and is not canceled.
//
// If a future major changes this, THIS test fails rather than the runner silently going
// back to misclassifying cancellations.
func TestPostgresDisarmsStatementTimeoutBeforeCommitTransactionCommand(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown

	_ = retryUnitFixture(t, ctx, db) // creates olv_ru_receipts
	const commitWork = 2 * time.Second
	armSlowCommit(t, ctx, db, commitWork)

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	const armed = 250 * time.Millisecond
	if _, err := setLocalTimeouts(ctx, tx, 0, armed); err != nil {
		t.Fatalf("arm the timeouts: %v", err)
	}
	// THE PREMISE, read back from the server rather than assumed from the SET.
	var effective string
	if err := tx.QueryRowContext(ctx,
		`SELECT pg_catalog.current_setting('statement_timeout')`).Scan(&effective); err != nil {
		t.Fatalf("read statement_timeout back: %v", err)
	}
	if effective != "250ms" {
		t.Fatalf("statement_timeout reads back as %q, not 250ms; the premise of this test is not established", effective)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO olv_ru_receipts VALUES ('slow-commit')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	started := time.Now()
	commitErr := tx.Commit()
	elapsed := time.Since(started)
	t.Logf("COMMIT_VS_STATEMENT_TIMEOUT|armed=%s|commit_work=%s|elapsed=%s|err=%v",
		armed, commitWork, elapsed, commitErr)

	if commitErr != nil {
		t.Fatalf("the COMMIT failed: %v", commitErr)
	}
	if elapsed < commitWork {
		t.Fatalf("the commit took %s, less than the %s of deferred work; the trigger did not run inside it and this measures nothing",
			elapsed, commitWork)
	}
	// The whole point: it ran eight times its armed timeout and was not canceled.
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM olv_ru_receipts`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("the receipt table holds %d rows, want 1: the commit did not become durable", rows)
	}
}

// TestRetryUnitBoundsThePostconditionByItsBudget is the last unbounded roundtrip.
//
// Every statement this runner issues goes through lockBudget.context so the deadline
// governs the wire and not merely the code between roundtrips. The postcondition read
// was the exception: it ran on the CALLER's context, which on a boot path has no
// deadline at all, so a projection that hung left the whole unit hanging with every lock
// it had taken still held — the deadline bounding everything except the reading that
// decides whether the work may commit.
//
// The projection here blocks until its context ends, with a long escape hatch so the
// unbounded case FAILS with a diagnosis instead of hanging the suite.
//
// Mutation that must turn this red: pass ctx instead of the budget's context.
func TestRetryUnitBoundsThePostconditionByItsBudget(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 3})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	u := retryUnitFixture(t, ctx, db)
	var sawDeadline, hadDeadline bool
	u.ProjectObject = func(pctx context.Context, _ rowQuerier) (objectProjection, error) {
		_, hadDeadline = pctx.Deadline()
		select {
		case <-pctx.Done():
			sawDeadline = true
			return objectProjection{}, pctx.Err()
		case <-time.After(45 * time.Second):
			return objectProjection{}, errors.New("the postcondition projection ran unbounded: its context never ended")
		}
	}

	const budget = 3 * time.Second
	b := newLockBudget(budget, time.Now, sleepCtx, jitterFloat)
	started := time.Now()
	f, _, _ := u.attempt(ctx, conn, b, prestate{TargetExists: true})
	elapsed := time.Since(started)

	t.Logf("POSTCONDITION_BOUND|elapsed=%s|budget=%s|deadline_present=%v|ctx_ended=%v|phase=%s",
		elapsed, budget, hadDeadline, sawDeadline, f.Phase)

	if !hadDeadline {
		t.Error("the postcondition projection was handed a context with no deadline: the budget does not govern the reading that authorizes the commit")
	}
	if !sawDeadline {
		t.Fatalf("the postcondition projection was not ended by its own context (phase %s, err %v)", f.Phase, f.Err)
	}
	if elapsed > budget+5*time.Second {
		t.Errorf("the attempt took %s against a %s budget", elapsed, budget)
	}
	if f.Err == nil {
		t.Fatal("the attempt succeeded even though its postcondition never completed")
	}
	// FAIL CLOSED AS A SPENT BUDGET, not as a postcondition that was checked and found
	// wanting. This assertion was the wrong way round: it demanded
	// ErrMigrationPostconditionFailed, which reads as "the object is not in its declared
	// state" — said about an object nobody managed to look at. The two diagnoses send an
	// operator in opposite directions, and the first sends them hunting for a corrupted
	// object that is perfectly fine.
	if !errors.Is(f.Err, ErrMigrationLockBudgetExceeded) {
		t.Errorf("a postcondition cut short by the budget reported %v; the clock ran out, so it must fail closed on the budget sentinel", f.Err)
	}
	if errors.Is(f.Err, ErrMigrationPostconditionFailed) {
		t.Errorf("a postcondition that was never read reported %v: nobody looked at the object, so nothing may be claimed about its state", f.Err)
	}
	// The work must not be on disk: the deferred rollback releases everything.
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM olv_ru_receipts`).Scan(&rows); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if rows != 0 {
		t.Errorf("the receipt table holds %d rows after a unit whose poststate was never verified", rows)
	}
}

// TestReconcileIsBoundedAndSurvivesTheCallersCancellation pins the one context in this
// file that must NOT be the budget's.
//
// Reconciliation is not the unit's work; it is the DIAGNOSIS of the unit's failure, and
// it is reached precisely when that failure was a deadline expiring or a caller
// canceling mid-COMMIT. Bounding it by the budget it exists to explain would kill it
// exactly when it is needed — both readings would fail, the matrix would answer
// outcomeUnknown, and a unit that genuinely committed would be reported as
// undetermined, which leaves an operator with nothing but manual inspection.
//
// Unbounded is not the alternative either: these run on the connection whose COMMIT just
// failed. So the contract is BOTH, and this asserts both halves.
//
// Mutations that must turn this red: derive the projection context from ctx directly
// (inherits the cancellation), or from the budget (dies with it); or drop the timeout.
func TestReconcileIsBoundedAndSurvivesTheCallersCancellation(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	u := retryUnitFixture(t, ctx, db)

	type seen struct {
		err      error
		deadline time.Duration
		ok       bool
	}
	var receiptSeen, objectSeen seen
	u.ProjectReceipt = func(rctx context.Context, _ rowQuerier) (receiptProjection, error) {
		receiptSeen.err = rctx.Err()
		dl, ok := rctx.Deadline()
		receiptSeen.ok = ok
		if ok {
			receiptSeen.deadline = time.Until(dl)
		}
		return receiptProjection{Present: false}, nil
	}
	u.ProjectObject = func(rctx context.Context, _ rowQuerier) (objectProjection, error) {
		objectSeen.err = rctx.Err()
		dl, ok := rctx.Deadline()
		objectSeen.ok = ok
		if ok {
			objectSeen.deadline = time.Until(dl)
		}
		return objectProjection{}, nil
	}

	// The caller has already given up — the state reconciliation is normally reached in.
	canceled, cancel := context.WithCancel(ctx)
	cancel()

	out := u.reconcileThrough(canceled, conn, prestate{TargetExists: true})

	t.Logf("RECONCILE_CTX|receipt_err=%v|receipt_deadline=%s(%v)|object_err=%v|object_deadline=%s(%v)|outcome=%s",
		receiptSeen.err, receiptSeen.deadline, receiptSeen.ok,
		objectSeen.err, objectSeen.deadline, objectSeen.ok, out)

	for name, s := range map[string]seen{"receipt": receiptSeen, "object": objectSeen} {
		if s.err != nil {
			t.Errorf("the %s projection inherited the caller's cancellation (%v); reconciliation is reached BECAUSE the caller canceled, so inheriting it means never answering the question",
				name, s.err)
		}
		if !s.ok {
			t.Errorf("the %s projection ran with no deadline; it runs on the connection whose COMMIT just failed", name)
			continue
		}
		if s.deadline <= 0 || s.deadline > reconcileProjectionTimeout+time.Second {
			t.Errorf("the %s projection had %s left, want a bound around %s", name, s.deadline, reconcileProjectionTimeout)
		}
	}
	// Both readings succeeded, so the matrix must have produced a real verdict rather
	// than the fail-closed unknown an unreadable projection yields.
	if out == outcomeUnknown {
		t.Error("reconciliation answered unknown even though both projections returned cleanly")
	}
}

// TestRetryUnitGivesItsCallbacksTheUnitBudget pins the half of the deadline that lives in
// the CONTEXT rather than in PostgreSQL, and it was missing entirely.
//
// Execute and Receipt used to be called with the CALLER's context. On a boot path that
// context typically carries no deadline at all, so a callback doing exactly the right
// thing — watching the context it was handed — had nothing to watch. Measured with a
// 500ms unit budget, on a callback that cooperated perfectly:
//
//	EXECUTE_CONTEXT_BOUND|elapsed=2.008353536s|budget=500ms|deadline_present=false|
//	context_ended=false
//
// The two bounds a unit's work has are not interchangeable, and relying on either alone
// leaves this hole. statement_timeout governs each STATEMENT and restarts for the next,
// so it says nothing about a callback issuing twenty of them, or waiting on a channel.
// The transaction's own context aborts SQL issued on tx, but cannot make a Go function
// return. Only the context handed to the callback can.
//
// TestRetryUnitStopsAtItsBudget does NOT cover this: its Execute returns a synthetic
// deadlock immediately, so it exercises the retry/backoff loop and never a callback that
// takes time. Two tests, two properties.
//
// Mutation that must turn this red: pass ctx instead of the budget's context to either
// callback.
func TestRetryUnitGivesItsCallbacksTheUnitBudget(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	// A cooperative callback: it inspects what it was given and waits for it, with a long
	// escape so an unbounded context FAILS with a diagnosis instead of hanging the suite.
	type seen struct {
		hadDeadline bool
		ended       bool
		left        time.Duration
	}
	cooperate := func(cctx context.Context, s *seen, spend func()) error {
		dl, ok := cctx.Deadline()
		s.hadDeadline = ok
		if ok {
			s.left = time.Until(dl)
		}
		select {
		case <-cctx.Done():
			s.ended = true
			// The budget's own clock has to agree, or the deadline surfaces as whatever
			// the canceled statement returned instead of as the spent budget. With the
			// clock driven rather than read, saying so is this line.
			spend()
			return cctx.Err()
		case <-time.After(20 * time.Second):
			return errors.New("the callback's context never ended: it did not carry the unit budget")
		}
	}

	// THE BUDGET IS BIGGER THAN IT LOOKS, AND THE CLOCK IS DRIVEN. Both are the fix for a
	// premise that depended on the host.
	//
	// It used to be 500ms measured by time.Now, and reaching either callback costs the same
	// nine round trips the sibling test documents. If the budget ran out among them the
	// callback was never entered, and the failure read "the callback's context never ended,
	// so the budget did not govern it" — an accusation aimed at the runner for something the
	// machine did. This repository has measured a SINGLE round trip over 250ms on this host,
	// so half a second shared nine ways was never a safe premise.
	//
	// With the clock still, the budget is no longer a pool the setup drains: it is the real
	// deadline each individual round trip is handed, and two seconds is an order of magnitude
	// above the worst round trip anyone here has measured. What the callback then does is
	// spend the budget deliberately, so the routing assertion below is about the property and
	// not about whether the host was fast enough to reach it.
	//
	// WHAT THIS DOES NOT FIX, corrected twice now because the first correction still promised
	// too much. The deadline handed to each derived context is REAL time — lockBudget.context
	// builds it from the remaining DURATION, not from the injected clock — so:
	//
	//   - a still clock means each DERIVED context starts again from the full budget, so the
	//     pool no longer drains ACROSS derived contexts; but
	//   - SEVERAL roundtrips share one derived context before either callback is reached, and
	//     those still share its real deadline. A host that stalls them past it fails this
	//     fixture pointing at the runner.
	//
	// So the sharing is NARROWED, not removed, and nothing here should say otherwise. Three
	// successive rewordings of this paragraph each claimed a bit less than the last while still
	// claiming too much, which is how a false statement survives being corrected. Removing the
	// sharing means injecting the timer into lockBudget.context — production surface, its own
	// change, not something to smuggle into a test fix.
	const budget = 2 * time.Second

	for _, tc := range []struct {
		name  string
		phase string
	}{
		{name: "Execute", phase: "execute"},
		{name: "Receipt", phase: "receipt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 3})
			if err != nil {
				t.Fatalf("open pool: %v", err)
			}
			defer db.Close() //nolint:errcheck // test teardown
			conn, err := db.Conn(ctx)
			if err != nil {
				t.Fatalf("conn: %v", err)
			}
			defer conn.Close() //nolint:errcheck // test teardown

			var clockMu sync.Mutex
			clockBase, clockOffset := time.Now(), time.Duration(0)
			now := func() time.Time {
				clockMu.Lock()
				defer clockMu.Unlock()
				return clockBase.Add(clockOffset)
			}
			spend := func() {
				clockMu.Lock()
				defer clockMu.Unlock()
				clockOffset += 2 * budget
			}

			u := retryUnitFixture(t, ctx, db)
			var s seen
			if tc.name == "Execute" {
				u.Execute = func(cctx context.Context, _ *sql.Tx, _ prestate) error {
					return cooperate(cctx, &s, spend)
				}
			} else {
				u.Receipt = func(cctx context.Context, _ *sql.Tx, _ prestate) error {
					return cooperate(cctx, &s, spend)
				}
			}

			b := newLockBudget(budget, now, sleepCtx, jitterFloat)
			runErr := u.run(ctx, conn, b)

			t.Logf("CALLBACK_CONTEXT|phase=%s|budget=%s|deadline_present=%v|context_ended=%v|left_at_entry=%s|err=%v",
				tc.phase, budget, s.hadDeadline, s.ended, s.left, runErr)

			if !s.hadDeadline {
				t.Error("the callback was handed a context with no deadline: the unit's budget does not reach the work it is supposed to bound")
			}
			if !s.ended {
				t.Fatal("the callback's context never ended, so the budget did not govern it")
			}
			// The proportional wall-clock ceiling that used to sit here is GONE. It compared
			// real elapsed time against a budget that is now virtual, so it could only ever
			// have measured the host — and the three assertions around it are the property.
			// And the deadline must surface as the SPENT BUDGET, not as whatever error the
			// canceled statement happened to produce.
			if !errors.Is(runErr, ErrMigrationLockBudgetExceeded) {
				t.Errorf("run = %v; a budget that expired inside a callback must surface as %v, which is the one condition a caller can act on",
					runErr, ErrMigrationLockBudgetExceeded)
			}
			// Nothing may have been committed.
			var rows int
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM olv_ru_receipts`).Scan(&rows); err != nil {
				t.Fatalf("count receipts: %v", err)
			}
			if rows != 0 {
				t.Errorf("the receipt table holds %d rows after a unit whose budget expired mid-flight", rows)
			}
		})
	}
}

// TestRetryUnitReconcilesAgainstThePrestateItJudgedAgainst is the handoff that was
// missing, and it is the difference between a boot that finishes and one that stops.
//
// attempt re-reads the precondition under the lock and judges everything downstream
// against THAT reading. But it returned only the failure and the holders, so run()
// reconciled with the projection it still held from before the lock. Measured with an
// adopt-legacy unit, for which both O and A are authorized prestates:
//
//	ROUND9_LOCKED_PRESTATE|projects=2|initial=O|locked=A|durable_state=A|receipts=1|
//	err=... outcome could not be determined ... (divergent)
//
// The receipt and the 'A' state were durable and correct. Against pre=A the object
// satisfies adoption; against the stale pre=O it looks divergent — so a valid, committed
// unit halted the boot for a human to inspect.
//
// Two existing regressions LOOK like they cover this together and do not: deleting
// `pre = locked` left both green. The first uses the same prestate in both readings, so
// there is no difference to lose; the second changes to an UNauthorised state and returns
// before reconciliation is ever reached.
//
// Mutation that must turn this red: hand reconcile() the pre-lock projection —
// `_ = judged; res := u.reconcile(ctx, pre)`. Verified, and it reproduces the original
// measurement exactly: receipts=1 durable with the outcome (divergent).
//
// NOTE ON A MUTATION THAT DOES NOT ISOLATE THIS. Deleting `pre = locked` inside attempt
// also turns it red, but for a different reason — the postcondition then compares the
// object against the stale prestate and fails before committing, so nothing reaches
// reconciliation at all. Red is not enough; it has to be red for the reason claimed.
func TestRetryUnitReconcilesAgainstThePrestateItJudgedAgainst(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	u := retryUnitFixture(t, ctx, db)
	// Adoption, because it is the intent for which the two readings can BOTH be
	// authorized and yet demand different poststates. Under create-guard or the O -> A
	// transition a changed prestate is refused outright and never reaches the handoff.
	u.Spec = unitSpec{Intent: intentAdoptLegacy, CanonicalEnableState: guardStateOrigin}

	const epoch = 7
	var projects int
	u.Project = func(context.Context, rowQuerier) (prestate, error) {
		projects++
		state := guardStateOrigin
		if projects >= 2 {
			// The world moved between the unprotected projection and the lock. Both states
			// are legal for adoption, which is what makes the staleness silent.
			state = guardStateAlways
		}
		return prestate{
			TargetExists: true, GuardPresent: true, GuardEnableState: state,
			GuardMatchesCanonical: true, Epoch: epoch,
		}, nil
	}
	u.ProjectObject = func(context.Context, rowQuerier) (objectProjection, error) {
		return objectProjection{
			Exists: true, GuardPresent: true, MatchesCanonical: true,
			GuardEnableState: guardStateAlways,
		}, nil
	}
	u.ProjectReceipt = func(ctx context.Context, dbx rowQuerier) (receiptProjection, error) {
		var n int
		if err := dbx.QueryRowContext(ctx, `SELECT count(*) FROM olv_ru_receipts`).Scan(&n); err != nil {
			return receiptProjection{}, err
		}
		return receiptProjection{Present: n > 0, Epoch: epoch}, nil
	}

	// A real commit whose acknowledgement is then lost — the only route that reaches
	// reconciliation with work genuinely on disk.
	restore := commitTx
	commitTx = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("round9: durable commit acknowledgement lost")
	}
	defer func() { commitTx = restore }()

	b := newLockBudget(30*time.Second, time.Now, sleepCtx, jitterFloat)
	runErr := u.run(ctx, conn, b)

	var receipts int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM olv_ru_receipts`).Scan(&receipts); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	t.Logf("LOCKED_PRESTATE|projects=%d|initial=%s|locked=%s|receipts=%d|run_err=%v",
		projects, guardStateOrigin, guardStateAlways, receipts, runErr)

	// THE PREMISES: both projections ran, and the work is durable. Without the second
	// projection there is no staleness to lose, and this test would pass vacuously.
	if projects < 2 {
		t.Fatalf("the prestate was projected %d times; the locked re-read never happened, so this run shows nothing", projects)
	}
	if receipts != 1 {
		t.Fatalf("the receipt table holds %d rows, want exactly 1", receipts)
	}

	if runErr != nil {
		t.Errorf("run = %v after a durable, correctly adopted unit; reconciliation was judged against the pre-lock projection instead of the one the attempt actually used, so a valid unit halts the boot",
			runErr)
	}
}

// TestRetryUnitReportsASpentBudgetAsASpentBudget covers the routing half of the deadline.
//
// Every roundtrip already gets a budget-derived context. What was missing is that the
// SHAPE of the resulting error decided where it went. A cooperative callback waiting for
// the deadline it was handed produced:
//
//	ROUND9_LOCKED_PROJECT_BUDGET|elapsed=150.725029ms|budget=150ms|projects=2|
//	err=... needs a new session ... re-project ...|budget_sentinel=false|new_session=true
//
// classifyFailure routes a non-PgError before commit to retryNewSession, so "this unit ran
// out of time" reached the caller as "replace the session holding the cluster-wide
// coordination lock" — a decision about the whole migration, taken on an error's shape.
// The coordinator already had the answer: consult the budget's own clock rather than trust
// the error, which also survives pgx turning an expired context into driver.ErrBadConn.
//
// Mutation that must turn this red: drop the budgetFailure call around the locked
// re-projection.
//
// # THE BUDGET'S CLOCK IS DRIVEN BY THIS TEST, AND THAT IS THE FIX FOR A REAL FLAKE
//
// This test used to hand the unit a 400 ms budget measured by time.Now, and the hub's sweep
// showed what that costs. Reaching the locked re-projection at all takes NINE round trips
// against PostgreSQL — the pre-lock projection, BeginTx, two set_config per armAcquisition,
// two LOCK TABLE, and verifyLockFootprint against pg_locks. If the budget ran out in any of
// them the second projection never happened, `projects` stayed at 1, and the test died
// saying "the locked re-projection did not run to its deadline" — accusing the code of
// something the machine did. This repository has its own evidence that the premise is not
// safe: pgstore_test.go documents a SINGLE round trip taking more than 250 ms on this host.
//
// Load did not break the property. It broke the PREMISE, which is the more treacherous of
// the two because the failure message points at the wrong place.
//
// So the clock is injected and this test drives it. It does not advance while the fixture
// does its round trips, which makes the premise independent of how fast the host is; and it
// jumps past the deadline at the exact moment the property is about to be exercised. What is
// left is the property itself: an error of the misroutable SHAPE, produced while the budget's
// own clock says the budget is gone, must surface as a spent budget. No wall clock decides
// anything here, and the real waiting the old version did was only a way of producing that
// pair — the pair is now produced directly.
// errRoutingOpaque is an error the router cannot classify by shape.
//
// It exists so a budget test cannot accidentally pass through budgetSpent's
// context.DeadlineExceeded short-circuit instead of through the budget's clock.
var errRoutingOpaque = errors.New("sqlstore test: an error whose shape tells the router nothing")

func TestRetryUnitReportsASpentBudgetAsASpentBudget(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 3})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close() //nolint:errcheck // test teardown

	// Generous in the budget's own units, because with the clock still it is also the REAL
	// deadline every individual round trip is handed. A tight number here would put back the
	// same fragility one level down. Declared before the callback that spends it.
	const budget = 30 * time.Second

	// THE DRIVEN CLOCK. It stands still through every round trip and moves only where this
	// test moves it, so "did the fixture reach the second projection" stops being a question
	// about the host. Guarded because the runner reads it from more than one goroutine.
	var clockMu sync.Mutex
	clockBase, clockOffset := time.Now(), time.Duration(0)
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clockBase.Add(clockOffset)
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		defer clockMu.Unlock()
		clockOffset += d
	}

	u := retryUnitFixture(t, ctx, db)
	inner := u.Project
	var projects int
	var handedDeadline bool
	u.Project = func(pctx context.Context, dbx rowQuerier) (prestate, error) {
		projects++
		if projects < 2 {
			return inner(pctx, dbx)
		}
		// THE LOCKED RE-READ. It must still be handed a deadline — that half of the
		// contract is asserted, not assumed.
		if _, ok := pctx.Deadline(); !ok {
			return prestate{}, errors.New("the locked re-projection was handed no deadline")
		}
		handedDeadline = true
		// THE ERROR IS OPAQUE ON PURPOSE, and that is a correction the contrast forced.
		//
		// The first version of this returned context.DeadlineExceeded, and budgetSpent
		// (migrationlockacquire.go:231-238) short-circuits on exactly that error WITHOUT
		// consulting the clock. So the test passed with its only advance() deleted: it was
		// pinning the SHAPE of the error, which nobody doubted, and never the routing rule
		// it announces. A fixture that keeps passing when you remove the line it exists to
		// exercise is not a fixture, and this one was mine. The mutation that shows it is
		// advance(0) — the clock that does not move — because DELETING the call is a compile
		// error, and a red that never ran the test verifies nothing.
		//
		// An opaque error leaves the budget's own clock as the ONLY route to
		// ErrMigrationLockBudgetExceeded, which is the rule under test.
		advance(2 * budget)
		return prestate{}, errRoutingOpaque
	}

	b := newLockBudget(budget, now, sleepCtx, jitterFloat)
	runErr := u.run(ctx, conn, b)

	t.Logf("LOCKED_PROJECT_BUDGET|budget=%s|projects=%d|deadline_handed=%v|budget_sentinel=%v|new_session=%v|err=%v",
		budget, projects, handedDeadline,
		errors.Is(runErr, ErrMigrationLockBudgetExceeded),
		errors.Is(runErr, ErrMigrationNeedsNewSession), runErr)

	if projects < 2 || !handedDeadline {
		t.Fatalf("the locked re-projection never ran under a deadline (projects=%d, deadline_handed=%v)", projects, handedDeadline)
	}
	// THE PREMISE OF THE ROUTING RULE, asserted rather than assumed: the budget really is
	// gone by its own clock at the moment the router decides. Without this the test could
	// still drift back into proving something about the error's shape.
	if !b.expired() {
		t.Fatalf("the driven clock left %s on the budget, so nothing here exercised the rule that a SPENT budget is reported as one", b.remaining())
	}
	if !errors.Is(runErr, ErrMigrationLockBudgetExceeded) {
		t.Errorf("run = %v; a spent budget must surface as %v, which is the one condition the caller can act on",
			runErr, ErrMigrationLockBudgetExceeded)
	}
	// AND IT MUST NOT ASK FOR A NEW SESSION. That is a decision about the whole
	// migration — releasing and re-acquiring the cluster-wide coordination lock — and
	// taking it because a context expired is the routing error this closes.
	if errors.Is(runErr, ErrMigrationNeedsNewSession) {
		t.Errorf("run = %v: an expired unit budget asked the caller to replace the session that holds the coordination lock", runErr)
	}
	// The wall-clock ceiling that used to close this test is GONE on purpose. It compared
	// elapsed real time against a budget that is now virtual, so it measured the host and
	// nothing else — and the property above does not depend on it.
}
