// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// fakeClock drives the budget without sleeping. A retry test that really sleeps
// is a test nobody runs often enough to trust.
type fakeClock struct {
	t      time.Time
	slept  []time.Duration
	sleepE error
}

func (c *fakeClock) now() time.Time { return c.t }
func (c *fakeClock) sleep(ctx context.Context, d time.Duration) error {
	if c.sleepE != nil {
		return c.sleepE
	}
	c.slept = append(c.slept, d)
	c.t = c.t.Add(d)
	return nil
}

// TestLockBudgetClampNeverExceedsWhatIsLeft is the regression for the defect the
// contrast measured: a deadline that is not hard.
//
// lock_timeout applies PER ACQUISITION and restarts for every relation, so a unit
// that takes several locks can wait a multiple of the value an operator set —
// measured at 4703 ms against a 3 s budget for three relations in one statement.
// The budget only stays hard if every individual statement is clamped to what is
// left at the moment it is issued, which is what clamp exists to do.
//
// Mutation that must turn this red: return d unchanged from clampPositive.
func TestLockBudgetClampNeverExceedsWhatIsLeft(t *testing.T) {
	t.Parallel()
	c := &fakeClock{t: time.Unix(0, 0)}
	b := newLockBudget(10*time.Second, c.now, c.sleep, func() float64 { return 1 })

	if got, ok := b.clampPositive(60 * time.Second); got != 10*time.Second || !ok {
		t.Errorf("clampPositive(60s) with 10s left = (%v, %v), want (10s, true): a per-statement timeout longer than the remaining budget lets the statement outlive the deadline it is supposed to respect", got, ok)
	}
	if got, ok := b.clampPositive(2 * time.Second); got != 2*time.Second || !ok {
		t.Errorf("clampPositive(2s) with 10s left = (%v, %v), want it untouched", got, ok)
	}

	// Spend most of it and check the clamp follows the clock, not the initial total.
	c.t = c.t.Add(9 * time.Second)
	if got, ok := b.clampPositive(60 * time.Second); got != time.Second || !ok {
		t.Errorf("clampPositive(60s) with 1s left = (%v, %v), want (1s, true)", got, ok)
	}
	c.t = c.t.Add(5 * time.Second)
	if !b.expired() {
		t.Error("budget past its deadline does not report expired")
	}
	if got := b.remaining(); got != 0 {
		t.Errorf("remaining past the deadline = %v, want 0 and never negative", got)
	}
}

// TestLockBudgetRefusesRatherThanRenderingAnUnlimitedTimeout is the regression for a
// defect that would have inverted the deadline at the exact moment it mattered.
//
// These durations become PostgreSQL timeout GUCs, and PostgreSQL reads
// lock_timeout = 0 as DISABLED. A spent budget that clamps to zero and is handed
// straight to SET LOCAL therefore removes the ceiling from the last statement of an
// exhausted budget — making it the only unbounded statement in the whole unit.
//
// So an exhausted budget must REFUSE, distinguishably, rather than return a number
// the caller can pass on.
//
// Mutation that must turn this red: have clampPositive report ok on a spent budget.
func TestLockBudgetRefusesRatherThanRenderingAnUnlimitedTimeout(t *testing.T) {
	t.Parallel()
	c := &fakeClock{t: time.Unix(0, 0)}
	b := newLockBudget(time.Second, c.now, c.sleep, func() float64 { return 1 })

	c.t = c.t.Add(time.Second)
	got, ok := b.clampPositive(30 * time.Second)
	if ok {
		t.Errorf("clampPositive on a spent budget reported ok with %v: rendered as a GUC that is 'no limit', which is the opposite of what an exhausted deadline means", got)
	}

	// And a live budget must never answer zero either, whatever it is asked for.
	c2 := &fakeClock{t: time.Unix(0, 0)}
	b2 := newLockBudget(time.Second, c2.now, c2.sleep, func() float64 { return 1 })
	if got, ok := b2.clampPositive(0); ok && got == 0 {
		t.Error("clampPositive(0) on a LIVE budget reported a usable zero; zero disables the timeout, so 'the caller asked for no timeout' must not be expressible as an accepted clamp")
	}
}

