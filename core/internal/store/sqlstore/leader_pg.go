// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// leaderLockName is the cluster-wide advisory-lock name. It is hashed to the
// bigint key Postgres advisory locks take, reusing the exact hashtextextended
// idiom the per-tenant audit append already uses (audit.go lockTenant) — only the
// scope differs: one fixed key for the whole cluster, not one per tenant. The
// ".v1" guards against a future change to the election protocol colliding with an
// in-flight old leader during a rolling upgrade.
const leaderLockName = "olivares.leader.v1"

// leaderEpochTable holds the single-row monotonic fencing epoch. It is
// cluster infrastructure, not tenant data, so it carries no row-level-security
// policy and is excluded from the tenant-isolation self-test.
const leaderEpochTable = "leader_epoch"

// defaultLeaderPoll is how often a follower retries acquisition and a leader
// re-checks it still holds the lock session. It is the upper bound on top of TCP
// keepalive for how long a standby waits before noticing it can take over.
const defaultLeaderPoll = 2 * time.Second

// resignDeadline bounds Resign when the caller supplies no deadline of its own.
// The production shutdown path does exactly that — sqlStore.Close passes
// context.Background() — and the fast-handoff contract (store.LeaderElector)
// cannot be honored by a resignation that waits indefinitely behind a wedged
// fence read on an alive-but-stuck server. Found by the 2026-07-26 fence-fix
// audit (an internal design note (not shipped)).
const resignDeadline = 5 * time.Second

// errLockSessionTerminationFailed marks the ONE failure a resignation must not
// swallow (round-5 audit P1): both server-side termination levers failed, so
// the advisory lock's release is bound only by the session keepalives — the
// caller shutting the store down has to see that, not a warning in a log it
// may not be reading. errors.Is-distinguishable from the ordinary forced-path
// note ("session busy past the deadline") that a SUCCESSFUL termination emits.
var errLockSessionTerminationFailed = errors.New("leader-election: server-side termination failed")

// lockBackend is the Postgres primitive the elector drives, abstracted so the
// state machine is unit-testable without a real database. The real implementation
// (pgLockBackend) holds a dedicated, keepalive-tuned session and a session-level
// advisory lock on it; a fake implementation in tests simulates contention and
// session death.
type lockBackend interface {
	// ensure creates the epoch table if absent (idempotent). Called once on Run.
	ensure(ctx context.Context) error
	// tryLock attempts to take the cluster lock on a FRESH dedicated session,
	// holding that session on success. Returns held=false (with the session closed)
	// when another node holds the lock. A non-nil error is a backend fault.
	tryLock(ctx context.Context) (held bool, err error)
	// bumpEpoch increments and returns the cluster epoch, on the held lock session,
	// under the lock. Called once per acquisition, after OnPromote succeeds.
	bumpEpoch(ctx context.Context) (uint64, error)
	// healthy proves the held lock session is still alive (and thus the lock still
	// held). A non-nil error means the session — and the lock — is gone.
	healthy(ctx context.Context) error
	// verifyHeldEpoch is the DURABLE fence read: it reads the persisted
	// cluster epoch/holder row ON THE HELD LOCK SESSION — so a dead session (a
	// lost lock) fails the read itself — and errors unless the recorded holder
	// is this backend. It returns the persisted epoch, which is the truth a
	// polling-lag local cache cannot provide.
	verifyHeldEpoch(ctx context.Context) (uint64, error)
	// unlock releases the lock and closes the held session. Idempotent: a no-op
	// when no session is held.
	unlock(ctx context.Context) error
	// close releases the backend's connection pool. Called on Resign.
	close() error
}

// pgElector is the Postgres session-advisory-lock leader elector. Exactly
// one node in the cluster holds the lock and is the active writer; the rest poll
// to take over. See store.LeaderElector for the contract and the CP-over-AP
// rationale.
type pgElector struct {
	backend lockBackend
	log     *slog.Logger
	poll    time.Duration

	mu            sync.Mutex
	onPromote     func(context.Context) error
	promoteCancel context.CancelFunc // non-nil while the promotion callback runs

	armed     atomic.Bool
	leader    atomic.Bool
	promoting atomic.Bool
	resigned  atomic.Bool
	epoch     atomic.Uint64

	cancel context.CancelFunc
	done   chan struct{}
}

var _ elector = (*pgElector)(nil)

// newPGElector builds the Postgres elector with the real backend: a dedicated,
// single-connection pool opened from the application DSN with aggressive TCP
// keepalives so a dead leader's lock session — and thus the lock — is reaped
// quickly. The pool is independent of the RLS-scoped application pool, so the
// keepalive tuning and the long-held lock session never affect request traffic.
func newPGElector(cfg store.Config, log *slog.Logger) (*pgElector, error) {
	if log == nil {
		log = slog.Default()
	}
	be, err := newPGLockBackend(cfg.DSN, cfg.OwnerDSN)
	if err != nil {
		return nil, err
	}
	return &pgElector{backend: be, log: log, poll: defaultLeaderPoll}, nil
}

func (e *pgElector) IsLeader() bool {
	return e.leader.Load() && !e.resigned.Load()
}

// Active reports externally established service leadership. A node stays
// non-active while its OnPromote bootstrap runs, so readiness, routing and public
// background loops cannot observe it as serving before that barrier completes.
// The store's private write gate deliberately has narrower bootstrap semantics;
// see active below.
func (e *pgElector) Active() bool {
	if e.resigned.Load() {
		return false
	}
	return !e.armed.Load() || e.leader.Load()
}

