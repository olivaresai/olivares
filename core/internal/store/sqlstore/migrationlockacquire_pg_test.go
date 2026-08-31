// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/store"
)

// TestCoordinationLockAttributesTheHolderWhileWaiting is the D3 property that a
// blocking acquisition cannot have.
//
// pg_advisory_lock blocks the connection until it wins, and a blocked session
// cannot answer questions — including the question of who is blocking it. After
// the wait fails there is nothing left to ask either: the request is gone, and
// pg_blocking_pids returns empty for a session that is no longer waiting. That is
// exactly what the refutation of the original D3 measured, and why it concluded
// post-hoc attribution was impossible.
//
// Polling with pg_try_advisory_lock dissolves that: the connection is never
// blocked, so it can name the holder BETWEEN attempts, on itself, while the
// information is still true.
//
// Mutation that must turn this red: stop populating Holders on a failed attempt.
func TestCoordinationLockAttributesTheHolderWhileWaiting(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	holder, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open holder pool: %v", err)
	}
	defer holder.Close() //nolint:errcheck // test teardown
	hconn, err := holder.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer hconn.Close() //nolint:errcheck // test teardown

	var holderPID int
	if err := hconn.QueryRowContext(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&holderPID); err != nil {
		t.Fatalf("holder pid: %v", err)
	}
	if _, err := hconn.ExecContext(ctx,
		"SELECT pg_catalog.pg_try_advisory_lock("+migrationLockKeyExpr+")"); err != nil {
		t.Fatalf("holder acquire: %v", err)
	}

	waiter, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open waiter pool: %v", err)
	}
	defer waiter.Close() //nolint:errcheck // test teardown
	wconn, err := waiter.Conn(ctx)
	if err != nil {
		t.Fatalf("waiter conn: %v", err)
	}
	defer wconn.Close() //nolint:errcheck // test teardown

	var seen []lockAttempt
	b := newLockBudget(400*time.Millisecond, time.Now, sleepCtx, jitterFloat)
	_, err = acquireCoordinationLock(ctx, wconn, b, func(_ context.Context, a lockAttempt) { seen = append(seen, a) })
	if !errors.Is(err, ErrMigrationCoordinationTimeout) {
		t.Fatalf("acquisition = %v, want ErrMigrationCoordinationTimeout against a held key", err)
	}
	if len(seen) == 0 {
		t.Fatal("no attempt was emitted; the observer of the DDL phase is built on this stream")
	}

	var attributed bool
	for _, a := range seen {
		if a.WaiterPID == 0 {
			t.Error("an attempt carried no waiter PID: a second connection cannot ask pg_blocking_pids about a session it cannot name, and a blocked session cannot report its own")
		}
		for _, h := range a.Holders {
			if h.PID.Valid && h.PID.Int64 == int64(holderPID) {
				attributed = true
			}
		}
	}
	if !attributed {
		t.Errorf("no attempt named the holder (pid %d) across %d attempts: attribution while waiting is the whole reason acquisition polls instead of blocking", holderPID, len(seen))
	}

	// The error text must carry the attribution too. "Boot timed out" without a
	// holder is a diagnosis an operator cannot act on.
	if !strings.Contains(err.Error(), "held by") {
		t.Errorf("the timeout error does not name the holder: %v", err)
	}
}

