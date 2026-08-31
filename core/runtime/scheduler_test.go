// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

func quietScheduler() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type schedulerModule struct {
	name string
	got  chan event.Event
}

func (m *schedulerModule) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: m.name, Type: sdk.TypeModule, APIVersion: sdk.APIVersion}
}
func (m *schedulerModule) Init(_ context.Context, host sdk.Host) error {
	_, err := host.Subscribe([]event.Type{event.TypeEdgeObserved}, func(_ context.Context, e event.Event) error {
		m.got <- e
		return nil
	})
	return err
}
func (*schedulerModule) Start(context.Context) error { return nil }
func (*schedulerModule) Stop(context.Context) error  { return nil }

// countingSource records every Gather pass and returns immediately (a BATCH/poll
// source). When fail is set each pass returns an error (to exercise re-poll
// backoff); when emit is set it emits one edge per pass (to prove re-polled passes
// reach the bus).
type countingSource struct {
	name  string
	fail  bool
	emit  bool
	calls atomic.Int64
}

func (c *countingSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: c.name, Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (c *countingSource) Open(context.Context, sdk.Config) error { return nil }
func (c *countingSource) Gather(ctx context.Context, sink sdk.Sink) error {
	c.calls.Add(1)
	if c.emit {
		_ = sink.Emit(ctx, model.EdgeObservation{
			OriginRef: "agent", ResourceRef: "public.t", Mode: model.ModeRead, ObservedAt: time.Now().UTC(),
		})
	}
	if c.fail {
		return errors.New("boom")
	}
	return nil
}
func (c *countingSource) Close(context.Context) error { return nil }

// wedgedSource deliberately ignores Gather cancellation. Its block channel is
// never closed: Stop must take the timeout-drain arm and still invoke Close.
type wedgedSource struct {
	name    string
	entered chan struct{}
	block   chan struct{}
	closes  atomic.Int64
}

func (s *wedgedSource) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: s.name, Type: sdk.TypeSource, APIVersion: sdk.APIVersion}
}
func (*wedgedSource) Open(context.Context, sdk.Config) error { return nil }
func (s *wedgedSource) Gather(context.Context, sdk.Sink) error {
	close(s.entered)
	<-s.block
	return nil
}
func (s *wedgedSource) Close(context.Context) error {
	s.closes.Add(1)
	return nil
}

func statusByName(rt *Runtime, name string) (ComponentStatus, bool) {
	for _, cs := range rt.Status() {
		if cs.Name == name {
			return cs, true
		}
	}
	return ComponentStatus{}, false
}

// TestPollSourceRePollsAndEmits proves the engine-owned scheduler re-runs a batch
// source's Gather at its interval (not once), and the re-polled passes reach the
// bus — the difference between a dormant seed and a live sampling source.
func TestPollSourceRePollsAndEmits(t *testing.T) {
	rt := New(Options{Logger: quietScheduler()})
	src := &countingSource{name: "poll.source", emit: true}
	mod := &schedulerModule{name: "sink.module", got: make(chan event.Event, 64)}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.AddPollSource(src, sdk.Config{}, "tenant-x", 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Collect at least 3 re-polled emissions within a generous window.
	deadline := time.After(2 * time.Second)
	for got := 0; got < 3; {
		select {
		case <-mod.got:
			got++
		case <-deadline:
			t.Fatalf("re-poll emitted only %d events; calls=%d", got, src.calls.Load())
		}
	}

	// The source is a live, scheduled component: Running, not Stopped or Failed.
	cs, _ := statusByName(rt, "poll.source")
	if cs.Status != StatusRunning {
		t.Errorf("poll source status = %q, want running", cs.Status)
	}

	// Cancellation: after Stop, no further Gather passes run.
	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rt.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	settled := src.calls.Load()
	time.Sleep(80 * time.Millisecond)
	if got := src.calls.Load(); got != settled {
		t.Errorf("Gather ran %d more times after Stop (want 0)", got-settled)
	}
}

// TestPollSourceRetriesWithBackoffOnError proves a polling source whose Gather
// errors is RETRIED (not left down like a one-shot source) and stays Running with
// its last error recorded — the auto-restart-with-backoff S02 deferred.
func TestPollSourceRetriesWithBackoffOnError(t *testing.T) {
	rt := New(Options{Logger: quietScheduler()})
	src := &countingSource{name: "flaky.source", fail: true}
	if err := rt.AddPollSource(src, sdk.Config{}, "tenant-x", 10*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})

	// It must retry: ≥2 Gather passes despite each erroring.
	deadline := time.After(2 * time.Second)
	for src.calls.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("failing poll source retried only %d times; want ≥2", src.calls.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}

	// A failing POLL source stays Running (scheduled) with its last error visible —
	// it is NOT marked Failed-and-down (that is the one-shot source's behavior).
	cs, _ := statusByName(rt, "flaky.source")
	if cs.Status != StatusRunning {
		t.Errorf("failing poll source status = %q, want running (it is being retried)", cs.Status)
	}
	if cs.Err == "" {
		t.Error("failing poll source should record its last Gather error")
	}
}

