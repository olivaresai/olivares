// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/olivaresai/olivares/core/store"
)

// TestObserverNamesTheHolderBeforeTheWaiterFails is the ratification gate D3 asks
// for, and the one property no post-hoc query can deliver.
//
// A blocked session answers nothing, and once its wait ends the causal record is
// gone: pg_blocking_pids returns empty for a session that is no longer waiting.
// The original D3 concluded from that measurement that attribution was impossible;
// it is not, but only from a SECOND connection, and only WHILE the first is still
// blocked. That is what this asserts, and the assertion is deliberately about
// ordering — attribution obtained after the failure would satisfy a weaker test
// and would not be the property.
//
// Mutation that must turn this red: start the observer after the blocking
// statement returns instead of before it is issued.
func TestObserverNamesTheHolderBeforeTheWaiterFails(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	setup, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 3})
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer setup.Close() //nolint:errcheck // test teardown
	if _, err := setup.ExecContext(ctx, `CREATE TABLE olv_obs_target(id integer)`); err != nil {
		t.Fatalf("create target: %v", err)
	}
	if _, err := setup.ExecContext(ctx,
		`CREATE OR REPLACE FUNCTION olv_obs_noop() RETURNS trigger LANGUAGE plpgsql AS $f$ BEGIN RETURN NULL; END $f$`); err != nil {
		t.Fatalf("create function: %v", err)
	}

	// A REAL writer, not an artificial LOCK TABLE: it takes ROW EXCLUSIVE, which is
	// exactly what conflicts with the SHARE ROW EXCLUSIVE that CREATE TRIGGER needs.
	// A synthetic lock would prove the plumbing works against a situation that never
	// happens in production.
	holderConn, err := setup.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer holderConn.Close() //nolint:errcheck // test teardown
	tx, err := holderConn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("holder begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // released at the end of the test
	if _, err := tx.ExecContext(ctx, `INSERT INTO olv_obs_target VALUES (1)`); err != nil {
		t.Fatalf("holder insert: %v", err)
	}
	var holderPID int
	if err := tx.QueryRowContext(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&holderPID); err != nil {
		t.Fatalf("holder pid: %v", err)
	}

	waiterConn, err := setup.Conn(ctx)
	if err != nil {
		t.Fatalf("waiter conn: %v", err)
	}
	defer waiterConn.Close() //nolint:errcheck // test teardown
	var waiterPID int
	if err := waiterConn.QueryRowContext(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&waiterPID); err != nil {
		t.Fatalf("waiter pid: %v", err)
	}

	// BEFORE the blocking statement, which is the whole point.
	obs := startBlockObserver(ctx, dsns.App, waiterPID)

	// THE WAIT IS ENDED BY THIS TEST, NOT BY A LOCK TIMEOUT.
	//
	// It used to set lock_timeout to 1500ms and hope a probe landed inside that window.
	// The observer probes once immediately — before the DDL is even issued, so that one
	// finds nothing — and then every observerProbeInterval, which left about six chances
	// on a busy host where this repository has measured a SINGLE round trip taking more
	// than 250ms. Losing the race printed "the observer did not name the blocking
	// session", which reads as a defect in the observer and was the machine.
	//
	// So the block is now unbounded and the test ends it, after WAITING FOR THE CONDITION
	// the property is about: an attribution recorded while the waiter is still blocked.
	// The ordering being asserted is unchanged and is now explicit rather than raced —
	// the wait cannot be ended before the attribution exists, by construction.
	//
	// AND IT IS ENDED SERVER-SIDE, with pg_cancel_backend, which is not a detail. Ending
	// it by canceling the Go context poisons the client connection — measured here:
	// the self-query below came back `driver: bad connection`, the same failure this
	// campaign already met when a canceled parent took the connection down with it. A
	// server-side cancel ends the STATEMENT and leaves the session usable, which is what
	// the premise check further down needs.
	ddlDone := make(chan error, 1)
	go func() {
		_, err := waiterConn.ExecContext(ctx,
			`CREATE TRIGGER olv_obs_guard BEFORE TRUNCATE ON olv_obs_target FOR EACH STATEMENT EXECUTE FUNCTION olv_obs_noop()`)
		ddlDone <- err
	}()
	// AND THE BLOCK IS ENDED ON EVERY PATH, not just the happy one. MEASURED while writing
	// this: a failing run left the DDL blocked, and isolatedPG's DROP DATABASE then waited
	// on it until the whole package timed out. A test whose failure mode is hanging the
	// suite is worse than the flake it replaces. `defer` rather than t.Cleanup because the
	// pool above is closed by a defer, and this must run before it.
	defer func() {
		var b bool
		_ = setup.QueryRowContext(ctx, "SELECT pg_catalog.pg_cancel_backend($1)", waiterPID).Scan(&b)
		select {
		case <-ddlDone:
		default:
		}
	}()

	// The bound here is a hang detector, not a threshold: it is two orders of magnitude
	// above the probe interval, so no amount of scheduler noise reaches it, and only a
	// genuinely absent attribution can.
	waitForObservedHolder(t, obs, 30*time.Second)

	var canceled bool
	if err := setup.QueryRowContext(ctx, "SELECT pg_catalog.pg_cancel_backend($1)", waiterPID).Scan(&canceled); err != nil {
		t.Fatalf("could not end the waiter's block: %v", err)
	}
	ddlErr := <-ddlDone
	if ddlErr == nil {
		t.Fatal("the DDL acquired its lock; the writer above did not block it and this test proves nothing")
	}

	holders, degraded := obs.stopAndReport()
	if degraded != nil {
		t.Fatalf("the observer degraded and this test cannot judge attribution: %v", degraded)
	}
	var found bool
	for _, h := range holders {
		if h.PID.Valid && h.PID.Int64 == int64(holderPID) {
			found = true
		}
	}
	if !found {
		t.Errorf("the observer did not name the blocking session (pid %d); it saw %v. Attribution obtained only after the wait is exactly what pg_blocking_pids cannot give, which is why the observer starts first", holderPID, holders)
	}

	// The blocked waiter itself must be unable to answer, which is the reason the
	// observer exists. Asked after its own failure, it sees nothing.
	// The query must SUCCEED and return zero. Scanning array_length into an int fails
	// on NULL — which is what an empty array yields — so the previous form treated
	// every possible error, including a broken connection or a renamed function, as
	// proof of the premise. A premise established by an error that was never inspected
	// is not established at all.
	var selfBlockers int
	if err := waiterConn.QueryRowContext(ctx,
		"SELECT COALESCE(pg_catalog.array_length(pg_catalog.pg_blocking_pids(pg_catalog.pg_backend_pid()), 1), 0)").Scan(&selfBlockers); err != nil {
		t.Fatalf("could not ask the waiter about its own past wait, so the premise of this design is untested rather than confirmed: %v", err)
	}
	if selfBlockers != 0 {
		t.Errorf("the waiter attributed its own past wait (%d blockers): if that were possible the observer would be unnecessary, and the premise of this design would be wrong", selfBlockers)
	}
}

