// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"testing"
	"time"
)

// The package's tests never spend the tripped-address delay on the wall clock.
// It is installed for the whole test binary, so the login tests in the external
// test package are off the clock too, and it keeps the cancellation semantics of
// the real wait so an abandoned attempt still reports itself.
func init() {
	sleepLoginDelay = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
}

// armAddress is the address key the arming helpers write to and no assertion
// reads; quietAddress is never written at all, for an assertion that wants to
// read an account key with no address in the way.
const (
	armAddress   = "ip:192.0.2.9"
	quietAddress = "ip:0.0.0.0"
)

// TestThrottleDecidesFromBothKeysTogether walks the decision table. The two keys
// are one decision, and which of them may say no is the whole shape of the
// control: an account is one person's, so refusing it denies that person alone,
// while a client address is shared by everyone behind a proxy. A tripped address
// therefore refuses only the accounts the spray is already walking — including
// one whose admitted attempt has not reported its outcome yet — and charges
// everybody else instead of turning them away.
func TestThrottleDecidesFromBothKeysTogether(t *testing.T) {
	const (
		account = "email:member@example.test"
		address = "ip:203.0.113.1"
		decoy   = "email:decoy@example.test"
	)
	lockAccount := func(tr *throttle) {
		for i := 0; i < 5; i++ {
			tr.record(account, armAddress, loginAdmit, loginFailed)
		}
	}
	warmAccount := func(tr *throttle) { tr.record(account, armAddress, loginAdmit, loginFailed) }
	tripAddress := func(tr *throttle) {
		for i := 0; i < 5; i++ {
			tr.record(decoy, address, loginAdmit, loginFailed)
		}
	}
	admitOne := func(tr *throttle) { tr.decide(account, address) }

	cases := []struct {
		name string
		arm  []func(*throttle)
		want loginVerdict
	}{
		{
			name: "nothing has failed",
			want: loginAdmit,
		},
		{
			name: "the account spent its own five failures",
			arm:  []func(*throttle){lockAccount},
			want: loginRefuse,
		},
		{
			name: "the account is locked and the address has tripped as well",
			arm:  []func(*throttle){lockAccount, tripAddress},
			want: loginRefuse,
		},
		{
			name: "the address has tripped and this account already failed from it",
			arm:  []func(*throttle){warmAccount, tripAddress},
			want: loginRefuse,
		},
		{
			name: "the address has tripped and this account has an attempt in flight",
			arm:  []func(*throttle){tripAddress, admitOne},
			want: loginRefuse,
		},
		{
			name: "the account failed once but no address has tripped",
			arm:  []func(*throttle){warmAccount},
			want: loginAdmit,
		},
		{
			name: "the address has tripped and this account has a clean record",
			arm:  []func(*throttle){tripAddress},
			want: loginDelay,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := newThrottle(5, 15*time.Minute, nil)
			for _, arm := range tc.arm {
				arm(tr)
			}
			got, wait := tr.decide(account, address)
			if got != tc.want {
				t.Fatalf("verdict = %d, want %d", got, tc.want)
			}
			wantWait := time.Duration(0)
			if tc.want == loginDelay {
				wantWait = trippedPeerDelay
			}
			if wait != wantWait {
				t.Fatalf("price = %s, want %s", wait, wantWait)
			}
		})
	}
}

// TestATrippedAddressAdmitsOneCleanAttemptAtATime is the count bound read at the
// seam that has to hold it. Deciding and recording the admission must be the same
// locked step, or every request that arrives while the first is still hashing sees
// the same clean account and is let through as well.
func TestATrippedAddressAdmitsOneCleanAttemptAtATime(t *testing.T) {
	tr := newThrottle(5, 15*time.Minute, nil)
	const (
		account = "email:member@example.test"
		address = "ip:203.0.113.1"
		decoy   = "email:decoy@example.test"
	)
	for i := 0; i < 5; i++ {
		tr.record(decoy, address, loginAdmit, loginFailed)
	}

	admitted := 0
	for i := 0; i < 8; i++ {
		if v, _ := tr.decide(account, address); v != loginRefuse {
			admitted++
		}
	}
	if admitted != 1 {
		t.Fatalf("%d of 8 attempts were admitted with one already in flight, want 1", admitted)
	}

	// Reporting the outcome releases the admission — and the account is warm now,
	// so the next attempt is refused for its own failure rather than for the one
	// that was in flight.
	tr.record(account, address, loginDelay, loginFailed)
	if v, _ := tr.decide(account, address); v != loginRefuse {
		t.Fatalf("an account that failed from a tripped address was admitted again: verdict = %d, want %d", v, loginRefuse)
	}
}