// TestPollSourceBackoffJitterWithinBand proves failed Gather retries use the
// jittered exponential delay while preserving the base backoff sequence.
func TestPollSourceBackoffJitterWithinBand(t *testing.T) {
	type sample struct {
		base time.Duration
		wait time.Duration
	}
	samples := make(chan sample, 4)
	factors := []float64{0.8, 1.2, 0.9}
	call := 0
	original := jitterRepollBackoff
	t.Cleanup(func() { jitterRepollBackoff = original })
	jitterRepollBackoff = func(base time.Duration) time.Duration {
		factor := factors[call%len(factors)]
		call++
		wait := time.Duration(float64(base) * factor)
		samples <- sample{base: base, wait: wait}
		return wait
	}

	rt := New(Options{Logger: quietScheduler()})
	src := &countingSource{name: "jitter.source", fail: true}
	const poll = 10 * time.Millisecond
	if err := rt.AddPollSource(src, sdk.Config{}, "tenant-x", poll); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.Stop(ctx)
	})

	wantBase := []time.Duration{poll, 2 * poll, 4 * poll}
	wantWait := []time.Duration{8 * time.Millisecond, 24 * time.Millisecond, 36 * time.Millisecond}
	for i := range wantBase {
		select {
		case got := <-samples:
			low, high := got.base*8/10, got.base*12/10
			if got.wait < low || got.wait > high {
				t.Errorf("jittered wait %s outside [%s,%s] for base %s", got.wait, low, high, got.base)
			}
			if got.base != wantBase[i] || got.wait != wantWait[i] {
				t.Errorf("retry %d = base %s wait %s, want base %s wait %s", i, got.base, got.wait, wantBase[i], wantWait[i])
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for jitter sample %d", i)
		}
	}
}

// TestOneShotSourceUnchanged confirms backward compatibility: a source registered
// with AddSource (poll=0) runs Gather exactly once and then Stops — not re-polled.
func TestOneShotSourceUnchanged(t *testing.T) {
	rt := New(Options{Logger: quietScheduler()})
	src := &countingSource{name: "oneshot.source"}
	if err := rt.AddSource(src, sdk.Config{}, "tenant-x"); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = rt.Stop(context.Background()) })

	// Wait for the single pass to complete and the source to settle as stopped.
	deadline := time.After(2 * time.Second)
	for {
		cs, _ := statusByName(rt, "oneshot.source")
		if cs.Status == StatusStopped {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("one-shot source never settled stopped (status now %q)", cs.Status)
		case <-time.After(5 * time.Millisecond):
		}
	}
	time.Sleep(60 * time.Millisecond)
	if got := src.calls.Load(); got != 1 {
		t.Errorf("one-shot source Gather ran %d times, want exactly 1", got)
	}
}