// TestLockBudgetBackoffFloorsAZeroJitterSample pins that a zero jitter sample is a
// SHORT WAIT and not an exhausted deadline.
//
// Full jitter multiplies the exponential delay by a value in [0,1), so zero is a
// legitimate sample. The delay was then converted to a Duration and any result <= 0
// was read as "no budget left" — so on a budget with minutes remaining, a single
// unlucky sample returned a premature coordination timeout. The observable symptom
// is the opposite of a busy loop, which is why reasoning about it as one missed it.
//
// Mutation that must turn this red: remove the floor from backoffDelay.
func TestLockBudgetBackoffFloorsAZeroJitterSample(t *testing.T) {
	t.Parallel()
	c := &fakeClock{t: time.Unix(0, 0)}
	b := newLockBudget(time.Hour, c.now, c.sleep, func() float64 { return 0 })

	ok, err := b.backoff(context.Background(), 1, 200*time.Millisecond, 5*time.Second)
	if err != nil {
		t.Fatalf("backoff: %v", err)
	}
	if !ok {
		t.Fatal("a zero jitter sample was reported as an exhausted budget, with an hour left: the caller turns that into a premature timeout naming a holder that never had to be waited out")
	}
	if len(c.slept) != 1 || c.slept[0] <= 0 {
		t.Errorf("slept %v, want exactly one strictly positive wait", c.slept)
	}
}

// TestLockBudgetFirstBackoffIsTheBase pins the schedule's first step.
//
// The loop doubled once even at attempt 1, which silently made the configured base
// the SECOND step: a 200ms base produced a 400ms first ceiling. Small, but it is a
// constant an operator reads and reasons about.
func TestLockBudgetFirstBackoffIsTheBase(t *testing.T) {
	t.Parallel()
	c := &fakeClock{t: time.Unix(0, 0)}
	b := newLockBudget(time.Hour, c.now, c.sleep, func() float64 { return 1 })

	if got := b.backoffDelay(1, 200*time.Millisecond, 5*time.Second); got != 200*time.Millisecond {
		t.Errorf("first backoff ceiling = %v, want the configured base of 200ms", got)
	}
	if got := b.backoffDelay(2, 200*time.Millisecond, 5*time.Second); got != 400*time.Millisecond {
		t.Errorf("second backoff ceiling = %v, want 400ms", got)
	}
	if got := b.backoffDelay(99, 200*time.Millisecond, 5*time.Second); got != 5*time.Second {
		t.Errorf("a late backoff = %v, want the 5s ceiling", got)
	}
}

// TestLockBudgetRefusesAClockThatDoesNotAdvance pins the invariant that makes every
// other guarantee in this file meaningful.
//
// The clock and the sleeper are injected. A pair where sleeping does not advance
// time makes remaining() constant, so the deadline can never be reached and the
// acquisition loop polls forever — the single failure a deadline exists to prevent,
// reached through the seam that exists to test it. Nothing else in the loop bounds
// the iteration count, so this has to fail loudly rather than be counted around.
//
// Mutation that must turn this red: drop the post-sleep clock check.
func TestLockBudgetRefusesAClockThatDoesNotAdvance(t *testing.T) {
	t.Parallel()
	frozen := time.Unix(0, 0)
	b := newLockBudget(time.Hour,
		func() time.Time { return frozen },
		func(context.Context, time.Duration) error { return nil },
		func() float64 { return 1 })

	ok, err := b.backoff(context.Background(), 1, time.Second, time.Minute)
	if ok {
		t.Error("backoff reported a successful wait on a clock that did not move; the caller would loop forever on a budget that cannot expire")
	}
	if !errors.Is(err, ErrLockBudgetStalled) {
		t.Errorf("backoff on a stalled clock returned %v, want ErrLockBudgetStalled: a bounded wait built on an unbounded clock is not bounded, and saying so is the only honest answer", err)
	}
}

