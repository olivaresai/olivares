// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"sync"
	"time"
)

// throttle is an in-memory, per-key login attempt limiter. It is keyed by both
// account (email) and client address, and it is the ONE place that decides what
// happens to a login attempt: the login path asks it once (decide) and reports
// one outcome (record). Both are a single locked step, which is what makes the
// bound below true rather than merely intended.
//
// WHY THE TWO KEYS ANSWER DIFFERENTLY. An account is one person's; refusing it
// denies that person and nobody else. A client address is shared — behind an
// ingress, a reverse proxy or a NAT gateway it is every user's address — so an
// address that refuses unconditionally refuses the whole deployment, the
// break-glass administrator included, on five anonymous failures.
//
// The answer is not to stop refusing, it is to refuse the right attempt. A
// tripped address still says no to the accounts the spray is walking: any account
// that has itself failed inside the window, or that already has an attempt in
// flight, gets nothing more from that address. What a tripped address may not do
// is refuse an account with a clean record; that attempt pays trippedPeerDelay
// and is then read normally. And a SUCCESS never clears an address: a stranger's
// correct password is evidence about their account, not about an address they
// share with everyone behind the same ingress — clearing it there would make the
// refusal weaker the busier the deployment is.
//
// WHAT BOUNDS THE KEY MAP. Refusals add nothing: they return before any write.
// Admissions do, and an e-mail string need not name a real account, so from one
// tripped address an attacker adds one entry per distinct string invented — about
// 900 per window in series, times however many requests they run in parallel, at
// roughly 150 bytes each. That growth is the price of not refusing a bystander;
// it is bounded by the delay and by the sweep, never by the set of accounts that
// exist. Rotating SOURCE addresses buys five more keys per address, unchanged
// from before; keying IPv6 by /64 is the open follow-up.
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
	// sleep is where a delayed attempt spends trippedPeerDelay, beside the injected
	// clock and injected the same way: nil means a real timer. newThrottle takes it
	// from sleepLoginDelay so the package's own tests are off the wall clock, and a
	// test that needs to hold attempts open replaces it on its own throttle.
	sleep func(ctx context.Context, d time.Duration) error
}

type attemptState struct {
	fails       int
	lockedUntil time.Time
	seen        time.Time
	// inFlightUntil marks an ACCOUNT whose admitted attempt has not reported its
	// outcome yet. It is what makes "one guess per account per window from a tripped
	// address" a bound and not an average: without it, every request that arrives
	// while the first is still hashing sees the same clean account and is admitted
	// too. It expires on its own so an attempt whose process died cannot deny its
	// own account for longer than reservationTTL.
	inFlightUntil time.Time
}

// loginVerdict is what the throttle says about one login attempt. The login path
// asks for one and acts on it; the reasoning lives here, not there.
type loginVerdict int

const (
	// loginAdmit reads the credential now.
	loginAdmit loginVerdict = iota
	// loginDelay waits out the returned duration first. The attempt is then read
	// exactly like any other — this is a price, never a refusal.
	loginDelay
	// loginRefuse answers ErrLockedOut without reading the credential.
	loginRefuse
)

// loginOutcome is what the login path reports back, exactly once, for every
// attempt the throttle did not refuse.
type loginOutcome int

const (
	// loginAbandoned is an attempt that reached no verdict on the credential: a
	// cancelled wait, a store error, a refusal by the enforcement policy, a session
	// that could not be minted. It neither accuses the account nor absolves it.
	loginAbandoned loginOutcome = iota
	// loginFailed is a credential that did not match.
	loginFailed
	// loginSucceeded is a credential that did.
	loginSucceeded
)

// trippedPeerDelay is what a tripped CLIENT ADDRESS costs a login for an account
// that has not itself failed.
//
// It is a price, and the honest limit is worth stating: it slows a serial walk
// down a list of accounts and costs a bystander a second. It is not what bounds
// the attack — the refusals above are, and they bound it by COUNT: one guess per
// account per window ONCE THE ADDRESS HAS TRIPPED, which the reservation keeps
// true however many requests run at once. Against a FRESH address nothing is
// reserved, because nothing is refused there either (decide admits and writes
// nothing), so C parallel attempts on one clean account are all read, up to the
// fifth failure that trips the address. That opening burst is what this throttle
// has always allowed; the reservation closes the window after it, not before.
const trippedPeerDelay = time.Second

// reservationTTL is how long an admitted attempt may hold its account before the
// throttle assumes the attempt is gone. It must outlast a delay plus a store read
// plus an argon2id verification by a wide margin, because it is a backstop for a
// process that died and never the control itself: record releases the reservation
// on every ordinary return.
const reservationTTL = time.Minute

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
		sleep:    sleepLoginDelay,
	}
}

