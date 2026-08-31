// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/store"
)

// blockObserver attributes a BLOCKING wait from a second connection.
//
// It exists because the coordination lock's trick does not generalise. Coordination
// polls with pg_try_advisory_lock, so that connection is never blocked and can name
// its own holder between attempts. A DDL statement has no try-variant: CREATE
// TRIGGER either takes its lock or waits, and a waiting session can answer nothing.
// After it fails there is nothing left to ask either — pg_blocking_pids returns
// empty for a session that is no longer waiting, which is precisely what the
// refutation of the original D3 measured.
//
// So the only way to answer "who is blocking this DDL" is to ask from somewhere
// else, WHILE it is still blocked. That is this.
//
// Three properties are contractual, not incidental:
//
//   - It is started BEFORE the blocking statement is issued. Starting it after the
//     first timeout loses precisely the first holder, which is the one that matters.
//   - Its failure is NEVER the boot's failure. At the limit of max_connections a
//     non-superuser cannot open anything — and that is exactly the moment there IS
//     contention to observe. An observer that could refuse the boot would turn a
//     diagnostic aid into an outage.
//   - Its pool lives OUTSIDE cfg.MaxConns. The waiter has already taken the pool's
//     connection; competing with it for another would deadlock the diagnosis
//     against the thing it is diagnosing.
type blockObserver struct {
	pool *sql.DB
	stop chan struct{}
	done chan struct{}
	mu   sync.Mutex
	// poolClosed makes ownership of the pool unambiguous between the caller and an
	// abandoned goroutine. stopAndReport can walk away from a probe that is still in
	// flight, and whoever finishes last has to close the pool — without this flag both
	// paths either closed it twice or neither did, which leaks a connection per
	// abandoned observation on a boot path.
	poolClosed bool
	holders    []lockHolder
	degraded   error
}

// closePool closes the observer's pool exactly once, whoever gets there first.
func (o *blockObserver) closePool() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.pool != nil && !o.poolClosed {
		o.poolClosed = true
		_ = o.pool.Close() //nolint:errcheck // best effort; the observation is over
	}
}

// observerProbeInterval is how often the observer asks. Fast enough to catch a
// holder that releases early, slow enough not to add load to a database already
// under contention.
const observerProbeInterval = 250 * time.Millisecond

// observerProbeTimeout bounds one probe. Short: an observation that hangs is worse
// than one that is missing, because it delays the report of the wait it explains.
const observerProbeTimeout = 2 * time.Second

// observerStopTimeout bounds how long STOPPING may take.
//
// Deliberately far shorter than a probe: at this point the answer is either already
// gathered or not coming, and the caller is holding locks on a deadline. Waiting a
// full probe here would let the diagnostic spend the budget it exists to explain.
const observerStopTimeout = 100 * time.Millisecond

// observerDSN picks the connection the observer dials: the owner DSN when a split
// topology configures a distinct one, otherwise the application DSN.
//
// Same route, same database, same TLS and pooler as the waiter, on purpose: an
// observer that reached the server by a different path could report a state the
// waiter is not in. The admin pool is deliberately NOT reused — it is optional, it
// has a different privilege boundary, and it may be busy with the cross-tenant read
// that runs at the same point in boot.
func observerDSN(cfg store.Config) string {
	if owner := strings.TrimSpace(cfg.OwnerDSN); owner != "" && owner != cfg.DSN {
		return owner
	}
	return cfg.DSN
}

// startBlockObserver opens the observer's own single connection and begins probing
// who blocks waiterPID. It ALWAYS returns a usable value: when it cannot open, the
// returned observer is degraded and reports that, rather than failing the caller.
func startBlockObserver(ctx context.Context, dsn string, waiterPID int) *blockObserver {
	o := &blockObserver{stop: make(chan struct{}), done: make(chan struct{})}

	pool, err := openPGPinnedToEngineSchema(dsn, 1)
	if err != nil {
		o.degraded = fmt.Errorf("observation_unavailable: %w", err)
		slog.Warn("could not open the migration block observer; the wait will proceed without attribution",
			"err", err)
		close(o.done)
		return o
	}
	o.pool = pool

	go o.run(ctx, waiterPID)
	return o
}

