// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// migrationbudgetclock_test.go closes the last residual the acquisition budget carried, and the
// residual was a mismatch rather than a gap.
//
// A lockBudget takes its clock as an argument, so a test can hold time still and measure the
// retry loop's arithmetic instead of the host's scheduling. Its per-roundtrip contexts, however,
// were derived with context.WithTimeout — which runs on REAL time. A budget whose deadline sat
// at the Unix epoch plus five seconds therefore handed every roundtrip a REAL five-second
// deadline, and under load the roundtrips outran it: the fixture failed saying its callback was
// never reached, which is true and points at the wrong thing. Driving the clock narrowed the
// window and could not close it, because the real time never entered through the clock.
//
// The derivation is injected now (budgetTimer), and this file is the clock that pairs with it.

// drivenClock is a clock that moves only when a test moves it, TOGETHER with the timer that
// derives contexts from it.
//
// The two are one type on purpose. A driven clock handed to a budget that still derives its
// deadlines from the host is the exact pairing this closes, and keeping them apart is what let
// that pairing look correct for as long as it did.
type drivenClock struct {
	mu      sync.Mutex
	now     time.Time
	pending []*drivenDeadline
}

func newDrivenClock(at time.Time) *drivenClock { return &drivenClock{now: at} }

// Now reads the clock; it is the func a lockBudget takes.
func (c *drivenClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// advance moves the clock and expires every context whose deadline it has passed.
func (c *drivenClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var due []*drivenDeadline
	kept := c.pending[:0]
	for _, p := range c.pending {
		if p.deadline.After(now) {
			kept = append(kept, p)
			continue
		}
		due = append(due, p)
	}
	c.pending = kept
	c.mu.Unlock()
	// Expired OUTSIDE the lock: whatever the close wakes may read this clock, and holding it
	// across that is how a fixture deadlocks in a way that looks like the code under test.
	for _, p := range due {
		p.expire(context.DeadlineExceeded)
	}
}

// after is the budgetTimer.
//
// A duration that is already spent yields a context already past its deadline, which is what
// context.WithTimeout does with the same input — the production and the driven timer answer the
// spent case identically, and TestASpentBudgetHandsBackAnExpiredContext asserts it of both.
func (c *drivenClock) after(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	dl := &drivenDeadline{Context: parent, done: make(chan struct{})}
	c.mu.Lock()
	dl.deadline = c.now.Add(d)
	due := !dl.deadline.After(c.now)
	if !due {
		c.pending = append(c.pending, dl)
	}
	c.mu.Unlock()

	cancel := func() { dl.expire(context.Canceled) }
	if due {
		dl.expire(context.DeadlineExceeded)
		return dl, cancel
	}
	// The watcher ends when the context does, by any route, so a canceled context leaves no
	// goroutine behind. A canceled entry stays in `pending` until an advance walks past it,
	// which costs a slice slot in a test and cannot change an answer: expire keeps the FIRST
	// reason, exactly as context does.
	go func() {
		select {
		case <-parent.Done():
			dl.expire(parent.Err())
		case <-dl.done:
		}
	}()
	return dl, cancel
}

// drivenDeadline is a context whose expiry the test decides and whose Err is DeadlineExceeded.
//
// The error matters as much as the timing: budgetSpent routes on context.DeadlineExceeded to
// tell "we ran out of our own time" from "the operator stopped us", so a fake that ended in
// context.Canceled would exercise the other branch and prove nothing about this one.
type drivenDeadline struct {
	context.Context
	deadline time.Time
	done     chan struct{}
	mu       sync.Mutex
	err      error
}

func (d *drivenDeadline) Deadline() (time.Time, bool) { return d.deadline, true }
func (d *drivenDeadline) Done() <-chan struct{}       { return d.done }

func (d *drivenDeadline) Err() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}

// expire ends the context once; the first reason wins, which is what context itself guarantees.
func (d *drivenDeadline) expire(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return
	}
	d.err = err
	close(d.done)
}

// TestTheBudgetsRoundtripDeadlineRunsOnTheBudgetsOwnClock is the property that could not be
// asserted while the derivation was hard-wired.
//
// THE DISCRIMINATING ASSERTION IS THE FIRST ONE AND IT COSTS NO WAITING. With
// context.WithTimeout the derived deadline is the HOST's now plus an hour, which is not the
// epoch plus an hour on any machine this will ever run on. The rest — that a real wait moves
// nothing, that a nanosecond short of the budget is still alive, that a nanosecond past it is
// not — is the behavior that assertion implies, exercised rather than assumed.
func TestTheBudgetsRoundtripDeadlineRunsOnTheBudgetsOwnClock(t *testing.T) {
	const budget = time.Hour
	start := time.Unix(0, 0)
	clock := newDrivenClock(start)
	b := newLockBudget(budget, clock.Now, sleepCtx, jitterFloat).withTimer(clock.after)

	ctx, cancel := b.context(context.Background())
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("the derived context carries no deadline, so nothing bounds a roundtrip that hangs")
	}
	if want := start.Add(budget); !deadline.Equal(want) {
		t.Fatalf("the roundtrip deadline is %s where the budget's own clock puts it at %s: the context is measuring the host's time, not the budget's",
			deadline.UTC(), want.UTC())
	}

	select {
	case <-ctx.Done():
		t.Fatal("the context expired while the budget's clock still had an hour left")
	case <-time.After(20 * time.Millisecond):
	}

	clock.advance(budget - time.Nanosecond)
	select {
	case <-ctx.Done():
		t.Fatal("the context expired one nanosecond before the budget was spent")
	default:
	}

	clock.advance(time.Nanosecond)
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the budget's clock passed its deadline and the derived context is still alive")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("a spent budget ended the roundtrip with %v, and budgetSpent routes on context.DeadlineExceeded to tell that apart from an operator's cancellation",
			ctx.Err())
	}
}