// active is the store-private write-gate predicate. It differs from public
// Active only during OnPromote: the callback must provision/bootstrap through
// Store.Mutate before leadership is published, but no public consumer may serve
// from that intermediate state.
func (e *pgElector) active() bool {
	if e.resigned.Load() {
		return false
	}
	return !e.armed.Load() || e.leader.Load() || e.promoting.Load()
}

func (e *pgElector) Epoch() uint64 { return e.epoch.Load() }

// FencedEpoch implements store.EpochFencer: the context-aware DURABLE
// leadership fence (review P1). Unlike Active()/Epoch() — which read
// process-local state that can lag reality by up to a poll tick after another
// node bumped the cluster epoch, or forever on a paused process — it verifies
// against Postgres: the persisted leader_epoch row is read ON THE HELD LOCK
// SESSION (verifyHeldEpoch), so a dead session fails the read itself and a
// stolen leadership shows up as a foreign holder/epoch. Unarmed (pre-Run /
// single-writer fallback) there is no lock session or cluster to verify, so it
// returns the local epoch — the historical embedded semantics.
func (e *pgElector) FencedEpoch(ctx context.Context) (uint64, error) {
	if e.resigned.Load() {
		return 0, fmt.Errorf("leader-election: resigned (shutting down); refusing the epoch fence")
	}
	if !e.armed.Load() {
		return e.epoch.Load(), nil
	}
	if !e.leader.Load() && !e.promoting.Load() {
		return 0, fmt.Errorf("leader-election: standby; refusing the epoch fence")
	}
	ep, err := e.backend.verifyHeldEpoch(ctx)
	if err != nil {
		return 0, fmt.Errorf("leader-election: durable epoch fence failed: %w", err)
	}
	return ep, nil
}

var _ store.EpochFencer = (*pgElector)(nil)

func (e *pgElector) OnPromote(fn func(context.Context) error) {
	e.mu.Lock()
	e.onPromote = fn
	e.mu.Unlock()
}

func (e *pgElector) onPromoteCb() func(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.onPromote
}

// Run arms the gate, performs the initial election, and starts the maintenance
// loop. It returns once the initial role (leader or follower) is known. An error
// from the initial promotion (the OnPromote bootstrap) aborts boot.
func (e *pgElector) Run(ctx context.Context) error {
	e.armed.Store(true)
	if err := e.backend.ensure(ctx); err != nil {
		return fmt.Errorf("leader-election: ensure epoch table: %w", err)
	}
	became, err := e.tryBecomeLeader(ctx)
	if err != nil {
		return err
	}
	// Install the loop UNDER e.mu with a resigned re-read in the same critical
	// section (round-4 audit P2): Resign reads e.cancel/e.done under the same
	// mutex after its CAS, so either this install aborts here, or Resign sees
	// the installed cancel/done pair and drains it — never a background loop
	// nobody cancels, and never an unsynchronized read of either field.
	loopCtx, cancel := context.WithCancel(context.Background())
	e.mu.Lock()
	if e.resigned.Load() {
		e.mu.Unlock()
		cancel()
		_ = e.backend.unlock(ctx)
		return fmt.Errorf("leader-election: resigned during startup; the maintenance loop was not started")
	}
	e.cancel = cancel
	e.done = make(chan struct{})
	loopDone := e.done
	e.mu.Unlock()
	go func() {
		defer close(loopDone)
		e.loopBody(loopCtx)
	}()
	if became {
		e.log.Info("leader-election: acquired leadership at boot (active writer)", "epoch", e.epoch.Load())
	} else {
		e.log.Info("leader-election: standby — another node is the active writer; /readyz reports not-ready so the load balancer drains this node until it is promoted")
	}
	return nil
}

