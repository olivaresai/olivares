// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The oracles below are the admission bookkeeping itself: the reference count,
// the number of permits buffered in the entry's token channel and the number of
// surviving entries. All three are exact, so no test here concludes anything from
// elapsed time. Where a duration appears it is a WATCHDOG (waitFor's poll bound,
// or a channel receive bound) or the expiry that is itself the subject.

// opLockRefs reports key's live reference count, or 0 when the entry is gone.
func opLockRefs(rt *runtimeState, key string) int {
	rt.opMu.Lock()
	defer rt.opMu.Unlock()
	l := rt.opLocks[key]
	if l == nil {
		return 0
	}
	return l.refs
}

// opLockPermits reports how many permits key's entry currently holds: 1 when the
// operation is free, 0 while an owner holds it, and -1 when no entry exists.
// Anything above 1 would be a minted second permit.
func opLockPermits(rt *runtimeState, key string) int {
	rt.opMu.Lock()
	defer rt.opMu.Unlock()
	l := rt.opLocks[key]
	if l == nil {
		return -1
	}
	return len(l.token)
}

func opLockEntries(rt *runtimeState) int {
	rt.opMu.Lock()
	defer rt.opMu.Unlock()
	return len(rt.opLocks)
}

// waitOpRefs blocks until key carries want references. It observes real
// bookkeeping, so it proves the waiter is QUEUED rather than assuming it from a
// sleep; the deadline inside waitFor is only the test's watchdog.
func waitOpRefs(t *testing.T, rt *runtimeState, key string, want int) {
	t.Helper()
	waitFor(t, fmt.Sprintf("%d reference(s) on %q", want, key), func() bool {
		return opLockRefs(rt, key) == want
	})
}

// admissionWatchdog bounds every channel receive in this file. A correct
// implementation answers immediately; this only keeps a regression from hanging
// the package instead of failing it.
const admissionWatchdog = 10 * time.Second

func recvErr(t *testing.T, what string, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(admissionWatchdog):
		t.Fatalf("timed out waiting for: %s", what)
		return nil
	}
}

func TestOperationAdmission_RefusesBeforeAllocatingAnEntry(t *testing.T) {
	t.Parallel()
	rt := newRuntimeState()
	const key = "t|run-refusals"

	t.Run("nil context", func(t *testing.T) {
		//nolint:staticcheck // passing a nil context is precisely the refusal under test.
		release, err := rt.lockRunContext(nil, key)
		if !errors.Is(err, errNilOperationContext) {
			t.Fatalf("nil context admitted or wrong error: %v", err)
		}
		if release != nil {
			t.Fatal("a refused admission handed out a release closure")
		}
	})

	t.Run("already canceled cannot take a free permit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		release, err := rt.lockRunContext(ctx, key)
		if !errors.Is(err, context.Canceled) || release != nil {
			t.Fatalf("a canceled caller was admitted to a free key: %v", err)
		}
	})

	t.Run("already expired cannot take a free permit", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		release, err := rt.lockRunContext(ctx, key)
		if !errors.Is(err, context.DeadlineExceeded) || release != nil {
			t.Fatalf("an expired caller was admitted to a free key: %v", err)
		}
	})

	if got := opLockEntries(rt); got != 0 {
		t.Fatalf("refused admissions left %d entry/entries behind", got)
	}
	// The permit was never consumed: the key is still immediately acquirable.
	release, err := rt.lockRunContext(context.Background(), key)
	if err != nil {
		t.Fatalf("the free key lost its permit to a refused caller: %v", err)
	}
	release()
	if got := opLockEntries(rt); got != 0 {
		t.Fatalf("a completed operation left %d entry/entries behind", got)
	}
}

