// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/sdk"
)

// liveSource is a controllable source for the live-reconfiguration tests. It
// records Open/Close, signals when Gather is entered, and (unless one-shot)
// blocks in Gather until its ctx is canceled — the realistic streaming shape.
type liveSource struct {
	name    string
	openErr error
	poll    bool // when true Gather returns nil (the engine re-polls); else streams
	opened  atomic.Bool
	closed  atomic.Bool
	closes  atomic.Int32
	entered chan struct{}
	once    sync.Once
}

func newLiveSource(name string) *liveSource {
	return &liveSource{name: name, entered: make(chan struct{})}
}

func (s *liveSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: s.name, Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}

func (s *liveSource) Open(context.Context, sdk.Config) error {
	if s.openErr != nil {
		return s.openErr
	}
	s.opened.Store(true)
	return nil
}

func (s *liveSource) Gather(ctx context.Context, _ sdk.Sink) error {
	s.once.Do(func() { close(s.entered) })
	if s.poll {
		return nil // a batch/poll pass: the engine sleeps then re-runs
	}
	<-ctx.Done()
	return ctx.Err()
}

func (s *liveSource) Close(context.Context) error { s.closed.Store(true); s.closes.Add(1); return nil }

// waitEntered blocks until the source's Gather has run at least once.
func (s *liveSource) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-s.entered:
	case <-time.After(2 * time.Second):
		t.Fatalf("source %q Gather never ran", s.name)
	}
}

func startedRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	rt := runtime.New(runtime.Options{Logger: quiet()})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})
	return rt
}

func liveCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func inventoryNames(rt *runtime.Runtime) map[string]runtime.Status {
	out := map[string]runtime.Status{}
	for _, s := range rt.LiveSourceInventory() {
		out[s.Name] = s.Status
	}
	return out
}

func TestAddSourceLiveRunsAndStopsAsOne(t *testing.T) {
	rt := startedRuntime(t)
	src := newLiveSource("live.a")

	if err := rt.AddSourceLive(liveCtx(t), src, sdk.Config{}, "tenant-x", 0); err != nil {
		t.Fatalf("AddSourceLive: %v", err)
	}
	src.waitEntered(t)

	if !src.opened.Load() {
		t.Error("source was not Opened")
	}
	if inv := inventoryNames(rt); inv["live.a"] != runtime.StatusRunning {
		t.Errorf("inventory[live.a] = %q, want running", inv["live.a"])
	}

	// Stop must cancel the live-added source too (the graph stops as one).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rt.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !src.closed.Load() {
		t.Error("live-added source was not Closed by Stop")
	}
}

func TestRemoveSourceLiveIsolatesOthers(t *testing.T) {
	rt := startedRuntime(t)
	a, b := newLiveSource("live.a"), newLiveSource("live.b")
	if err := rt.AddSourceLive(liveCtx(t), a, sdk.Config{}, "t", 0); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddSourceLive(liveCtx(t), b, sdk.Config{}, "t", 0); err != nil {
		t.Fatal(err)
	}
	a.waitEntered(t)
	b.waitEntered(t)

	if err := rt.RemoveSourceLive(liveCtx(t), "live.a"); err != nil {
		t.Fatalf("RemoveSourceLive: %v", err)
	}
	if !a.closed.Load() {
		t.Error("removed source was not Closed")
	}
	inv := inventoryNames(rt)
	if _, present := inv["live.a"]; present {
		t.Error("removed source still in inventory")
	}
	if inv["live.b"] != runtime.StatusRunning {
		t.Errorf("sibling status = %q, want still running", inv["live.b"])
	}
	if b.closed.Load() {
		t.Error("removing one source closed an unrelated sibling")
	}
}