// tryBecomeLeader takes the lock and, on success, runs the promotion bootstrap
// BEFORE advertising leadership so a node never serves as leader with a
// half-finished bootstrap. It is the single unit-testable promotion transition.
func (e *pgElector) tryBecomeLeader(ctx context.Context) (bool, error) {
	if e.resigned.Load() {
		return false, nil
	}
	held, err := e.backend.tryLock(ctx)
	if err != nil {
		return false, fmt.Errorf("leader-election: acquire: %w", err)
	}
	if !held {
		return false, nil
	}
	// Re-check AFTER acquiring: a Resign that ran between the entry check and
	// the acquisition must not let this node publish leadership (or hold the
	// lock) post-resignation — it would keep a standby from taking over while
	// this node reports inactive (round-2 audit P1, third interleaving).
	if e.resigned.Load() {
		_ = e.backend.unlock(ctx)
		return false, nil
	}
	// We hold the lock. Permit writes for the bootstrap (promoting), run it, and
	// only then advertise leadership. On any failure, drop the lock and stay a
	// follower so another node — or this one on the next tick — can try cleanly.
	e.promoting.Store(true)
	if cb := e.onPromoteCb(); cb != nil {
		// The callback runs under a ctx that Resign CANCELS: the real promotion
		// bootstrap writes through Store.System with no leadership gate (that is
		// deliberate for bootstrap), so without this a mid-promotion resignation
		// could release the lock while the callback keeps writing after the
		// handoff (round-3 audit, second P1). An idempotent write already in
		// flight at cancel time completes its statement and stops at the next —
		// a bounded, documented residual, not an indefinite window.
		pctx, pcancel := context.WithCancel(ctx)
		e.mu.Lock()
		if e.resigned.Load() {
			// Linearization point (round-4 audit P1): Resign re-reads
			// promoteCancel under this same mutex AFTER its resigned CAS, so
			// inside the critical section exactly one order exists — either
			// Resign already won (this branch: the callback never starts), or
			// the cancelador below is published and Resign will see and fire
			// it. No interleaving lets the callback run with a context nobody
			// can cancel.
			e.mu.Unlock()
			pcancel()
			e.promoting.Store(false)
			_ = e.backend.unlock(ctx)
			return false, nil
		}
		e.promoteCancel = pcancel
		e.mu.Unlock()
		perr := cb(pctx)
		e.mu.Lock()
		e.promoteCancel = nil
		e.mu.Unlock()
		pcancel()
		if perr != nil {
			e.promoting.Store(false)
			_ = e.backend.unlock(ctx)
			return false, fmt.Errorf("leader-election: promotion bootstrap failed (staying follower): %w", perr)
		}
	}
	// Re-check AFTER the callback too: a resignation during the bootstrap must
	// not bump the epoch or publish leadership on a session whose termination
	// has already begun.
	if e.resigned.Load() {
		e.promoting.Store(false)
		_ = e.backend.unlock(ctx)
		return false, nil
	}
	ep, eerr := e.backend.bumpEpoch(ctx)
	if eerr != nil {
		e.promoting.Store(false)
		_ = e.backend.unlock(ctx)
		return false, fmt.Errorf("leader-election: bump epoch: %w", eerr)
	}
	e.epoch.Store(ep)
	e.leader.Store(true)
	e.promoting.Store(false)
	return true, nil
}

// loopBody maintains leadership: a leader re-checks its lock session is alive
// each tick and steps down the instant it is not (so it stops writing before a
// standby takes over — no split brain); a follower retries acquisition. The
// caller owns closing the done channel (Run installs both under e.mu).
func (e *pgElector) loopBody(ctx context.Context) {
	t := time.NewTicker(e.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.tick(ctx)
		}
	}
}

// tick is one maintenance step, factored out so a unit test can drive the state
// machine deterministically against a fake backend without the wall clock.
func (e *pgElector) tick(ctx context.Context) {
	if e.resigned.Load() {
		return
	}
	if e.leader.Load() {
		// Bound the health check by the poll interval: its ctx is the loop's
		// (no deadline), so an unbounded PingContext wedged on an alive-but-
		// stuck server would jam the maintenance loop — and with it Resign's
		// wait on e.done — indefinitely. A session that cannot answer a ping
		// within one poll interval is not healthy by this elector's own
		// definition of liveness.
		hctx, cancel := context.WithTimeout(ctx, e.poll)
		err := e.backend.healthy(hctx)
		cancel()
		if err != nil {
			// Lost the lock session: step down BEFORE anything else so the write-gate
			// closes and /readyz drains this node. The lock is already gone server-side
			// (or will be when Postgres reaps the dead session), so a standby can take
			// over; we just stop pretending to be leader.
			e.leader.Store(false)
			_ = e.backend.unlock(ctx)
			e.log.Warn("leader-election: lost the lock session; stepped down to standby (a healthy node will take over)", "err", err)
		}
		return
	}
	if _, err := e.tryBecomeLeader(ctx); err != nil {
		e.log.Warn("leader-election: takeover attempt failed; will retry", "err", err)
	}
}

// Resign relinquishes leadership for a fast, clean handoff at graceful shutdown.
// It is idempotent: the composition root calls it for the fast handoff and the
// store's Close calls it again as a backstop; only the first call does the work.
//
// Resign is BOUNDED even when its caller is not: the production path is
// sqlStore.Close → Resign(context.Background()), so without a default deadline
// here a fence read wedged on an alive-but-stuck server (blocked behind a table
// lock, say) would hold the session semaphore and make graceful shutdown wait
// indefinitely. With the deadline, unlock's forced path terminates the wedged
// session's socket, which both frees the semaphore and releases the advisory
// lock server-side.
func (e *pgElector) Resign(ctx context.Context) error {
	if !e.resigned.CompareAndSwap(false, true) {
		return nil
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, resignDeadline)
		defer cancel()
	}
	// Stop a promotion bootstrap in flight (its writes run with no leadership
	// gate) and take the loop handles, all under the same mutex the installers
	// use — the linearization the round-4 audit demanded.
	e.mu.Lock()
	if e.promoteCancel != nil {
		e.promoteCancel()
	}
	cancel, done := e.cancel, e.done
	e.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-ctx.Done():
			// The maintenance loop is stuck mid-tick on the wedged session. Do
			// not wait: unlock's forced path below terminates that session,
			// which unwedges the tick; the loop then observes its canceled
			// context and exits on its own.
		}
	}
	// Unlock UNCONDITIONALLY, not only when this node believes it leads: a
	// session can own the advisory lock while e.leader is still false (tryLock
	// succeeded, promotion in flight), and gating on the flag left that session
	// — and the lock — alive past Resign (round-2 audit P1). unlock is
	// idempotent when nothing is held.
	e.leader.Store(false)
	uerr := e.backend.unlock(ctx)
	if uerr != nil && !errors.Is(uerr, errLockSessionTerminationFailed) {
		// An ordinary forced-path note (the session WAS terminated, the caller
		// just hit its deadline first) is operational noise, not a contract
		// failure: log it and move on.
		e.log.Warn("leader-election: unlock on resign failed (the lock releases when the terminated session dies server-side)", "err", uerr)
		uerr = nil
	}
	// close() then SEALS the backend: every remaining socket dies and no new
	// dial can create one, so even a session unlock could not see (b.conn
	// already detached by a wedged unlock, a dial completing mid-shutdown)
	// terminates here. A TERMINATION failure, on the other hand, must reach
	// the caller (round-5 audit P1): close still runs, and both errors travel.
	cerr := e.backend.close()
	return errors.Join(uerr, cerr)
}

