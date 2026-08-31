// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package ratelimit

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// Store is the shared-bucket backend seam: in an HA cluster the token
// buckets must be GLOBAL — N nodes each running the in-proc shards multiply
// every quota by N. Implementations live OUTSIDE this package (it stays pure;
// the Postgres one is core/api/ratelimit/pgstore); the interface is
// deliberately narrow and storage-agnostic so a Redis implementation can drop
// in WITHOUT touching the limiter if perf data ever demands it (the
// decision: no Redis in v1, but the seam must permit it).
//
// Take must preserve the in-proc invariants (each is pinned by a test):
// refill = min(burst, tokens + elapsed*rate) with elapsed clamped >= 0; a new
// bucket starts FULL; last-take advances on EVERY take (allowed or denied —
// the anti-reset-under-attack property); admission is all-or-nothing across
// reqs IN ORDER (a denial decrements nothing); states reports post-take tokens
// per req, same order.
type Store interface {
	Take(ctx context.Context, reqs []StoreRequest) (allowed bool, states []StoreState, err error)
}

// StoreRequest names one bucket and the limit governing it for this take.
// Limits are passed per-take, never persisted, so a re-quota applies
// immediately with no state migration (same property as the in-proc shards).
type StoreRequest struct {
	Key   string
	Limit Limit
}

// StoreState is one bucket's post-take state.
type StoreState struct {
	Tokens float64
}

// Failure posture (this changes the premise and is documented in the
// contract delta): with a store, the limiter HAS an external dependency. A
// store error or timeout falls back to the LOCAL in-proc shards — enforcement
// degrades to per-node (exactly the pre behavior, bounded, never
// "unlimited", so the fail-closed guarantee holds) — and never to a denial
// storm (a store blip must not 429 every request) nor to added latency on
// every request during a brownout. The fallback LATCHES (circuit breaker):
// after storeFailThreshold consecutive failures the store is skipped for
// storeCooldown, then a single take probes it half-open. Fallbacks are counted
// (olivares_http_ratelimit_store_fallback_total) and the breaker state is a
// gauge (olivares_http_ratelimit_store_up), so degraded mode is alertable,
// never silent.
const (
	// storeFailThreshold is the consecutive-failure count that opens the breaker.
	storeFailThreshold = 3
	// storeCooldown is how long an open breaker skips the store before probing.
	storeCooldown = 5 * time.Second
	// defaultStoreTimeout bounds one Take round trip. It is the breaker's trip
	// sensor: a store slower than this is treated as down for that take. Kept
	// well under the 300ms API p99 SLO so even the requests that trip the
	// breaker stay inside the latency budget.
	defaultStoreTimeout = 250 * time.Millisecond
)

// storeGate decides whether THIS take may try the shared store, returning the
// breaker state it observed (threaded to storeTake so a success can only close
// the breaker state it raced against — a stale slow success from before a
// fresh open must not clear it). States:
//   - closed (openUntil 0): proceed.
//   - open, not expired: stay local.
//   - open, expired: exactly ONE take wins the half-open probe via CAS — and
//     the CAS itself RE-EXTENDS the open window, so the losers stay local and
//     a failed probe leaves the breaker open with zero extra bookkeeping. A
//     hard-down store costs one stalled request per cooldown, not a stampede
//     of 250ms timeouts (the "never a per-request latency tax in a brownout"
//     promise in the contract delta).
func (l *Limiter) storeGate(now time.Time) (proceed bool, observed int64) {
	open := l.storeOpenUntil.Load()
	if open == 0 {
		return true, 0
	}
	if now.UnixNano() < open {
		return false, open
	}
	next := now.Add(storeCooldown).UnixNano()
	if l.storeOpenUntil.CompareAndSwap(open, next) {
		return true, next // this take is the half-open probe
	}
	return false, 0 // another take won the probe race
}

