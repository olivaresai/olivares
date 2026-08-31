// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubQuerier is a rowQuerier that never touches a database.
//
// Reconciliation's bound is a property of the runner, not of PostgreSQL: what has to be
// proved is that a projection which ignores its context cannot hang the caller. A real
// server would only add a second thing that could go wrong.
type stubQuerier struct{}

func (stubQuerier) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, errors.New("stubQuerier: not a database")
}
func (stubQuerier) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }

// boundTestUnit is the minimum retryUnit reconcile() needs: it never runs anything.
func boundTestUnit() retryUnit {
	return retryUnit{
		Spec: unitSpec{Intent: intentCreateGuard, CanonicalEnableState: guardStateOrigin},
		Plan: lockPlan{
			Target:          plannedLock{Schema: "public", Name: "bound_target", Mode: lockModeRowExclusive},
			TargetStatement: `LOCK TABLE ONLY "public"."bound_target" IN ROW EXCLUSIVE MODE`,
		},
	}
}

// TestReconcileAbandonsAProjectionThatIgnoresItsContext is the bound, proved rather than
// claimed.
//
// The previous shape passed a context with a deadline to each projection and called that
// a hard bound. It is not one. context.WithTimeout is a REQUEST: it ends a callback that
// watches its context and does nothing at all to one that does not. Every projection here
// is supplied by the caller, so "it will respect its deadline" is an assumption about
// code this package does not own — and on a boot path the cost of that assumption being
// wrong is a process that never finishes starting and never says why.
//
// The earlier regression could not have caught this: both of its projections returned
// immediately, and it only inspected that the context it was handed had a deadline and
// was not already canceled. It asserted the promise, not the property.
//
// So: a projection that blocks on something no context can reach, and the measurement
// that reconcile still returns.
//
// Mutation that must turn this red: await the goroutine instead of racing it against
// reconcileTotalBound.
func TestReconcileAbandonsAProjectionThatIgnoresItsContext(t *testing.T) {
	t.Parallel()

	// Unblocked by the test itself once abandonment has been observed, so the goroutine's
	// clean-up can be watched rather than assumed. It is closed in cleanup too, in case an
	// assertion fails first and the test returns early.
	forever := make(chan struct{})
	var unblockOnce sync.Once
	unblock := func() { unblockOnce.Do(func() { close(forever) }) }
	t.Cleanup(unblock)

	entered := make(chan struct{})
	released := make(chan struct{})
	var releases atomic.Int32

	u := boundTestUnit()
	u.ReconcileSession = func(context.Context) (rowQuerier, func(), error) {
		return stubQuerier{}, func() {
			if releases.Add(1) == 1 {
				close(released)
			}
		}, nil
	}
	u.ProjectReceipt = func(context.Context, rowQuerier) (receiptProjection, error) {
		close(entered)
		<-forever // ignores its context entirely, which is the whole point
		return receiptProjection{}, nil
	}
	u.ProjectObject = func(context.Context, rowQuerier) (objectProjection, error) {
		return objectProjection{}, nil
	}

	started := time.Now()
	out := u.reconcile(context.Background(), prestate{TargetExists: true})
	elapsed := time.Since(started)

	// THE PREMISE: the projection really did get called and really is stuck. Without
	// this the test would pass just as well against a reconcile that never called it.
	select {
	case <-entered:
	default:
		t.Fatal("the blocking projection was never entered, so nothing about abandonment is shown")
	}
	t.Logf("RECONCILE_ABANDON|elapsed=%s|bound=%s|outcome=%s", elapsed, reconcileTotalBound, out)

	if out != outcomeUnknown {
		t.Errorf("reconcile answered %q while one of its two readings never completed; anything but unknown is a claim about state nobody observed", out)
	}
	if elapsed < reconcileTotalBound {
		t.Errorf("reconcile returned after %s, before its %s bound", elapsed, reconcileTotalBound)
	}
	if elapsed > reconcileTotalBound+10*time.Second {
		t.Errorf("reconcile took %s against a %s bound: it waited for the projection instead of abandoning it", elapsed, reconcileTotalBound)
	}

	// The session stays with the abandoned goroutine, which is what makes abandonment
	// safe: reconcile must NOT have released a handle a projection is still using.
	select {
	case <-released:
		t.Error("the reconcile session was released while a projection was still using it; closing a connection out from under an in-flight read is the race this ownership rule exists to prevent")
	default:
	}

	// AND IT MUST BE RELEASED EVENTUALLY, exactly once, when the projection finally
	// returns. Checking only that it had NOT been released left the opposite failure
	// uncovered: dropping the release altogether satisfies that assertion perfectly, so
	// mutating it away kept this test — and two others — green. An abandoned goroutine
	// that never frees its session is a connection leaked per lost acknowledgement.
	unblock()
	select {
	case <-released:
	case <-time.After(15 * time.Second):
		t.Fatal("the abandoned goroutine never released its session after its projection returned")
	}
	// Give any second release a chance to land before counting.
	time.Sleep(200 * time.Millisecond)
	if n := releases.Load(); n != 1 {
		t.Errorf("the reconcile session was released %d times, want exactly 1: the owning goroutine is the only path that may free it", n)
	}
}