// ---------------------------------------------------------------------------
// pgLockBackend — the real Postgres primitive.
// ---------------------------------------------------------------------------

type pgLockBackend struct {
	db     *sql.DB
	dsn    string // the app DSN; terminateSession's control connection uses it
	ddlDSN string // owner DSN under the split topology; the app DSN otherwise
	holder string

	// sess is the SESSION semaphore: it is held for the entire duration of
	// every operation that uses the held lock session — never just for the
	// pointer read. That distinction is the RAMA-E 2026-07-26 product panic:
	// a mutex that guarded only the b.conn pointer let N concurrent fence
	// reads interleave Query→Next→Close sequences on one pgx connection.
	// database/sql serializes each individual driver call (sql.go:3059) but
	// not the sequence, and pgx forbids concurrent use outright (pgx/v5
	// conn.go:65). The corrupted stream either failed the read — refusing
	// valid evidence claims as ledger_unavailable — or panicked the process:
	// database/sql sizes the scan destination from Columns() (sql.go:3062)
	// and pgx stdlib writes one entry per received field without re-checking
	// len(dest) (stdlib/sql.go:877-885).
	//
	// A channel, not a mutex, so waiters honor ctx cancellation: the fence
	// sits on the evidence-claim write path (store.ClaimEvidenceOperation →
	// FencedEpoch), and a request whose deadline passed must fail its own
	// claim rather than queue indefinitely behind a stalled health tick.
	sess chan struct{}

	// mu guards the b.conn POINTER only and is never held across I/O.
	// Lock order: sess before mu. unlock's forced path takes mu alone by
	// design — it must run precisely when sess is wedged by a stalled
	// operation, and its termination lever is the SOCKET (killer), not
	// sql.Conn.Close: Close waits for the in-flight operation and then
	// returns the physical connection to the pool (database/sql sql.go
	// Conn.close → releaseConn(nil)), which neither interrupts anything nor
	// releases the session advisory lock.
	mu   sync.Mutex
	conn *sql.Conn // the held lock session; nil when not holding the lock

	// kill is the SERVER-side identity of the held lock session, captured at
	// acquisition: the backend PID and pgx's protocol cancel handle. Round-3
	// audit (an internal design note (not shipped)):
	// closing the client socket does NOT bound server-side termination — with
	// client_connection_check_interval at its default 0, a backend blocked in
	// a query notices the dead socket only at its next socket interaction,
	// and the session advisory lock lives until SESSION end. Termination must
	// therefore be server-side: CancelRequest (processed by the postmaster
	// without a backend slot) plus pg_terminate_backend over a transient
	// control connection. Guarded by mu, like conn.
	kill *sessionKill

	// protocolCancel / sqlTerminate are the TWO server-side levers as
	// independently injectable seams (round-5 audit P2: an aggregate seam could
	// not fail one lever at a time, so it proved nothing about their
	// complementarity). Production wiring is set in newPGLockBackend.
	protocolCancel func(context.Context, *sessionKill) error
	sqlTerminate   func(context.Context, uint32) (bool, error)

	// killer owns every live TCP socket the lock pool ever dialed. After the
	// round-3 audit its role is narrowed to what a client-side lever can
	// honestly guarantee: killing local transports unwedges CLIENT operations,
	// and seal() is the barrier that no new session of a closed backend can be
	// created. It is NOT the session-termination mechanism — that is
	// terminateSession.
	killer *lockSessionDialer
}

// sessionKill carries what bounded server-side termination needs.
type sessionKill struct {
	pid    uint32
	cancel func(context.Context) error
}

// terminateSession ends the lock session ON THE SERVER, bounded:
//  1. protocol CancelRequest — the postmaster processes it without a backend
//     slot (works even with max_connections exhausted); a query waiting on a
//     lock is interruptible, so the wedged backend goes idle and then notices
//     its dead socket at the next read;
//  2. pg_terminate_backend over a transient control connection on the app DSN
//     (a role may terminate its own sessions) — kills the backend immediately
//     and releases the advisory lock regardless of what it was executing.
//
// The control connection deliberately uses the plain driver, NOT the sealed
// lock dialer: sealing must never block the very mechanism that terminates
// the session (the round-3 finding against the previous design).
func (b *pgLockBackend) terminateSession(kill *sessionKill) error {
	if kill == nil || kill.pid == 0 {
		return fmt.Errorf("%w: no captured session identity", errLockSessionTerminationFailed)
	}
	// Lever 1: protocol cancel, on its OWN budget so a stalled cancel dial can
	// never starve the confirmed lever below. A CancelRequest is unconfirmable
	// by protocol design; its error is carried, not trusted either way.
	cctx, ccancel := context.WithTimeout(context.Background(), time.Second)
	cancelErr := b.protocolCancel(cctx, kill)
	ccancel()
	// Lever 2: CONFIRMED termination on its own budget.
	tctx, tcancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer tcancel()
	terminated, err := b.sqlTerminate(tctx, kill.pid)
	if err != nil {
		return fmt.Errorf("%w: terminate backend %d: %v (protocol cancel: %v)", errLockSessionTerminationFailed, kill.pid, err, cancelErr)
	}
	if !terminated {
		return fmt.Errorf("%w: backend %d did not confirm termination within its budget (protocol cancel: %v)", errLockSessionTerminationFailed, kill.pid, cancelErr)
	}
	return nil
}