// storeTake runs one shared-store take with the bounded timeout, maintaining
// the breaker. ok=false means "use the local fallback" (already counted).
// observed is the breaker state storeGate saw (0 = it was closed).
func (l *Limiter) storeTake(ctx context.Context, now time.Time, reqs []req, observed int64) (bool, Decision, bool) {
	sreqs := make([]StoreRequest, len(reqs))
	for i, r := range reqs {
		sreqs[i] = StoreRequest{Key: r.key, Limit: r.lim}
	}
	tctx, cancel := context.WithTimeout(ctx, l.storeTimeout)
	defer cancel()
	allowed, states, err := l.store.Take(tctx, sreqs)
	if err != nil {
		// A dying PARENT context — canceled (client disconnect) OR expired (a
		// client-supplied RPC deadline) — is never evidence about store health:
		// fall back quietly without tripping or counting. Otherwise a tenant
		// sending tiny gRPC deadlines could open the breaker at will and degrade
		// every node to per-node quotas (×replicas). The 250ms tctx expiring
		// while the parent is still live remains the real trip sensor.
		if ctx.Err() != nil {
			return false, Decision{}, false
		}
		l.noteStoreFailure(now)
		if l.mStoreFallback != nil {
			l.mStoreFallback.Inc()
		}
		if l.warnStore.allow(now) {
			l.warn("ratelimit: shared store take failed; enforcing per-node until it recovers", err)
		}
		return false, Decision{}, false
	}
	l.storeFails.Store(0)
	// Close only the breaker state this take raced against: a slow success that
	// STARTED before a fresh open (a flapping brownout) must not clear it, or
	// the latch holds for milliseconds instead of the cooldown. observed==0
	// (closed) trivially CASes 0→0; a probe CASes its own extension away.
	l.storeOpenUntil.CompareAndSwap(observed, 0)
	if len(states) != len(reqs) {
		// A backend bug, not an outage: treat like a failure (fallback, counted).
		l.noteStoreFailure(now)
		if l.mStoreFallback != nil {
			l.mStoreFallback.Inc()
		}
		return false, Decision{}, false
	}
	tokens := make([]float64, len(states))
	lims := make([]Limit, len(reqs))
	for i := range states {
		tokens[i] = states[i].Tokens
		lims[i] = reqs[i].lim
	}
	return allowed, bindingFrom(tokens, lims, allowed), true
}

// noteStoreFailure counts a consecutive failure and opens the breaker at the
// threshold. The open window is absolute (now + cooldown); after it expires,
// storeGate elects a single half-open probe.
func (l *Limiter) noteStoreFailure(now time.Time) {
	if l.storeFails.Add(1) >= storeFailThreshold {
		l.storeOpenUntil.Store(now.Add(storeCooldown).UnixNano())
		l.storeFails.Store(0)
	}
}

// warn logs through the seam the constructor captured (nil-safe).
func (l *Limiter) warn(msg string, err error) {
	if l.logWarn != nil {
		l.logWarn(msg, err)
	}
}

// writeStoreUp emits the breaker-state gauge at scrape time: 1 while takes go
// to the shared store, 0 while the breaker is open (per-node fallback mode; it
// returns to 1 only when a half-open probe succeeds).
func (l *Limiter) writeStoreUp(w io.Writer) {
	const name = "olivares_http_ratelimit_store_up"
	up := 1
	if l.storeOpenUntil.Load() != 0 {
		up = 0
	}
	fmt.Fprintf(w, "# HELP %s Whether rate-limit takes are reaching the shared store (0 = circuit open, enforcing per-node).\n# TYPE %s gauge\n%s %d\n", name, name, name, up)
}

// logRate throttles a warn category (one per 10s) without depending on a
// logger type (the package stays pure; the closure is injected via Config).
type logRate struct {
	last atomic.Int64
}

func (t *logRate) allow(now time.Time) bool {
	n, last := now.Unix(), t.last.Load()
	if n-last < 10 {
		return false
	}
	return t.last.CompareAndSwap(last, n)
}
