// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"time"
)

// ErrMigrationCoordinationTimeout is returned when the acquisition budget for the
// cluster-wide migration lock runs out. It carries the attribution gathered on the
// way, because "boot timed out" without naming who held the lock is a diagnosis an
// operator cannot act on.
var ErrMigrationCoordinationTimeout = errors.New("sqlstore: migration coordination lock not acquired within its budget")

const (
	// migrationLockKeyExpr is the advisory key, spelled once. Every catalog call is
	// qualified to pg_catalog: an unqualified call resolves through a search_path
	// an untrusted role can influence, and shadowing a builtin that runs on the
	// migration connection is the highest-value target in this file.
	migrationLockKeyExpr = `pg_catalog.hashtextextended('olivares.migrate.v1', 0)`

	// coordinationBudget bounds acquisition of the coordination lock. It is
	// separate from, and much shorter than, the per-unit DDL budget: waiting for
	// another NODE to finish migrating is a different situation from waiting for a
	// long-running writer to release a table, and conflating them would make one
	// of the two wrong.
	coordinationBudget = 5 * time.Minute

	coordinationBackoffMax = 5 * time.Second
)

// coordinationBackoffBase is the floor of the poll interval. The poll is cheap — one try plus one
// catalog read — so the floor is short enough to acquire promptly when the holder finishes, while
// coordinationBackoffMax keeps a long wait from hammering pg_locks.
//
// IT IS A VAR FOR THE SAME REASON guardCloseLockTimeout IS ONE: a regression that PLACES a budget
// below one backoff step — not because anything requires that ordering, but so the clamp is the
// path it exercises — would otherwise be forced under 200ms, and the budget ALSO bounds every
// roundtrip (lockBudget.context derives that bound from the remaining duration). A test on the
// real clock was therefore left with under 200ms for a PostgreSQL roundtrip on a host where one
// has been measured over 250ms — and it duly failed a full-package run while passing five times
// in isolation. Scaling both is what keeps the arithmetic the test's and the scheduling the
// host's.
//
// A budget that drives its own clock can now pair a timer with it (budgetTimer) and take the
// host out of the answer entirely. These acquisition regressions stay on the real pair on
// purpose: what they measure IS the real roundtrip.
var coordinationBackoffBase = 200 * time.Millisecond

// newCoordinationBudget builds the acquisition budget for the coordination lock.
//
// It is a variable, not a call, so a test can shrink a five-minute wait to
// milliseconds. The alternative — threading a budget through Open's signature
// purely for tests — would put a test concern in a production API, and the
// alternative to THAT is a test that really waits five minutes, which is a test
// nobody runs.
var newCoordinationBudget = func() *lockBudget {
	return newLockBudget(coordinationBudget, time.Now, sleepCtx, jitterFloat)
}

// lockAttempt is one poll of the coordination lock, emitted whether it succeeded
// or not. The observer of the DDL phase is built on this stream, which is why the
// waiter PID travels with every attempt rather than being read once elsewhere.
type lockAttempt struct {
	// N counts from 1.
	N int
	// WaiterPID is this connection's backend PID, captured BEFORE the first
	// attempt. It is what a second connection needs to ask pg_blocking_pids about
	// while this one is blocked, and it cannot be obtained from a blocked session.
	WaiterPID int
	// Acquired reports whether this attempt took the lock.
	Acquired bool
	// Holders is who held it at this attempt, empty when it was acquired or when
	// attribution was unavailable. Attribution being empty is NOT the same as
	// nobody holding it: a holder can release between the failed try and the read.
	Holders []lockHolder
	// Remaining is the budget left after this attempt.
	Remaining time.Duration
}

