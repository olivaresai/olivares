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
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/store"
)

// TestLeaderElectionPostgres exercises the REAL Postgres session-advisory-lock
// elector end to end on an ISOLATED database, and skips when no Postgres
// is configured — the same convention as TestPostgresIntegration. This is the test a
// CI job with a postgres service runs to verify HA by EXECUTION, not just by the
// fake-backend unit tests. It proves: exactly one of two electors on one Postgres
// leads; the other is a drained standby; and when the leader resigns the standby
// takes over and the fencing epoch advances.
func TestLeaderElectionPostgres(t *testing.T) {
	// its own database, so the single olivares.leader.v1 advisory lock is
	// this test's alone — the "exactly one leader" and epoch-delta assertions are
	// only meaningful if no other suite can hold it.
	dsn := isolatedPG(t).App
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: dsn}

	a, err := newPGElector(cfg, nil)
	if err != nil {
		t.Fatalf("elector A: %v", err)
	}
	a.poll = 200 * time.Millisecond
	b, err := newPGElector(cfg, nil)
	if err != nil {
		t.Fatalf("elector B: %v", err)
	}
	b.poll = 200 * time.Millisecond

	if err := a.Run(ctx); err != nil {
		t.Fatalf("A.Run: %v", err)
	}
	defer a.Resign(ctx) //nolint:errcheck
	if err := b.Run(ctx); err != nil {
		t.Fatalf("B.Run: %v", err)
	}
	defer b.Resign(ctx) //nolint:errcheck

	// Exactly one leader; the other is a drained standby.
	if a.IsLeader() == b.IsLeader() {
		t.Fatalf("exactly one elector must lead, got A=%v B=%v", a.IsLeader(), b.IsLeader())
	}
	leader, standby := a, b
	if b.IsLeader() {
		leader, standby = b, a
	}
	if standby.Active() {
		t.Fatal("the standby must report not-Active so the load balancer drains it")
	}
	epoch1 := leader.Epoch()
	if epoch1 == 0 {
		t.Fatal("leader epoch must be non-zero after acquisition")
	}

	// The leader resigns (graceful handoff). The standby's maintenance loop must
	// promote it within a few poll intervals, with the fencing epoch advanced.
	if err := leader.Resign(ctx); err != nil {
		t.Fatalf("leader Resign: %v", err)
	}
	if !eventuallyTrue(2*time.Second, standby.IsLeader) {
		t.Fatal("the standby did not take over after the leader resigned")
	}
	if standby.Epoch() <= epoch1 {
		t.Fatalf("epoch must advance on failover: was %d, now %d", epoch1, standby.Epoch())
	}
}