// TestLockBudgetBackoffStopsAtTheDeadline pins that the WAIT itself cannot push
// past the deadline. A backoff that sleeps its full exponential delay would
// happily overshoot a budget it was meant to respect — the same class of defect
// as an unclamped lock_timeout, one layer up.
func TestLockBudgetBackoffStopsAtTheDeadline(t *testing.T) {
	t.Parallel()
	c := &fakeClock{t: time.Unix(0, 0)}
	b := newLockBudget(5*time.Second, c.now, c.sleep, func() float64 { return 1 })

	// A late attempt would want 8s of exponential delay; only 5s exist.
	ok, err := b.backoff(context.Background(), 6, time.Second, 30*time.Second)
	if err != nil {
		t.Fatalf("backoff: %v", err)
	}
	if !ok {
		t.Fatal("backoff refused to wait while the budget still had time")
	}
	if len(c.slept) != 1 {
		t.Fatalf("slept %d times, want 1", len(c.slept))
	}
	if c.slept[0] > 5*time.Second {
		t.Errorf("backoff slept %v with a 5s budget: the wait overran the deadline it exists to respect", c.slept[0])
	}
	if !b.expired() {
		t.Error("the budget should be spent after a wait that consumed it")
	}
	ok, err = b.backoff(context.Background(), 1, time.Second, 30*time.Second)
	if err != nil || ok {
		t.Errorf("backoff on an expired budget = (%v, %v), want (false, nil)", ok, err)
	}
}

// TestLockBudgetBackoffAppliesJitter pins that the delay is jittered rather than
// fixed. Every node that boots together contends together, and an unjittered
// exponential backoff keeps that population synchronized on exactly the schedule
// that made them collide.
//
// Mutation that must turn this red: drop the jitter multiplication.
func TestLockBudgetBackoffAppliesJitter(t *testing.T) {
	t.Parallel()
	c := &fakeClock{t: time.Unix(0, 0)}
	b := newLockBudget(time.Hour, c.now, c.sleep, func() float64 { return 0.25 })

	if _, err := b.backoff(context.Background(), 2, time.Second, time.Minute); err != nil {
		t.Fatalf("backoff: %v", err)
	}
	// attempt 2 doubles the 1s base once -> 2s; the injected jitter takes a quarter.
	if want := 500 * time.Millisecond; c.slept[0] != want {
		t.Errorf("slept %v, want %v (2s of exponential delay times the injected 0.25 jitter)", c.slept[0], want)
	}
}

// TestClassifyFailurePutsTheCallerFirst is the one ordering that cannot be got
// wrong. PostgreSQL reports a statement canceled by the CLIENT as 57014, the
// same code it reports for one killed by statement_timeout. A classifier that
// read the code before the context would turn an operator's cancellation into a
// retry loop against a database they are trying to stop touching.
//
// Mutation that must turn this red: check the SQLSTATE before ctx.Err().
func TestClassifyFailurePutsTheCallerFirst(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	canceled := &pgconn.PgError{Code: sqlStateQueryCanceled, Message: "canceling statement due to user request"}
	got, err := classifyFailure(ctx, unitFailure{Phase: phaseExecute, Err: canceled})
	if got != retryPropagate {
		t.Errorf("a 57014 raised while the caller's context is canceled classified as %v, want retryPropagate: the operator asked to stop, and that is not the unit's failure to retry around", got)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the propagated error lost context.Canceled: %v", err)
	}
}