func TestOperationAdmission_HeldKeyBlocksWaiterUntilCancellation(t *testing.T) {
	t.Parallel()
	rt := newRuntimeState()
	const key = "t|run-held"

	release, err := rt.lockRunContext(context.Background(), key)
	if err != nil {
		t.Fatalf("first admission refused: %v", err)
	}
	if got := opLockPermits(rt, key); got != 0 {
		t.Fatalf("an owner does not hold the only permit: buffered=%d", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var bodyRan atomic.Bool
	waited := make(chan error, 1)
	go func() {
		r, werr := rt.lockRunContext(ctx, key)
		if werr == nil {
			bodyRan.Store(true) // the protected body of a canceled waiter must never run
			r()
		}
		waited <- werr
	}()

	waitOpRefs(t, rt, key, 2) // holder + queued waiter
	select {
	case werr := <-waited:
		t.Fatalf("a waiter was admitted while the key was held: %v", werr)
	case <-time.After(50 * time.Millisecond):
		// Not the oracle: the reference count above already proves it is queued.
	}

	cancel()
	if werr := recvErr(t, "canceled waiter to return", waited); !errors.Is(werr, context.Canceled) {
		t.Fatalf("canceled waiter returned %v, want context.Canceled", werr)
	}
	if bodyRan.Load() {
		t.Fatal("a canceled waiter ran its protected body")
	}
	// The holder has NOT released yet: the waiter left on its own cancellation.
	if got := opLockRefs(rt, key); got != 1 {
		t.Fatalf("the canceled waiter did not drop exactly one reference: refs=%d", got)
	}
	if got := opLockPermits(rt, key); got != 0 {
		t.Fatalf("a canceled waiter returned a permit it never owned: buffered=%d", got)
	}

	release()
	if got := opLockEntries(rt); got != 0 {
		t.Fatalf("entries were not reclaimed after holder and waiter finished: %d", got)
	}
}

func TestOperationAdmission_QueuedWaiterReportsItsOwnDeadline(t *testing.T) {
	t.Parallel()
	rt := newRuntimeState()
	const key = "t|run-deadline"

	release, err := rt.lockRunContext(context.Background(), key)
	if err != nil {
		t.Fatalf("first admission refused: %v", err)
	}
	defer func() {
		release()
		if got := opLockEntries(rt); got != 0 {
			t.Errorf("entries were not reclaimed: %d", got)
		}
	}()

	// ORDERING IS ASSERTED, NOT ASSUMED. A deadline short enough to fire before the
	// waiter arrives would be answered by the pre-admission check instead, and would
	// prove nothing about a waiter that is already queued. The reference count below
	// can only reach 2 AFTER that check has passed, because a refusal there returns
	// before any reference is taken; the deadline is set well beyond the watchdog
	// that bounds the observation, so in every passing run the waiter is queued
	// first and expires second. The expiry is the subject; the exact error is the
	// oracle.
	const queuedDeadline = 4 * time.Second // > waitFor's 3 s watchdog, by construction
	ctx, cancel := context.WithTimeout(context.Background(), queuedDeadline)
	defer cancel()
	waited := make(chan error, 1)
	go func() {
		r, werr := rt.lockRunContext(ctx, key)
		if werr == nil {
			r()
		}
		waited <- werr
	}()
	waitOpRefs(t, rt, key, 2) // past the precheck and blocked in the select
	if dl, ok := ctx.Deadline(); !ok || !time.Now().Before(dl) {
		t.Fatal("the deadline fired before the waiter was observed queued: this run would " +
			"have exercised the precheck, not expiry while queued")
	}
	if werr := recvErr(t, "expired waiter to return", waited); !errors.Is(werr, context.DeadlineExceeded) {
		t.Fatalf("expired waiter returned %v, want context.DeadlineExceeded", werr)
	}
	if got := opLockRefs(rt, key); got != 1 {
		t.Fatalf("the expired waiter did not drop exactly one reference: refs=%d", got)
	}
}

func TestOperationAdmission_DifferentKeysAreIndependent(t *testing.T) {
	t.Parallel()
	rt := newRuntimeState()

	held, err := rt.lockRunContext(context.Background(), "t|run-a")
	if err != nil {
		t.Fatalf("admission on run-a refused: %v", err)
	}
	other, err := rt.lockRunContext(context.Background(), "t|run-b")
	if err != nil {
		t.Fatalf("a held key blocked an unrelated key: %v", err)
	}
	other()
	held()
	if got := opLockEntries(rt); got != 0 {
		t.Fatalf("independent keys left %d entry/entries behind", got)
	}
}

func TestOperationAdmission_CompatibilityAndContextualOwnersExcludeEachOther(t *testing.T) {
	t.Parallel()

	t.Run("compatibility holder blocks a contextual waiter", func(t *testing.T) {
		rt := newRuntimeState()
		const key = "t|run-compat-first"
		release := rt.lockRun(key) // the kill-switch entrypoint
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		waited := make(chan error, 1)
		go func() {
			r, werr := rt.lockRunContext(ctx, key)
			if werr == nil {
				r()
			}
			waited <- werr
		}()
		waitOpRefs(t, rt, key, 2)
		cancel()
		if werr := recvErr(t, "contextual waiter", waited); !errors.Is(werr, context.Canceled) {
			t.Fatalf("contextual waiter was admitted against a compatibility holder: %v", werr)
		}
		release()
		if got := opLockEntries(rt); got != 0 {
			t.Fatalf("entries not reclaimed: %d", got)
		}
	})

	t.Run("contextual holder blocks the compatibility entrypoint", func(t *testing.T) {
		rt := newRuntimeState()
		const key = "t|run-ctx-first"
		release, err := rt.lockRunContext(context.Background(), key)
		if err != nil {
			t.Fatalf("contextual admission refused: %v", err)
		}
		admitted := make(chan struct{})
		go func() {
			r := rt.lockRun(key)
			close(admitted)
			r()
		}()
		waitOpRefs(t, rt, key, 2)
		select {
		case <-admitted:
			t.Fatal("the compatibility entrypoint entered a key held contextually")
		default:
		}
		release()
		select {
		case <-admitted:
		case <-time.After(admissionWatchdog):
			t.Fatal("the compatibility entrypoint never took the released permit")
		}
		waitFor(t, "the compatibility owner to release", func() bool { return opLockEntries(rt) == 0 })
	})
}

func TestOperationAdmission_CancellationReleaseRaceKeepsOneOwnerAndNoOrphan(t *testing.T) {
	t.Parallel()

	var live atomic.Int32
	enter := func(t *testing.T) {
		t.Helper()
		if n := live.Add(1); n != 1 {
			t.Errorf("%d owners held the same run operation at once", n)
		}
	}

	// Each iteration is a FUNCTION so that every exit path unwinds through its
	// defers. A t.Fatal from the queued-phase observation below is a goroutine exit,
	// not a return, so cleanup that is not deferred would simply never run.
	for i := range 100 {
		func() {
			key := fmt.Sprintf("t|run-race-%d", i)
			rt := newRuntimeState()
			release, err := rt.lockRunContext(context.Background(), key)
			if err != nil {
				t.Fatalf("holder admission refused: %v", err) // no worker exists yet
			}
			enter(t)

			ctx, cancel := context.WithCancel(context.Background())
			var releaseOnce sync.Once
			releaseHolder := func() {
				releaseOnce.Do(func() {
					live.Add(-1)
					release()
				})
			}

			var wg sync.WaitGroup
			wg.Add(2)
			finished := make(chan struct{})
			go func() { wg.Wait(); close(finished) }()
			// waitBoth is the FINITE completion observation. An orphaned survivor is a
			// regression of exactly what this test exists to prove, so it has to fail
			// here — an unbounded wait would instead park until the package timeout
			// killed the whole run, naming nothing.
			waitBoth := func(phase string) {
				select {
				case <-finished:
				case <-time.After(admissionWatchdog):
					t.Errorf("iteration %d: both workers had not finished %s within %s",
						i, phase, admissionWatchdog)
				}
			}

			// ⛔ FAILURE CLEANUP, REGISTERED BEFORE ANY ASSERTION CAN ABORT. If the
			// queued-phase observation fataled, the racer would stay parked on a key
			// nobody would ever release and the survivor would stay parked forever.
			// Cleanup cancels the racer and hands the permit over, then waits for both.
			//
			// It cannot make the success assertion vacuous. The survivor waits on
			// context.Background(), which nothing here cancels, so its ONLY way to
			// finish is to actually acquire the released permit — the ordinary handoff
			// this test is about. raced records that the body already made the finite
			// observation itself, so a real watchdog failure is reported once.
			raced := false
			defer func() {
				cancel()
				releaseHolder()
				if !raced {
					waitBoth("after failure cleanup")
				}
			}()

			// The racer may be admitted or canceled; either outcome is correct, and
			// neither may leave a permit or a reference behind.
			go func() {
				defer wg.Done()
				r, werr := rt.lockRunContext(ctx, key)
				if werr == nil {
					enter(t)
					live.Add(-1)
					r()
				}
			}()
			// The survivor never gives up, so it proves the next waiter is not orphaned
			// by a cancellation that races the handoff.
			go func() {
				defer wg.Done()
				r, werr := rt.lockRunContext(context.Background(), key)
				if werr != nil {
					t.Errorf("an uncancelled waiter was refused: %v", werr)
					return
				}
				enter(t)
				live.Add(-1)
				r()
			}()

			// Both waiters must be QUEUED before the race starts. Without this the
			// holder could release before either had called in, and the iteration would
			// exercise an uncontended handoff while claiming to exercise a race.
			waitOpRefs(t, rt, key, 3) // holder + racer + survivor
			raced = true
			go cancel()
			releaseHolder()
			waitBoth("the intended handoff")
			if got := opLockEntries(rt); got != 0 {
				t.Fatalf("iteration %d leaked %d entry/entries", i, got)
			}
		}()
	}
}

func TestOperationAdmission_RepeatedReleaseIsIdempotentUnderConcurrency(t *testing.T) {
	t.Parallel()
	rt := newRuntimeState()
	const key = "t|run-release"

	release, err := rt.lockRunContext(context.Background(), key)
	if err != nil {
		t.Fatalf("admission refused: %v", err)
	}

	// A successor keeps the entry observable across the handoff, so the permit
	// count can be read exactly instead of inferred.
	successor := make(chan func(), 1)
	go func() {
		r, werr := rt.lockRunContext(context.Background(), key)
		if werr != nil {
			t.Errorf("successor refused: %v", werr)
			close(successor)
			return
		}
		successor <- r
	}()
	waitOpRefs(t, rt, key, 2)

	var wg sync.WaitGroup
	for range 8 { // concurrent duplicates, not merely sequential ones
		wg.Add(1)
		go func() {
			defer wg.Done()
			release()
		}()
	}
	wg.Wait()

	var next func()
	select {
	case next = <-successor:
	case <-time.After(admissionWatchdog):
		t.Fatal("the successor never received the released permit")
	}
	if next == nil {
		t.Fatal("the successor was refused")
	}
	if got := opLockPermits(rt, key); got != 0 {
		t.Fatalf("eight releases minted %d buffered permit(s) under a live owner", got+1)
	}
	if got := opLockRefs(rt, key); got != 1 {
		t.Fatalf("repeated release did not drop exactly one reference: refs=%d", got)
	}

	next()
	next() // releasing the successor twice as well
	if got := opLockEntries(rt); got != 0 {
		// A reference count driven below zero can never reach zero again, so a
		// surviving entry here is exactly the underflow signature.
		t.Fatalf("entries were not reclaimed after repeated release: %d", got)
	}
}

func TestOperationAdmission_AllEntriesReclaimAfterMixedTraffic(t *testing.T) {
	t.Parallel()
	rt := newRuntimeState()

	var wg sync.WaitGroup
	for i := range 16 {
		key := fmt.Sprintf("t|run-mixed-%d", i%4)
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			r, err := rt.lockRunContext(ctx, key)
			if err != nil {
				return // a canceled or expired waiter is a legitimate outcome here
			}
			r()
		}()
	}
	wg.Wait()
	if got := opLockEntries(rt); got != 0 {
		t.Fatalf("mixed holders and waiters left %d entry/entries behind", got)
	}
}

// admissionCountingData counts every store transaction the module opens, so an
// admission refusal can be proved to have reached no store, ledger or audit write.
type admissionCountingData struct {
	inner api.ModuleData
	calls atomic.Int64
}

func (d *admissionCountingData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.calls.Add(1)
	return d.inner.View(ctx, tenant, fn)
}

func (d *admissionCountingData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.calls.Add(1)
	return d.inner.Mutate(ctx, tenant, fn)
}

// admissionEntrypointHarness wires the counted seams an admission refusal must not
// reach: the store, the credential source and the process runner.
func admissionEntrypointHarness(t *testing.T) (*Module, model.TenantID, *admissionCountingData, *fakeRunner, *atomic.Int64) {
	t.Helper()
	fr := &fakeRunner{}
	var credCalls atomic.Int64
	creds := CredentialSourceFunc(func(context.Context, CredentialRequest) (Credential, error) {
		credCalls.Add(1)
		return Credential{ID: "cred-1", Token: "tok-secret", Scheme: "mock", NotAfter: farFuture}, nil
	})
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(creds))
	counted := &admissionCountingData{inner: m.data}
	m.data = counted
	return m, tenant, counted, fr, &credCalls
}

