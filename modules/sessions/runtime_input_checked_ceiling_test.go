// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/store"
)

func deliverCheckedWithin(t *testing.T, d time.Duration, checks RuntimeInputChecks) (RuntimeInputResult, int, error) {
	t.Helper()
	f, proc, request := runtimeInputRawFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	result, err := f.m.DeliverRuntimeInputChecked(ctx, request, checks)
	return result, proc.sentCount(), err
}

// requireRefusedAtCeiling holds a deadline past the ceiling to the entry gate's
// own error, so a refusal from another check cannot stand in for it.
func requireRefusedAtCeiling(t *testing.T, deadline time.Duration) {
	t.Helper()
	result, sent, err := deliverCheckedWithin(t, deadline, runtimeInputChecksAllowForTest())
	if !errors.Is(err, ErrRuntimeInputAuthority) || result.Attempted || sent != 0 {
		t.Fatalf("deadline %s past the ceiling %s: %#v %v sent=%d", deadline, CheckedRuntimeInputCeiling(), result, err, sent)
	}
}

// A production build keeps the 250 ms section: a deadline past it is refused
// before anything is sent.
func TestRuntimeInputCheckedProductionCeilingIs250ms(t *testing.T) {
	if got := CheckedRuntimeInputCeiling(); got != 250*time.Millisecond {
		t.Fatalf("default ceiling = %s, want 250ms", got)
	}
	for _, d := range []time.Duration{500 * time.Millisecond, time.Second} {
		requireRefusedAtCeiling(t, d)
	}
}

// A real non-test build cannot widen the ceiling, even when it calls the
// exported setter directly.
func TestRuntimeInputCheckedProductionBuildRefusesTestCeiling(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "checked-ceiling")
	buildContext, cancelBuild := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancelBuild()
	build := exec.CommandContext(buildContext, "go", "build", "-o", binary, "./testdata/non_test_ceiling")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build non-test ceiling probe: %v\n%s", err, out)
	}
	out, err := exec.Command(binary).CombinedOutput()
	if err != nil {
		t.Fatalf("run non-test ceiling probe: %v\n%s", err, out)
	}
	for _, want := range []string{"before=250ms", "panic=sessions.SetTestCheckedRuntimeInputCeiling is test-only", "after=250ms"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("non-test probe missing %q:\n%s", want, out)
		}
	}
}

// The seam widens the ceiling for the test binary only, and its restore returns
// production behavior.
func TestRuntimeInputCheckedTestSeamWidensCeiling(t *testing.T) {
	restore := SetTestCheckedRuntimeInputCeiling(2 * time.Second)
	t.Cleanup(restore)
	if got := CheckedRuntimeInputCeiling(); got != 2*time.Second {
		t.Fatalf("ceiling with seam = %s, want 2s", got)
	}
	result, sent, err := deliverCheckedWithin(t, time.Second, runtimeInputChecksAllowForTest())
	if err != nil || !result.Attempted || result.Outcome != RuntimeInputAccepted || sent != 1 {
		t.Fatalf("1s deadline under a 2s seam: %#v %v sent=%d", result, err, sent)
	}
	requireRefusedAtCeiling(t, 3*time.Second)
	restore()
	if got := CheckedRuntimeInputCeiling(); got != 250*time.Millisecond {
		t.Fatalf("ceiling after restore = %s, want 250ms", got)
	}
	requireRefusedAtCeiling(t, time.Second)
}

// A restore puts back what its own call replaced, so a nested widening (a
// package-wide one and a per-test one) does not drop the outer one.
func TestRuntimeInputCheckedTestSeamRestoreReturnsThePreviousCeiling(t *testing.T) {
	outer := SetTestCheckedRuntimeInputCeiling(2 * time.Second)
	t.Cleanup(outer)
	inner := SetTestCheckedRuntimeInputCeiling(3 * time.Second)
	t.Cleanup(inner)
	inner()
	if got := CheckedRuntimeInputCeiling(); got != 2*time.Second {
		t.Fatalf("ceiling after the inner restore = %s, want the outer 2s", got)
	}
	outer()
	if got := CheckedRuntimeInputCeiling(); got != 250*time.Millisecond {
		t.Fatalf("ceiling after the outer restore = %s, want 250ms", got)
	}
}

// A restore removes only its own override, once, even when callers do not
// restore in stack order. No stale value can leak into a later test.
func TestRuntimeInputCheckedTestSeamRestoreIsScopedAndIdempotent(t *testing.T) {
	outer := SetTestCheckedRuntimeInputCeiling(2 * time.Second)
	t.Cleanup(outer)
	inner := SetTestCheckedRuntimeInputCeiling(3 * time.Second)
	t.Cleanup(inner)
	outer()
	if got := CheckedRuntimeInputCeiling(); got != 3*time.Second {
		t.Fatalf("out-of-order outer restore changed the active ceiling to %s", got)
	}
	outer()
	if got := CheckedRuntimeInputCeiling(); got != 3*time.Second {
		t.Fatalf("repeated outer restore changed the active ceiling to %s", got)
	}
	inner()
	if got := CheckedRuntimeInputCeiling(); got != 250*time.Millisecond {
		t.Fatalf("ceiling after both restores = %s, want 250ms", got)
	}
	inner()
	if got := CheckedRuntimeInputCeiling(); got != 250*time.Millisecond {
		t.Fatalf("repeated inner restore changed the ceiling to %s", got)
	}
}