// TestCoordinationLockNeverAcquiresAfterItsDeadline is the decisive regression for a
// deadline that was not hard.
//
// The loop used to return straight to its head after a backoff and issue another
// pg_try_advisory_lock without re-checking expiry. The wait can legitimately consume
// exactly the remainder — clamping it to the remaining budget is what makes it safe
// — so there was always a case where the next attempt happened after the deadline.
// And if the holder released during that final sleep, that attempt SUCCEEDED: the
// boot took the coordination lock and reported success, out of budget, having
// promised a bounded wait.
//
// This drives exactly that sequence: the sleeper releases the holder mid-wait and
// consumes the whole remaining budget. A correct implementation must fail closed
// with the budget error even though the lock is, at that instant, free for the
// taking.
//
// TWO barriers now stop it, and the measurements separate them, because a test that
// conflates them would go green after one of the two was lost:
//
//   - removing ONLY the b.expired() check: the late attempt HAPPENS — measured, the run reports
//     two attempts where one is correct — and it does not acquire, because the budget-derived
//     context is already canceled. So what this test catches is the extra ATTEMPT. An assertion
//     on the returned error alone would not have caught it. What error it returns is NOT recorded
//     here: an earlier version predicted an opaque `context already done`, the contrast ran the
//     mutation and got something else, and a prediction nobody re-measured is worth less than
//     saying plainly that the attempt count is what this test reads;
//   - removing BOTH it and the derived context, which is the shape this replaced: acquisition
//     returns nil. Measured. The boot takes the lock past its deadline and reports success.
//
// (Two consecutive blocks here used to give incompatible outcomes for the SAME mutation, one
// written from the error and one from the attempt count. They are one block now, and both facts
// are in it, because a header that contradicts itself teaches the next reader nothing.)
//
// Mutation that must turn this red: remove the b.expired() check at the top of the acquisition
// loop.
func TestCoordinationLockNeverAcquiresAfterItsDeadline(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	holder, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open holder pool: %v", err)
	}
	defer holder.Close() //nolint:errcheck // test teardown
	hconn, err := holder.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer hconn.Close() //nolint:errcheck // test teardown
	var took bool
	if err := hconn.QueryRowContext(ctx,
		"SELECT pg_catalog.pg_try_advisory_lock("+migrationLockKeyExpr+")").Scan(&took); err != nil {
		t.Fatalf("holder acquire: %v", err)
	}
	if !took {
		t.Fatal("could not take the coordination key in a private database")
	}

	waiter, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open waiter pool: %v", err)
	}
	defer waiter.Close() //nolint:errcheck // test teardown
	wconn, err := waiter.Conn(ctx)
	if err != nil {
		t.Fatalf("waiter conn: %v", err)
	}
	defer wconn.Close() //nolint:errcheck // test teardown

	// THE WAIT LANDS ON THE DEADLINE BECAUSE THIS TEST PUTS IT THERE, not because jitter
	// happened to sample 1.
	//
	// Round eleven pointed out that pinning jitter at 1 feeds lockBudget an input its own
	// contract excludes — it documents [0,1). Replacing it with the largest legal value
	// turned the test RED with "reported 2 attempts, want 1", which is worse news than the
	// finding: the property depended on that illegal input to land exactly on the deadline,
	// so the fixture only worked because it was cheating.
	//
	// The state this test is about is "the wait consumed the remainder". The sleeper below
	// sets that state outright, so the arithmetic is the test's, the jitter stays inside its
	// contract, and the property no longer rides on a sampler value production cannot emit.
	const budgetTotal = 5 * time.Second
	budgetDeadline := time.Unix(0, 0).Add(budgetTotal)
	// BOTH CEILINGS SCALED, and the RATIO is what the property needs — not the absolute size.
	//
	// The budget has to be shorter than one backoff step so the wait is clamped to exactly what
	// remains and lands on the deadline. At the production floor of 200ms that forced a budget of
	// 50ms — and a budget is ALSO the real deadline each roundtrip gets, because lockBudget.context
	// derives it from the remaining duration and not from the injected clock. So this fixture had
	// under 50ms of real time to reach its first sleep. Measured: red in a full-package run at
	// loadavg 21 with "the sleeper never ran", green five times in isolation. Scaling both by 100
	// keeps budget < step and gives every roundtrip five real seconds.
	oldBase := coordinationBackoffBase
	coordinationBackoffBase = 20 * time.Second
	t.Cleanup(func() { coordinationBackoffBase = oldBase })
	c := &fakeClock{t: time.Unix(0, 0)}
	var releasedDuringSleep bool
	sleeper := func(_ context.Context, d time.Duration) error {
		// The holder goes away mid-wait. From here on a try would SUCCEED, which is
		// what makes an unchecked extra attempt an acquisition rather than a retry.
		if !releasedDuringSleep {
			var freed bool
			if err := hconn.QueryRowContext(ctx,
				"SELECT pg_catalog.pg_advisory_unlock("+migrationLockKeyExpr+")").Scan(&freed); err != nil {
				t.Errorf("release the holder mid-sleep: %v", err)
			} else if !freed {
				t.Error("the holder did not actually own the lock it was releasing; this test would be vacuous")
			}
			releasedDuringSleep = true
		}
		c.t = c.t.Add(d)
		// AND AT LEAST TO THE DEADLINE. Production's clock would be there after a wait
		// clamped to the remainder; making it explicit is what frees the jitter.
		if c.t.Before(budgetDeadline) {
			c.t = budgetDeadline
		}
		return nil
	}

	var seen []lockAttempt
	b := newLockBudget(budgetTotal, c.now, sleeper, func() float64 { return math.Nextafter(1, 0) })
	attempts, err := acquireCoordinationLock(ctx, wconn, b, func(_ context.Context, a lockAttempt) {
		seen = append(seen, a)
	})
	if !releasedDuringSleep {
		t.Fatal("the sleeper never ran, so the race this test exists for was never set up")
	}
	if !errors.Is(err, ErrMigrationCoordinationTimeout) {
		t.Fatalf("acquisition returned %v after its budget was spent; the lock was free by then, so an unchecked attempt would have TAKEN it and reported a success the caller was promised could not happen", err)
	}
	if attempts != 1 {
		t.Errorf("reported %d attempts, want 1: the attempt after the deadline must not happen at all", attempts)
	}
	for _, a := range seen {
		if a.Acquired {
			t.Errorf("attempt %d acquired the lock after the deadline had passed", a.N)
		}
	}
	if len(seen) != 1 {
		t.Errorf("emitted %d attempts, want 1: a second poll was issued past the deadline", len(seen))
	}

	// And nothing may hold the key now: the holder released it, and the waiter must
	// not have picked it up on the way out.
	obs, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open observer pool: %v", err)
	}
	defer obs.Close() //nolint:errcheck // test teardown
	oconn, err := obs.Conn(ctx)
	if err != nil {
		t.Fatalf("observer conn: %v", err)
	}
	defer oconn.Close() //nolint:errcheck // test teardown
	holders, err := migrationLockHolders(ctx, oconn)
	if err != nil {
		t.Fatalf("read holders: %v", err)
	}
	if len(holders) != 0 {
		t.Errorf("the coordination key is held by %v after a run that timed out: the acquisition succeeded past its own deadline", holders)
	}
}