// TestPGLockBackendConcurrentFenceReads pins the concurrency contract of the
// held lock session (RAMA-E encargo 2026-07-26): N goroutines fence-reading
// through verifyHeldEpoch while the maintenance tick health-checks the SAME
// session — exactly the production mix, since every evidence claim calls
// FencedEpoch (core/store/evidenceops.go:236) and the elector loop calls
// healthy() every poll tick.
//
// Before the fix, pgLockBackend's mutex guarded only the b.conn POINTER and
// each session use ran outside it. database/sql serializes individual driver
// calls (database/sql/sql.go:3059) but not the Query→Next→Close SEQUENCE, so
// two goroutines interleaved result sets on one pgx connection — which pgx
// forbids (pgx/v5 conn.go:65 "not safe for concurrent usage"). The corrupted
// stream either errored (surfacing as ledger_unavailable refusals of valid
// claims) or panicked the whole process: database/sql sizes the scan
// destination from Columns() (sql.go:3062-3066) and pgx stdlib writes one
// entry per received field without re-checking len(dest)
// (stdlib/sql.go:877-885) — index out of range, process down.
//
// 800 fence reads racing 100 health checks make that interleaving all but
// certain, so this fails FAST on the broken code (observed: panic or fence
// error within the first rounds) instead of the 4-in-10 flake rate the
// 6-worker claim test exhibited. Any error or panic here is a product defect
// on the audit-ledger write path, never test noise.
func TestPGLockBackendConcurrentFenceReads(t *testing.T) {
	dsn := isolatedPG(t).App
	ctx := context.Background()

	be, err := newPGLockBackend(dsn, "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	defer be.close() //nolint:errcheck
	if err := be.ensure(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	held, err := be.tryLock(ctx)
	if err != nil {
		t.Fatalf("tryLock: %v", err)
	}
	if !held {
		t.Fatal("lock not acquired on an isolated database — nothing else may hold it")
	}
	defer be.unlock(ctx) //nolint:errcheck
	if _, err := be.bumpEpoch(ctx); err != nil {
		t.Fatalf("bumpEpoch: %v", err)
	}

	const readers = 8
	const iters = 100
	start := time.Now()
	errCh := make(chan error, readers+1)
	var wg sync.WaitGroup
	for w := 0; w < readers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				if _, verr := be.verifyHeldEpoch(ctx); verr != nil {
					errCh <- verr
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iters; i++ {
			if herr := be.healthy(ctx); herr != nil {
				errCh <- herr
				return
			}
		}
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent use of the held lock session failed: %v", err)
	}
	elapsed := time.Since(start)
	total := readers * iters
	t.Logf("%d fence reads + %d health checks in %v (%.0f fence reads/s)",
		total, iters, elapsed, float64(total)/elapsed.Seconds())
}

// TestPGElectorResignTerminatesAWedgedSession is the executable form of the
// three evidence items the fence-fix audit demanded
// (an internal design note (not shipped)): a bounded
// Resign through the REAL elector path must not wait for a wedged fence read,
// must terminate the physical session (socket kill — sql.Conn.Close alone
// waits for the operation and then POOLS the still-locked session), and the
// advisory lock must be free for a second elector afterwards.
//
// The wedge is the hard case keepalives cannot bound: the server is ALIVE, and
// the fence read blocks behind an ACCESS EXCLUSIVE table lock held by an open
// superuser transaction.
func TestPGElectorResignTerminatesAWedgedSession(t *testing.T) {
	pg := isolatedPG(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App}

	a, err := newPGElector(cfg, nil)
	if err != nil {
		t.Fatalf("elector A: %v", err)
	}
	a.poll = 200 * time.Millisecond
	if err := a.Run(ctx); err != nil {
		t.Fatalf("A.Run: %v", err)
	}
	if !a.IsLeader() {
		t.Fatal("A must lead on an isolated database")
	}
	pinNoDeadClientCheck(t, a.backend.(*pgLockBackend))

	super, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatalf("superuser pool: %v", err)
	}
	defer super.Close() //nolint:errcheck
	wedge, err := super.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("wedge tx: %v", err)
	}
	defer wedge.Rollback() //nolint:errcheck
	if _, err := wedge.ExecContext(ctx,
		"LOCK TABLE "+leaderEpochTable+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatalf("wedge lock: %v", err)
	}

	// The fence read now blocks server-side holding the session semaphore.
	fenceErr := make(chan error, 1)
	go func() {
		_, ferr := a.FencedEpoch(ctx)
		fenceErr <- ferr
	}()
	blocked := eventuallyTrue(5*time.Second, func() bool {
		var n int
		if err := super.QueryRowContext(ctx,
			`SELECT count(*) FROM pg_locks WHERE relation = to_regclass($1) AND NOT granted`,
			leaderEpochTable).Scan(&n); err != nil {
			return false
		}
		return n > 0
	})
	if !blocked {
		t.Fatal("the fence read never blocked on the wedge; the scenario did not arm")
	}

	// PRODUCTION contract: Resign receives an unbounded context (sqlStore.Close
	// passes context.Background()); the elector must apply its own deadline.
	start := time.Now()
	if err := a.Resign(ctx); err != nil {
		t.Fatalf("Resign: %v", err)
	}
	if elapsed := time.Since(start); elapsed > resignDeadline+2*time.Second {
		t.Fatalf("Resign took %v; the default bound is %v — it waited for the wedged read", elapsed, resignDeadline)
	}

	// The wedged read must have been TERMINATED, not left running against a
	// session we no longer track.
	select {
	case ferr := <-fenceErr:
		if ferr == nil {
			t.Fatal("the wedged fence read reported success; it must fail when its session is terminated")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wedged fence read is still blocked after Resign: the physical session was not terminated")
	}

	// Round-3 recipe: the advisory lock must be FREE while the wedge is STILL
	// held — asserting only after rollback would let a lock that survived the
	// whole interval pass. A control session tries the same key directly.
	ctlConn, err := super.Conn(ctx)
	if err != nil {
		t.Fatalf("control conn: %v", err)
	}
	defer ctlConn.Close() //nolint:errcheck
	freed := eventuallyTrue(6*time.Second, func() bool {
		var got bool
		if qerr := ctlConn.QueryRowContext(ctx,
			"SELECT pg_try_advisory_lock(hashtextextended($1, 0))", leaderLockName).Scan(&got); qerr != nil {
			return false
		}
		return got
	})
	if !freed {
		t.Fatal("the advisory lock survived Resign while the wedge held: the server session was not terminated")
	}
	if _, err := ctlConn.ExecContext(ctx,
		"SELECT pg_advisory_unlock(hashtextextended($1, 0))", leaderLockName); err != nil {
		t.Fatalf("control unlock: %v", err)
	}

	// With the wedge lifted, a full second elector takes over end to end.
	if err := wedge.Rollback(); err != nil {
		t.Fatalf("lift wedge: %v", err)
	}
	b, err := newPGElector(cfg, nil)
	if err != nil {
		t.Fatalf("elector B: %v", err)
	}
	b.poll = 200 * time.Millisecond
	if err := b.Run(ctx); err != nil {
		t.Fatalf("B.Run: %v", err)
	}
	defer b.Resign(ctx) //nolint:errcheck
	if !eventuallyTrue(4*time.Second, b.IsLeader) {
		t.Fatal("no takeover after resignation: the terminated session's advisory lock survived")
	}
}

// TestPGLockBackendUnlockNeverPoolsTheLockSession pins the pooling half of the
// audit: a session that ever held the advisory lock is physically retired on
// unlock, never returned to the one-connection pool. pgx's ResetSession does
// not clear session advisory locks (stdlib/sql.go:553-584), and
// pg_advisory_lock is re-entrant per session, so a pooled ex-lock session
// would answer the next acquisition with a stale extra lock count.
func TestPGLockBackendUnlockNeverPoolsTheLockSession(t *testing.T) {
	pg := isolatedPG(t)
	ctx := context.Background()

	be, err := newPGLockBackend(pg.App, "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	defer be.close() //nolint:errcheck
	if err := be.ensure(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	sessionPID := func() int {
		t.Helper()
		if err := be.acquireSession(ctx); err != nil {
			t.Fatalf("acquire session: %v", err)
		}
		defer be.releaseSession()
		var pid int
		if err := be.conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
			t.Fatalf("read backend pid: %v", err)
		}
		return pid
	}
	advisoryHeld := func() int {
		t.Helper()
		super, err := sql.Open("pgx", pg.Superuser)
		if err != nil {
			t.Fatalf("superuser pool: %v", err)
		}
		defer super.Close() //nolint:errcheck
		var n int
		if err := super.QueryRowContext(ctx, `
			SELECT count(*) FROM pg_locks l,
			  (SELECT hashtextextended($1, 0) AS k) x
			WHERE l.locktype = 'advisory' AND l.granted
			  AND l.classid = ((x.k >> 32) & 4294967295)::oid
			  AND l.objid = (x.k & 4294967295)::oid`,
			leaderLockName).Scan(&n); err != nil {
			t.Fatalf("count advisory locks: %v", err)
		}
		return n
	}

	held, err := be.tryLock(ctx)
	if err != nil || !held {
		t.Fatalf("tryLock: held=%v err=%v", held, err)
	}
	pid1 := sessionPID()
	if got := advisoryHeld(); got != 1 {
		t.Fatalf("after tryLock: %d granted advisory locks for the key, want 1", got)
	}

	if err := be.unlock(ctx); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if got := advisoryHeld(); got != 0 {
		t.Fatalf("after unlock: %d granted advisory locks for the key, want 0 — the session survived with the lock", got)
	}

	held, err = be.tryLock(ctx)
	if err != nil || !held {
		t.Fatalf("re-tryLock: held=%v err=%v", held, err)
	}
	defer be.unlock(ctx) //nolint:errcheck
	pid2 := sessionPID()
	if pid1 == pid2 {
		t.Fatalf("the ex-lock session (backend pid %d) was returned to the pool and reused: "+
			"a session that held the advisory lock must be physically retired", pid1)
	}
	if got := advisoryHeld(); got != 1 {
		t.Fatalf("after re-acquire: %d granted advisory locks, want exactly 1 (no residual re-entrant count)", got)
	}
}

// TestPGElectorResignDuringPromotionReleasesTheLock pins the round-2 P1
// deterministically: a session can OWN the advisory lock while e.leader is
// still false (tryLock succeeded, the promotion bootstrap is running), and a
// Resign in that window used to skip unlock entirely (it gated on wasLeader),
// leaving the lock alive until process death. The promotion callback blocks on
// a channel, so the window is held open by construction — no timing luck.
//
// The assertions are the audit's items 1 and 2: Resign returns bounded, and a
// SECOND backend acquires the same advisory lock BEFORE the original
// promotion is allowed to continue.
func TestPGElectorResignDuringPromotionReleasesTheLock(t *testing.T) {
	pg := isolatedPG(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App}

	a, err := newPGElector(cfg, nil)
	if err != nil {
		t.Fatalf("elector A: %v", err)
	}
	a.poll = 200 * time.Millisecond
	promoteEntered := make(chan struct{})
	promoteRelease := make(chan struct{})
	a.OnPromote(func(context.Context) error {
		close(promoteEntered)
		<-promoteRelease
		return nil
	})
	runDone := make(chan error, 1)
	go func() { runDone <- a.Run(ctx) }()
	select {
	case <-promoteEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("promotion never started; the scenario did not arm")
	}
	// The lock is owned and e.leader is still false: exactly the window.
	if a.IsLeader() {
		t.Fatal("leader must not be published while the promotion callback runs")
	}

	start := time.Now()
	if err := a.Resign(ctx); err != nil {
		t.Fatalf("Resign during promotion: %v", err)
	}
	if elapsed := time.Since(start); elapsed > resignDeadline+2*time.Second {
		t.Fatalf("Resign took %v; want bounded by ~%v", elapsed, resignDeadline)
	}

	// Item 2: the advisory lock must be acquirable by another backend BEFORE
	// the original promotion unblocks.
	other, err := newPGLockBackend(pg.App, "")
	if err != nil {
		t.Fatalf("second backend: %v", err)
	}
	defer other.close() //nolint:errcheck
	acquired := eventuallyTrue(4*time.Second, func() bool {
		held, lerr := other.tryLock(ctx)
		return lerr == nil && held
	})
	if !acquired {
		t.Fatal("the advisory lock survived a mid-promotion Resign: unlock was skipped for a " +
			"session that owned the lock with e.leader still false")
	}
	defer other.unlock(ctx) //nolint:errcheck

	close(promoteRelease)
	select {
	case rerr := <-runDone:
		if rerr == nil && a.IsLeader() {
			t.Fatal("a resigned elector published leadership after its session was terminated")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the promotion was released")
	}
}

// TestPGLockBackendCloseTerminatesAWedgedOperation pins close() as the
// unconditional terminator (round-2 audit, items 3 and 4): even when the
// forced-unlock path has nothing to kill (b.conn already detached by a
// concurrent unlock, or unlock skipped entirely), close() must seal the dialer
// — killing the wedged session's socket so the in-flight operation dies and
// the server releases the advisory lock — and refuse any new dial afterwards.
func TestPGLockBackendCloseTerminatesAWedgedOperation(t *testing.T) {
	pg := isolatedPG(t)
	ctx := context.Background()

	be, err := newPGLockBackend(pg.App, "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	if err := be.ensure(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	held, err := be.tryLock(ctx)
	if err != nil || !held {
		t.Fatalf("tryLock: held=%v err=%v", held, err)
	}
	if _, err := be.bumpEpoch(ctx); err != nil {
		t.Fatalf("bumpEpoch: %v", err)
	}
	pinNoDeadClientCheck(t, be)

	super, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatalf("superuser pool: %v", err)
	}
	defer super.Close() //nolint:errcheck
	wedge, err := super.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("wedge tx: %v", err)
	}
	defer wedge.Rollback() //nolint:errcheck
	if _, err := wedge.ExecContext(ctx,
		"LOCK TABLE "+leaderEpochTable+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatalf("wedge lock: %v", err)
	}
	opErr := make(chan error, 1)
	go func() {
		_, verr := be.verifyHeldEpoch(ctx)
		opErr <- verr
	}()
	blocked := eventuallyTrue(5*time.Second, func() bool {
		var n int
		if qerr := super.QueryRowContext(ctx,
			`SELECT count(*) FROM pg_locks WHERE relation = to_regclass($1) AND NOT granted`,
			leaderEpochTable).Scan(&n); qerr != nil {
			return false
		}
		return n > 0
	})
	if !blocked {
		t.Fatal("the fence read never blocked; the scenario did not arm")
	}

	// The server-side identity captured at acquisition is what terminateSession
	// uses; remember it to assert the BACKEND died, not merely the client op.
	be.mu.Lock()
	wedgedPID := int64(be.kill.pid)
	be.mu.Unlock()
	if wedgedPID == 0 {
		t.Fatal("no captured session PID; the termination path has nothing to kill")
	}

	// Directly close() — NOT unlock — as a wedged-unlock shutdown would.
	if err := be.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case verr := <-opErr:
		if verr == nil {
			t.Fatal("the wedged operation reported success; its session must have been terminated")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wedged operation survived close(): the session was not terminated")
	}

	// Round-3 recipe, all WITH THE WEDGE STILL HELD: the server backend is
	// gone, and the advisory lock is acquirable by someone else. Client-side
	// socket death alone cannot make either true while the wedge blocks the
	// query (client_connection_check_interval is pinned to 0 on the session).
	gone := eventuallyTrue(6*time.Second, func() bool {
		var n int
		if qerr := super.QueryRowContext(ctx,
			"SELECT count(*) FROM pg_stat_activity WHERE pid = $1", wedgedPID).Scan(&n); qerr != nil {
			return false
		}
		return n == 0
	})
	if !gone {
		t.Fatalf("backend %d still exists while the wedge holds: close() did not terminate the server session", wedgedPID)
	}
	ctlConn, err := super.Conn(ctx)
	if err != nil {
		t.Fatalf("control conn: %v", err)
	}
	defer ctlConn.Close() //nolint:errcheck
	var got bool
	if err := ctlConn.QueryRowContext(ctx,
		"SELECT pg_try_advisory_lock(hashtextextended($1, 0))", leaderLockName).Scan(&got); err != nil {
		t.Fatalf("control try lock: %v", err)
	}
	if !got {
		t.Fatal("the advisory lock survived close() while the wedge held")
	}
	if _, err := ctlConn.ExecContext(ctx,
		"SELECT pg_advisory_unlock(hashtextextended($1, 0))", leaderLockName); err != nil {
		t.Fatalf("control unlock: %v", err)
	}

	// Item 4: no dial may be created after the seal. The pool is also closed,
	// so exercise the DIALER barrier directly: a sealed dialer refuses even a
	// fresh, well-formed dial.
	//
	// The target comes from the isolated DSN, not from 127.0.0.1:5432: with the
	// CI port now ephemeral a fixed literal has nothing listening, so a dialer
	// whose sealing regressed would fail the connect anyway and the test would
	// pass WITHOUT having tested the seal.
	u, perr := url.Parse(pg.App)
	if perr != nil {
		t.Fatalf("parse isolated app DSN: %v", perr)
	}
	if u.Host == "" {
		t.Fatalf("isolated app DSN has no host:port (%q); this test cannot discriminate without one", pg.App)
	}
	// POSITIVE CONTROL: without it, the test cannot distinguish "sealed
	// correctly" from "dead target" — which is exactly the defect being fixed.
	if c, oerr := (&net.Dialer{}).DialContext(ctx, "tcp", u.Host); oerr != nil {
		t.Fatalf("control: an unsealed dialer must reach %s, got %v", u.Host, oerr)
	} else {
		_ = c.Close()
	}
	if _, derr := be.killer.DialContext(ctx, "tcp", u.Host); derr == nil {
		t.Fatal("a sealed dialer produced a new connection")
	}
}

// pinNoDeadClientCheck sets client_connection_check_interval=0 on the HELD
// lock session (round-3 recipe): the discriminating property is that the
// server does NOT notice a dead client mid-query on its own, so the tests must
// not depend on whatever the operator configured. The product deliberately
// leaves the operator's value untouched (round-4 audit), so the tests pin it.
func pinNoDeadClientCheck(t *testing.T, be *pgLockBackend) {
	t.Helper()
	ctx := context.Background()
	if err := be.acquireSession(ctx); err != nil {
		t.Fatalf("acquire session to pin GUC: %v", err)
	}
	defer be.releaseSession()
	if _, err := be.conn.ExecContext(ctx, "SET client_connection_check_interval = 0"); err != nil {
		t.Fatalf("pin client_connection_check_interval=0: %v", err)
	}
}

// TestPGLockBackendTerminationFailureIsLoud pins the round-4 error contract:
// when the server-side termination lever fails (partition, permission, an
// unconfirmed terminate), the forced unlock must SAY so — never swallow it —
// because the caller's only remaining bound is the session keepalives pinned
// at acquisition, and an operator debugging a slow handoff needs the cause.
func TestPGLockBackendTerminationFailureIsLoud(t *testing.T) {
	pg := isolatedPG(t)
	ctx := context.Background()

	be, err := newPGLockBackend(pg.App, "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	if err := be.ensure(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	held, err := be.tryLock(ctx)
	if err != nil || !held {
		t.Fatalf("tryLock: held=%v err=%v", held, err)
	}
	// Inject BOTH lever failures independently (the per-lever seams).
	be.protocolCancel = func(context.Context, *sessionKill) error {
		return fmt.Errorf("injected: cancel unreachable")
	}
	be.sqlTerminate = func(context.Context, uint32) (bool, error) {
		return false, fmt.Errorf("injected: terminate unreachable")
	}

	// Wedge an operation so unlock takes the forced path.
	super, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatalf("superuser pool: %v", err)
	}
	defer super.Close() //nolint:errcheck
	wedge, err := super.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("wedge tx: %v", err)
	}
	defer wedge.Rollback() //nolint:errcheck
	if _, err := wedge.ExecContext(ctx,
		"LOCK TABLE "+leaderEpochTable+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatalf("wedge lock: %v", err)
	}
	opErr := make(chan error, 1)
	go func() {
		_, verr := be.verifyHeldEpoch(ctx)
		opErr <- verr
	}()
	blocked := eventuallyTrue(5*time.Second, func() bool {
		var n int
		if qerr := super.QueryRowContext(ctx,
			`SELECT count(*) FROM pg_locks WHERE relation = to_regclass($1) AND NOT granted`,
			leaderEpochTable).Scan(&n); qerr != nil {
			return false
		}
		return n > 0
	})
	if !blocked {
		t.Fatal("the fence read never blocked; the scenario did not arm")
	}

	expired, cancel := context.WithTimeout(ctx, time.Millisecond)
	defer cancel()
	<-expired.Done()
	uerr := be.unlock(expired)
	if uerr == nil {
		t.Fatal("forced unlock with a failing termination lever reported success")
	}
	if !errors.Is(uerr, errLockSessionTerminationFailed) ||
		!strings.Contains(uerr.Error(), "injected: terminate unreachable") ||
		!strings.Contains(uerr.Error(), "injected: cancel unreachable") {
		t.Fatalf("the lever failures were swallowed instead of propagated: %v", uerr)
	}

	// Cleanup: lift the wedge, restore the real levers and close for real.
	_ = wedge.Rollback()
	<-opErr
	be.protocolCancel = func(ctx context.Context, kill *sessionKill) error {
		if kill.cancel == nil {
			return fmt.Errorf("no protocol cancel handle captured")
		}
		return kill.cancel(ctx)
	}
	be.sqlTerminate = be.sqlTerminateBackend
	if err := be.close(); err != nil {
		t.Fatalf("close after restore: %v", err)
	}
}

// TestPGLockBackendLeversAreComplementary pins what the aggregate seam could
// not (round-5 audit P2): the two server-side levers fail INDEPENDENTLY, and a
// dead protocol cancel alone must not fail termination — pg_terminate_backend
// is the confirmed lever.
func TestPGLockBackendLeversAreComplementary(t *testing.T) {
	pg := isolatedPG(t)
	ctx := context.Background()

	be, err := newPGLockBackend(pg.App, "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	defer be.close() //nolint:errcheck
	if err := be.ensure(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	held, err := be.tryLock(ctx)
	if err != nil || !held {
		t.Fatalf("tryLock: held=%v err=%v", held, err)
	}
	// Only the PROTOCOL lever fails; the SQL lever stays real.
	be.protocolCancel = func(context.Context, *sessionKill) error {
		return fmt.Errorf("injected: cancel unreachable")
	}
	be.mu.Lock()
	kill := be.kill
	be.mu.Unlock()
	if err := be.terminateSession(kill); err != nil {
		t.Fatalf("a failing protocol cancel must not fail termination while "+
			"pg_terminate_backend confirms it: %v", err)
	}
}

// TestPGElectorResignPropagatesTerminationFailure is the round-5 blocker made
// executable at the layer that used to lose it: with both levers failing, the
// error must survive pgElector.Resign itself — not only the backend call — so
// sqlStore.Close (which now joins Resign's return) can surface it.
func TestPGElectorResignPropagatesTerminationFailure(t *testing.T) {
	pg := isolatedPG(t)
	ctx := context.Background()
	cfg := store.Config{Engine: store.EnginePostgres, DSN: pg.App}

	a, err := newPGElector(cfg, nil)
	if err != nil {
		t.Fatalf("elector: %v", err)
	}
	a.poll = 200 * time.Millisecond
	if err := a.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !a.IsLeader() {
		t.Fatal("must lead on an isolated database")
	}
	be := a.backend.(*pgLockBackend)
	pinNoDeadClientCheck(t, be)
	be.protocolCancel = func(context.Context, *sessionKill) error {
		return fmt.Errorf("injected: cancel unreachable")
	}
	be.sqlTerminate = func(context.Context, uint32) (bool, error) {
		return false, fmt.Errorf("injected: terminate unreachable")
	}

	// Wedge a fence read so Resign's unlock takes the forced path.
	super, err := sql.Open("pgx", pg.Superuser)
	if err != nil {
		t.Fatalf("superuser pool: %v", err)
	}
	defer super.Close() //nolint:errcheck
	wedge, err := super.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("wedge tx: %v", err)
	}
	defer wedge.Rollback() //nolint:errcheck
	if _, err := wedge.ExecContext(ctx,
		"LOCK TABLE "+leaderEpochTable+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatalf("wedge lock: %v", err)
	}
	fenceErr := make(chan error, 1)
	go func() {
		_, ferr := a.FencedEpoch(ctx)
		fenceErr <- ferr
	}()
	blocked := eventuallyTrue(5*time.Second, func() bool {
		var n int
		if qerr := super.QueryRowContext(ctx,
			`SELECT count(*) FROM pg_locks WHERE relation = to_regclass($1) AND NOT granted`,
			leaderEpochTable).Scan(&n); qerr != nil {
			return false
		}
		return n > 0
	})
	if !blocked {
		t.Fatal("the fence read never blocked; the scenario did not arm")
	}

	rerr := a.Resign(ctx)
	if rerr == nil {
		t.Fatal("Resign swallowed a confirmed termination failure")
	}
	if !errors.Is(rerr, errLockSessionTerminationFailed) ||
		!strings.Contains(rerr.Error(), "injected: terminate unreachable") {
		t.Fatalf("Resign's return does not carry the termination failure: %v", rerr)
	}

	_ = wedge.Rollback()
	select {
	case <-fenceErr:
	case <-time.After(10 * time.Second):
		t.Fatal("the wedged fence read never returned after the wedge lifted")
	}
}

// eventuallyTrue polls fn until it returns true or the deadline passes. It is for
// the DSN-gated integration test only, where real Postgres timing is in play; the
// fast unit tests drive the state machine directly with no waiting.
func eventuallyTrue(within time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fn()
}

// TestLockPoolStampsTheFenceInTheEngineSchema is the regression for a defect found in
// round 5 of the X3 review, and for an incomplete fix of my own.
//
// bumpEpoch and verifyHeldEpoch address leaderEpochTable UNQUALIFIED, so whatever
// search_path the lock connection inherits decides where the durable fencing epoch is
// written and read. Pinning only the DDL connection that runs ensure() is not enough:
// measured on a split owner/app topology, the pinned owner created public.leader_epoch
// while this pool stamped the epoch into the app role's shadow —
// `lock_search_path="other, public" epoch=1 public_rows=0 shadow_rows=1`. A fence
// written where nobody verifies it is worse than no fence at all: leadership would look
// monotonic to a reader of the shadow and say nothing about the ledger.
//
// The role here is a DISPOSABLE per-test role, never the cluster-global olivares_app:
// ALTER ROLE ... SET search_path on a shared role changes behavior for every other
// suite running against the same server.
func TestLockPoolStampsTheFenceInTheEngineSchema(t *testing.T) {
	dsn, role := customRolePG(t)
	ctx := context.Background()

	// First backend just to create public.leader_epoch through the normal path.
	seedBE, err := newPGLockBackend(dsn, "")
	if err != nil {
		t.Fatalf("seed backend: %v", err)
	}
	if err := seedBE.ensure(ctx); err != nil {
		t.Fatalf("seed ensure: %v", err)
	}
	_ = seedBE.close()

	seed, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open seed connection: %v", err)
	}
	defer seed.Close() //nolint:errcheck
	for _, stmt := range []string{
		"CREATE SCHEMA other",
		"CREATE TABLE other.leader_epoch (LIKE public.leader_epoch INCLUDING ALL)",
		"ALTER ROLE " + role + " SET search_path = other, public",
	} {
		if _, err := seed.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed shadow fence (%s): %v", stmt, err)
		}
	}

	// A FRESH probe: ALTER ROLE ... SET applies at connection time, so the connection
	// that issued it still carries the old search_path and would test nothing.
	probe, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open post-ALTER probe: %v", err)
	}
	defer probe.Close() //nolint:errcheck
	var resolved string
	if err := probe.QueryRowContext(ctx, "SELECT current_schema()").Scan(&resolved); err != nil {
		t.Fatalf("read current_schema: %v", err)
	}
	if resolved != "other" {
		t.Fatalf("fixture precondition: an ordinary connection for this role should resolve to the shadow schema, got %q", resolved)
	}

	be, err := newPGLockBackend(dsn, "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	defer be.close() //nolint:errcheck
	if err := be.ensure(ctx); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	held, err := be.tryLock(ctx)
	if err != nil {
		t.Fatalf("tryLock: %v", err)
	}
	if !held {
		t.Fatal("lock not acquired on an isolated database — nothing else may hold it")
	}
	defer be.unlock(ctx) //nolint:errcheck
	if _, err := be.bumpEpoch(ctx); err != nil {
		t.Fatalf("bumpEpoch: %v", err)
	}

	var shadowRows, realRows int
	if err := probe.QueryRowContext(ctx, "SELECT count(*) FROM other.leader_epoch").Scan(&shadowRows); err != nil {
		t.Fatalf("count shadow fence rows: %v", err)
	}
	if err := probe.QueryRowContext(ctx, "SELECT count(*) FROM public.leader_epoch").Scan(&realRows); err != nil {
		t.Fatalf("count real fence rows: %v", err)
	}
	if shadowRows != 0 {
		t.Errorf("the elector stamped the durable fence into the SHADOW table (%d rows): the lock pool is following the role's search_path", shadowRows)
	}
	if realRows == 0 {
		t.Error("no fence row landed in the engine schema's leader_epoch")
	}
}