// TestObserverDegradesInsteadOfFailingTheBoot pins the property that makes the
// observer safe to add to a boot path at all.
//
// At the limit of max_connections a non-superuser cannot open anything — and that
// is precisely the moment there IS contention worth observing. An observer whose
// failure propagated would convert a diagnostic aid into an outage, at the worst
// possible time.
//
// Mutation that must turn this red: return an error from startBlockObserver and
// make the caller honor it.
func TestObserverDegradesInsteadOfFailingTheBoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// TEST-NET-1 never answers, with a one-second connect timeout.
	obs := startBlockObserver(ctx, "postgres://nobody:nope@192.0.2.1:5432/none?connect_timeout=1&sslmode=disable", 1234)
	if obs == nil {
		t.Fatal("startBlockObserver returned nil; a caller cannot be asked to nil-check a diagnostic")
	}
	holders, degraded := obs.stopAndReport()
	if len(holders) != 0 {
		t.Errorf("an observer that never connected reported %d holder(s)", len(holders))
	}
	if degraded == nil {
		t.Fatal("an observer that could not observe reported no degradation; silence here is indistinguishable from 'nobody was blocking', which is a different and much stronger claim")
	}
	// EITHER label is correct, and which one arrives is a race the observer is now
	// allowed to lose on purpose.
	//
	// "observation_unavailable" means the probe came back and could not connect.
	// "observation_incomplete" means stopAndReport hit its own 100ms bound first and
	// abandoned the goroutine — which is the right trade: at that point the answer is
	// either gathered or not coming, and the caller is holding locks on a deadline.
	// Waiting a full probe timeout here would let the diagnostic spend the very budget
	// it exists to explain.
	//
	// What must NOT happen is silence, or a label that reads as an observation.
	if !strings.Contains(degraded.Error(), "observation_unavailable") &&
		!strings.Contains(degraded.Error(), "observation_incomplete") {
		t.Errorf("degradation carries neither observation label, so a caller cannot tell it from a real attribution: %v", degraded)
	}
}