// TestReconcileFailsClosedWhenItCannotOpenASession covers the other new branch.
//
// An unopenable session is not an absent receipt, and the difference is the whole reason
// receiptProjection carries Readable separately from Present.
func TestReconcileFailsClosedWhenItCannotOpenASession(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		session     func(context.Context) (rowQuerier, func(), error)
		wantRelease bool
	}{
		{
			name:    "open fails with no release",
			session: func(context.Context) (rowQuerier, func(), error) { return nil, nil, errors.New("no connection") },
		},
		{
			name: "open fails but supplies a release",
			session: func(context.Context) (rowQuerier, func(), error) {
				return nil, func() {}, errors.New("dialed, then failed")
			},
			// THE HALF THE CONTRACT NAMES AND NOTHING PINNED. A callback can take a pool
			// slot and only then discover it cannot hand back a handle, so a release
			// returned ALONGSIDE an error must still run. Mutating the guard to
			// `release != nil && err == nil` loses exactly this case and left both of the
			// other subtests green.
			wantRelease: true,
		},
		{
			name:    "open returns no handle and no error",
			session: func(context.Context) (rowQuerier, func(), error) { return nil, func() {}, nil },
			// A non-nil release on a failed open must still be called: the callback may
			// have taken a slot before deciding it could not hand back a handle.
			wantRelease: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			u := boundTestUnit()
			var releases atomic.Int32
			inner := tc.session
			u.ReconcileSession = func(ctx context.Context) (rowQuerier, func(), error) {
				db, release, err := inner(ctx)
				if release == nil {
					return db, nil, err
				}
				return db, func() { releases.Add(1); release() }, err
			}
			called := 0
			u.ProjectReceipt = func(context.Context, rowQuerier) (receiptProjection, error) {
				called++
				return receiptProjection{Present: true}, nil
			}
			u.ProjectObject = func(context.Context, rowQuerier) (objectProjection, error) {
				called++
				return objectProjection{Exists: true, GuardPresent: true, MatchesCanonical: true,
					GuardEnableState: guardStateOrigin}, nil
			}
			started := time.Now()
			got := u.reconcile(context.Background(), prestate{TargetExists: true})
			elapsed := time.Since(started)
			t.Logf("RECONCILE_OPEN_FAILED|elapsed=%s|bound=%s|outcome=%s", elapsed, reconcileTotalBound, got)
			if got != outcomeUnknown {
				t.Errorf("reconcile = %q with no usable session, want %q", got, outcomeUnknown)
			}
			// A KNOWN failure must return at once, not ride out the backstop. Dropping the
			// `done <- reading{}` on this path leaves the goroutine finished and the caller
			// waiting the full 35s for an answer it already had — measured green at 35.10s
			// before this assertion existed.
			if elapsed > 5*time.Second {
				t.Errorf("an open that failed immediately took %s to report; the caller waited for the backstop instead of the signal", elapsed)
			}
			if called != 0 {
				t.Errorf("the projections ran %d times without a session to run through", called)
			}
			if want := int32(0); !tc.wantRelease && releases.Load() != want {
				t.Errorf("release ran %d times when the open returned none", releases.Load())
			}
			if tc.wantRelease {
				// WAIT FOR IT, do not read it. There is no happens-before edge between the
				// goroutine's `done <- reading{}` — which is what unblocks reconcile and
				// returns us here — and the `defer release()` that runs as it returns. A
				// valid execution can land on this line first, and round fourteen measured
				// exactly that: the loaded full-package run reported "release ran 0 times"
				// while the same test passed 100/100 isolated and 5000/5000 at GOMAXPROCS=1,
				// and inserting a bare runtime.Gosched() after the send reproduced the red in
				// 0.016s. The message accused a callback that had not failed to release; it
				// had not released YET.
				//
				// The bound is a hang detector, not a threshold: a release that is coming
				// arrives in microseconds, and one that never comes is the defect.
				deadline := time.Now().Add(10 * time.Second)
				for releases.Load() == 0 && time.Now().Before(deadline) {
					runtime.Gosched()
				}
				if releases.Load() != 1 {
					t.Errorf("release ran %d times on a failed open that supplied one, want exactly 1: the callback may already hold a slot", releases.Load())
				}
			}
		})
	}
}