// sqlTerminateBackend is the production SQL lever: the two-argument
// pg_terminate_backend (PostgreSQL 14+, within the supported 15-18 range)
// waits up to the given milliseconds and reports whether the backend actually
// terminated — the one-argument form only reports that a signal was sent. The
// control connection uses the plain driver on the app DSN (a role may
// terminate its own sessions; the sealed lock dialer can never block this).
func (b *pgLockBackend) sqlTerminateBackend(ctx context.Context, pid uint32) (bool, error) {
	ctl, err := sql.Open("pgx", b.dsn)
	if err != nil {
		return false, fmt.Errorf("open termination control connection: %w", err)
	}
	defer ctl.Close() //nolint:errcheck
	var terminated bool
	// pg_catalog-QUALIFIED, and that is not style. This connection is deliberately the
	// plain driver on the app DSN, so it carries whatever search_path that role brings —
	// and a search_path naming a writable schema ahead of pg_catalog is legal and
	// settable by the role itself. A same-signature shadow costs nothing to write:
	//   CREATE FUNCTION other.pg_terminate_backend(integer, bigint)
	//     RETURNS boolean LANGUAGE sql AS $$ SELECT true $$;
	// Measured on 15.18 against a PID that cannot exist: the unqualified call answers
	// `t` while pg_catalog's answers `f` with "PID 999999 is not a PostgreSQL backend
	// process". This is the lever that kills a session wedged on the leader advisory
	// lock, so a forged `true` tells the elector the old leader is gone while it still
	// holds the lock — a fencing failure on the durable evidence fence, not a cosmetic
	// one.
	//
	// The pool is NOT given the search_path hook the other pools carry: this is the
	// emergency termination path, and it should not acquire a pre-flight statement that
	// can fail before the lever is pulled. Qualifying the name is sufficient here and
	// cannot fail.
	if err := ctl.QueryRowContext(ctx,
		"SELECT pg_catalog.pg_terminate_backend($1, 2000)", int64(pid)).Scan(&terminated); err != nil {
		return false, err
	}
	return terminated, nil
}

// lockSessionDialer wraps the keepalive-tuned dialer and tracks every live
// connection it produced. It exists for one call: killLive, the only lever
// that can interrupt an operation wedged on an ALIVE server (keepalives only
// bound dead-network stalls, and database/sql exposes no way to cancel another
// goroutine's in-flight driver call).
type lockSessionDialer struct {
	d      *net.Dialer
	mu     sync.Mutex
	sealed bool
	live   map[net.Conn]struct{}
}

func (l *lockSessionDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	c, err := l.d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	t := &trackedConn{Conn: c}
	t.untrack = func() {
		l.mu.Lock()
		delete(l.live, t)
		l.mu.Unlock()
	}
	l.mu.Lock()
	if l.sealed {
		// The backend is closing: a dial that completed after the seal's kill
		// sweep must not slip through and survive termination (round-2 audit,
		// SUSPECT: a snapshot is not a barrier). Registration is mu-serialized
		// with seal(), so every connection either lands in the sweep or dies
		// here.
		l.mu.Unlock()
		_ = c.Close()
		return nil, fmt.Errorf("leader-election: lock backend is closed; refusing a new session")
	}
	l.live[t] = struct{}{}
	l.mu.Unlock()
	return t, nil
}

// seal permanently refuses new dials, then force-closes every live socket.
// Called by close(): after seal returns, no session of this backend can exist
// beyond the ones already dying, and none can be created.
func (l *lockSessionDialer) seal() int {
	l.mu.Lock()
	l.sealed = true
	conns := make([]net.Conn, 0, len(l.live))
	for c := range l.live {
		conns = append(conns, c)
	}
	l.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	return len(conns)
}

// killLive force-closes every live socket this dialer produced and reports how
// many. Safe against concurrent dials and closes; a socket already closing is
// a no-op.
func (l *lockSessionDialer) killLive() int {
	l.mu.Lock()
	conns := make([]net.Conn, 0, len(l.live))
	for c := range l.live {
		conns = append(conns, c)
	}
	l.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	return len(conns)
}

// trackedConn removes itself from the dialer's live set exactly once, whether
// pgx closes it or killLive does.
type trackedConn struct {
	net.Conn
	once    sync.Once
	untrack func()
}

func (t *trackedConn) Close() error {
	t.once.Do(t.untrack)
	return t.Conn.Close()
}

// forceDiscard physically retires the session behind conn instead of returning
// it to the pool: Raw's contract is that returning driver.ErrBadConn marks the
// driver connection bad, so releaseConn closes it rather than pooling it
// (database/sql sql.go Conn.Raw → release(err)). This matters because a
// session-level advisory lock belongs to the SESSION, pg_advisory_lock is
// re-entrant per session, and pgx's ResetSession does not clear advisory locks
// on reuse (pgx v5.10.0 stdlib/sql.go ResetSession) — so a pooled ex-lock
// session would answer the next pg_try_advisory_lock with a stale extra lock
// count. A session whose lock state is not known clean is never pooled.
func forceDiscard(conn *sql.Conn) error {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	// Returning ErrBadConn from Raw already closes the handle and discards the
	// physical connection (grabConn's release closes the Conn on ErrBadConn),
	// so ErrConnDone here IS the success signal, not a failure.
	if err := conn.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
		return err
	}
	return nil
}