// TestObserverDSNPrefersTheOwnerWhenSplit pins which connection the observer
// dials. Same route, database, TLS and pooler as the waiter: an observer that
// reached the server another way could report a state the waiter is not in.
func TestObserverDSNPrefersTheOwnerWhenSplit(t *testing.T) {
	t.Parallel()
	const app = "postgres://app@h/db"
	const owner = "postgres://owner@h/db"

	if got := observerDSN(store.Config{DSN: app}); got != app {
		t.Errorf("single-role picked %q, want the application DSN", got)
	}
	if got := observerDSN(store.Config{DSN: app, OwnerDSN: owner}); got != owner {
		t.Errorf("split topology picked %q, want the owner DSN %q: the waiter is the owner session, and observing as a different role can see a different state", got, owner)
	}
	if got := observerDSN(store.Config{DSN: app, OwnerDSN: "   "}); got != app {
		t.Errorf("a blank owner DSN picked %q, want the application DSN", got)
	}
	if got := observerDSN(store.Config{DSN: app, OwnerDSN: app}); got != app {
		t.Errorf("an owner DSN equal to the app DSN picked %q, want the same value rather than a second identical pool", got)
	}
}

// TestObserverAttributesAHolderOwnedByAnotherRole closes the same coverage hole that
// hid the attribution defect, in the component where it mattered MORE.
//
// Every other test in this file runs holder, waiter and observer on one DSN, so the
// only relationship they exercise is a role looking at ITSELF — the one case
// PostgreSQL does not restrict. Under a real role boundary, pg_stat_activity blanks
// the describing columns of other roles' sessions, and describing OTHER sessions is
// this component's entire job: a session blocking this one is by construction not
// this one. So the coalesce to TIMESTAMPTZ '-infinity' did not degrade the observer,
// it disabled it — every row of every probe failed to scan, and a wait that WAS
// attributable reported that nobody could be named.
//
// Mutation that must turn this red: coalesce backend_start back to '-infinity' in
// holderColumns.
func TestObserverAttributesAHolderOwnedByAnotherRole(t *testing.T) {
	dsns := isolatedPGSplit(t)
	ctx := context.Background()

	// The OWNER creates the table and keeps a write lock on it; the APP role is the
	// one that waits and the one that observes. That is the production shape: the
	// migration connection is not a superuser and has no business being granted
	// pg_read_all_stats to make a log line prettier.
	owner, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.Owner, MaxConns: 3})
	if err != nil {
		t.Fatalf("open owner pool: %v", err)
	}
	defer owner.Close() //nolint:errcheck // test teardown
	for _, ddl := range []string{
		`CREATE TABLE olv_obs_xrole(id integer)`,
		`GRANT ALL ON TABLE olv_obs_xrole TO PUBLIC`,
	} {
		if _, err := owner.ExecContext(ctx, ddl); err != nil {
			t.Fatalf("owner setup %q: %v", ddl, err)
		}
	}

	holderConn, err := owner.Conn(ctx)
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer holderConn.Close() //nolint:errcheck // test teardown
	tx, err := holderConn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("holder begin: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck // released at the end of the test
	if _, err := tx.ExecContext(ctx, `INSERT INTO olv_obs_xrole VALUES (1)`); err != nil {
		t.Fatalf("holder insert: %v", err)
	}
	var holderPID int
	if err := tx.QueryRowContext(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&holderPID); err != nil {
		t.Fatalf("holder pid: %v", err)
	}

	app, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 3})
	if err != nil {
		t.Fatalf("open app pool: %v", err)
	}
	defer app.Close() //nolint:errcheck // test teardown
	waiterConn, err := app.Conn(ctx)
	if err != nil {
		t.Fatalf("waiter conn: %v", err)
	}
	defer waiterConn.Close() //nolint:errcheck // test teardown
	var waiterPID int
	if err := waiterConn.QueryRowContext(ctx, "SELECT pg_catalog.pg_backend_pid()").Scan(&waiterPID); err != nil {
		t.Fatalf("waiter pid: %v", err)
	}

	// Establish the premise. If the observing role could see everything, this would
	// pass for the wrong reason and keep passing after a regression.
	var restricted bool
	if err := waiterConn.QueryRowContext(ctx,
		"SELECT a.backend_start IS NULL FROM pg_catalog.pg_stat_activity a WHERE a.pid = $1",
		holderPID).Scan(&restricted); err != nil {
		t.Fatalf("premise probe: %v", err)
	}
	if !restricted {
		t.Fatalf("the observing role can read backend_start of another role's session, so this test cannot exercise the restricted path it exists for")
	}

	obs := startBlockObserver(ctx, dsns.App, waiterPID)

	// LOCK TABLE is only legal inside a transaction block (25P01), so the wait has to
	// happen in one. Measured the hard way: without it the statement fails instantly
	// and never blocks, which is why the assertion below insists on a SPECIFIC SQLSTATE
	// rather than on any error at all. That state is 57014 now, not the 55P03 this
	// paragraph named until the wait stopped being ended by a lock_timeout — a comment
	// that keeps citing the code its own test no longer asserts is exactly the drift this
	// campaign keeps paying for.
	wtx, err := waiterConn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("waiter begin: %v", err)
	}
	defer wtx.Rollback() //nolint:errcheck // test teardown

	// THE SAME CORRECTION AS THE ORDERING TEST ABOVE, for the same measured reason.
	//
	// This used to arm lock_timeout at 1500ms and hope one of the observer's probes landed
	// inside that window. That is about six chances at observerProbeInterval on a host
	// where a single round trip has been measured over 250ms, and losing the race printed
	// "the observer did not name the blocking holder" — a defect message for a scheduling
	// event. The block is unbounded now and this test ends it once the attribution exists.
	blockDone := make(chan error, 1)
	go func() {
		_, err := wtx.ExecContext(ctx, `LOCK TABLE ONLY olv_obs_xrole IN SHARE ROW EXCLUSIVE MODE`)
		blockDone <- err
	}()
	// Ended on EVERY path: a failing run that left this blocked would hang the DROP
	// DATABASE in isolatedPG's cleanup and take the package's timeout with it.
	defer func() {
		var b bool
		_ = app.QueryRowContext(ctx, "SELECT pg_catalog.pg_cancel_backend($1)", waiterPID).Scan(&b)
		select {
		case <-blockDone:
		default:
		}
	}()

	waitForObservedHolder(t, obs, 30*time.Second)

	var canceled bool
	if err := app.QueryRowContext(ctx, "SELECT pg_catalog.pg_cancel_backend($1)", waiterPID).Scan(&canceled); err != nil {
		t.Fatalf("could not end the waiter's block: %v", err)
	}
	blockErr := <-blockDone
	if blockErr == nil {
		t.Fatal("the waiter acquired its lock; the writer above did not block it and this test proves nothing")
	}
	// THE ERROR MUST STILL BE THE RIGHT ONE, and the rigor is unchanged even though the
	// code moved: accepting any error would let this pass while never blocking at all.
	// What proves the block now is the ATTRIBUTION — pg_blocking_pids named the holder,
	// which it only does for a session that is waiting — and the code below proves the
	// statement was killed while running rather than failing on its own account. That is
	// a stronger pair than 55P03 alone, which only ever said "some lock timed out".
	var pgErr *pgconn.PgError
	if !errors.As(blockErr, &pgErr) || pgErr.Code != sqlStateQueryCanceled {
		t.Fatalf("the waiter failed with %v, not the cancellation this test issued (57014): it did not block until this test ended the wait", blockErr)
	}

	holders, degraded := obs.stopAndReport()
	if degraded != nil {
		t.Fatalf("the observer degraded under a plain role boundary — which is every production deployment, not an edge case: %v", degraded)
	}
	var found bool
	for _, h := range holders {
		if h.PID.Valid && h.PID.Int64 == int64(holderPID) {
			found = true
			if h.BackendStart.Valid {
				t.Errorf("backend_start came back valid after the premise probe said it was NULL: %v", h.BackendStart)
			}
			if !strings.Contains(h.String(), "since="+holderUnavailable) {
				t.Errorf("an unreadable backend_start rendered as %q instead of naming the column unavailable", h.String())
			}
		}
	}
	if !found {
		t.Errorf("the observer did not name the blocking holder (pid %d) across %d attributions; pg_blocking_pids never hides the PID, so this is the answer being thrown away rather than never obtained", holderPID, len(holders))
	}
}