// The seam only widens: a narrower ceiling would let a test pass by refusing.
func TestRuntimeInputCheckedTestSeamNeverNarrows(t *testing.T) {
	for _, d := range []time.Duration{100 * time.Millisecond, 249 * time.Millisecond} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("a ceiling of %s was accepted", d)
				}
			}()
			SetTestCheckedRuntimeInputCeiling(d)
		}()
		if got := CheckedRuntimeInputCeiling(); got != 250*time.Millisecond {
			t.Fatalf("ceiling after a refused %s = %s, want 250ms", d, got)
		}
	}
	restore := SetTestCheckedRuntimeInputCeiling(maxCheckedRuntimeInputLease)
	t.Cleanup(restore)
	if got := CheckedRuntimeInputCeiling(); got != maxCheckedRuntimeInputLease {
		t.Fatalf("ceiling at the production boundary = %s, want 250ms", got)
	}
}

// Readers never see a torn or foreign value while overrides are set and restored
// from other goroutines (the race detector holds the locking).
func TestRuntimeInputCheckedCeilingConcurrentSetRestoreRead(t *testing.T) {
	allowed := map[time.Duration]bool{250 * time.Millisecond: true, 2 * time.Second: true, 3 * time.Second: true}
	var foreign atomic.Int64
	stop := make(chan struct{})
	var readers, writers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
					if !allowed[CheckedRuntimeInputCeiling()] {
						foreign.Add(1)
					}
				}
			}
		})
	}
	for _, d := range []time.Duration{2 * time.Second, 3 * time.Second} {
		for range 2 {
			writers.Go(func() {
				for range 200 {
					restore := SetTestCheckedRuntimeInputCeiling(d)
					done := make(chan struct{})
					go func() { restore(); close(done) }()
					<-done
				}
			})
		}
	}
	writers.Wait()
	close(stop)
	readers.Wait()
	if n := foreign.Load(); n != 0 {
		t.Fatalf("%d reads returned a value nobody set", n)
	}
	if got := CheckedRuntimeInputCeiling(); got != 250*time.Millisecond {
		t.Fatalf("ceiling after every restore = %s, want 250ms", got)
	}
}

// The seam moves only the entry ceiling: a work lease that ends inside the
// widened deadline still refuses, so a wider test ceiling never outlives a proof.
func TestRuntimeInputCheckedTestSeamDoesNotExtendTheWorkLease(t *testing.T) {
	t.Cleanup(SetTestCheckedRuntimeInputCeiling(2 * time.Second))
	f, runner := newManagedStopWorkFixture(t, store.Config{Engine: store.EngineSQLite, DSN: ":memory:", Debug: true})
	managed, _, _, _ := f.boundRun(t)
	target := runtimeInputTargetForTest(t, f, managed.RunRef)
	request := RuntimeInputRequest{Target: target, Authority: runtimeInputAuthorityForTest(t, f, target), Mode: RuntimeInputRaw, Raw: []byte(`{"type":"user"}`)}
	deliver := func(request RuntimeInputRequest) (RuntimeInputResult, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return f.m.DeliverRuntimeInputChecked(ctx, request, runtimeInputChecksAllowForTest())
	}
	short := request
	work := *target.Work
	work.LeaseExpiresAt = time.Now().Add(300 * time.Millisecond).Format(time.RFC3339Nano)
	short.Target.Work = &work
	if result, err := deliver(short); err == nil || result.Attempted || runner.lastProc().sentCount() != 0 {
		t.Fatalf("a 1s deadline past a 300ms work lease crossed: %#v %v", result, err)
	}
	if result, err := deliver(request); err != nil || result.Outcome != RuntimeInputAccepted || runner.lastProc().sentCount() != 1 {
		t.Fatalf("the same delivery with the real lease under the 2s seam: %#v %v", result, err)
	}
}

// The symptom of the loaded runner: a stall past 250 ms inside the section.
// Production refuses with nothing sent and the stall never finishes; a test that
// widened the ceiling tolerates the whole stall and the delivery lands.
func TestRuntimeInputCheckedStallInsideSectionLandsOnlyUnderTheSeam(t *testing.T) {
	const stall = 300 * time.Millisecond
	var entered, stalled atomic.Bool
	checks := runtimeInputChecksAllowForTest()
	checks.send = func(ctx context.Context, _ store.Scope) error {
		entered.Store(true)
		select {
		case <-time.After(stall):
			if err := ctx.Err(); err != nil {
				return err
			}
			stalled.Store(true)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	result, sent, err := deliverCheckedWithin(t, 250*time.Millisecond, checks)
	if (!errors.Is(err, ErrRuntimeInputRefused) && !errors.Is(err, context.DeadlineExceeded)) || result.Attempted || sent != 0 || !entered.Load() || stalled.Load() {
		t.Fatalf("a %s stall inside the 250ms section: %#v %v sent=%d entered=%v stalled=%v", stall, result, err, sent, entered.Load(), stalled.Load())
	}
	t.Cleanup(SetTestCheckedRuntimeInputCeiling(2 * time.Second))
	result, sent, err = deliverCheckedWithin(t, time.Second, checks)
	if err != nil || !result.Attempted || result.Outcome != RuntimeInputAccepted || sent != 1 || !stalled.Load() {
		t.Fatalf("a %s stall under the widened section: %#v %v sent=%d stalled=%v", stall, result, err, sent, stalled.Load())
	}
}
