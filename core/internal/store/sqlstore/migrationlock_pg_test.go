// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"
	"time"

	"github.com/olivaresai/olivares/core/internal/store/dialect"
	"github.com/olivaresai/olivares/core/store"
)

// TestMigrationWorkRunsOnTheLockHoldingConnection is the regression for residual
// R1. withMigrationLock used to pin the cluster-wide advisory lock on one
// checked-out connection and then run the schema work on the POOL, which took a
// DIFFERENT connection. That cost a connection nobody accounted for (a
// one-connection pool HUNG at boot instead of failing) and, less obviously but
// more importantly, it made every session-scoped setting useless: a lock_timeout
// set on the lock session governed no statement the migration actually ran.
//
// The property asserted here is identity, not behavior-by-proxy: the backend PID
// that executes migration work must BE the backend PID that holds the advisory
// lock. A test that merely checked "boot succeeded" would pass just as happily
// with the two on separate connections.
//
// Mutation that must turn this red: change `return fn(conn)` back to `return
// fn(db)` in withMigrationLock. The pool cannot hand back the connection it has
// already checked out for the lock, so the work lands on a different backend and
// the PIDs differ.
func TestMigrationWorkRunsOnTheLockHoldingConnection(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("postgres dialect unavailable")
	}

	// Four connections: deliberately MORE than the work needs, so that if the
	// migration work were still running on the pool it would succeed in getting a
	// second connection rather than deadlock. This test must fail on the PIDs, not
	// on connection starvation — a test that goes red for the wrong reason is a
	// trap for whoever reads it next.
	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 4})
	if err != nil {
		t.Fatalf("open postgres pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown

	var workPID, holderPID int64
	var holders int

	if err := withMigrationLock(ctx, db, dia, func(mdb dialect.Execer) error {
		// The PID of whatever connection the migration work was handed.
		rows, err := mdb.QueryContext(ctx, "SELECT pg_catalog.pg_backend_pid()")
		if err != nil {
			return err
		}
		defer rows.Close() //nolint:errcheck // scanned below
		if !rows.Next() {
			return rows.Err()
		}
		if err := rows.Scan(&workPID); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}

		// The PID actually holding the advisory lock, read from a SEPARATE pooled
		// connection so it cannot trivially agree with the one above. Advisory locks
		// are per-database and this database is private to this test, so exactly one
		// granted advisory lock must be visible.
		lockRows, err := db.QueryContext(ctx, `
SELECT l.pid
FROM pg_catalog.pg_locks l
JOIN pg_catalog.pg_database d ON d.oid = l.database
WHERE l.locktype = 'advisory'
  AND l.granted
  AND d.datname = pg_catalog.current_database()`)
		if err != nil {
			return err
		}
		defer lockRows.Close() //nolint:errcheck // scanned below
		for lockRows.Next() {
			if err := lockRows.Scan(&holderPID); err != nil {
				return err
			}
			holders++
		}
		return lockRows.Err()
	}); err != nil {
		t.Fatalf("withMigrationLock: %v", err)
	}

	if holders != 1 {
		t.Fatalf("expected exactly one granted advisory lock in this private database, saw %d — the assertion below would be meaningless", holders)
	}
	if workPID == 0 || holderPID == 0 {
		t.Fatalf("did not read both PIDs (work=%d holder=%d)", workPID, holderPID)
	}
	if workPID != holderPID {
		t.Errorf("migration work ran on backend PID %d while the migration advisory lock is held by backend PID %d: the schema work is NOT on the lock-holding connection, so it consumes a second pooled connection and no session-scoped setting on the lock session governs it (residual R1)", workPID, holderPID)
	}
}