// TestReconcileDoesNotAbandonThreeCooperativePhases is the other side of the backstop,
// and it is the one the sixth round's fix broke without noticing.
//
// reconcile performs THREE sequential operations, each allowed a full
// reconcileProjectionTimeout: opening the session, reading the receipt, reading the
// object. The backstop was sized for two, because the mandatory session open was added by
// the very fix that made reconciliation correct and the literal was not revisited. A
// perfectly legible reconciliation — every callback inside its own limit — was then cut
// off and turned into ErrMigrationOutcomeUnknown, halting a boot after a durable commit:
//
//	RECONCILE_SESSION_BUDGET|elapsed=25.034182711s|bound=25s|session_opened=true|
//	receipt_done=true|object_started=true|object_done=false|outcome=unknown
//
// So this pins the complement of the abandonment test: slow but COOPERATIVE work must
// finish. The distinction between the two tests is the whole point — one has a callback
// that ignores its context and must be abandoned, this one has callbacks that respect it
// and must not be.
//
// Mutation that must turn this red: set reconcileCooperativePhases back to 2.
func TestReconcileDoesNotAbandonThreeCooperativePhases(t *testing.T) {
	t.Parallel()

	// Each phase takes 90% of its individual allowance, so all three are legal and their
	// sum (27s at the current 10s timeout) exceeds a backstop sized for only two.
	phase := reconcileProjectionTimeout * 9 / 10

	// COOPERATIVE: every sleep watches its context. That is what separates this from the
	// abandonment case, and asserting it below is what stops this test from passing
	// because the work was secretly fast.
	cooperativeSleep := func(ctx context.Context, d time.Duration) error {
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	var sessionOpened, receiptDone, objectStarted, objectDone bool

	u := boundTestUnit()
	u.ReconcileSession = func(ctx context.Context) (rowQuerier, func(), error) {
		if err := cooperativeSleep(ctx, phase); err != nil {
			return nil, func() {}, err
		}
		sessionOpened = true
		return stubQuerier{}, func() {}, nil
	}
	u.ProjectReceipt = func(ctx context.Context, _ rowQuerier) (receiptProjection, error) {
		if err := cooperativeSleep(ctx, phase); err != nil {
			return receiptProjection{}, err
		}
		receiptDone = true
		return receiptProjection{Present: false}, nil
	}
	u.ProjectObject = func(ctx context.Context, _ rowQuerier) (objectProjection, error) {
		objectStarted = true
		if err := cooperativeSleep(ctx, phase); err != nil {
			return objectProjection{}, err
		}
		objectDone = true
		// The prestate below is an untouched target, so an untouched object makes the
		// matrix answer not-applied — a real verdict rather than the fail-closed unknown.
		return objectProjection{Exists: true}, nil
	}

	started := time.Now()
	out := u.reconcile(context.Background(), prestate{TargetExists: true})
	elapsed := time.Since(started)

	t.Logf("RECONCILE_COOPERATIVE|elapsed=%s|bound=%s|phase=%s|session_opened=%v|receipt_done=%v|object_started=%v|object_done=%v|outcome=%s",
		elapsed, reconcileTotalBound, phase, sessionOpened, receiptDone, objectStarted, objectDone, out)

	// THE PREMISES, all three, so a run where a phase was skipped cannot read as a pass.
	if !sessionOpened || !receiptDone || !objectStarted {
		t.Fatalf("not every phase ran (session=%v receipt=%v object_started=%v); this run does not exercise the three-phase path",
			sessionOpened, receiptDone, objectStarted)
	}
	// THE DEFECT FIRST, so its diagnosis is the one an operator reads. Checking the
	// duration premise ahead of it reported "the phases did not actually take their time"
	// for a run whose phases were cut off — true in letter, misleading in substance.
	if !objectDone {
		t.Fatalf("the second projection was cut off at %s although every callback stayed inside its own %s limit; the backstop is narrower than the contract it is supposed to protect",
			elapsed, reconcileProjectionTimeout)
	}
	if elapsed < 3*phase {
		t.Fatalf("reconcile took %s, less than the %s of cooperative work it was given: the phases did not actually take their time",
			elapsed, 3*phase)
	}
	if out == outcomeUnknown {
		t.Errorf("reconcile answered %q after three cooperative phases that all completed; a legible reconciliation must not be turned into an unknown outcome, which is what halts a boot after a durable commit", out)
	}
	if elapsed >= reconcileTotalBound {
		t.Errorf("reconcile took %s, reaching its %s backstop: cooperative work is being abandoned", elapsed, reconcileTotalBound)
	}
}