// acquireSession claims exclusive use of the lock session for one whole
// operation, or gives up when ctx does.
func (b *pgLockBackend) acquireSession(ctx context.Context) error {
	select {
	case b.sess <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *pgLockBackend) releaseSession() { <-b.sess }

// newPGLockBackend opens the lock pool from the application DSN. ownerDSN (the
// DDL/migrations role of the owner/app split, empty when not split) is kept for
// ensure(): under the split the app role deliberately has no CREATE on the
// schema, so the one-time epoch-table DDL must run as the owner.
func newPGLockBackend(dsn, ownerDSN string) (*pgLockBackend, error) {
	cfg, killer, err := lockConnConfig(dsn)
	if err != nil {
		return nil, err
	}
	ddlDSN := ownerDSN
	if ddlDSN == "" {
		ddlDSN = dsn
	}
	// Pinned like the store's own pools, and for a sharper reason: bumpEpoch and
	// verifyHeldEpoch address leaderEpochTable UNQUALIFIED, so an inherited search_path
	// decides where the durable fence is stamped and read. Pinning only the DDL
	// connection is not enough — measured on a split owner/app topology, the owner
	// created public.leader_epoch while this pool wrote the epoch to the app role's
	// shadow: `lock_search_path="other, public" epoch=1 public_rows=0 shadow_rows=1`.
	// A fence written where nobody verifies it is worse than no fence.
	db := stdlib.OpenDB(*cfg, stdlib.OptionAfterConnect(pinSearchPath))
	// Exactly one physical session backs the lock: a session-level advisory lock
	// lives on the connection that took it, and the same connection must be the one
	// we health-check and unlock. Never recycle it out from under the lock.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown"
	}
	b := &pgLockBackend{
		db:     db,
		dsn:    dsn,
		ddlDSN: ddlDSN,
		holder: fmt.Sprintf("%s/%d", host, os.Getpid()),
		sess:   make(chan struct{}, 1),
		killer: killer,
	}
	b.protocolCancel = func(ctx context.Context, kill *sessionKill) error {
		if kill.cancel == nil {
			return fmt.Errorf("no protocol cancel handle captured")
		}
		return kill.cancel(ctx)
	}
	b.sqlTerminate = b.sqlTerminateBackend
	return b, nil
}