// TestClassifyFailureMatrix pins the whole decision table. Each row exists
// because the wrong answer has a specific cost, named in the message.
func TestClassifyFailureMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		err     error
		phase   unitPhase
		armed   time.Duration
		elapsed time.Duration
		want    retryDecision
		why     string
	}{
		{
			name: "lock not available is an attempt result, not a verdict",
			err:  &pgconn.PgError{Code: sqlStateLockNotAvailable}, phase: phaseAcquire,
			want: retryNewTransaction,
			why:  "treating it as permanent would give up on the first busy writer",
		},
		{
			name: "deadlock retries the WHOLE unit",
			err:  &pgconn.PgError{Code: sqlStateDeadlockDetected}, phase: phaseExecute,
			want: retryNewTransaction,
			why:  "a deadlock victim has lost every lock it held, so resuming mid-unit is not possible",
		},
		{
			name: "serialization failure retries the whole unit",
			err:  &pgconn.PgError{Code: sqlStateSerializationFailure}, phase: phaseExecute,
			want: retryNewTransaction,
			why:  "a serialisable database is a supported deployment and this is its normal conflict signal",
		},
		{
			name: "query canceled after the armed timeout elapsed is our own",
			err:  &pgconn.PgError{Code: sqlStateQueryCanceled}, phase: phaseExecute,
			armed: time.Second, elapsed: time.Second,
			want: retryNewTransaction,
			why:  "the statement ran out the budget this unit armed for it, which is an attempt result",
		},
		{
			name: "query canceled from OUTSIDE is never retried blind",
			err:  &pgconn.PgError{Code: sqlStateQueryCanceled}, phase: phaseExecute,
			armed: time.Minute, elapsed: 20 * time.Millisecond,
			want: retryNever,
			why:  "nobody in this runner asked for it: retrying loops against an operator who is actively canceling",
		},
		{
			name: "an outside cancellation at the commit boundary reconciles instead",
			err:  &pgconn.PgError{Code: sqlStateQueryCanceled}, phase: phaseCommit,
			armed: time.Minute, elapsed: 20 * time.Millisecond,
			want: retryAfterReconcile,
			why:  "at the boundary the unit's fate outranks the cancellation's origin",
		},
		{
			name: "statement completion unknown before the boundary needs a new session",
			err:  &pgconn.PgError{Code: sqlStateCompletionUnknown}, phase: phaseAcquire,
			want: retryNewSession,
			why:  "nothing committed, but the session's state is as unknown as the statement's",
		},
		{
			name: "statement completion unknown AT COMMIT must reconcile",
			err:  &pgconn.PgError{Code: sqlStateCompletionUnknown}, phase: phaseCommit,
			want: retryAfterReconcile,
			why:  "at the boundary an unknown statement completion is an unknown unit outcome",
		},
		{
			name: "cannot connect now is transient but not on this session",
			err:  &pgconn.PgError{Code: sqlStateCannotConnectNow}, phase: phaseAcquire,
			want: retryNewSession,
			why:  "the server is starting up or recovering; giving up is wrong and reusing the refused session is too",
		},
		{
			name: "admin shutdown respects the operator",
			err:  &pgconn.PgError{Code: sqlStateAdminShutdown}, phase: phaseExecute,
			want: retryNever,
			why:  "reconnecting around a maintenance window fights the person who opened it, and nothing was committed",
		},
		{
			name: "admin shutdown DURING the commit is ambiguous, not deferential",
			err:  &pgconn.PgError{Code: sqlStateAdminShutdown}, phase: phaseCommit,
			want: retryAfterReconcile,
			why:  "the server may have made the transaction durable before dropping the connection; deferring to the operator is right about the intent and wrong about the ledger. A backend terminated from a DEFERRED constraint trigger — which fires inside COMMIT — arrives exactly here",
		},
		{
			name: "a deadlock in receipt still retries the whole unit",
			err:  &pgconn.PgError{Code: sqlStateDeadlockDetected}, phase: phaseReceipt,
			want: retryNewTransaction,
			why:  "the server aborted the transaction itself, so nothing committed and there is nothing to reconcile",
		},
		{
			name: "insufficient privilege is permanent",
			err:  &pgconn.PgError{Code: sqlStateInsufficientPriv}, phase: phaseAcquire,
			want: retryNever,
			why:  "retrying a privilege burns the budget and hides the diagnosis an operator needs",
		},
		{
			name: "undefined object is permanent",
			err:  &pgconn.PgError{Code: sqlStateUndefinedObject}, phase: phaseAcquire,
			want: retryNever,
			why:  "the unit assumed an object that is not there; another attempt cannot conjure it",
		},
		{
			name: "aborted transaction is a runner bug, not a condition",
			err:  &pgconn.PgError{Code: sqlStateInFailedTransaction}, phase: phaseReceipt,
			want: retryNever,
			why:  "the real failure was the earlier statement; this one is its shadow",
		},
		{
			name: "a COMMIT with no answer is THE ambiguous case",
			err:  errors.New("connection reset by peer"), phase: phaseCommit,
			want: retryAfterReconcile,
			why:  "a COMMIT can succeed on the server and lose the acknowledgement on the wire, so a blind retry applies the unit twice",
		},
		{
			name: "a transport failure while WRITING the receipt is not the boundary",
			err:  errors.New("connection reset by peer"), phase: phaseReceipt,
			want: retryNewSession,
			why:  "writing the receipt is an INSERT inside the transaction; COMMIT was never called, so nothing reached durable storage and there is nothing to reconcile — folding Receipt in with Commit invited the matrix to answer 'applied' for work about to be rolled back",
		},
		{
			name: "a deadlock AT COMMIT is not ambiguous",
			err:  &pgconn.PgError{Code: sqlStateDeadlockDetected}, phase: phaseCommit,
			want: retryNewTransaction,
			why:  "the server ANSWERED, so it definitively rolled back; reconciling would spend two catalog reads to be told what the SQLSTATE already said",
		},
		{
			name: "transaction resolution unknown reconciles in ANY phase",
			err:  &pgconn.PgError{Code: sqlStateResolutionUnknown}, phase: phaseAcquire,
			want: retryAfterReconcile,
			why:  "08007 is literally 'I cannot tell whether the transaction resolved', which is the definition of a case to ask about rather than guess; asking is harmless when nothing was written",
		},
		{
			name: "a non-server error BEFORE the boundary needs a new session",
			err:  errors.New("connection reset by peer"), phase: phaseAcquire,
			want: retryNewSession,
			why:  "nothing committed, so there is nothing to reconcile — but the transport just failed, and a new transaction on a broken session is not a remedy",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, _ := classifyFailure(context.Background(), unitFailure{
				Phase: tc.phase, Err: tc.err, Armed: tc.armed, Elapsed: tc.elapsed})
			if got != tc.want {
				t.Errorf("classified as %v, want %v — %s", got, tc.want, tc.why)
			}
		})
	}
}