// TestCoordinationLockRedactsTheHolderQuery pins the boundary that makes this
// safe to log. pg_stat_activity exposes the running query text, that text can
// carry tenant data, and this ends up in a boot log an operator pastes into a
// ticket. The holder view must name a session without carrying its payload.
//
// Mutation that must turn this red: add the query column to the holder query and
// render it in String().
func TestCoordinationLockRedactsTheHolderQuery(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	// A marker that would be visible in pg_stat_activity.query if it were read.
	const marker = "olv_probe_secret_marker_do_not_log"

	holder, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open holder pool: %v", err)
	}
	defer holder.Close() //nolint:errcheck // test teardown
	hconn, err := holder.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer hconn.Close() //nolint:errcheck // test teardown
	if _, err := hconn.ExecContext(ctx,
		"SELECT pg_catalog.pg_try_advisory_lock("+migrationLockKeyExpr+") /* "+marker+" */"); err != nil {
		t.Fatalf("holder acquire: %v", err)
	}

	obs, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open observer pool: %v", err)
	}
	defer obs.Close() //nolint:errcheck // test teardown
	oconn, err := obs.Conn(ctx)
	if err != nil {
		t.Fatalf("observer conn: %v", err)
	}
	defer oconn.Close() //nolint:errcheck // test teardown

	holders, err := migrationLockHolders(ctx, oconn)
	if err != nil {
		t.Fatalf("read holders: %v", err)
	}
	if len(holders) == 0 {
		t.Fatal("the holder was not visible at all; this test would be vacuous")
	}
	for _, h := range holders {
		if strings.Contains(h.String(), marker) {
			t.Errorf("the rendered holder carries the query text: %s", h.String())
		}
	}
	if strings.Contains(describeHolders(holders), marker) {
		t.Error("describeHolders carries the query text into the error an operator will paste into a ticket")
	}
}