// ensure creates the epoch table on a transient connection to the DDL DSN.
// This is the elector's only DDL: under the hardened owner/app split (docs/SECURITY-HARDENING.md
// §4, cloud Supabase/staging roles) the app role has no CREATE on the schema —
// running this on the app pool failed engine boot with SQLSTATE 42501
// (permission denied for schema public); found by the staging rehearsal.
// The owner's DEFAULT PRIVILEGES grant the app role DML on the created table,
// so the lock/epoch traffic itself stays on the app pool.
//
// It goes through the schema-pinned opener for the same reason the store's own pools
// do: leaderEpochTable is UNQUALIFIED, so an inherited search_path decides where this
// CREATE lands. The app pool that then reads and stamps the epoch is pinned to
// dialect.EngineSchema, so an unpinned DDL connection would build the durable evidence
// fence in one schema while the fence is consulted in another.
func (b *pgLockBackend) ensure(ctx context.Context) error {
	ddl, err := openPGPinnedToEngineSchema(b.ddlDSN, 0)
	if err != nil {
		return fmt.Errorf("open DDL connection: %w", err)
	}
	defer ddl.Close() //nolint:errcheck
	_, err = ddl.ExecContext(ctx, fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %s (id INTEGER PRIMARY KEY, epoch BIGINT NOT NULL, holder TEXT NOT NULL, acquired_at TEXT NOT NULL)`,
		leaderEpochTable))
	return err
}

func (b *pgLockBackend) tryLock(ctx context.Context) (bool, error) {
	if err := b.acquireSession(ctx); err != nil {
		return false, err
	}
	defer b.releaseSession()
	b.mu.Lock()
	already := b.conn != nil
	b.mu.Unlock()
	if already {
		return true, nil // already holding
	}
	conn, err := b.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	var got bool
	if err := conn.QueryRowContext(ctx,
		fmt.Sprintf("SELECT pg_catalog.pg_try_advisory_lock(pg_catalog.hashtextextended('%s', 0))", leaderLockName),
	).Scan(&got); err != nil {
		// The acquisition's outcome is UNKNOWN (the server may have granted the
		// lock before the read failed): never pool a session whose lock state is
		// not known clean.
		_ = forceDiscard(conn)
		return false, err
	}
	if !got {
		// Known clean — the session holds nothing — so pooling it keeps the
		// 2-second follower poll from redialing TCP+auth every tick.
		_ = conn.Close()
		return false, nil
	}
	// Capture the SERVER identity of this session while it is idle and sess is
	// held: terminateSession needs the PID and the protocol cancel handle, and
	// after this point the session may be busy whenever termination is wanted.
	// LIFETIME of the retained cancel handle (round-4 audit P2, decided): the
	// method value references the driver connection obtained inside Raw, which
	// database/sql says must not be used outside the callback. It is retained
	// anyway, deliberately: the driverConn stays CHECKED OUT to b.conn for the
	// handle's entire useful life — the pool cannot recycle or close it until
	// forceDiscard returns it, which happens strictly AFTER any use of the
	// handle — and reimplementing the cancel wire format would lose pgx's TLS
	// mirroring and unix-socket fallback. If this deviation is ever ruled out,
	// the fallback is dropping the cancel lever: pg_terminate_backend is the
	// confirmed one.
	kill := &sessionKill{}
	if rerr := conn.Raw(func(dc any) error {
		sc, ok := dc.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("unexpected driver connection %T", dc)
		}
		pg := sc.Conn().PgConn()
		kill.pid = pg.PID()
		kill.cancel = pg.CancelRequest
		return nil
	}); rerr != nil || kill.pid == 0 {
		// An acquisition whose server identity is unknown cannot be terminated
		// in bounded time later: refuse it NOW (round-4 audit — a capture
		// failure must fail the acquisition, never proceed silently).
		_ = forceDiscard(conn)
		if rerr == nil {
			rerr = fmt.Errorf("no backend pid reported")
		}
		return false, fmt.Errorf("leader-election: capture session identity: %w", rerr)
	}
	// Bound SERVER-side detection of a dead client: session-level TCP
	// keepalives mirror the client dialer's tuning, so even when BOTH
	// termination levers are unreachable (leader<->server partition) the
	// server kernel reaps the session — and releases the advisory lock — in
	// ~idle+interval*count rather than the OS default. USERSET on TCP
	// connections; PostgreSQL ignores them on unix sockets. The operator's
	// client_connection_check_interval is deliberately left untouched (the
	// previous revision pinned it to 0, degrading an operator-enabled check).
	for _, ka := range []string{
		"SET tcp_keepalives_idle = 5",
		"SET tcp_keepalives_interval = 2",
		"SET tcp_keepalives_count = 3",
	} {
		if _, err := conn.ExecContext(ctx, ka); err != nil {
			// The session is still IDLE here, so physically discarding it ends
			// it immediately server-side; no termination lever is needed.
			_ = forceDiscard(conn)
			return false, err
		}
	}
	b.mu.Lock()
	b.conn = conn
	b.kill = kill
	b.mu.Unlock()
	return true, nil
}

func (b *pgLockBackend) bumpEpoch(ctx context.Context) (uint64, error) {
	if err := b.acquireSession(ctx); err != nil {
		return 0, err
	}
	defer b.releaseSession()
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()
	if conn == nil {
		return 0, fmt.Errorf("leader-election: bumpEpoch without a held lock session")
	}
	var epoch int64
	err := conn.QueryRowContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (id, epoch, holder, acquired_at) VALUES (1, 1, $1, $2)
ON CONFLICT (id) DO UPDATE SET epoch = %s.epoch + 1, holder = $1, acquired_at = $2
RETURNING epoch`, leaderEpochTable, leaderEpochTable),
		b.holder, model.NewTimestamp(time.Now()).String(),
	).Scan(&epoch)
	if err != nil {
		return 0, err
	}
	return uint64(epoch), nil
}

func (b *pgLockBackend) healthy(ctx context.Context) error {
	if err := b.acquireSession(ctx); err != nil {
		return err
	}
	defer b.releaseSession()
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("leader-election: no held lock session")
	}
	// Verify the SESSION that holds the lock is alive (PingContext on the conn), not
	// merely that the pool can reach the server.
	return conn.PingContext(ctx)
}

// verifyHeldEpoch reads the persisted epoch/holder ON the held lock session and
// verifies the holder is this node. Running the read on that exact session is
// what makes this a fence: if Postgres reaped the session (and with it the
// advisory lock), the read fails; if another node acquired and bumped, the row
// names the other holder. No new mechanism — this is the same leader_epoch row
// bumpEpoch maintains under the lock.
func (b *pgLockBackend) verifyHeldEpoch(ctx context.Context) (uint64, error) {
	if err := b.acquireSession(ctx); err != nil {
		return 0, err
	}
	defer b.releaseSession()
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()
	if conn == nil {
		return 0, fmt.Errorf("leader-election: no held lock session")
	}
	var epoch int64
	var holder string
	if err := conn.QueryRowContext(ctx,
		fmt.Sprintf("SELECT epoch, holder FROM %s WHERE id = 1", leaderEpochTable),
	).Scan(&epoch, &holder); err != nil {
		return 0, fmt.Errorf("leader-election: read persisted epoch: %w", err)
	}
	if holder != b.holder {
		return 0, fmt.Errorf("leader-election: persisted holder is %q, not this node (%q): leadership moved", holder, b.holder)
	}
	return uint64(epoch), nil
}