// TestClassifyFailureSeesThroughWrapping is small but load-bearing: the runner
// wraps every error with the step that produced it, so a classifier matching on
// the concrete type instead of errors.As would classify every real failure as
// "reconcile" and silently disable the whole table above.
func TestClassifyFailureSeesThroughWrapping(t *testing.T) {
	t.Parallel()
	wrapped := fmt.Errorf("sqlstore: core migrations: %w",
		fmt.Errorf("statement 3: %w", &pgconn.PgError{Code: sqlStateDeadlockDetected}))
	if got, _ := classifyFailure(context.Background(), unitFailure{Phase: phaseExecute, Err: wrapped}); got != retryNewTransaction {
		t.Errorf("a wrapped deadlock classified as %v, want retryNewTransaction: the runner wraps every error with its step, so unwrapping is not optional", got)
	}
}

// TestLockBudgetContextExpiresRatherThanCancels pins which of the two context errors
// a spent budget produces, because the whole attribution path branches on it.
//
// budgetSpent distinguishes "the operator stopped us" (propagate, no attribution)
// from "we ran out of the time we allotted ourselves" (a coordination timeout that
// names the holder) by asking whether the error is DeadlineExceeded. A spent budget
// that handed back a Canceled context therefore fell through to the generic branch,
// and the boot reported `try migration lock: timeout: context already done: context
// canceled` — an opaque driver error, with the holder it had just attributed thrown
// away.
//
// Mutation that must turn this red: build the spent-budget context with
// context.WithCancel followed by cancel().
func TestLockBudgetContextExpiresRatherThanCancels(t *testing.T) {
	t.Parallel()
	c := &fakeClock{t: time.Unix(0, 0)}
	b := newLockBudget(time.Second, c.now, c.sleep, func() float64 { return 1 })
	c.t = c.t.Add(2 * time.Second)

	ctx, cancel := b.context(context.Background())
	defer cancel()
	if err := ctx.Err(); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a spent budget produced %v, want context.DeadlineExceeded: the caller tells its own expiry from the operator's cancellation by exactly this, and getting it wrong discards the attribution the timeout was supposed to carry", err)
	}

	// And a live budget must still bound, not cancel.
	c2 := &fakeClock{t: time.Unix(0, 0)}
	b2 := newLockBudget(time.Hour, c2.now, c2.sleep, func() float64 { return 1 })
	ctx2, cancel2 := b2.context(context.Background())
	defer cancel2()
	if err := ctx2.Err(); err != nil {
		t.Errorf("a live budget produced an already-finished context: %v", err)
	}
	if _, ok := ctx2.Deadline(); !ok {
		t.Error("a live budget produced a context with no deadline, so a hanging roundtrip would outlive the budget entirely")
	}
}