// TestMigrationLockPassesThePoolOnSQLite fixes the other half of the contract:
// SQLite has nothing to serialize across, so withMigrationLock must hand fn the
// pool itself rather than checking out a connection it would then have to manage.
// Without this, a change that made the Postgres path unconditional would silently
// alter SQLite's single-writer semantics.
func TestMigrationLockPassesThePoolOnSQLite(t *testing.T) {
	ctx := context.Background()

	dia, ok := dialect.New(store.EngineSQLite)
	if !ok {
		t.Fatal("sqlite dialect unavailable")
	}
	db, err := openSQLite(":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown

	var got dialect.Execer
	if err := withMigrationLock(ctx, db, dia, func(mdb dialect.Execer) error {
		got = mdb
		return nil
	}); err != nil {
		t.Fatalf("withMigrationLock: %v", err)
	}
	if got != dialect.Execer(db) {
		t.Errorf("sqlite path handed fn %T, want the pool itself (*sql.DB): there is nothing to serialize across, so checking out a connection would only add a lifetime to manage", got)
	}
}

// TestMigrationLockSessionIsNeverPooledHoldingTheLock is the regression for the
// leak that "unlock once and pool the connection" cannot close.
//
// pg_advisory_lock is RE-ENTRANT per session. If any migration step takes the
// same key again — and migration work is arbitrary — the session's lock count is
// 2, a single pg_advisory_unlock reports true, and the session goes back to the
// pool STILL HOLDING it. pgx's ResetSession does not clear advisory locks on
// reuse, so the next user of that pooled session inherits a stale lock count and
// the cluster-wide migration lock silently stops serializing anything.
//
// The property asserted is therefore about the SERVER, not about our bookkeeping:
// once withMigrationLock returns, this database must have no granted advisory
// lock left at all.
//
// Mutation that must turn this red: replace the forceDiscard(conn) in
// withMigrationLock's cleanup with conn.Close(). The re-entrant callback below
// then leaves one granted advisory lock behind on a pooled session.
func TestMigrationLockSessionIsNeverPooledHoldingTheLock(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("postgres dialect unavailable")
	}

	// Two connections: small enough that a pooled session would very likely be
	// handed straight back out, which is exactly the hazard being fixed.
	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open postgres pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown

	if err := withMigrationLock(ctx, db, dia, func(mdb dialect.Execer) error {
		// Stand in for a migration step that re-takes the coordination key. This is
		// not contrived: the lock is re-entrant precisely so nested code may take it,
		// and nothing in the callback contract forbids it.
		_, err := mdb.ExecContext(ctx,
			"SELECT pg_catalog.pg_advisory_lock(pg_catalog.hashtextextended('olivares.migrate.v1', 0))")
		return err
	}); err != nil {
		t.Fatalf("withMigrationLock: %v", err)
	}

	var leaked int
	rows, err := db.QueryContext(ctx, `
SELECT l.pid
FROM pg_catalog.pg_locks l
JOIN pg_catalog.pg_database d ON d.oid = l.database
WHERE l.locktype = 'advisory'
  AND l.granted
  AND d.datname = pg_catalog.current_database()`)
	if err != nil {
		t.Fatalf("read advisory locks: %v", err)
	}
	defer rows.Close() //nolint:errcheck // counted below
	for rows.Next() {
		var pid int64
		if err := rows.Scan(&pid); err != nil {
			t.Fatalf("scan: %v", err)
		}
		leaked++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if leaked != 0 {
		t.Errorf("%d advisory lock(s) still granted after withMigrationLock returned: the lock session was returned to the pool while it still held the migration key, so the next user of that session inherits it and the cluster-wide lock stops serializing migrations", leaked)
	}
}

// TestOpenBootsOnASingleConnection is the regression the PID test could not be.
//
// The PID test calls withMigrationLock directly, so it only proves the callback is
// HANDED the lock-holding connection — not that the seven steps inside actually use
// it. A review mutated the very first call site back to the pool
// (`migrate.Apply(ctx, ownerDB, ...)`) and the entire package stayed green, which
// means the property was pinned by nothing.
//
// This pins it where it cannot be faked: a real Open against a real database with a
// pool of ONE. Every step that reached for a second connection would block forever
// against its own lock holder, so the boot can only complete if all of them run on
// the connection the advisory lock is held on. The context deadline turns that
// would-be hang into a failure with a diagnosis — a hanging test teaches nothing.
//
// AuditSpoolMaxBytes stays zero on purpose: the spool recompute is the OTHER
// two-connection demand at boot, it is refused up front for exactly this
// combination, and it is not what this test is about.
//
// It registers a MODULE with both a descriptor and a file migration, because
// register=nil leaves three of the seven steps inert — applyModuleTables, the
// module reconcileColumns and applyModuleFileMigrations do nothing without one, so
// mutating those three would stay green against an empty registry. A regression
// that cannot observe half the call sites it claims to pin is the same trap as the
// PID test it replaces.
//
// Mutation that must turn this red: point ANY of the seven callback steps in
// store.go at `ownerDB` instead of `mdb`.
func TestOpenBootsOnASingleConnection(t *testing.T) {
	dsns := isolatedPG(t)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	st, err := Open(ctx, store.Config{
		Engine:   store.EnginePostgres,
		DSN:      dsns.App,
		MaxConns: 1,
	}, registerBootProbeModule)
	if err != nil {
		if ctx.Err() != nil {
			t.Fatalf("boot did not finish on a one-connection pool before the deadline: some migration step is still asking the pool for a second connection while the advisory lock holds the only one (%v)", err)
		}
		t.Fatalf("Open on a one-connection pool: %v", err)
	}
	defer st.Close() //nolint:errcheck // test teardown
}

// TestMigrationLockRetiresTheSessionOnEveryExit covers the paths a happy-path test
// never reaches: the callback returning an error, and the callback canceling the
// context it was given.
//
// Both leave the session in a state nobody can call clean, and both used to end at
// the same `conn.Close()` that returns it to the pool. The assertion is deliberately
// about the SERVER (no advisory lock left) and about the POOL (no connection kept),
// because either alone can look right while the other is wrong.
func TestMigrationLockRetiresTheSessionOnEveryExit(t *testing.T) {
	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("postgres dialect unavailable")
	}

	cases := []struct {
		name string
		fn   func(context.Context, context.CancelFunc) func(dialect.Execer) error
	}{
		{
			name: "callback returns an error",
			fn: func(context.Context, context.CancelFunc) func(dialect.Execer) error {
				return func(dialect.Execer) error { return errors.New("migration step failed") }
			},
		},
		{
			name: "callback cancels its context",
			fn: func(_ context.Context, cancel context.CancelFunc) func(dialect.Execer) error {
				return func(dialect.Execer) error {
					cancel()
					return context.Canceled
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsns := isolatedPG(t)
			db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
			if err != nil {
				t.Fatalf("open postgres pool: %v", err)
			}
			defer db.Close() //nolint:errcheck // test teardown

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			if err := withMigrationLock(ctx, db, dia, tc.fn(ctx, cancel)); err == nil {
				t.Fatal("withMigrationLock reported success for a failing callback")
			}

			// PHYSICAL discard, checked BEFORE anything else touches the pool. Asserting
			// only "no advisory lock is granted" is not enough: a pooled session that
			// happened to release its lock would satisfy that and still be exactly the
			// hazard — pgx does not clear advisory state on reuse, so the session must
			// not survive at all. Replacing forceDiscard with conn.Close() leaves an idle
			// connection here.
			if open := db.Stats().OpenConnections; open != 0 {
				t.Errorf("the pool still holds %d connection(s) after a failed callback: the lock session was returned to the pool instead of being physically retired", open)
			}

			// The server must hold nothing. Read it on a context of its own: the
			// caller's may have been canceled by the case itself.
			probe, cancelProbe := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancelProbe()
			var leaked int
			if err := db.QueryRowContext(probe, `
SELECT count(*)
FROM pg_catalog.pg_locks l
JOIN pg_catalog.pg_database d ON d.oid = l.database
WHERE l.locktype = 'advisory'
  AND l.granted
  AND d.datname = pg_catalog.current_database()`).Scan(&leaked); err != nil {
				t.Fatalf("read advisory locks: %v", err)
			}
			if leaked != 0 {
				t.Errorf("%d advisory lock(s) still granted after a failed callback: the lock session survived the failure path", leaked)
			}
		})
	}
}

// registerBootProbeModule registers a module with BOTH a descriptor and a file
// migration, so a boot exercises applyModuleTables, the module reconcileColumns and
// applyModuleFileMigrations. With register=nil those three steps are inert and a
// regression through Open cannot see them at all.
func registerBootProbeModule(reg store.ExtensionRegistry) error {
	if err := reg.Register(widgetDescriptor); err != nil {
		return err
	}
	const stmt = "CREATE TABLE IF NOT EXISTS bootprobe_note(id integer)"
	return reg.Migrations("bootprobe", fstest.MapFS{
		"postgres/0001_probe.sql": &fstest.MapFile{Data: []byte(stmt)},
		"sqlite/0001_probe.sql":   &fstest.MapFile{Data: []byte(stmt)},
	})
}

// TestMigrationLockRetiresTheSessionWhenAcquisitionFails covers the path that the
// error and cancel cases never reach: the failure happens INSIDE the acquisition,
// so the callback never runs and the client never learns whether the lock was
// granted.
//
// That ambiguity is the whole point. A pg_advisory_lock canceled by a deadline can
// still have been granted server-side before the cancellation arrived, so the
// client sees an error for a lock it owns. Pooling that session hands the next user
// a lock nobody believes exists — and the previous shape of this code did exactly
// that, because its flag said "not locked" and took the conn.Close() branch.
//
// The failure is produced with the CONTEXT STILL ALIVE, via a session lock_timeout,
// and that detail is the whole test rather than an incidental choice.
//
// A first version canceled the context instead, and it was NOT mutation-sensitive:
// swapping forceDiscard for conn.Close() left it green. The reason is not that
// database/sql discards any connection whose query was canceled — that was my
// explanation and it was wrong. It is that pgx closes the connection on
// cancellation and the resulting driver.ErrBadConn is what makes Close() discard
// instead of pool. So a canceled acquisition hid the defect behind the driver.
//
// With lock_timeout the statement returns 55P03 on a perfectly healthy connection,
// nothing marks it bad, and pooling it is exactly the hazard: PostgreSQL may have
// granted the lock server-side before the timeout fired, so the next user of that
// session inherits a lock nobody believes exists.
//
// Mutation that must turn this red: make the cleanup pool the connection instead of
// retiring it.
func TestMigrationLockRetiresTheSessionWhenAcquisitionFails(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	dia, ok := dialect.New(store.EnginePostgres)
	if !ok {
		t.Fatal("postgres dialect unavailable")
	}

	// A separate pool holds the key, so the pool under test cannot acquire it.
	holder, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open holder pool: %v", err)
	}
	defer holder.Close() //nolint:errcheck // test teardown
	holderConn, err := holder.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer holderConn.Close() //nolint:errcheck // test teardown
	if _, err := holderConn.ExecContext(ctx,
		"SELECT pg_catalog.pg_advisory_lock(pg_catalog.hashtextextended('olivares.migrate.v1', 0))"); err != nil {
		t.Fatalf("holder acquire: %v", err)
	}

	// A budget of milliseconds instead of five minutes: the acquisition exhausts it
	// against the holder above and returns, with the caller's context untouched.
	prev := newCoordinationBudget
	newCoordinationBudget = func() *lockBudget {
		return newLockBudget(50*time.Millisecond, time.Now, sleepCtx, jitterFloat)
	}
	t.Cleanup(func() { newCoordinationBudget = prev })

	db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open postgres pool: %v", err)
	}
	defer db.Close() //nolint:errcheck // test teardown

	ran := false
	err = withMigrationLock(ctx, db, dia, func(dialect.Execer) error {
		ran = true
		return nil
	})
	if ctx.Err() != nil {
		t.Fatal("the caller context was canceled: this test only means something while it stays alive")
	}
	if err == nil {
		t.Fatal("withMigrationLock acquired a key another session holds")
	}
	if !errors.Is(err, ErrMigrationCoordinationTimeout) {
		t.Errorf("acquisition failed with %v, want ErrMigrationCoordinationTimeout: the budget is what must stop it, and a different error means something else did", err)
	}
	if ran {
		t.Error("the callback ran even though the acquisition failed")
	}
	if open := db.Stats().OpenConnections; open != 0 {
		t.Errorf("the pool still holds %d connection(s) after a FAILED acquisition: the acquisition budget ran out on a healthy connection that had been asking for a lock somebody else holds, so its state is only known clean because this code makes it so", open)
	}

	// Exactly one granted advisory lock — the holder's. Two would mean the failed
	// acquisition actually took one and nothing released it.
	var granted int
	probe, cancelProbe := context.WithTimeout(ctx, 10*time.Second)
	defer cancelProbe()
	if err := holder.QueryRowContext(probe, `
SELECT count(*)
FROM pg_catalog.pg_locks l
JOIN pg_catalog.pg_database d ON d.oid = l.database
WHERE l.locktype = 'advisory'
  AND l.granted
  AND d.datname = pg_catalog.current_database()`).Scan(&granted); err != nil {
		t.Fatalf("read advisory locks: %v", err)
	}
	if granted != 1 {
		t.Errorf("expected exactly the holder's advisory lock to be granted, saw %d", granted)
	}
}
