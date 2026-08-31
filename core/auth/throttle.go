// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"sync"
	"time"
)

// throttle is an in-memory, per-key login attempt limiter with lockout. It is
// keyed by both account (email) and client IP, so neither a single account nor a
// single source can be brute-forced. It is bounded: expired entries are swept
// lazily so the map cannot grow without limit under a distributed attack.
//
// In-memory is the right scope for a single-node control plane; a distributed
// deployment would back this with a shared store, which the constructor leaves
// room for. Concurrency is a single mutex (login is not a hot path; the work
// behind it — argon2id — dwarfs lock contention).
type throttle struct {
	mu        sync.Mutex
	attempts  map[string]*attemptState
	max       int
	window    time.Duration
	now       func() time.Time
	lastSweep time.Time
}

type attemptState struct {
	fails       int
	lockedUntil time.Time
	seen        time.Time
}

// newThrottle returns a throttle that locks a key for window after max
// consecutive failures within window. now defaults to time.Now.
func newThrottle(maxFails int, window time.Duration, now func() time.Time) *throttle {
	if now == nil {
		now = time.Now
	}
	return &throttle{
		attempts: make(map[string]*attemptState),
		max:      maxFails,
		window:   window,
		now:      now,
	}
}

// allowed reports whether key may attempt now. If locked, it returns the
// remaining lockout duration.
func (t *throttle) allowed(key string) (bool, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.sweepLocked(now)
	st, ok := t.attempts[key]
	if !ok {
		return true, 0
	}
	if now.Before(st.lockedUntil) {
		return false, st.lockedUntil.Sub(now)
	}
	return true, 0
}

// fail records a failed attempt for key, locking it once max is reached.
func (t *throttle) fail(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.sweepLocked(now)
	st := t.attempts[key]
	if st == nil {
		st = &attemptState{}
		t.attempts[key] = st
	}
	st.fails++
	st.seen = now
	if st.fails >= t.max {
		st.lockedUntil = now.Add(t.window)
	}
}

// reset clears any failure state for key (called on a successful login).
func (t *throttle) reset(key string) {
	t.mu.Lock()
	delete(t.attempts, key)
	t.mu.Unlock()
}

// sweepLocked removes entries that are neither locked nor recently active. It
// runs at most once per window to keep allowed/fail O(1) amortized.
func (t *throttle) sweepLocked(now time.Time) {
	if now.Sub(t.lastSweep) < t.window {
		return
	}
	t.lastSweep = now
	for k, st := range t.attempts {
		if now.After(st.lockedUntil) && now.Sub(st.seen) > t.window {
			delete(t.attempts, k)
		}
	}
}