// TestClassifyFailureNeverReconcilesThisRunnersOwnRefusal is the regression for the
// worst defect the second contrast round found: a unit that never ran reporting
// success.
//
// The generic non-PgError branch routes to reconciliation, and it is right to: a
// broken wire or a lost COMMIT acknowledgement leaves the server's state genuinely
// unknown. But this runner also raises its OWN sentinels — a budget spent after the
// locks were taken but before Execute, a lock footprint outside the declared plan, a
// clock that cannot expire — and those are decisions taken locally, before or instead
// of doing the work. Their effect is known exactly: nothing happened.
//
// Measured by the contrast with an injected clock: the budget refusal reached
// reconciliation, reconciliation answered "applied", and run() returned nil with
// execute_calls=0 and receipt_calls=0.
//
// Mutation that must turn this red: delete the sentinel branch and let these fall
// through to the non-PgError case.
func TestClassifyFailureNeverReconcilesThisRunnersOwnRefusal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		why  string
	}{
		{
			name: "budget spent before execute", err: ErrMigrationLockBudgetExceeded,
			why: "the unit refused itself before doing any work, so there is no uncertain outcome to reconcile — and reconciling it can answer 'applied' for a unit that never ran",
		},
		{
			name: "lock footprint outside the plan", err: ErrMigrationLockFootprint,
			why: "the refusal happens before the commit and the rollback undoes it; treating it as an unknown outcome would let a unit that took undeclared locks be recorded as applied",
		},
		{
			name: "a clock that cannot expire", err: ErrLockBudgetStalled,
			why: "an invariant violation in the runner is not a database condition to reconcile around",
		},
		{
			name: "an unauthorized unit", err: ErrMigrationUnauthorised,
			why: "the unit refused itself before touching anything; retrying or reconciling an unauthorized change is how it eventually gets made",
		},
		{
			name: "a postcondition that was not reached", err: ErrMigrationPostconditionFailed,
			why: "the work did not leave the object in its declared state and the transaction is about to roll back, so there is nothing uncertain and nothing to retry into",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, phase := range []unitPhase{phaseAcquire, phaseExecute, phaseReceipt, phaseCommit} {
				got, err := classifyFailure(context.Background(),
					unitFailure{Phase: phase, Err: fmt.Errorf("wrapped: %w", tc.err)})
				if got != retryNever {
					t.Errorf("in %s classified as %v, want retryNever — %s", phase, got, tc.why)
				}
				if !errors.Is(err, tc.err) {
					t.Errorf("in %s the sentinel was lost from the returned error: %v", phase, err)
				}
			}
		})
	}
}

// TestClassifyCancelNeverMistakesAnInstantCancellationForOurTimeout is the regression
// for a tolerance that scaled wrong.
//
// The slack was a fixed 50ms, so with a short armed timeout it swallowed the entire
// budget: at Armed=50ms every cancellation satisfied Elapsed+50ms >= 50ms, INCLUDING
// Elapsed=0. An external pg_cancel_backend arriving instantly was therefore filed as
// this runner's own timeout and retried — which is arguing in a loop with an operator
// who is actively canceling.
//
// Mutation that must turn this red: restore a fixed tolerance, or drop the Elapsed>0
// requirement.
func TestClassifyCancelNeverMistakesAnInstantCancellationForOurTimeout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	for _, armed := range []time.Duration{
		time.Millisecond, 10 * time.Millisecond, 50 * time.Millisecond,
		time.Second, time.Minute,
	} {
		got := classifyCancel(ctx, unitFailure{Phase: phaseExecute, Armed: armed, Elapsed: 0})
		if got != cancelUnknown {
			t.Errorf("armed=%v elapsed=0 classified as %v, want cancelUnknown: a statement killed by its OWN timeout necessarily ran for that timeout, so a measured zero is either an instant external cancellation or a clock that cannot see the interval — and both must fail closed", armed, got)
		}
	}

	// A statement that genuinely ran out its own timeout must still be recognized,
	// including when the client's stopwatch reads slightly short of the server's.
	for _, tc := range []struct {
		armed, elapsed time.Duration
	}{
		{time.Second, time.Second},
		{time.Second, 950 * time.Millisecond},
		{50 * time.Millisecond, 48 * time.Millisecond},
	} {
		if got := classifyCancel(ctx, unitFailure{Phase: phaseExecute, Armed: tc.armed, Elapsed: tc.elapsed}); got != cancelOwnTimeout {
			t.Errorf("armed=%v elapsed=%v classified as %v, want cancelOwnTimeout: our own budget expiring is an attempt result, not a permanent failure", tc.armed, tc.elapsed, got)
		}
	}

	// And an external cancellation part-way through is still external.
	if got := classifyCancel(ctx, unitFailure{Phase: phaseExecute, Armed: time.Minute, Elapsed: time.Second}); got != cancelUnknown {
		t.Errorf("a cancellation one second into a one-minute timeout classified as %v, want cancelUnknown", got)
	}
}

