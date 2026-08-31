// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// THE REGRESSION THE COVERAGE CANARY CANNOT BE, and that is the whole reason this file
// exists separately from migrationbudgetnorm_test.go.
//
// That canary counts occurrences of `b.context(ctx)` — sites that are ALREADY bounded. A
// callback that never received a bounded context in the first place is invisible to it by
// construction: removing the fix does not lower the count below its floor unless the fix
// was there to be counted. So the property has to be measured behaviourally, against a
// real server, on the shape the defect actually takes.
//
// THE SHAPE. `BeforeAttempt` is the hook that writes the durable "an attempt began" event.
// It opens its OWN transaction on the connection holding the cluster-wide migration lock,
// and its INSERT aims at a log with three unique indexes. The `SET LOCAL lock_timeout` and
// `statement_timeout` the runner arms live in the ATTEMPT's transaction, which does not
// exist yet, so nothing on the server bounds this. Until this fix the hook was called with
// the CALLER's raw context — on the boot path that is context.Background(), with no
// deadline, measured by migrationretry.go itself as
// `EXECUTE_CONTEXT_BOUND|deadline_present=false`. An uncommitted row of another session on
// any of those unique keys therefore made boot wait with no ceiling at all: the repository
// already measured that two inserts aiming at the same key of a unique index wait on each
// other regardless of lock mode (guardcoordinator.go, "ROW EXCLUSIVE DOES NOT CONFLICT
// WITH ITSELF").
//
// The fixture reproduces exactly that: an uncommitted squatter on the unique key the hook
// is about to take. With the fix the unit gives up when the budget is spent; without it the
// call never returns, so this test is written to FAIL ON TIMEOUT rather than hang the
// package — a regression that hangs CI is indistinguishable from a slow one.
func TestPostgresTheStartOfAnAttemptCannotWaitForever(t *testing.T) {
	t.Parallel()
	dsns := isolatedPG(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db, err := sql.Open("pgx", dsns.App)
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	// Three, not two: the unit's own connection, the squatter's, and the hook's.
	db.SetMaxOpenConns(6)
	t.Cleanup(func() { _ = db.Close() })

	// The ledger the hook appends to, standing in for the gate-event log: what matters is
	// the UNIQUE index, which is what the real INSERT contends on.
	if _, err := db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS olv_ba_ledger(ordinal bigint PRIMARY KEY, note text)`); err != nil {
		t.Fatalf("fixture ledger: %v", err)
	}

	// THE SQUATTER: another session inserts the ordinal and does NOT commit. Its
	// transaction is rolled back by the cleanup, so the test leaves nothing behind.
	squatter, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("squatter conn: %v", err)
	}
	t.Cleanup(func() { _ = squatter.Close() })
	stx, err := squatter.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("squatter begin: %v", err)
	}
	defer func() { _ = stx.Rollback() }()
	if _, err := stx.ExecContext(ctx, `INSERT INTO olv_ba_ledger VALUES (1, 'squatter')`); err != nil {
		t.Fatalf("squatter insert: %v", err)
	}

	unit := retryUnitFixture(t, ctx, db)
	// The hook does what production's does: its own transaction, on its own connection,
	// with the context it is HANDED — which is the whole subject of this test.
	hookEntered := make(chan struct{}, 1)
	unit.BeforeAttempt = func(hctx context.Context, _ int) error {
		select {
		case hookEntered <- struct{}{}:
		default:
		}
		tx, err := db.BeginTx(hctx, nil)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(hctx, `INSERT INTO olv_ba_ledger VALUES (1, 'unit')`); err != nil {
			return err
		}
		return tx.Commit()
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("unit conn: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// A short budget so the property is observable in a test rather than in ten minutes.
	// The value is the SUBJECT: with the fix the wait ends within it; without the fix the
	// budget is not consulted at all, because the context that carries it never reaches the
	// hook.
	const budget = 3 * time.Second
	b := newLockBudget(budget, time.Now, sleepCtx, func() float64 { return 1 })

	started := time.Now()
	done := make(chan error, 1)
	go func() { done <- unit.run(ctx, conn, b) }()

	select {
	case <-hookEntered:
	case <-time.After(30 * time.Second):
		t.Fatal("BeforeAttempt was never reached; the fixture is not exercising the path")
	}

	select {
	case err := <-done:
		// THE WAIT MUST BE BOUNDED BY *THIS* BUDGET, not merely finite. The contrast round
		// caught the earlier shape of this assertion: replacing the budget-derived context with
		// a FIXED context.WithTimeout(ctx, unitExecutionTimeout) — a 60-second ceiling that has
		// nothing to do with the unit's deadline — returned in 60.175s and the test passed,
		// because all it demanded was a return inside its own 90-second escape hatch. A test
		// that accepts any ceiling is not testing that the right ceiling is in force.
		//
		// The multiple is generous (the box is shared and a PG round trip is not free) and
		// still an order of magnitude under the 60s constant that used to slip through.
		if elapsed := time.Since(started); elapsed > 6*budget {
			t.Fatalf("BeforeAttempt returned after %s under a %s budget. It is bounded by SOMETHING, "+
				"but not by this unit's deadline — a fixed ceiling unrelated to the budget passes a "+
				"test that only asks for termination", elapsed, budget)
		}
		if err == nil {
			t.Fatal("the unit reported success while another session held the ordinal uncommitted")
		}
		// THE CLASSIFICATION MATTERS AS MUCH AS THE TIMING. A bounded context that
		// surfaced as a bare transport failure would classify as retryNewSession — which
		// asks the caller to replace the very session holding the cluster-wide migration
		// lock. Routing it through budgetFailure is what makes a spent budget say so.
		if !errors.Is(err, ErrMigrationLockBudgetExceeded) {
			t.Fatalf("a spent budget must be reported as a spent budget, got %v", err)
		}
	case <-time.After(90 * time.Second):
		// 30x the budget. Deliberately generous: this must fail for the RIGHT reason, not
		// because a loaded box was slow.
		t.Fatalf("BeforeAttempt did not return within 90s under a %s budget: the hook is running on an "+
			"UNBOUNDED context, so an uncommitted row on its unique key stalls boot with no ceiling. "+
			"On a boot path, forever means a process that never finishes starting and never says why", budget)
	}
}