func assertNoDownstreamEffects(t *testing.T, data *admissionCountingData, fr *fakeRunner, credCalls *atomic.Int64) {
	t.Helper()
	if got := data.calls.Load(); got != 0 {
		t.Errorf("a refused admission opened %d store transaction(s)", got)
	}
	if got := credCalls.Load(); got != 0 {
		t.Errorf("a refused admission issued %d credential(s)", got)
	}
	fr.mu.Lock()
	launches, procs := len(fr.specs), len(fr.procs)
	fr.mu.Unlock()
	if launches != 0 || procs != 0 {
		t.Errorf("a refused admission reached the runner: %d launch(es), %d process(es)", launches, procs)
	}
}

// The two tests below run the REAL entrypoints, not the primitive: a correct
// helper must not be able to hide a caller that was never migrated. If stopRun or
// StopForWork still waited uncancellably, each would block until the holder
// released and would then reach loadRun / assertRunWorkLease — a store call this
// asserts is zero.

func TestStopRunEntrypoint_RefusesWhileKeyIsHeldWithoutDownstreamEffects(t *testing.T) {
	t.Parallel()
	m, tenant, data, fr, credCalls := admissionEntrypointHarness(t)
	const runRef = "run-admission-stop"
	key := liveKey(tenant, runRef)

	release := m.rt.lockRun(key) // an emergency-stop style holder keeps the key
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		dto, err := m.stopRun(ctx, tenant, runRef, "user:u1", model.ActorUser)
		if dto != (runDTO{}) {
			t.Errorf("a refused stop returned a non-zero DTO: %+v", dto)
		}
		done <- err
	}()
	waitOpRefs(t, m.rt, key, 2)

	cancel()
	if err := recvErr(t, "stopRun to refuse", done); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopRun returned %v, want context.Canceled", err)
	}
	// The holder is still holding: the refusal came from the caller's context.
	if got := opLockRefs(m.rt, key); got != 1 {
		t.Fatalf("the refused stop did not drop exactly one reference: refs=%d", got)
	}
	assertNoDownstreamEffects(t, data, fr, credCalls)

	release()
	waitFor(t, "the stop key to be reclaimed", func() bool { return opLockRefs(m.rt, key) == 0 })
}

func TestStopForWorkEntrypoint_RefusesWhileKeyIsHeldWithoutDownstreamEffects(t *testing.T) {
	t.Parallel()
	m, tenant, data, fr, credCalls := admissionEntrypointHarness(t)
	const runRef = "run-admission-work-stop"
	key := liveKey(tenant, runRef)

	release, err := m.rt.lockRunContext(context.Background(), key)
	if err != nil {
		t.Fatalf("holder admission refused: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- m.StopForWork(ctx, tenant, runRef, 7, "operator asked, then left")
	}()
	waitOpRefs(t, m.rt, key, 2)

	cancel()
	if werr := recvErr(t, "StopForWork to refuse", done); !errors.Is(werr, context.Canceled) {
		t.Fatalf("StopForWork returned %v, want context.Canceled", werr)
	}
	if got := opLockRefs(m.rt, key); got != 1 {
		t.Fatalf("the refused work stop did not drop exactly one reference: refs=%d", got)
	}
	assertNoDownstreamEffects(t, data, fr, credCalls)

	release()
	waitFor(t, "the work stop key to be reclaimed", func() bool { return opLockRefs(m.rt, key) == 0 })
}