// TestStopReturnsWhenGatherIgnoresCancellation exercises Stop's timeout-drain
// arm: a wedged Gather cannot prevent the rest of teardown from completing.
func TestStopReturnsWhenGatherIgnoresCancellation(t *testing.T) {
	rt := New(Options{Logger: quietScheduler()})
	src := &wedgedSource{
		name: "wedged.source", entered: make(chan struct{}), block: make(chan struct{}),
	}
	if err := rt.AddSource(src, sdk.Config{}, "tenant-x"); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	select {
	case <-src.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("wedged Gather never started")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	stopDone := make(chan error, 1)
	go func() { stopDone <- rt.Stop(stopCtx) }()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop remained blocked after its context deadline")
	}
	if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
		t.Fatalf("Stop returned after %s, want bounded below 500ms", elapsed)
	}
	cs, ok := statusByName(rt, src.name)
	if !ok {
		t.Fatalf("source %q missing from status", src.name)
	}
	if cs.Status != StatusStopped && cs.Status != StatusFailed {
		t.Fatalf("wedged source status = %q, want stopped or failed", cs.Status)
	}
	if got := src.closes.Load(); got != 1 {
		t.Fatalf("Close calls = %d, want exactly 1", got)
	}
}

// TestSchedulePeriodicRunsImmediatelyAndRepeats proves the periodic-job scheduler
// (the path the roster SyncRoster rides) runs immediately when asked, then repeats
// on its interval, and is canceled on Stop.
func TestSchedulePeriodicRunsImmediatelyAndRepeats(t *testing.T) {
	rt := New(Options{Logger: quietScheduler()})
	var runs atomic.Int64
	if err := rt.SchedulePeriodic("job", 20*time.Millisecond, true, func(context.Context) error {
		runs.Add(1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Immediate pass + at least one interval pass.
	deadline := time.After(2 * time.Second)
	for runs.Load() < 2 {
		select {
		case <-deadline:
			t.Fatalf("periodic job ran only %d times; want ≥2", runs.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rt.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	settled := runs.Load()
	time.Sleep(80 * time.Millisecond)
	if got := runs.Load(); got != settled {
		t.Errorf("periodic job ran %d more times after Stop (want 0)", got-settled)
	}
}

// TestSchedulePeriodicGuards rejects an empty name, a non-positive interval and a
// nil func, and rejects registration after Start.
func TestSchedulePeriodicGuards(t *testing.T) {
	rt := New(Options{Logger: quietScheduler()})
	if err := rt.SchedulePeriodic("", time.Second, false, func(context.Context) error { return nil }); err == nil {
		t.Error("empty name should be rejected")
	}
	if err := rt.SchedulePeriodic("j", 0, false, func(context.Context) error { return nil }); err == nil {
		t.Error("non-positive interval should be rejected")
	}
	if err := rt.SchedulePeriodic("j", time.Second, false, nil); err == nil {
		t.Error("nil func should be rejected")
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Stop(context.Background()) })
	if err := rt.SchedulePeriodic("late", time.Second, false, func(context.Context) error { return nil }); err != ErrAlreadyStarted {
		t.Errorf("SchedulePeriodic after Start = %v, want ErrAlreadyStarted", err)
	}
}

// TestIngestLiftsObservationOntoBus proves the core side of CB-1 option C: a pushed
// observation (what the IngestService server calls Ingest with) is lifted onto the
// bus exactly as an in-process source's Sink would, stamped with the given
// tenant/source — so a remote collector's stream reaches the same subscribers.
func TestIngestLiftsObservationOntoBus(t *testing.T) {
	rt := New(Options{Logger: quietScheduler()})
	mod := &schedulerModule{name: "ingest.sink", got: make(chan event.Event, 8)}
	if err := rt.AddModule(mod, sdk.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = rt.Stop(context.Background()) })

	if err := rt.Ingest(context.Background(), "tenant-c", "edge-collector", model.EdgeObservation{
		OriginRef: "claude", ResourceRef: "s3://bucket", Mode: model.ModeWrite, ObservedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	select {
	case e := <-mod.got:
		if e.Type != event.TypeEdgeObserved {
			t.Errorf("event type = %q, want edge.observed", e.Type)
		}
		if e.Tenant != "tenant-c" {
			t.Errorf("event tenant = %q, want tenant-c", e.Tenant)
		}
		if e.Source != "edge-collector" {
			t.Errorf("event source = %q, want edge-collector", e.Source)
		}
		if e.ID == "" {
			t.Error("runtime should stamp an event ID on ingest")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pushed observation never reached the bus subscriber")
	}
}