// TestObserverStopDoesNotSpendTheBudgetItExplains pins the bound on STOPPING.
//
// The observer promises never to affect the deadline it diagnoses. It kept that
// promise for probing — each probe has its own timeout — and broke it at the end:
// stopAndReport joined the goroutine unconditionally, so a probe in flight against an
// unreachable server made the caller wait up to a full probe timeout, holding locks,
// on a budget the observer was supposed to leave alone.
//
// The observer here can never answer (TEST-NET-1, an unroutable address, so the connect
// hangs rather than being refused), which is exactly the case that used to cost the
// caller a second.
//
// Mutation that must turn this red: remove the timeout from the join in stopAndReport.
//
// # THIS TEST USED TO MEASURE A WALL CLOCK, AND THE MACHINE COULD FAIL IT
//
// It timed `time.Since(start)` against an absolute ceiling of 4 × observerStopTimeout =
// 400 ms. Nothing real happens inside that window — 5 of 5 runs measured 0.10 s exactly —
// so the 300 ms of headroom bought nothing but scheduler latency, and the hub's sweep
// REPRODUCED it red under load: at loadavg ~180, one run in 150 took 401.69 ms, on a tail
// that is thick and continuous (0.17 / 0.20 / 0.28 / 0.30 / 0.40) rather than a rare jump.
// A test that reddens when the host is busy accuses the code of what the machine did.
//
// The property is right and worth keeping. What was wrong is that a DURATION was standing
// in for a CAUSE. The two outcomes are already distinguishable without any clock:
//
//   - abandoned at the bound → `observation_incomplete`, written only by the timeout arm
//     of the select in stopAndReport;
//   - joined the goroutine    → `observation_unavailable`, written only by probe().
//
// So the assertion is now which of the two arms ran, which is exactly what the mutation
// changes, and the only timing left is the 20× separation between the stop bound (100 ms)
// and the ceiling on the goroutine finishing at all (observerProbeTimeout, 2 s). The tail
// the hub measured tops out at 0.40 s, so the margin is no longer where the noise is.
func TestObserverStopDoesNotSpendTheBudgetItExplains(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	obs := startBlockObserver(ctx,
		"postgres://nobody:nope@192.0.2.1:5432/none?connect_timeout=30&sslmode=disable", 1234)

	_, degraded := obs.stopAndReport()

	if degraded == nil {
		t.Fatal("an observer that could not observe reported no degradation")
	}
	// THE CAUSAL ASSERTION. Only the timeout arm of the select writes this.
	if !strings.Contains(degraded.Error(), "observation_incomplete") {
		t.Errorf("stopping reported %v: it JOINED the probe instead of abandoning it at the bound, so the diagnostic spent the deadline it promised not to affect while the caller holds locks", degraded)
	}
	// AND THE MAGNITUDE OF THE BOUND, as a relation between two constants rather than a
	// measurement — otherwise a mutant that widens observerStopTimeout to a full probe
	// would keep reporting `observation_incomplete` and keep this test green while
	// reintroducing the exact cost the bound exists to prevent.
	// THE RATIO IS THE CLAIM, not merely "smaller". The first version of this only asked for
	// stop < probe, which accepts a one-second stop against a two-second probe while the
	// comment above and the commit that introduced it both announce a 20x separation. An
	// assertion weaker than the sentence beside it is the defect this file keeps finding.
	if observerStopTimeout*20 > observerProbeTimeout {
		t.Errorf("observerStopTimeout is %v against a probe timeout of %v, so stop is not at most probe/20: the twenty-fold separation this test and its commit both announce is not what the constants say, and a bound that drifts up toward a whole probe is the cost the bound exists to remove",
			observerStopTimeout, observerProbeTimeout)
	}
}