func (b *pgLockBackend) unlock(ctx context.Context) error {
	if err := b.acquireSession(ctx); err != nil {
		// FORCED path: an operation holds the session past the caller's
		// deadline (wedged on an alive-but-stuck server, or shutdown expired).
		// sql.Conn.Close cannot force anything — it waits for the operation
		// and then POOLS the still-locked session — so the lever is the
		// transport: kill the socket(s). The server ends the session, which
		// releases the session-level advisory lock, and the stuck operation
		// fails immediately with a broken connection, releasing sess.
		b.mu.Lock()
		conn := b.conn
		kill := b.kill
		b.conn = nil
		b.kill = nil
		b.mu.Unlock()
		if conn == nil {
			return nil
		}
		// SERVER first (round-3 audit): cancel the in-flight query and terminate
		// the backend — that is what releases the advisory lock in bounded time.
		// Only then kill local transports to unwedge the client operation.
		termErr := b.terminateSession(kill)
		killed := b.killer.killLive()
		// Retire the handle in the background: forceDiscard waits for the
		// (now dying) operation to release the connection, then the pool
		// bookkeeping completes and the bad connection is physically dropped.
		// Never block THIS caller on it — the bound is the whole point.
		go func() { _ = forceDiscard(conn) }()
		if termErr != nil {
			// Both server-side levers failed (partition, denied, unconfirmed):
			// say so LOUDLY, and keep the sentinel REACHABLE by errors.Is — a
			// %v here stringified the chain and Resign could no longer tell a
			// termination failure from an ordinary forced-path note.
			return fmt.Errorf("leader-election: unlock forced: server-side termination FAILED: %w; killed %d local socket(s); the advisory lock releases when the server reaps the session via its keepalives (caller: %v)", termErr, killed, err)
		}
		return fmt.Errorf("leader-election: unlock forced: session busy past the deadline; backend terminated server-side and %d local socket(s) killed: %w", killed, err)
	}
	defer b.releaseSession()
	b.mu.Lock()
	conn := b.conn
	b.conn = nil
	b.kill = nil
	b.mu.Unlock()
	if conn == nil {
		return nil
	}
	// Best-effort explicit unlock for the fastest possible handoff, then retire
	// the session PHYSICALLY (forceDiscard): pooling an ex-lock session would
	// let the next acquisition reuse it re-entrantly with a stale lock count,
	// because pgx's ResetSession does not clear session advisory locks. The
	// discard also covers the failed-Exec case — a session whose unlock did not
	// provably run must die, and its death is what releases the lock.
	_, uerr := conn.ExecContext(ctx, fmt.Sprintf("SELECT pg_catalog.pg_advisory_unlock(pg_catalog.hashtextextended('%s', 0))", leaderLockName))
	cerr := forceDiscard(conn)
	if uerr != nil {
		return uerr
	}
	return cerr
}

// close terminates the backend UNCONDITIONALLY: seal the dialer (kill every
// live socket, refuse new dials — a barrier, not a snapshot) and then close the
// pool. This is what guarantees the advisory lock cannot survive Resign no
// matter which state the session was caught in: mid-promotion (the lock is
// owned while e.leader is still false, so Resign's unlock may see nothing to
// do), or mid-unlock (b.conn already detached while the explicit
// pg_advisory_unlock wedges). DB.Close alone closes only FREE connections — a
// borrowed session survives it, and with it the lock (round-2 audit P1).
func (b *pgLockBackend) close() error {
	// SERVER first, seal second — sealing before the cancel would refuse the
	// CancelRequest dial pgx issues on transport errors (round-3 finding), and
	// terminateSession's own control connection bypasses the sealed dialer by
	// construction.
	b.mu.Lock()
	kill := b.kill
	b.kill = nil
	b.mu.Unlock()
	var termErr error
	if kill != nil {
		// kill == nil is the NORMAL standby/idle close — nothing to terminate.
		termErr = b.terminateSession(kill)
	}
	_ = b.killer.seal()
	cerr := b.db.Close()
	if termErr != nil {
		return errors.Join(
			fmt.Errorf("leader-election: close: server-side termination FAILED: %w; the advisory lock releases when the server reaps the session via its keepalives", termErr),
			cerr)
	}
	return cerr
}

// lockConnConfig parses the lock DSN and installs the keepalive-tuned,
// session-tracking dialer. Split from newPGLockBackend so the
// no-server-rejected-params invariant is directly unit-testable. The returned
// dialer is the backend's kill switch (killLive) for bounded resignation.
func lockConnConfig(dsn string) (*pgx.ConnConfig, *lockSessionDialer, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("leader-election: parse lock DSN: %w", err)
	}
	ld := &lockSessionDialer{d: lockDialer(cfg.ConnectTimeout), live: make(map[net.Conn]struct{})}
	cfg.DialFunc = ld.DialContext
	return cfg, ld, nil
}

// lockDialer builds the dialer for the dedicated lock connection with aggressive
// TCP keepalives so a crashed leader's session — and thus the lock — is reaped
// fast, bounding hard-crash failover to a few seconds rather than the OS default
// (which can be minutes). Applied to the DEDICATED lock connection only, never
// the request-serving application pool.
//
// Keepalives MUST live on the dialer, not the DSN: the libpq keepalives_*
// parameters this used to append are not implemented by pgx — pgconn forwards
// unknown DSN parameters to the server as startup runtime parameters, and every
// PostgreSQL rejects those with FATAL 42704 `unrecognized configuration
// parameter "keepalives_interval"`, so the appended DSN broke leader election
// (and thus engine boot) against ANY Postgres reached over the pgx driver.
// connectTimeout carries a DSN connect_timeout through, since replacing DialFunc
// discards the dialer pgconn built from it. Keepalives apply to TCP only; Go
// silently skips them for unix-socket connections.
func lockDialer(connectTimeout time.Duration) *net.Dialer {
	return &net.Dialer{
		Timeout: connectTimeout,
		KeepAliveConfig: net.KeepAliveConfig{
			Enable:   true,
			Idle:     5 * time.Second,
			Interval: 2 * time.Second,
			Count:    3,
		},
	}
}