// attemptSink receives every attempt. It must tolerate being called from the
// acquisition path with no error return: observation must never be able to fail
// the boot it is observing.
//
// The context is bounded by the acquisition budget, and honoring it is COOPERATIVE.
// That word is the contract, and it is stated plainly because the alternative was to
// keep claiming a guarantee this code does not provide.
//
// emit calls the sink synchronously and cannot interrupt it. A sink that ignores its
// context therefore CAN outlive the budget — measured: a sink given 10ms slept 80ms
// and the caller waited. So the hard deadline covers the work against the DATABASE,
// and observation is explicitly outside it.
//
// The two alternatives were weighed and rejected for stated reasons. A goroutine per
// emission leaks one per poll per boot when the handler is the thing that is stuck,
// turning a logging problem into unbounded growth precisely under the contention it
// is trying to report. A bounded queue with a single worker keeps the deadline but
// adds a drop policy and its own failure modes to a boot path, to defend against a
// sink this package writes itself, whose body is one slog call. If the log
// destination can block forever, no in-process arrangement gives both a hard time
// bound and no lost lines; pretending otherwise is the thing to avoid.
type attemptSink func(context.Context, lockAttempt)

// acquireCoordinationLock takes the cluster-wide migration lock by POLLING rather
// than blocking, and returns the number of attempts it took.
//
// pg_advisory_lock would block the connection until it won, which costs the two
// things this design needs most: the session cannot answer any question while
// blocked, so it cannot attribute its own wait, and the wait cannot be bounded by
// anything but lock_timeout, which restarts per acquisition and so cannot express
// a hard deadline. pg_try_advisory_lock returns immediately, which leaves the
// connection free to name the holder between attempts and leaves the deadline
// where it can actually be enforced: outside the database, on a monotonic clock.
//
// The cost is real and declared: polling gives up FIFO fairness. Under sustained
// contention a poller can lose to a queue it never joins. That is acceptable for a
// fail-closed boot with a hard deadline — and it is the same trade the corrected
// D3 algorithm already accepted when it chose bounded attempts over one unbounded
// wait — but it is a trade, not a free win.
func acquireCoordinationLock(ctx context.Context, conn *sql.Conn, b *lockBudget, sink attemptSink) (int, error) {
	// Even the PID read is bounded by the budget. It is one roundtrip, but it is a
	// roundtrip on a connection that may already be in trouble, and a boot that hung
	// here would hang before it ever had a deadline to enforce.
	pctx, pcancel := b.context(ctx)
	var waiterPID int
	perr := conn.QueryRowContext(pctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&waiterPID)
	pcancel()
	if perr != nil {
		if budgetSpent(ctx, b, perr) {
			return 0, fmt.Errorf("%w before the first attempt (%v): %s",
				ErrMigrationCoordinationTimeout, perr, describeHolders(nil))
		}
		return 0, fmt.Errorf("sqlstore: read migration connection pid: %w", perr)
	}

	var last []lockHolder
	for attempt := 1; ; attempt++ {
		// EXPIRY IS CHECKED HERE, at the top, and that placement is the fix rather
		// than a tidy-up. The loop reaches this point two ways: on entry, and after a
		// backoff that may have consumed exactly the remainder. Without this check a
		// wait that ended precisely at the deadline was followed by one more
		// pg_try_advisory_lock — which, if the holder had released during that final
		// sleep, ACQUIRED the lock and returned success after the deadline had passed.
		if b.expired() {
			return attempt - 1, fmt.Errorf("%w after %d attempts: %s",
				ErrMigrationCoordinationTimeout, attempt-1, describeHolders(last))
		}

		actx, acancel := b.context(ctx)
		var acquired bool
		terr := conn.QueryRowContext(actx,
			"SELECT pg_catalog.pg_try_advisory_lock("+migrationLockKeyExpr+")").Scan(&acquired)
		acancel()
		if terr != nil {
			if budgetSpent(ctx, b, terr) {
				return attempt, fmt.Errorf("%w after %d attempts (%v): %s",
					ErrMigrationCoordinationTimeout, attempt, terr, describeHolders(last))
			}
			return attempt, fmt.Errorf("sqlstore: try migration lock: %w", terr)
		}
		if acquired {
			// A THIRD check, and it is not redundant with the two above. Those cover
			// starting an operation with no budget and bounding one in flight; this
			// covers an operation that STARTED inside the budget and RETURNED outside
			// it. The contrast demonstrated it with an injected clock: the try came
			// back granted with the budget already spent, and acquisition returned
			// success.
			//
			// Refusing here leaves the lock held on this connection, and that is safe
			// rather than sloppy: the caller marks the acquisition as attempted BEFORE
			// calling, and its defer attempts a bounded unlock and then physically
			// discards the session on every path (store.go). A session that is retired
			// cannot carry a stray advisory lock back into the pool.
			if b.expired() {
				return attempt, fmt.Errorf("%w after %d attempts: the lock was granted after the deadline had passed and has been refused: %s",
					ErrMigrationCoordinationTimeout, attempt, describeHolders(last))
			}
			emit(ctx, b, sink, lockAttempt{N: attempt, WaiterPID: waiterPID, Acquired: true, Remaining: b.remaining()})
			return attempt, nil
		}

		// Not blocked, so attribution happens HERE, on this same connection, while
		// the information is still true. After a blocking wait fails there is
		// nothing left to ask: the request is gone and pg_blocking_pids returns
		// empty for a session that is no longer waiting.
		hctx, hcancel := b.context(ctx)
		holders, herr := migrationLockHolders(hctx, conn)
		hcancel()
		if herr != nil {
			// Attribution is best-effort by contract. Losing it degrades the
			// diagnosis, never the boot.
			holders = nil
		}
		last = holders
		emit(ctx, b, sink, lockAttempt{N: attempt, WaiterPID: waiterPID, Holders: holders, Remaining: b.remaining()})

		waited, err := b.backoff(ctx, attempt, coordinationBackoffBase, coordinationBackoffMax)
		if err != nil {
			return attempt, err
		}
		if !waited {
			return attempt, fmt.Errorf("%w after %d attempts: %s",
				ErrMigrationCoordinationTimeout, attempt, describeHolders(last))
		}
	}
}