// decide is the ONE question the login path asks. It reads both keys together,
// returns what to do with the attempt and the price when that is a delay, and —
// in the same locked step — records the admission, so the count below is a bound
// and not a hope:
//
//	account key                  address key    verdict
//	locked out                   any            refuse   — this account's own five failures
//	failed in window, or         locked         refuse   — the spray is walking this account
//	  an attempt in flight
//	failed in window             not locked     admit    — an ordinary retry after a typo
//	clean                        locked         delay    — a bystander behind the address
//	clean                        not locked     admit
//
// It reads no account of record and cannot: an attempt that names an address no
// account holds is judged by the same two keys as any other, so the verdict says
// nothing about whether an account exists. That is deliberate — the login path
// spends argon2id time on an unknown email for the same reason.
func (t *throttle) decide(accountKey, addressKey string) (loginVerdict, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.sweepLocked(now)

	account := t.attempts[accountKey]
	if account != nil && now.Before(account.lockedUntil) {
		return loginRefuse, 0
	}
	address := t.attempts[addressKey]
	if address == nil || !now.Before(address.lockedUntil) {
		return loginAdmit, 0
	}
	if account != nil && (now.Before(account.inFlightUntil) ||
		(account.fails > 0 && now.Sub(account.seen) <= t.window)) {
		return loginRefuse, 0
	}
	t.entryLocked(accountKey).inFlightUntil = now.Add(reservationTTL)
	return loginDelay, trippedPeerDelay
}

// record is the ONE outcome the login path reports, for every attempt decide did
// not refuse. It releases the admission the same way decide took it — under the
// lock — and applies the outcome:
//
//	failed     both keys count one more failure, and lock at max
//	succeeded  the ACCOUNT key is cleared; the address key is left alone
//	abandoned  neither key moves
//
// The address key is deliberately untouched by a success. A correct password is
// evidence about the account that gave it and none at all about an address shared
// with strangers, and clearing it there would let the deployment's own ordinary
// logins un-trip a refusal a spray had earned — the busier the deployment, the
// weaker the control. The address ages out by its own window instead.
func (t *throttle) record(accountKey, addressKey string, verdict loginVerdict, outcome loginOutcome) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()

	if verdict == loginDelay {
		if st := t.attempts[accountKey]; st != nil {
			st.inFlightUntil = time.Time{}
		}
	}
	switch outcome {
	case loginFailed:
		t.failLocked(accountKey, now)
		t.failLocked(addressKey, now)
	case loginSucceeded:
		delete(t.attempts, accountKey)
	}
}

// failLocked counts one failure against key. The count is never handed back while
// the key stays warm: a failure that lands inside the window re-arms the lockout
// instead of starting a fresh five. Only the sweep hands a count back, and only
// after a full quiet window.
func (t *throttle) failLocked(key string, now time.Time) {
	st := t.entryLocked(key)
	st.fails++
	st.seen = now
	if st.fails >= t.max {
		st.lockedUntil = now.Add(t.window)
	}
}

// entryLocked returns key's state, creating it if this is the first thing the
// throttle has had to remember about it.
func (t *throttle) entryLocked(key string) *attemptState {
	st := t.attempts[key]
	if st == nil {
		st = &attemptState{}
		t.attempts[key] = st
	}
	return st
}

// sweepLocked removes entries that are neither locked, nor holding an admitted
// attempt, nor recently active. It runs at most once per window, so decide and
// record stay O(1) amortized.
//
// It is also the only path that hands a count back: a key quiet for a whole
// window is forgotten, so the next failure against it starts a fresh five. That
// is the rate this throttle has always had, and this change does not alter it —
// the alternative, remembering every key that ever failed, is what the sweep
// exists to prevent.
func (t *throttle) sweepLocked(now time.Time) {
	if now.Sub(t.lastSweep) < t.window {
		return
	}
	t.lastSweep = now
	for k, st := range t.attempts {
		if now.After(st.lockedUntil) && now.Sub(st.seen) > t.window && !now.Before(st.inFlightUntil) {
			delete(t.attempts, k)
		}
	}
}

// sleepLoginDelay is the binary-wide default for a throttle's sleep field.
// Production leaves it nil, so a delayed login waits on a real timer; the
// package's own tests install one that returns at once, so no test pays a
// production delay on the wall clock — a suite that really sleeps through it is
// slower for no signal, and it makes the duration a hidden input to every test
// that logs in. Same shape as the test seam accounts.go keeps for the bootstrap
// race.
var sleepLoginDelay func(ctx context.Context, d time.Duration) error

// waitOut spends d, or gives up early if the caller has gone away — a login the
// client is no longer waiting for must not hold a goroutine for the full delay,
// and the abandonment is returned so it can be counted rather than lost.
//
// What it holds meanwhile is one goroutine, its connection and a timer, for d,
// with no transaction and no lock held. Nothing in the engine caps how many of
// those there may be at once: /v1/auth/login is anonymous and unmetered, so the
// count is whatever the deployment's edge admits. The bound worth naming is the
// comparison — every attempt that waits here goes on to an argon2id verification
// whose memory cost dwarfs a parked goroutine, so this adds no order of magnitude
// to what an unmetered login endpoint already exposes.
func (t *throttle) waitOut(ctx context.Context, d time.Duration) error {
	if t.sleep != nil {
		return t.sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
