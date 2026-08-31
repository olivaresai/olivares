// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"testing"
	"time"
)

// guardclosebudget_test.go pins the MECHANISM of the close's deadline, without a database.
//
// The PostgreSQL legs measure what a boot does when the budget runs out. These measure the
// three properties that make that possible at all, and each is a property round four showed
// the previous shape did not have.

// TestGuardCloseTxClockGovernsTheContextItArms is the mechanism of the phase deadline.
func TestGuardCloseTxClockGovernsTheContextItArms(t *testing.T) {
	t.Parallel()

	t.Run("the armed phase cancels the context with its own cause", func(t *testing.T) {
		ctx, clock := newGuardCloseTxClock(context.Background(), time.Millisecond, errGuardCloseAcquisitionDeadline)
		defer clock.stop()
		<-ctx.Done()
		if got := context.Cause(ctx); !errors.Is(got, errGuardCloseAcquisitionDeadline) {
			t.Errorf("the context was canceled with %v, and a failure that does not name the phase is diagnosed as an opaque driver error", got)
		}
	})

	t.Run("re-arming moves the deadline to the next phase", func(t *testing.T) {
		// A long first phase, re-armed to a short one: the context must die on the SECOND
		// deadline, with the second cause. This is what makes acquisition and work two ceilings
		// on one transaction rather than one ceiling used twice.
		ctx, clock := newGuardCloseTxClock(context.Background(), time.Hour, errGuardCloseAcquisitionDeadline)
		defer clock.stop()
		select {
		case <-ctx.Done():
			t.Fatal("the context died before its first phase could possibly have expired")
		default:
		}
		clock.rearm(time.Millisecond, errGuardCloseWorkDeadline)
		<-ctx.Done()
		if got := context.Cause(ctx); !errors.Is(got, errGuardCloseWorkDeadline) {
			t.Errorf("after re-arming, the cause is %v; the failure names the phase that did not expire", got)
		}
	})

	t.Run("a phase that already expired is not resurrected", func(t *testing.T) {
		// THE PROPERTY THAT MAKES RE-ARMING SAFE. If acquisition ran out, the transaction is
		// already dead and the work phase must not hand it another two minutes. context's
		// cancel-cause keeps the FIRST cause, so the diagnosis stays on the phase that failed.
		ctx, clock := newGuardCloseTxClock(context.Background(), time.Nanosecond, errGuardCloseAcquisitionDeadline)
		defer clock.stop()
		<-ctx.Done()
		clock.rearm(time.Hour, errGuardCloseWorkDeadline)
		select {
		case <-ctx.Done():
		default:
			t.Fatal("re-arming revived a transaction whose acquisition budget was already spent")
		}
		if got := context.Cause(ctx); !errors.Is(got, errGuardCloseAcquisitionDeadline) {
			t.Errorf("the cause became %v; the phase that actually ran out is the one an operator has to fix", got)
		}
	})
}

// TestGuardCloseBudgetsAreSharedAcrossRetries is the second half of round four's F-12: the work
// budget used to be created INSIDE the attempt, so three retries meant three fresh ceilings.
//
// NOT PARALLEL, and deliberately: it READS guardCloseWorkBudget, and
// TestPostgresTheCloseCannotOutlastItsWorkBudget WRITES it to a millisecond to make a real boot
// hit the ceiling. Go resumes parallel tests while serial ones are still running, so the two
// can overlap — and this test would then measure the other one's value and fail for a reason
// that has nothing to do with what it asserts. A flake introduced by the fixture is still a
// flake.
func TestGuardCloseBudgetsAreSharedAcrossRetries(t *testing.T) {
	b := &guardCloseBudgets{
		acquire: newLockBudget(guardCloseAcquisitionBudget, time.Now, sleepCtx, jitterFloat),
	}
	first := b.workBudget()
	second := b.workBudget()
	if first != second {
		t.Fatal("a second attempt was handed a NEW work budget, so the ceiling of a close is its budget times the retry count")
	}

	// AND IT STARTS LAZILY, at the first attempt that gets its locks. A budget created beside
	// the acquisition one would be consumed by the very waiting the other exists to bound, and a
	// close that queued for two minutes would then have no time left to do the work it just
	// acquired the locks for.
	lazy := &guardCloseBudgets{
		acquire: newLockBudget(guardCloseAcquisitionBudget, time.Now, sleepCtx, jitterFloat),
	}
	if lazy.work != nil {
		t.Fatal("the work budget started before any lock was held, so acquisition spends it")
	}
	time.Sleep(2 * time.Millisecond)
	if remaining := lazy.workBudget().remaining(); remaining <= guardCloseWorkBudget-2*time.Millisecond {
		t.Errorf("the work budget has %v left of %v at the moment it starts, so it had been running during acquisition", remaining, guardCloseWorkBudget)
	}
}

// TestGuardCloseTxClockAttributesTheExpiryToThePhaseThatScheduledIt closes the attribution race.
//
// THE WINDOW. `time.AfterFunc` having expired does not mean its callback has run. The previous
// clock kept ONE mutable `cause` field which the callback read when it reached the mutex, and
// re-armed by Reset-ing the SAME timer — so a callback scheduled by the acquisition phase could
// read a cause the work phase had since written, and cancel the context naming a phase that had
// not run out. Round five forced that ordering and got the wrong attribution ten times out of
// ten.
//
// It widens no budget and resurrects no transaction, which is why it is a MEDIUM. What it costs
// is the diagnosis: acquisition means "other sessions held locks too long", work means "this
// close was slow once it had them", and they send an operator to different places.
//
// HOW THIS FIXTURE IS DETERMINISTIC, and the first attempt at it was not. Racing the mutex does
// NOT reproduce the window — measured: a 200-round fixture that held the clock's lock across the
// expiry and re-armed from another goroutine stayed green against the OLD shape too, because a
// callback parked on a mutex for milliseconds wins it back under Go's starvation mode. So the
// property is exercised directly instead: keep the timer the first phase armed, change phase,
// then make that timer fire. A cause that belongs to the callback survives the phase change; a
// cause read from shared state does not.
func TestGuardCloseTxClockAttributesTheExpiryToThePhaseThatScheduledIt(t *testing.T) {
	t.Parallel()
	ctx, clock := newGuardCloseTxClock(context.Background(), time.Hour, errGuardCloseAcquisitionDeadline)
	defer clock.stop()

	// The timer the ACQUISITION phase armed, captured before the phase changes.
	clock.mu.Lock()
	acquisitionTimer := clock.timer
	clock.mu.Unlock()

	clock.rearm(time.Hour, errGuardCloseWorkDeadline)

	// It fires now, long after the close moved on.
	//
	// Reset(0) on the stopped timer is NOT a replay of the original window — that window is a
	// callback already scheduled and not yet run, and there is no way to hold one in that state
	// on purpose (see the header for the 200-round attempt that failed to). What it reproduces
	// is the PROPERTY the window turns on: a callback the acquisition phase created, running
	// after the work phase began. If the cause travels with the callback, the phase change
	// cannot reach it; if it lives in shared state, it can. That is the difference under test.
	acquisitionTimer.Reset(0)

	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the revived acquisition timer never canceled the context")
	}
	if cause := context.Cause(ctx); !errors.Is(cause, errGuardCloseAcquisitionDeadline) {
		t.Fatalf("the timer the ACQUISITION phase armed canceled with %v; a callback must carry the cause of the phase that scheduled it, not read one a later phase can overwrite", cause)
	}
	t.Logf("GUARD_CLOCK_ATTRIBUTION|scheduled=acquisition|cause=acquisition")
}