// TestCoordinationLockLogRedactsAndNormalisesUntrustedMetadata is the redaction
// regression taken END TO END: through the real production sink, into a real slog
// handler, with an adversarial application_name.
//
// Checking String() alone was not enough. String() is one of several things that can
// put a holder into a log record, and the sink is where the decision actually
// happens — a warning that logged the holder struct itself, or added the query
// column, would have left the String() test perfectly green.
//
// application_name is attacker-chosen: any client may set it, and it lands in an
// operator's terminal. PostgreSQL does sanitize it (measured on 15.18: TAB, newline
// and U+202E all became '?', truncated at 63 characters), but this process reads it
// across a version boundary and possibly a pooler, so the policy is applied here
// rather than assumed there.
func TestCoordinationLockLogRedactsAndNormalisesUntrustedMetadata(t *testing.T) {
	captureLog := func(t *testing.T, fn func()) string {
		t.Helper()
		var buf bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
		t.Cleanup(func() { slog.SetDefault(prev) })
		fn()
		return buf.String()
	}

	// --- Leg 1: end to end, against a real server -------------------------------
	//
	// The marker only proves something when it can ONLY have come from
	// pg_stat_activity.query, so it goes in the holder's actual statement text and
	// the whole path runs for real: read the holders, hand them to the production
	// sink, capture what the handler emitted.
	t.Run("no statement text reaches the handler", func(t *testing.T) {
		const queryMarker = "olv_query_text_must_never_be_logged"
		dsns := isolatedPG(t)
		ctx := context.Background()

		holderDB, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
		if err != nil {
			t.Fatalf("open holder pool: %v", err)
		}
		defer holderDB.Close() //nolint:errcheck // test teardown
		hconn, err := holderDB.Conn(ctx)
		if err != nil {
			t.Fatalf("holder conn: %v", err)
		}
		defer hconn.Close() //nolint:errcheck // test teardown
		if _, err := hconn.ExecContext(ctx,
			"SELECT pg_catalog.pg_try_advisory_lock("+migrationLockKeyExpr+") /* "+queryMarker+" */"); err != nil {
			t.Fatalf("holder acquire: %v", err)
		}

		obsDB, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
		if err != nil {
			t.Fatalf("open observer pool: %v", err)
		}
		defer obsDB.Close() //nolint:errcheck // test teardown
		oconn, err := obsDB.Conn(ctx)
		if err != nil {
			t.Fatalf("observer conn: %v", err)
		}
		defer oconn.Close() //nolint:errcheck // test teardown

		holders, err := migrationLockHolders(ctx, oconn)
		if err != nil {
			t.Fatalf("read holders: %v", err)
		}
		if len(holders) == 0 {
			t.Fatal("the holder was not visible at all; this leg would be vacuous")
		}

		got := captureLog(t, func() {
			logLockAttempt(ctx, lockAttempt{N: 1, WaiterPID: 99, Holders: holders, Remaining: time.Minute})
		})
		if got == "" {
			t.Fatal("the first waiting attempt logged nothing; an operator watching an apparently hung boot has no way to learn who holds the lock")
		}
		if strings.Contains(got, queryMarker) {
			t.Errorf("the emitted log record carries text from a holder's statement: %s", got)
		}
	})

	// --- Leg 2: the content policy itself ---------------------------------------
	//
	// Built in Go rather than through the server ON PURPOSE. PostgreSQL 15.18 does
	// sanitize application_name (measured: TAB, newline and U+202E all became '?',
	// truncated at 63 characters), so a server-fed value could never exercise the
	// policy. The value crosses a version boundary and possibly a pooler before it
	// gets here, and it lands in an operator's terminal, so the guarantee has to hold
	// on this side too.
	t.Run("hostile session metadata is normalised and bounded", func(t *testing.T) {
		hostile := "app\tname\nline‮reversed" + strings.Repeat("x", 200)
		h := lockHolder{
			PID:             sql.NullInt64{Int64: 4242, Valid: true},
			ApplicationName: sql.NullString{String: hostile, Valid: true},
			State:           sql.NullString{String: "idle", Valid: true},
		}

		got := captureLog(t, func() {
			logLockAttempt(context.Background(), lockAttempt{N: 1, WaiterPID: 99, Holders: []lockHolder{h}, Remaining: time.Minute})
		})
		if strings.ContainsAny(got, "\t\r") || strings.Contains(got, "‮") {
			t.Errorf("the log line carries raw control characters from attacker-chosen metadata; a bidirectional override renders as nothing while reversing the text after it, so a line can be made to read as something it does not say: %q", got)
		}
		if !strings.Contains(got, "truncated") {
			t.Errorf("a 200-character application_name was copied into the log without a bound or a truncation marker: %s", got)
		}
		if !strings.Contains(got, "4242") {
			t.Errorf("the log lost the holder PID, which is the one field that is always readable and the only one an operator can act on: %s", got)
		}
		// Unreadable columns must be named as unreadable rather than rendered as empty.
		if !strings.Contains(got, "wait_event="+holderUnavailable) {
			t.Errorf("a NULL wait_event was not reported as unavailable, so 'I could not see this' is indistinguishable from 'this was empty': %s", got)
		}
	})
}

// TestCoordinationLockSaysWhenItCouldNotAttribute pins an honesty boundary that
// is easy to lose. "No holder was attributed" and "nobody held it" are different
// facts: a holder can release between the failed attempt and the read. An error
// that renders the first as the second sends an operator looking for a problem
// that has already gone.
func TestCoordinationLockSaysWhenItCouldNotAttribute(t *testing.T) {
	t.Parallel()
	got := describeHolders(nil)
	if !strings.Contains(got, "could be attributed") {
		t.Errorf("empty attribution renders as %q, which reads as a claim that nobody held the lock", got)
	}
}