// TestTerminationLeverCannotBeShadowed is the regression for the last member of a class
// that produced three separate defects in this review: an unqualified name resolved on a
// connection whose search_path the application role controls.
//
// sqlTerminateBackend runs the plain driver on the app DSN by design, so it carries that
// role's search_path — and a path naming a writable schema ahead of pg_catalog is legal
// and settable by the role itself. A same-signature other.pg_terminate_backend returning
// true unconditionally forges the answer of the one lever that kills a session wedged on
// the leader advisory lock: the elector concludes the previous leader is gone while it is
// still alive and still holds the lock.
//
// Measured before the fix, against a PID that cannot exist: unqualified answers `t`,
// pg_catalog answers `f` with "PID 999999 is not a PostgreSQL backend process".
func TestTerminationLeverCannotBeShadowed(t *testing.T) {
	dsn, role := customRolePG(t)
	ctx := context.Background()

	seed, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open seed connection: %v", err)
	}
	defer seed.Close() //nolint:errcheck
	for _, stmt := range []string{
		"CREATE SCHEMA other",
		`CREATE FUNCTION other.pg_terminate_backend(integer, bigint) RETURNS boolean LANGUAGE sql AS $fn$ SELECT true $fn$`,
		// pg_catalog named EXPLICITLY and second: that is what stops it being searched
		// first implicitly, and it is the whole precondition of the attack.
		"ALTER ROLE " + role + " SET search_path = other, pg_catalog, public",
	} {
		if _, err := seed.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed the shadow lever (%s): %v", stmt, err)
		}
	}

	be, err := newPGLockBackend(dsn, "")
	if err != nil {
		t.Fatalf("backend: %v", err)
	}
	defer be.close() //nolint:errcheck

	// A PID no backend can hold. The real function reports false (with a warning); only
	// the shadow reports true.
	const impossiblePID = 999999
	terminated, err := be.sqlTerminateBackend(ctx, impossiblePID)
	if err != nil {
		t.Fatalf("sqlTerminateBackend: %v", err)
	}
	if terminated {
		t.Error("sqlTerminateBackend reported that it terminated a backend that cannot exist: the call resolved to a function the application role is able to define, so the fencing lever can be forged")
	}
}