// TestTheUnitWorkDeadlineRunsOnTheBudgetsOwnClock is the HALF the test above did not reach, and
// round twenty is why it exists.
//
// The injection was made in both derivations — b.context and b.unitWorkContext — but only the
// first had a regression. Reverting unitWorkContext alone to context.WithTimeout left the whole
// suite green, so the arrangement that bounds a unit's post-lock work was guarded by nothing: a
// callback would have been bounded by the HOST while its acquisition was bounded by the budget,
// which is the exact mismatch migrationunit.go:1193-1197 says it closed.
//
// THE DISCRIMINATING ASSERTION IS THE DEADLINE'S ORIGIN, and it costs no waiting. On a clock
// parked at the Unix epoch, the budget's own timer puts the deadline at the epoch plus the
// ceiling; context.WithTimeout puts it at the host's now plus the ceiling, which is not the epoch
// on any machine this will ever run on.
//
// The two live cases are the two directions of "the SMALLER of the two bounds", because a
// ceiling that outlives the budget is not a ceiling and a budget clamped by nothing is not a
// budget. The third is the refusal, which is the same clampPositive answer and not a fourth kind.
func TestTheUnitWorkDeadlineRunsOnTheBudgetsOwnClock(t *testing.T) {
	start := time.Unix(0, 0)

	for _, tc := range []struct {
		name   string
		budget time.Duration
		want   time.Duration // the expiry this case must land on, measured from start
	}{
		{"the execution ceiling is the smaller bound", time.Hour, unitExecutionTimeout},
		{"what the budget has left is the smaller bound", unitExecutionTimeout / 4, unitExecutionTimeout / 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newDrivenClock(start)
			b := newLockBudget(tc.budget, clock.Now, sleepCtx, jitterFloat).withTimer(clock.after)

			ctx, cancel, ok := b.unitWorkContext(context.Background())
			if !ok {
				t.Fatal("a budget with time left refused to bound a unit's work, so the caller has nothing to hand the callback")
			}
			defer cancel()

			deadline, has := ctx.Deadline()
			if !has {
				t.Fatal("the unit's work context carries no deadline, so a callback that hangs is bounded by nothing at all")
			}
			if want := start.Add(tc.want); !deadline.Equal(want) {
				t.Fatalf("the unit-work deadline is %s where the budget's own clock puts it at %s: this derivation is measuring the host's time, not the budget's",
					deadline.UTC(), want.UTC())
			}

			clock.advance(tc.want - time.Nanosecond)
			select {
			case <-ctx.Done():
				t.Fatal("the unit's work context expired one nanosecond before its bound was spent")
			default:
			}

			clock.advance(time.Nanosecond)
			select {
			case <-ctx.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("the budget's clock passed the unit-work deadline and the derived context is still alive: the callback would run past the bound that exists to stop it")
			}
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("the spent unit-work context ended with %v, and budgetSpent routes on context.DeadlineExceeded to tell that apart from an operator's cancellation",
					ctx.Err())
			}
		})
	}

	t.Run("a spent budget bounds no work at all", func(t *testing.T) {
		clock := newDrivenClock(start)
		b := newLockBudget(0, clock.Now, sleepCtx, jitterFloat).withTimer(clock.after)
		if _, _, ok := b.unitWorkContext(context.Background()); ok {
			t.Fatal("a spent budget handed back a context for a unit's work; issuing work against it is the caller pretending it had time")
		}
	})
}

// TestASpentBudgetHandsBackAnExpiredContext holds the property the injection must not have
// changed, on BOTH timers.
//
// Asserting it of the driven one alone would say nothing about production; asserting it of
// production alone would let the driven timer drift into answering the spent case differently,
// and every driven-clock fixture in this package would then be measuring a shape production
// never takes.
func TestASpentBudgetHandsBackAnExpiredContext(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func() *lockBudget
	}{
		{"the production timer", func() *lockBudget {
			return newLockBudget(0, time.Now, sleepCtx, jitterFloat)
		}},
		{"a driven timer", func() *lockBudget {
			c := newDrivenClock(time.Unix(0, 0))
			return newLockBudget(0, c.Now, sleepCtx, jitterFloat).withTimer(c.after)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := tc.build().context(context.Background())
			defer cancel()
			select {
			case <-ctx.Done():
			default:
				t.Fatal("a spent budget handed out a live context, so the statement it bounds would run with no deadline at all")
			}
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatalf("the spent budget's context ended with %v; a spent budget that presents as cancellation is reported as an opaque driver error instead of a coordination timeout naming the holder",
					ctx.Err())
			}
		})
	}
}