// TestObserverPoolClosureEndsItsBackendSession is the half the mute listener cannot show.
//
// Against the mute fixture the probe times out and pgx discards the broken connection on
// its own, so the peer sees a hang-up whether or not sql.DB.Close is ever called —
// measured: deleting the Close inside closePool left that test green. The flag it checks
// proves closePool was REACHED, and nothing about what closePool does.
//
// Here the handshake completes, the probe succeeds, and the pool is left holding a
// healthy idle connection. That connection is a backend on the server, so its
// disappearance is observable from outside this process entirely — which is the only
// place an assertion about closing it can honestly live.
//
// Mutation that must turn this red: delete `_ = o.pool.Close()` from closePool, keeping
// the flag.
func TestObserverPoolClosureEndsItsBackendSession(t *testing.T) {
	dsns := isolatedPG(t)
	ctx := context.Background()

	// A control pool, opened first and never closed until teardown, so it contributes a
	// constant to every count below.
	ctl, err := openPostgres(store.Config{Engine: store.EnginePostgres, DSN: dsns.App, MaxConns: 2})
	if err != nil {
		t.Fatalf("open control pool: %v", err)
	}
	defer ctl.Close() //nolint:errcheck // test teardown

	backends := func(what string) int {
		var n int
		if err := ctl.QueryRowContext(ctx, `
SELECT count(*) FROM pg_catalog.pg_stat_activity
WHERE datname = pg_catalog.current_database()`).Scan(&n); err != nil {
			t.Fatalf("count backends (%s): %v", what, err)
		}
		return n
	}
	// Settle the control pool first: counting before it has dialed would move the
	// baseline under the test.
	if err := ctl.PingContext(ctx); err != nil {
		t.Fatalf("ping control: %v", err)
	}
	baseline := backends("baseline")

	obs := startBlockObserver(ctx, dsns.App, 1)
	if obs.pool == nil {
		t.Fatal("the observer did not open a pool")
	}

	// Wait for the observer's own backend to appear. Its first probe is immediate, so
	// this settles quickly; polling rather than sleeping keeps it from being a race.
	deadline := time.Now().Add(15 * time.Second)
	during := baseline
	for time.Now().Before(deadline) {
		if during = backends("during"); during > baseline {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// THE PREMISE. Without a live observer backend there is nothing whose closure could
	// be measured, and the assertions below would pass vacuously.
	if during <= baseline {
		t.Fatalf("the observer never established a backend (baseline=%d, during=%d); this run cannot show anything about closing one",
			baseline, during)
	}

	if _, err := obs.stopAndReport(); err != nil {
		t.Logf("the observer reported degradation while stopping: %v (the count below is the assertion)", err)
	}
	select {
	case <-obs.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the probe goroutine never exited")
	}

	// And the backend must be GONE. pg_stat_activity clears the row when the backend
	// exits, which happens when the client closes the socket — the effect of pool.Close.
	after := during
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if after = backends("after"); after <= baseline {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("OBSERVER_BACKENDS|baseline=%d|during=%d|after=%d", baseline, during, after)
	if after > baseline {
		t.Errorf("the observer's backend is still on the server after it stopped (baseline=%d, after=%d): its pool was never closed, so one connection leaks per observation against the very limit that makes contention worth observing",
			baseline, after)
	}
}

// waitForObservedHolder blocks until the observer has recorded at least one attribution.
//
// IT WAITS FOR A CONDITION, WHICH IS THE POINT. The tests that use it are about ordering —
// the holder is named WHILE the waiter is blocked, because pg_blocking_pids answers nothing
// once the wait is over — and ordering is a causal claim, not a duration. Racing a
// lock_timeout against a probe interval turned that claim into a bet on the host's load, and
// the hub's sweep found two tests in this file making it.
//
// The bound is a hang detector two orders of magnitude above observerProbeInterval, so a busy
// scheduler cannot reach it and a missing attribution always does.
func waitForObservedHolder(t *testing.T, o *blockObserver, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		o.mu.Lock()
		n := len(o.holders)
		degraded := o.degraded
		o.mu.Unlock()
		if n > 0 {
			return
		}
		if degraded != nil {
			t.Fatalf("the observer degraded before it could attribute anything: %v", degraded)
		}
		if time.Now().After(deadline) {
			t.Fatalf("no attribution after %s, which is %d probe intervals: the observer never named the blocking session while it was still blocking",
				within, int(within/observerProbeInterval))
		}
		time.Sleep(5 * time.Millisecond)
	}
}