func TestRemoveSourceLivePollingSource(t *testing.T) {
	rt := startedRuntime(t)
	p := newLiveSource("live.poll")
	p.poll = true
	if err := rt.AddSourceLive(liveCtx(t), p, sdk.Config{}, "t", 30*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	p.waitEntered(t)
	// Removal must wake the poll source's sleep and drain it promptly.
	if err := rt.RemoveSourceLive(liveCtx(t), "live.poll"); err != nil {
		t.Fatalf("RemoveSourceLive(poll): %v", err)
	}
	if !p.closed.Load() {
		t.Error("polling source was not Closed on live remove")
	}
}

func TestAddSourceLiveDenyClosedOnOpenFailure(t *testing.T) {
	rt := startedRuntime(t)
	bad := newLiveSource("live.x")
	bad.openErr = errors.New("cannot reach target")

	err := rt.AddSourceLive(liveCtx(t), bad, sdk.Config{}, "t", 0)
	if err == nil {
		t.Fatal("expected AddSourceLive to fail when Open fails")
	}
	if _, present := inventoryNames(rt)["live.x"]; present {
		t.Error("a source whose Open failed must not be wired (deny-closed)")
	}
	// The name must be freed so a corrected config can re-add under the same name.
	good := newLiveSource("live.x")
	if err := rt.AddSourceLive(liveCtx(t), good, sdk.Config{}, "t", 0); err != nil {
		t.Fatalf("re-add after a failed Open should succeed (name freed): %v", err)
	}
	good.waitEntered(t)
}

func TestAddSourceLiveRejectsDuplicateName(t *testing.T) {
	rt := startedRuntime(t)
	a := newLiveSource("live.dup")
	if err := rt.AddSourceLive(liveCtx(t), a, sdk.Config{}, "t", 0); err != nil {
		t.Fatal(err)
	}
	a.waitEntered(t)
	dup := newLiveSource("live.dup")
	if err := rt.AddSourceLive(liveCtx(t), dup, sdk.Config{}, "t", 0); err == nil {
		t.Error("expected a duplicate-name rejection")
	}
	if dup.opened.Load() {
		t.Error("a rejected duplicate must not be Opened")
	}
}

func TestReplaceSourceLiveRotatesAndIsDenyClosed(t *testing.T) {
	rt := startedRuntime(t)
	v1 := newLiveSource("live.rot")
	if err := rt.AddSourceLive(liveCtx(t), v1, sdk.Config{}, "t", 0); err != nil {
		t.Fatal(err)
	}
	v1.waitEntered(t)

	// A healthy replacement rotates in place: old closed, new running.
	v2 := newLiveSource("live.rot")
	if err := rt.ReplaceSourceLive(liveCtx(t), v2, sdk.Config{}, "t", 0); err != nil {
		t.Fatalf("ReplaceSourceLive: %v", err)
	}
	v2.waitEntered(t)
	if !v1.closed.Load() {
		t.Error("old instance was not Closed on rotate")
	}
	if !v2.opened.Load() {
		t.Error("new instance was not Opened on rotate")
	}
	if inv := inventoryNames(rt); len(inv) != 1 || inv["live.rot"] != runtime.StatusRunning {
		t.Errorf("after rotate inventory = %v, want one running live.rot", inv)
	}

	// A replacement whose Open fails must leave the CURRENT source running.
	v3 := newLiveSource("live.rot")
	v3.openErr = errors.New("bad new config")
	if err := rt.ReplaceSourceLive(liveCtx(t), v3, sdk.Config{}, "t", 0); err == nil {
		t.Fatal("expected ReplaceSourceLive to fail when the new Open fails")
	}
	if v2.closed.Load() {
		t.Error("a failed rotate must NOT tear down the running source (deny-closed)")
	}
	if inv := inventoryNames(rt); inv["live.rot"] != runtime.StatusRunning {
		t.Errorf("after a failed rotate, status = %q, want still running", inv["live.rot"])
	}
}

func TestReplaceSourceLiveUnknownSource(t *testing.T) {
	rt := startedRuntime(t)
	v := newLiveSource("live.ghost")
	if err := rt.ReplaceSourceLive(liveCtx(t), v, sdk.Config{}, "t", 0); !errors.Is(err, runtime.ErrSourceNotFound) {
		t.Errorf("ReplaceSourceLive(unknown) = %v, want ErrSourceNotFound", err)
	}
}

func TestRemoveSourceLiveUnknown(t *testing.T) {
	rt := startedRuntime(t)
	if err := rt.RemoveSourceLive(liveCtx(t), "nope"); !errors.Is(err, runtime.ErrSourceNotFound) {
		t.Errorf("RemoveSourceLive(unknown) = %v, want ErrSourceNotFound", err)
	}
}

func TestLiveMethodsRejectedWhenNotRunning(t *testing.T) {
	// Before Start.
	rt := runtime.New(runtime.Options{Logger: quiet()})
	if err := rt.AddSourceLive(liveCtx(t), newLiveSource("a"), sdk.Config{}, "t", 0); !errors.Is(err, runtime.ErrNotRunning) {
		t.Errorf("AddSourceLive before Start = %v, want ErrNotRunning", err)
	}
	if err := rt.RemoveSourceLive(liveCtx(t), "a"); !errors.Is(err, runtime.ErrNotRunning) {
		t.Errorf("RemoveSourceLive before Start = %v, want ErrNotRunning", err)
	}

	// After Stop.
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := rt.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddSourceLive(liveCtx(t), newLiveSource("b"), sdk.Config{}, "t", 0); !errors.Is(err, runtime.ErrNotRunning) {
		t.Errorf("AddSourceLive after Stop = %v, want ErrNotRunning", err)
	}
}

// TestLiveReconfigureConcurrent hammers the live API from many goroutines while
// the engine runs, to prove the reloadMu/r.mu discipline is race-free (run with
// -race). Each op uses a distinct name so the assertions stay deterministic.
func TestLiveReconfigureConcurrent(t *testing.T) {
	rt := startedRuntime(t)
	const n = 24
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("live.c%d", i)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			src := newLiveSource(name)
			if err := rt.AddSourceLive(ctx, src, sdk.Config{}, "t", 0); err != nil {
				return
			}
			// Half are removed again; half stay for Stop to reap.
			if i%2 == 0 {
				_ = rt.RemoveSourceLive(ctx, name)
			}
		}(i)
	}
	wg.Wait()
	// Whatever survived must be consistently running.
	for name, st := range inventoryNames(rt) {
		if st != runtime.StatusRunning {
			t.Errorf("surviving source %q status = %q, want running", name, st)
		}
	}
}