// TestCoordinationLockGivesTheSinkABoundedContext pins ONLY what its name says, and
// the narrowing is a correction.
//
// It used to be called ...BoundsEveryRoundtripAndTheSink, and it did not: the
// contrast mutated the derived context on the PID read and this test stayed green,
// because it never instrumented the PID, try or holder contexts at all. A test that
// claims more than it checks is worse than a missing test — it is a missing test that
// reads as present.
//
// What it does check is real and load-bearing: the sink is TOLD the budget's bound.
// Honoring it is cooperative by declared contract (see attemptSink), so this asserts
// the handover, not an enforcement that does not exist. The roundtrip bounding is
// covered separately by TestCoordinationLockReportsASpentBudgetAsItsOwnTimeout.
//
// Mutation that must turn this red: pass the caller's ctx to the sink instead of a
// budget-derived one.
func TestCoordinationLockGivesTheSinkABoundedContext(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	holder, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open holder pool: %v", err)
	}
	defer holder.Close() //nolint:errcheck // test teardown
	hconn, err := holder.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer hconn.Close() //nolint:errcheck // test teardown
	var took bool
	if err := hconn.QueryRowContext(ctx,
		"SELECT pg_catalog.pg_try_advisory_lock("+migrationLockKeyExpr+")").Scan(&took); err != nil {
		t.Fatalf("holder acquire: %v", err)
	}
	if !took {
		t.Fatal("could not take the coordination key in a private database")
	}

	waiter, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open waiter pool: %v", err)
	}
	defer waiter.Close() //nolint:errcheck // test teardown
	wconn, err := waiter.Conn(ctx)
	if err != nil {
		t.Fatalf("waiter conn: %v", err)
	}
	defer wconn.Close() //nolint:errcheck // test teardown

	// The caller's context is deliberately unbounded — that is the production shape,
	// and it is what makes an unbounded roundtrip invisible.
	var sinkDeadlines []time.Duration
	var sawBudgetDeadline bool
	b := newLockBudget(300*time.Millisecond, time.Now, sleepCtx, jitterFloat)
	_, err = acquireCoordinationLock(ctx, wconn, b, func(sctx context.Context, _ lockAttempt) {
		dl, ok := sctx.Deadline()
		if !ok {
			t.Error("the sink was handed a context with NO deadline: observation runs inside the wait it describes, so a sink that blocks spends the budget it exists to report on")
			return
		}
		sawBudgetDeadline = true
		sinkDeadlines = append(sinkDeadlines, time.Until(dl))
	})
	if !errors.Is(err, ErrMigrationCoordinationTimeout) {
		t.Fatalf("acquisition = %v, want a coordination timeout against a held key", err)
	}
	if !sawBudgetDeadline {
		t.Fatal("the sink never ran, so this test asserted nothing")
	}
	for i, d := range sinkDeadlines {
		if d > 300*time.Millisecond {
			t.Errorf("sink call %d was given %v, more than the whole 300ms budget: its bound is not derived from the budget", i, d)
		}
	}
}