// TestClassifyFailureCancellationDoesNotHideAnAmbiguousCommit pins the one exception
// to "the caller's cancellation wins".
//
// Before COMMIT the rule is absolute: an operator canceling must not provoke retries
// against a database they are trying to stop touching. At COMMIT it is not a rule
// about knowledge at all. database/sql's Tx.Commit wins its state transition and then
// calls into the driver, and pgx runs the commit on the context BeginTx was given, so
// a cancellation can land while the commit is in flight. "Canceled" and "committed"
// are not exclusive, and treating the cancellation as the whole story discards a unit
// whose fate is unknown — which is how a committed unit gets applied twice on the next
// boot.
//
// A SERVER error at commit stays propagated: the protocol says the transaction rolled
// back, and the cancellation does not erase that knowledge.
//
// Mutation that must turn this red: return retryPropagate for every canceled context.
func TestClassifyFailureCancellationDoesNotHideAnAmbiguousCommit(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := classifyFailure(ctx, unitFailure{
		Phase: phaseCommit, Err: errors.New("write: broken pipe"),
	})
	if got != retryAfterReconcile {
		t.Errorf("a canceled caller at an ambiguous COMMIT classified as %v, want retryAfterReconcile: the cancellation says nothing about whether the server applied it", got)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the returned error lost the cancellation: %v", err)
	}

	// Before the boundary, cancellation still wins outright.
	if got, _ := classifyFailure(ctx, unitFailure{
		Phase: phaseExecute, Err: errors.New("write: broken pipe"),
	}); got != retryPropagate {
		t.Errorf("a canceled caller before COMMIT classified as %v, want retryPropagate", got)
	}

	// And a SERVER error at commit is knowledge the cancellation does not erase.
	if got, _ := classifyFailure(ctx, unitFailure{
		Phase: phaseCommit, Err: &pgconn.PgError{Code: sqlStateDeadlockDetected},
	}); got != retryPropagate {
		t.Errorf("a canceled caller with a SERVER error at commit classified as %v, want retryPropagate: the protocol already says it rolled back", got)
	}
}

// TestClassifyCancelUsesTheTimeoutPostgresActuallyReceived covers the quantisation the
// earlier test walked straight past by only using round values.
//
// millisText rounds anything sub-millisecond UP to 1ms, so a nominal 400µs reaches the
// server as 1ms. Comparing against the nominal value declared a cancellation to be our
// own timeout 800µs before the timeout actually sent could possibly expire.
func TestClassifyCancelUsesTheTimeoutPostgresActuallyReceived(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// What the server received for a sub-millisecond request.
	if got := effectiveMillis(400 * time.Microsecond); got != time.Millisecond {
		t.Fatalf("effectiveMillis(400µs) = %v, want 1ms (millisText rounds up, and zero would mean unlimited)", got)
	}
	// Classified against the EFFECTIVE value, a 200µs cancellation is not ours.
	if got := classifyCancel(ctx, unitFailure{
		Phase: phaseExecute, Armed: effectiveMillis(400 * time.Microsecond), Elapsed: 200 * time.Microsecond,
	}); got != cancelUnknown {
		t.Errorf("a 200µs cancellation against a 1ms timeout classified as %v, want cancelUnknown: the timeout the server was given could not have fired yet", got)
	}

	// The floor must not swallow a short timeout either: at 3ms a 1ms cancellation is
	// still two thirds early.
	if got := classifyCancel(ctx, unitFailure{
		Phase: phaseExecute, Armed: 3 * time.Millisecond, Elapsed: time.Millisecond,
	}); got != cancelUnknown {
		t.Errorf("a 1ms cancellation against a 3ms timeout classified as %v, want cancelUnknown", got)
	}

	// A CALLBACK reports Armed=0 on purpose: PostgreSQL restarts statement_timeout per
	// statement while Elapsed wraps the whole callback, so their sum proves nothing
	// about any single statement. Unknown is the conservative answer.
	if got := classifyCancel(ctx, unitFailure{
		Phase: phaseExecute, Armed: 0, Elapsed: time.Hour,
	}); got != cancelUnknown {
		t.Errorf("a callback failure classified as %v, want cancelUnknown: an elapsed sum across many statements says nothing about the one that was canceled", got)
	}
}