// TestStopDuringLiveReconfigure races Stop against in-flight live adds (run with
// -race). It locks in the reloadMu-in-Stop invariant: a teardown and a live
// mutation never overlap, so no connector is Closed twice and there is no
// wg.Add-after-Wait. Each source must be Closed at most once.
func TestStopDuringLiveReconfigure(t *testing.T) {
	rt := runtime.New(runtime.Options{Logger: quiet()})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	const n = 20
	srcs := make([]*liveSource, n)
	for i := range srcs {
		srcs[i] = newLiveSource(fmt.Sprintf("race.%d", i))
	}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			// Either succeeds (then Stop will close it) or loses the race to Stop and
			// returns ErrNotRunning/ErrStopped (and closes itself in the abort path).
			_ = rt.AddSourceLive(ctx, srcs[i], sdk.Config{}, "t", 0)
		}(i)
	}
	// Race Stop against the adders.
	stopErrCh := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		stopErrCh <- rt.Stop(ctx)
	}()

	wg.Wait()
	if err := <-stopErrCh; err != nil {
		t.Fatalf("Stop during live reconfigure: %v", err)
	}
	// A second Stop is still a clean no-op (idempotent under reloadMu).
	_ = rt.Stop(context.Background())

	for _, s := range srcs {
		if c := s.closes.Load(); c > 1 {
			t.Errorf("source %q Closed %d times; must be at most once", s.name, c)
		}
	}
}