// TestASuccessLeavesTheAddressKeyAlone pins the rule that keeps the refusal alive
// in a busy deployment. Everyone behind one ingress shares its address, so a
// correct password is evidence about the account that gave it and none at all
// about the address — clearing the address there would let ordinary traffic
// un-trip a refusal a spray had earned, and the busier the deployment the faster.
func TestASuccessLeavesTheAddressKeyAlone(t *testing.T) {
	tr := newThrottle(5, 15*time.Minute, nil)
	const (
		address = "ip:203.0.113.2"
		decoy   = "email:decoy@example.test"
		member  = "email:member@example.test"
		another = "email:another@example.test"
	)
	for i := 0; i < 5; i++ {
		tr.record(decoy, address, loginAdmit, loginFailed)
	}
	if v, _ := tr.decide(member, address); v != loginDelay {
		t.Fatalf("a clean account behind a tripped address: verdict = %d, want %d", v, loginDelay)
	}
	tr.record(member, address, loginDelay, loginSucceeded)

	if v, _ := tr.decide(another, address); v != loginDelay {
		t.Fatalf("a successful login cleared the address a spray had tripped: verdict = %d, want %d", v, loginDelay)
	}
}

// TestThrottleHandsACountBackOnlyAfterAFullQuietWindow states, and holds, what a
// caller who waits a lockout out actually gets. While the key stays warm the
// lockout re-arms on a single failure; once a whole window passes with none, the
// sweep forgets the key and the next five failures are needed again. That is the
// rate this throttle has always had — the alternative, remembering every key that
// ever failed, is what the sweep exists to prevent.
func TestThrottleHandsACountBackOnlyAfterAFullQuietWindow(t *testing.T) {
	const window = 15 * time.Minute
	const (
		account = "email:sprayed@example.test"
		address = "ip:198.51.100.7"
	)

	t.Run("a failure on a warm key re-arms the lockout at once", func(t *testing.T) {
		now := time.Now()
		tr := newThrottle(5, window, func() time.Time { return now })
		for i := 0; i < 5; i++ {
			tr.record(account, address, loginAdmit, loginFailed)
		}
		if v, _ := tr.decide(account, quietAddress); v != loginRefuse {
			t.Fatalf("five failures did not lock the account: verdict = %d, want %d", v, loginRefuse)
		}
		now = now.Add(window) // the lockout has just lapsed; the key is exactly a window old
		if v, _ := tr.decide(account, quietAddress); v != loginAdmit {
			t.Fatalf("the lockout outlived its window: verdict = %d, want %d", v, loginAdmit)
		}
		tr.record(account, address, loginAdmit, loginFailed)
		if v, _ := tr.decide(account, quietAddress); v != loginRefuse {
			t.Fatalf("one failure on a warm key bought four more guesses: verdict = %d, want %d", v, loginRefuse)
		}
	})

	t.Run("a whole quiet window hands the count back, by the sweep", func(t *testing.T) {
		now := time.Now()
		tr := newThrottle(5, window, func() time.Time { return now })
		for i := 0; i < 5; i++ {
			tr.record(account, address, loginAdmit, loginFailed)
		}
		now = now.Add(window + time.Nanosecond) // one instant past the boundary
		for i := 0; i < 5; i++ {
			if v, _ := tr.decide(account, quietAddress); v != loginAdmit {
				t.Fatalf("guess %d of a fresh five: verdict = %d, want %d", i, v, loginAdmit)
			}
			tr.record(account, address, loginAdmit, loginFailed)
		}
		if v, _ := tr.decide(account, quietAddress); v != loginRefuse {
			t.Fatalf("a fresh five failures did not lock the account again: verdict = %d, want %d", v, loginRefuse)
		}
	})
}

// TestTheTrippedAddressDelayIsNotSpentOnTheWallClock keeps the package's own
// tests off the clock. The delay a tripped address charges is a production
// control, not something a test may pay for: a suite that really sleeps through
// it is slower for no signal, and it makes the delay's duration a hidden input
// to every test that logs in. The engine injects its clock; the wait is injected
// beside it.
func TestTheTrippedAddressDelayIsNotSpentOnTheWallClock(t *testing.T) {
	tr := newThrottle(5, 15*time.Minute, nil)
	start := time.Now()
	if err := tr.waitOut(context.Background(), trippedPeerDelay); err != nil {
		t.Fatalf("waiting out the tripped-address delay: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= trippedPeerDelay {
		t.Fatalf("the delay was spent on the wall clock (%s); no test may wait for it", elapsed)
	}
}