// emit hands an attempt to the sink under the budget's own deadline.
//
// "Under" means the sink is TOLD the bound, not held to it: the call is synchronous
// and a sink that ignores its context outlives the budget. See attemptSink for why
// that is the declared contract rather than a gap.
func emit(ctx context.Context, b *lockBudget, sink attemptSink, a lockAttempt) {
	if sink == nil {
		return
	}
	sctx, cancel := b.context(ctx)
	defer cancel()
	sink(sctx, a)
}

// budgetSpent reports whether a failure is this budget's deadline rather than the
// caller's cancellation.
//
// The distinction is the whole reason the derived context exists: one means "the
// operator asked us to stop" and the other "we ran out of the time we allotted
// ourselves", and only the second is a coordination timeout an operator should see
// with attribution attached.
//
// It does NOT decide on the error's shape alone, and that is a correction. A
// statement issued on an already-expired context does not necessarily come back as a
// context error at all: pgx marks the connection unusable and returns
// driver.ErrBadConn. Measured — an acquisition entered with a spent budget reported
// `read migration connection pid: driver: bad connection`, and the timeout with its
// attribution was thrown away. The budget's own clock is the reliable signal; the
// error shape is only a fast path.
func budgetSpent(parent context.Context, b *lockBudget, err error) bool {
	if parent.Err() != nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return b.expired()
}

// describeHolders renders attribution for an error message, and says plainly when
// there is none. "No holder was attributed" and "nobody held it" are different
// facts, and an error that blurs them sends an operator looking for the wrong
// thing.
func describeHolders(hs []lockHolder) string {
	if len(hs) == 0 {
		return "no holder could be attributed (the lock may have changed hands between the failed attempt and the read)"
	}
	out := "held by"
	for _, h := range hs {
		out += " [" + h.String() + "]"
	}
	return out
}