func (o *blockObserver) run(ctx context.Context, waiterPID int) {
	// The goroutine closes the pool on its way out. When stopAndReport abandoned it
	// past the stop bound, this is the ONLY path that still can.
	//
	// ORDER MATTERS, and it was inverted. Go runs defers LIFO, so registering
	// close(o.done) last made it run FIRST: `done` — the signal that says "the
	// abandoned goroutine finished" — fired while the pool it was supposed to have
	// released was still open. Anyone waiting on `done` and then looking at the pool
	// was racing the cleanup it had just been told was over, and won only inside one
	// scheduler slice; under load it lost. Registering close(o.done) FIRST makes it
	// run LAST, so `done` now means what it says: the pool is already closed.
	defer close(o.done)
	defer o.closePool()

	// Probe IMMEDIATELY, before the first tick, for two reasons that are both the
	// point of this component.
	//
	// It is the earliest possible attribution, and the contract is that the holder
	// present when the wait began is the one an operator needs.
	//
	// And it is the only moment this can learn whether it can observe at all:
	// sql.Open is LAZY and does not dial, so a pool aimed at an unreachable server
	// opens perfectly happily. Without a probe here, an observer that can never
	// answer would report no degradation, and "I could not look" would be
	// indistinguishable from "nobody was blocking" — a much stronger claim.
	o.probe(ctx, waiterPID)

	t := time.NewTicker(observerProbeInterval)
	defer t.Stop()
	for {
		select {
		case <-o.stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			o.probe(ctx, waiterPID)
		}
	}
}

// probe asks once who blocks waiterPID and folds the answer in.
func (o *blockObserver) probe(ctx context.Context, waiterPID int) {
	// A deadline of its own, short and NOT derived from the caller's: the caller's
	// context is about to be canceled by the very timeout this observation
	// explains, and losing the answer at that moment would defeat the purpose.
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), observerProbeTimeout)
	defer cancel()
	hs, err := blockingSessions(pctx, o.pool, waiterPID)

	o.mu.Lock()
	defer o.mu.Unlock()
	if err != nil {
		o.degraded = fmt.Errorf("observation_unavailable: %w", err)
		return
	}
	// Keep the FIRST attribution rather than the latest: a later probe may catch a
	// different session that arrived meanwhile, and the question being answered is
	// who was blocking when the wait started.
	if len(hs) > 0 && len(o.holders) == 0 {
		o.holders = hs
	}
}

// stopAndReport ends the observation and returns what it saw, plus whether it was
// degraded. It closes the observer's pool: this connection exists for one wait.
//
// The wait for the probe loop is BOUNDED, and that is a correction. A probe in flight
// can take up to observerProbeTimeout, so joining unconditionally let the diagnostic
// spend up to two seconds of the very deadline it was promised never to affect — the
// observer charging the boot for the privilege of explaining it.
//
// Past the bound it abandons the goroutine rather than waiting. That is safe and
// bounded in turn: the probe has its own timeout and its own pool, so it finishes on
// its own and closes nothing this path still needs. The pool is left to the goroutine
// in that case, which is why the close below is conditional.
func (o *blockObserver) stopAndReport() ([]lockHolder, error) {
	select {
	case <-o.stop:
	default:
		close(o.stop)
	}
	select {
	case <-o.done:
	case <-time.After(observerStopTimeout):
		o.mu.Lock()
		defer o.mu.Unlock()
		// Report what was gathered so far. Abandoning the goroutine loses nothing: it
		// only ever writes under this mutex, and its own probe timeout bounds it.
		return o.holders, fmt.Errorf("observation_incomplete: the block observer did not stop within %s and was abandoned", observerStopTimeout)
	}
	o.closePool()
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.holders, o.degraded
}

// blockingSessions names the sessions that block pid RIGHT NOW, from a connection
// that is not itself blocked.
//
// pg_blocking_pids is the only source that answers CAUSALLY — these are the
// sessions whose locks this one is waiting on — and it is only truthful while the
// wait is in progress. Everything else available after the fact answers a weaker
// question ("who holds locks on that relation now"), which is a useful fallback and
// is not the same claim.
//
// Every catalog reference is qualified: this runs on a connection whose search_path
// an untrusted role could otherwise influence, and shadowing pg_blocking_pids would
// let an attacker choose the answer an operator sees.
// The nullable projection matters MORE here than it does for self-attribution. The
// observer's whole job is to describe OTHER sessions — a session blocking this one
// is by construction not this one — so under any restricted role every describable
// column is NULL on every row. Coalescing backend_start to '-infinity' therefore did
// not degrade this component, it disabled it: the scan failed, the probe recorded
// "observation_unavailable", and a wait that WAS attributable reported that nobody
// could be named.
func blockingSessions(ctx context.Context, db *sql.DB, pid int) ([]lockHolder, error) {
	const q = `
SELECT b.pid,
       ` + holderColumns + `
FROM pg_catalog.unnest(pg_catalog.pg_blocking_pids($1)) AS b(pid)
LEFT JOIN pg_catalog.pg_stat_activity a ON a.pid = b.pid
ORDER BY b.pid`

	rows, err := db.QueryContext(ctx, q, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // error surfaced via rows.Err below
	return scanHolders(rows)
}