// TestCoordinationLockIgnoresTheOtherAdvisoryKeySpace pins that attribution
// identifies a lock by its KEY SPACE as well as its bits.
//
// PostgreSQL stores a 64-bit advisory key as classid/objid with objsubid = 1, and
// the two-integer variant of the same functions as classid/objid with objsubid = 2.
// The two key spaces are documented as non-overlapping and objsubid is the ONLY
// thing in the catalog that separates them. A filter without it attributes a session
// holding pg_try_advisory_lock(hi, lo) as if it held ours, whenever hi and lo happen
// to be our key's halves — which is not a coincidence anyone has to arrange, it is
// what the same key looks like taken the other way.
//
// It matters because the failed try and this read are separate statements: the real
// holder can release in between, and the answer an operator gets is then a confident
// name for a session that has nothing to do with their wait.
//
// Mutation that must turn this red: drop `AND l.objsubid = 1`.
func TestCoordinationLockIgnoresTheOtherAdvisoryKeySpace(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	conn := func(name string) *sql.Conn {
		t.Helper()
		db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
		if err != nil {
			t.Fatalf("open %s pool: %v", name, err)
		}
		t.Cleanup(func() { _ = db.Close() })
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("%s conn: %v", name, err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	// The real holder, on the bigint key the coordination lock actually uses.
	real := conn("real")
	var realPID int
	if err := real.QueryRowContext(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&realPID); err != nil {
		t.Fatalf("real pid: %v", err)
	}
	var key int64
	if err := real.QueryRowContext(ctx, "SELECT "+migrationLockKeyExpr).Scan(&key); err != nil {
		t.Fatalf("read key: %v", err)
	}
	var took bool
	if err := real.QueryRowContext(ctx,
		"SELECT pg_catalog.pg_try_advisory_lock("+migrationLockKeyExpr+")").Scan(&took); err != nil {
		t.Fatalf("real acquire: %v", err)
	}
	if !took {
		t.Fatal("could not take the coordination key in a private database")
	}

	// The impostor: the SAME 64 bits, taken as two 32-bit integers. Splitting in Go
	// rather than SQL keeps the arithmetic unambiguous — the low half does not fit in
	// a signed int32 for every possible key, and this is a test, not the place to
	// re-derive the cast the production query is being checked against.
	hi := int32(key >> 32)   //nolint:gosec // deliberate 32-bit split of a 64-bit key
	lo := int32(uint32(key)) //nolint:gosec // deliberate 32-bit split of a 64-bit key
	impostor := conn("impostor")
	var impostorPID int
	if err := impostor.QueryRowContext(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&impostorPID); err != nil {
		t.Fatalf("impostor pid: %v", err)
	}
	if err := impostor.QueryRowContext(ctx,
		"SELECT pg_catalog.pg_try_advisory_lock($1::int, $2::int)", hi, lo).Scan(&took); err != nil {
		t.Fatalf("impostor acquire: %v", err)
	}
	if !took {
		t.Fatal("could not take the colliding two-integer key; this test would be vacuous")
	}

	holders, err := migrationLockHolders(ctx, conn("observer"))
	if err != nil {
		t.Fatalf("read holders: %v", err)
	}

	var sawReal, sawImpostor bool
	for _, h := range holders {
		switch {
		case h.PID.Int64 == int64(realPID):
			sawReal = true
		case h.PID.Int64 == int64(impostorPID):
			sawImpostor = true
		}
	}
	if !sawReal {
		t.Errorf("the real holder (pid %d) was not attributed at all", realPID)
	}
	if sawImpostor {
		t.Errorf("a session holding the TWO-INTEGER key (pid %d, hi=%d lo=%d) was attributed as a holder of the coordination lock: the filter does not distinguish the key spaces, so it names sessions that have nothing to do with this wait", impostorPID, hi, lo)
	}
	if len(holders) != 1 {
		t.Errorf("attributed %d holders for one lock: %v", len(holders), holders)
	}
}

// TestCoordinationLockAttributesAHolderOwnedByAnotherRole is the regression for the
// defect that made attribution work in tests and fail in production.
//
// pg_stat_activity blanks the describing columns of OTHER roles' sessions for a role
// without pg_read_all_stats. Measured on 15.18, one NOSUPERUSER role observing
// another: state, wait_event and backend_start all NULL, the row itself present.
// The previous query coalesced backend_start to TIMESTAMPTZ '-infinity' and scanned
// it into a time.Time; pgx hands that sentinel over as a STRING, the scan failed,
// and since attribution is best-effort by contract the caller turned the error into
// "no holder could be attributed". The PID was right there in the row.
//
// The old tests could not see it because holder and observer shared one DSN, so the
// only relationship they ever exercised was a role looking at ITSELF — which is
// exactly the case PostgreSQL does not restrict.
//
// Mutation that must turn this red: coalesce backend_start back to '-infinity'.
func TestCoordinationLockAttributesAHolderOwnedByAnotherRole(t *testing.T) {
	dsns := isolatedPGSplit(t)
	ctx := context.Background()

	openConn := func(dsn, name string) *sql.Conn {
		t.Helper()
		db, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsn, MaxConns: 2})
		if err != nil {
			t.Fatalf("open %s pool: %v", name, err)
		}
		t.Cleanup(func() { _ = db.Close() })
		c, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("%s conn: %v", name, err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	// The holder runs as the OWNER role; the observer as the APPLICATION role. That
	// is the production relationship: the migration connection is not a superuser and
	// has no business being granted pg_read_all_stats to make a log line prettier.
	holder := openConn(dsns.Owner, "holder")
	var holderPID int
	if err := holder.QueryRowContext(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&holderPID); err != nil {
		t.Fatalf("holder pid: %v", err)
	}
	var took bool
	if err := holder.QueryRowContext(ctx,
		"SELECT pg_catalog.pg_try_advisory_lock("+migrationLockKeyExpr+")").Scan(&took); err != nil {
		t.Fatalf("holder acquire: %v", err)
	}
	if !took {
		t.Fatal("could not take the coordination key in a private database")
	}

	observer := openConn(dsns.App, "observer")

	// Establish the premise before asserting on it. If this role could see everything,
	// the test would pass for the wrong reason and go on passing after a regression.
	var restricted bool
	if err := observer.QueryRowContext(ctx,
		"SELECT a.backend_start IS NULL FROM pg_catalog.pg_stat_activity a WHERE a.pid = $1",
		holderPID).Scan(&restricted); err != nil {
		t.Fatalf("premise probe: %v", err)
	}
	if !restricted {
		t.Fatalf("the observing role can read backend_start of a session owned by another role, so this test cannot exercise the restricted path it exists for. Either the roles are no longer distinct or one of them gained pg_read_all_stats; both make the attribution guarantee untested rather than satisfied")
	}

	holders, err := migrationLockHolders(ctx, observer)
	if err != nil {
		t.Fatalf("attribution failed outright under the restricted role — this is the production path, and its failure is silent because the caller discards it as best-effort: %v", err)
	}
	var found *lockHolder
	for i := range holders {
		if holders[i].PID.Valid && holders[i].PID.Int64 == int64(holderPID) {
			found = &holders[i]
		}
	}
	if found == nil {
		t.Fatalf("the holder (pid %d) was not attributed although pg_locks.pid is never privilege-gated; a boot would time out saying nobody could be named", holderPID)
	}
	if found.BackendStart.Valid {
		t.Errorf("backend_start came back valid after the premise probe said it was NULL: %v", found.BackendStart)
	}

	// And the render must say "I could not see this" rather than inventing a value.
	rendered := found.String()
	if !strings.Contains(rendered, "since="+holderUnavailable) {
		t.Errorf("an unreadable backend_start rendered as %q; it must name the column as unavailable rather than print a sentinel date an operator would try to interpret", rendered)
	}
	if strings.Contains(rendered, "1970") || strings.Contains(rendered, "0001-01-01") {
		t.Errorf("the rendered holder carries a zero-value timestamp, which reads as a real fact about the holder: %s", rendered)
	}
	if !strings.Contains(describeHolders(holders), "pid=") {
		t.Errorf("the operator-facing description lost the PID: %s", describeHolders(holders))
	}
}

// TestCoordinationLockKeyIsNegativeAndStillMatches pins the arithmetic that the
// holder filter depends on, and it exists because the naive form works BY
// ACCIDENT.
//
// hashtextextended returns a SIGNED bigint, and this key is negative. An
// arithmetic shift sign-extends, so `(key >> 32)::int` produces a negative number
// where pg_locks stores an oid. PostgreSQL still matches them — comparing oid with
// int reinterprets the bits rather than rejecting them — which means the unmasked
// form passes every test while relying on a coercion nobody wrote down.
//
// This test fails if the key ever stops being negative (in which case the comment
// above is stale and misleading) and it fails if the masked filter stops finding a
// lock the session provably holds.
func TestCoordinationLockKeyIsNegativeAndStillMatches(t *testing.T) {
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

	var key int64
	if err := conn.QueryRowContext(ctx, "SELECT "+migrationLockKeyExpr).Scan(&key); err != nil {
		t.Fatalf("read key: %v", err)
	}
	if key >= 0 {
		t.Fatalf("the coordination key is %d, no longer negative: the masking comment in migrationLockHolders explains a hazard that no longer applies and must be re-checked, not left to rot", key)
	}

	var got bool
	if err := conn.QueryRowContext(ctx, "SELECT pg_catalog.pg_try_advisory_lock("+migrationLockKeyExpr+")").Scan(&got); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !got {
		t.Fatal("could not take the key in a private database")
	}

	holders, err := migrationLockHolders(ctx, conn)
	if err != nil {
		t.Fatalf("read holders: %v", err)
	}
	if len(holders) == 0 {
		t.Errorf("the filter found no holder for a key this very session holds (key=%d): the classid/objid extraction does not match what pg_locks stores", key)
	}
}

// TestCoordinationLockRefusesALockGrantedAfterTheDeadline covers the gap the second
// contrast round measured, which the sibling test's name claimed but did not reach.
//
// TestCoordinationLockNeverAcquiresAfterItsDeadline drives the budget to zero in the
// SLEEPER, before the next try is issued — so it proves the barrier at the top of the
// loop. It never exercises a try that STARTS inside the budget and RETURNS outside
// it. That case needs a third check, after the roundtrip and before accepting the
// grant, and without it the contrast reproduced `err=<nil>, expired_on_return=true`.
//
// The clock is spent from inside the query itself, which is what makes this the
// return path rather than the entry path: the budget is alive when the statement is
// issued and dead when it answers.
//
// Mutation that must turn this red: drop the b.expired() check guarding the
// acquired=true branch.
func TestCoordinationLockRefusesALockGrantedAfterTheDeadline(t *testing.T) {
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

	// The lock is FREE: this is not about contention. It is about a grant arriving
	// after the deadline, which is the one case where refusing looks perverse and is
	// nonetheless required — a caller promised a bounded wait must not be handed a
	// resource outside that bound.
	// The clock is spent between issuing the try and examining its answer. The
	// acquisition path reads the clock in a fixed order on a first-attempt success,
	// measured on this code:
	//
	//   1. newLockBudget, computing the deadline
	//   2. the derived context for the PID read
	//   3. b.expired() at the top of the loop
	//   4. the derived context for the try
	//   5. b.expired() AFTER the try returns   <- the check under test
	//   6. b.remaining() for the emitted attempt
	//
	// So spending the budget from read 5 onwards puts it alive at 4 and dead at 5,
	// which is exactly "started inside, returned outside".
	const expireFromRead = 5
	c := &fakeClock{t: time.Unix(0, 0)}
	var reads int
	clock := func() time.Time {
		reads++
		if reads >= expireFromRead {
			c.t = time.Unix(0, 0).Add(time.Hour)
		}
		return c.t
	}
	b := newLockBudget(time.Second, clock, c.sleep, func() float64 { return 1 })

	attempts, err := acquireCoordinationLock(ctx, conn, b, nil)
	// If the number of clock reads before the try ever changes, this test would start
	// exercising the ENTRY barrier instead of the RETURN one and silently stop
	// covering what it exists for. Anchoring on the attempt count makes that loud.
	if attempts != 1 {
		t.Fatalf("reached %d attempts, want exactly 1: the budget was spent before the try was issued, so this ran the entry barrier and not the return check it exists for", attempts)
	}
	if !errors.Is(err, ErrMigrationCoordinationTimeout) {
		t.Fatalf("acquisition = %v after %d attempts, want ErrMigrationCoordinationTimeout: the grant arrived after the deadline and accepting it breaks the bound the caller was promised", err, attempts)
	}
	if !strings.Contains(err.Error(), "after the deadline") {
		t.Errorf("the refusal does not say the grant was late, so an operator cannot tell it from ordinary contention: %v", err)
	}

	// The lock may well be held by this session now — the server granted it. What
	// must NOT happen is reporting success. The caller retires this connection on
	// every path, which is what makes refusing safe.
	if err == nil {
		t.Error("acquisition reported success outside its budget")
	}
}

// TestCoordinationLockReportsASpentBudgetAsItsOwnTimeout covers the roundtrip
// bounding that the sink test was wrongly claiming.
//
// The PID read happens BEFORE the loop, so it has no expiry check in front of it —
// its only protection is the budget-derived context. This drives that path directly:
// enter with the budget already spent and require the coordination timeout rather
// than a driver error.
//
// The distinction is the whole reason budgetSpent exists. Both arrive as a context
// error, but "the operator stopped us" and "we ran out of our own time" are different
// diagnoses, and only the second is a timeout an operator should see with attribution
// attached.
//
// Mutation that must turn this red: use the caller's ctx for the PID read instead of
// a budget-derived one.
func TestCoordinationLockReportsASpentBudgetAsItsOwnTimeout(t *testing.T) {
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

	c := &fakeClock{t: time.Unix(0, 0)}
	b := newLockBudget(time.Second, c.now, c.sleep, func() float64 { return 1 })
	c.t = c.t.Add(2 * time.Second) // spent before the first roundtrip

	var emitted int
	_, err = acquireCoordinationLock(ctx, conn, b, func(context.Context, lockAttempt) { emitted++ })
	if !errors.Is(err, ErrMigrationCoordinationTimeout) {
		t.Fatalf("acquisition = %v, want ErrMigrationCoordinationTimeout: a spent budget must surface as this node's own deadline, not as an opaque driver error with the attribution thrown away", err)
	}
	if emitted != 0 {
		t.Errorf("emitted %d attempts on a budget that was spent before the first roundtrip", emitted)
	}
	// The caller's context was never canceled, so nothing here may present as the
	// operator having stopped the boot.
	if errors.Is(err, context.Canceled) {
		t.Errorf("a spent budget surfaced as a cancellation: %v", err)
	}
}