// migrationLockHolders names the sessions currently holding the coordination lock
// in THIS database.
//
// THE KEY SPACE IS PART OF THE IDENTITY. pg_locks stores a 64-bit advisory key as
// classid (high 32 bits), objid (low 32 bits) and objsubid = 1; the TWO-INTEGER
// variant of the same functions stores its two arguments the same way but with
// objsubid = 2. PostgreSQL documents the two key spaces as non-overlapping, and the
// only thing in the catalog that distinguishes them is objsubid. Filtering without
// it means a session holding the (int,int) lock whose arguments happen to be our
// high and low halves is attributed as if it held ours. Measured on 15.18 with both
// locks taken at once, the predicate without objsubid returned 2 rows for 1 lock.
//
// That is not a cosmetic over-count. The failed try and this read are separate
// statements, so the real holder can release in between; a confident, wrong
// attribution then names a session that has nothing to do with the wait.
//
// BOTH halves of the key are masked to 32 bits before the comparison. hashtextextended
// returns a SIGNED bigint and this particular key is negative (measured:
// -8165820779145242747), so an arithmetic shift sign-extends and `(key >> 32)::int`
// yields -1901253308 where pg_locks stores the oid 2393713988. Those are the same 32
// bits, and PostgreSQL does match them — comparing oid with int reinterprets rather
// than rejects — so the unmasked form works by an implicit coercion instead of by
// saying what it means. The masking was NOT the fix for anything: the contrast probe
// confirmed the unmasked comparison matched the real row. It stays because it makes
// the arithmetic explicit and stops the filter depending on a coercion nobody wrote
// down.
//
// Scoped to the current database because pg_locks is cluster-wide: an unscoped read
// would attribute an unrelated advisory lock in another database.
//
// Columns of other sessions in pg_stat_activity come back NULL without
// pg_read_all_stats. They are carried as NULL — see lockHolder for why coalescing
// them lost every attribution under the restricted role.
func migrationLockHolders(ctx context.Context, conn *sql.Conn) ([]lockHolder, error) {
	const q = `
SELECT l.pid,
       ` + holderColumns + `
FROM pg_catalog.pg_locks l
LEFT JOIN pg_catalog.pg_stat_activity a ON a.pid = l.pid
JOIN pg_catalog.pg_database d ON d.oid = l.database
WHERE l.locktype = 'advisory'
  AND l.granted
  AND d.datname = pg_catalog.current_database()
  AND l.classid = ((` + migrationLockKeyExpr + ` >> 32) & 4294967295)::oid
  AND l.objid = (` + migrationLockKeyExpr + ` & 4294967295)::oid
  AND l.objsubid = 1
ORDER BY l.pid`

	rows, err := conn.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // error surfaced via rows.Err below
	return scanHolders(rows)
}

// sleepCtx waits for d, or returns early when the caller gives up. A plain
// time.Sleep in a boot path makes an operator's cancellation wait for a backoff
// they asked to abandon.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// jitterFloat is the production jitter source, in [0,1).
//
// Deliberately NOT cryptographic: this decorrelates boot retries, it is not a
// secret, and a crypto source here would add a failure mode (entropy stalls at
// boot on constrained hosts) to buy a property nobody needs.
// The suppression is gosec's own `#nosec`, NOT golangci-lint's `//nolint:gosec`, and
// the difference is load-bearing: the blocking SAST gate runs gosec directly
// (scripts/sast.sh), and gosec does not read `//nolint:`. This line carried only the
// golangci-lint form and therefore sat RED on `main` — the gate said so on every run,
// but no run ever completed, so nobody read it.
func jitterFloat() float64 { return mrand.Float64() } // #nosec G404 -- decorrelation, not secrecy

// logLockAttempt is the production sink: it says nothing while the first attempt
// succeeds, which is the overwhelmingly common case, and names the holder once a
// wait actually starts.
//
// The threshold is deliberate. Logging every poll of a five-minute wait would
// bury the one line that matters, and logging none of them leaves an operator
// watching a boot that appears hung with no way to learn who is holding it.
func logLockAttempt(_ context.Context, a lockAttempt) {
	switch {
	case a.Acquired && a.N == 1:
		// The normal path. Silence is the correct amount of noise.
	case a.Acquired:
		slog.Info("migration coordination lock acquired",
			"attempts", a.N, "waiter_pid", a.WaiterPID)
	case a.N == 1 || a.N%10 == 0:
		slog.Warn("waiting for the migration coordination lock; another node is migrating",
			"attempt", a.N, "waiter_pid", a.WaiterPID,
			"remaining", a.Remaining.Round(time.Second).String(),
			"holders", describeHolders(a.Holders))
	}
}