// TestClassifyFailureCancellationDoesNotHideAnyAmbiguousCommitCode extends the
// cancellation exception to every SQLSTATE that means "I do not know".
//
// The first version covered only the transport error, so three codes that say so
// explicitly were still turned into a propagated cancellation — discarding a unit
// whose fate was undetermined. A canceled caller settles none of them.
//
// Mutation that must turn this red: restrict the exception to non-PgError again.
func TestClassifyFailureCancellationDoesNotHideAnyAmbiguousCommitCode(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tc := range []struct {
		name string
		err  error
		why  string
	}{
		{"transaction resolution unknown", &pgconn.PgError{Code: sqlStateResolutionUnknown},
			"08007 says the resolution is unknown in as many words"},
		{"statement completion unknown", &pgconn.PgError{Code: sqlStateCompletionUnknown},
			"at the boundary an unknown statement completion IS an unknown unit completion"},
		{"admin shutdown", &pgconn.PgError{Code: sqlStateAdminShutdown},
			"a shutdown delivered during the commit may have arrived after the server made it durable"},
	} {
		if got, _ := classifyFailure(ctx, unitFailure{Phase: phaseCommit, Err: tc.err}); got != retryAfterReconcile {
			t.Errorf("%s at COMMIT with a canceled caller classified as %v, want retryAfterReconcile — %s", tc.name, got, tc.why)
		}
	}

	// A settled server error is still propagated: the protocol says it rolled back.
	if got, _ := classifyFailure(ctx, unitFailure{
		Phase: phaseCommit, Err: &pgconn.PgError{Code: sqlStateSerializationFailure},
	}); got != retryPropagate {
		t.Errorf("a settled server error at commit classified as %v, want retryPropagate", got)
	}
}

// TestCancelToleranceIsBoundedInAbsoluteTerms pins the ceiling a proportion cannot
// give.
//
// A quarter of a sixty-second timeout is fifteen seconds, so an external cancellation
// three quarters of the way through was filed as our own timeout and retried. What the
// tolerance compensates for is clock skew and one network roundtrip, and neither grows
// with the length of the statement.
//
// Mutation that must turn this red: remove the absolute cap.
func TestCancelToleranceIsBoundedInAbsoluteTerms(t *testing.T) {
	t.Parallel()
	if got := cancelTolerance(time.Minute); got > cancelToleranceMax {
		t.Errorf("cancelTolerance(1m) = %v, want at most %v", got, cancelToleranceMax)
	}
	// An external cancellation 45s into a 60s timeout must not read as ours.
	if got := classifyCancel(context.Background(), unitFailure{
		Phase: phaseExecute, Armed: time.Minute, Elapsed: 45 * time.Second,
	}); got != cancelUnknown {
		t.Errorf("a cancellation 45s into a 60s timeout classified as %v, want cancelUnknown: fifteen seconds of slack is not clock skew", got)
	}
	// And a genuine expiry is still recognized.
	if got := classifyCancel(context.Background(), unitFailure{
		Phase: phaseExecute, Armed: time.Minute, Elapsed: 59900 * time.Millisecond,
	}); got != cancelOwnTimeout {
		t.Errorf("a statement that ran out its own 60s timeout classified as %v, want cancelOwnTimeout", got)
	}
}
