// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventbus_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
)

// quietLogger discards bus warnings/errors so a test that deliberately triggers
// a handler panic does not spam output.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func edgeEvent() event.Event {
	return event.FromObservation("t1", "src", model.EdgeObservation{
		ResourceRef: "public.t", Mode: model.ModeRead, ObservedAt: time.Now().UTC(),
	})
}

// recv waits for ch to deliver, failing the test on timeout.
func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		panic("unreachable")
	}
}

func TestPublishDelivers(t *testing.T) {
	b := eventbus.NewInProc(eventbus.Options{Logger: quietLogger()})
	defer b.Close()

	got := make(chan event.Event, 1)
	_, err := b.Subscribe(nil, func(_ context.Context, e event.Event) error {
		got <- e
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := b.Publish(context.Background(), edgeEvent()); err != nil {
		t.Fatal(err)
	}
	e := recv(t, got)
	if e.Type != event.TypeEdgeObserved {
		t.Errorf("got %q, want edge.observed", e.Type)
	}
	if _, ok := event.EdgeOf(e); !ok {
		t.Error("payload did not survive delivery")
	}
}

func TestTypeFilter(t *testing.T) {
	b := eventbus.NewInProc(eventbus.Options{Logger: quietLogger()})
	defer b.Close()

	hits := make(chan event.Type, 4)
	// Only interested in cost events.
	_, err := b.Subscribe([]event.Type{event.TypeCostSampled}, func(_ context.Context, e event.Event) error {
		hits <- e.Type
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	_ = b.Publish(ctx, edgeEvent()) // must NOT be delivered
	_ = b.Publish(ctx, event.FromObservation("t1", "src", model.CostSample{CostMicroUSD: 1, OccurredAt: time.Now().UTC()}))

	if got := recv(t, hits); got != event.TypeCostSampled {
		t.Errorf("first delivered event = %q, want cost.sampled (edge should have been filtered)", got)
	}
	// No second delivery should arrive.
	select {
	case extra := <-hits:
		t.Errorf("unexpected extra delivery: %q", extra)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestFanOutToMultipleSubscribers(t *testing.T) {
	b := eventbus.NewInProc(eventbus.Options{Logger: quietLogger()})
	defer b.Close()

	const n = 5
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		_, err := b.Subscribe(nil, func(_ context.Context, _ event.Event) error {
			wg.Done()
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Publish(context.Background(), edgeEvent()); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("not all subscribers received the event")
	}
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	b := eventbus.NewInProc(eventbus.Options{Logger: quietLogger()})
	defer b.Close()

	var count atomic.Int64
	sub, err := b.Subscribe(nil, func(_ context.Context, _ event.Event) error {
		count.Add(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sub.Unsubscribe()
	// Idempotent: a second call must not block or panic.
	sub.Unsubscribe()

	if err := b.Publish(context.Background(), edgeEvent()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if got := count.Load(); got != 0 {
		t.Errorf("unsubscribed handler ran %d times, want 0", got)
	}
}

func TestPanicIsolation(t *testing.T) {
	b := eventbus.NewInProc(eventbus.Options{Logger: quietLogger()})
	defer b.Close()

	// A panicking subscriber must not stop the bus or starve a healthy one.
	if _, err := b.Subscribe(nil, func(_ context.Context, _ event.Event) error {
		panic("boom")
	}); err != nil {
		t.Fatal(err)
	}
	healthy := make(chan struct{}, 1)
	if _, err := b.Subscribe(nil, func(_ context.Context, _ event.Event) error {
		healthy <- struct{}{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := b.Publish(context.Background(), edgeEvent()); err != nil {
		t.Fatal(err)
	}
	recv(t, healthy) // healthy subscriber still delivered

	// And the bus survives a second publish after a panic.
	if err := b.Publish(context.Background(), edgeEvent()); err != nil {
		t.Fatal(err)
	}
	recv(t, healthy)
}

func TestClosedBusRejects(t *testing.T) {
	b := eventbus.NewInProc(eventbus.Options{Logger: quietLogger()})
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	// Close is idempotent.
	if err := b.Close(); err != nil {
		t.Fatalf("second Close should be nil, got %v", err)
	}
	if err := b.Publish(context.Background(), edgeEvent()); err != eventbus.ErrBusClosed {
		t.Errorf("Publish after Close = %v, want ErrBusClosed", err)
	}
	if _, err := b.Subscribe(nil, func(context.Context, event.Event) error { return nil }); err != eventbus.ErrBusClosed {
		t.Errorf("Subscribe after Close = %v, want ErrBusClosed", err)
	}
}

func TestPublishContextCancellation(t *testing.T) {
	// A buffer of 1 and a handler that blocks lets us saturate the queue, so a
	// publisher with a canceled context returns ctx.Err() rather than blocking
	// forever.
	b := eventbus.NewInProc(eventbus.Options{Logger: quietLogger(), Buffer: 1})
	defer b.Close()

	block := make(chan struct{})
	defer close(block)
	if _, err := b.Subscribe(nil, func(_ context.Context, _ event.Event) error {
		<-block // never returns until test ends → queue fills
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Fill: first event goes to the handler (blocked), second fills the buffer,
	// third would block — so cancel and expect ctx.Err.
	_ = b.Publish(context.Background(), edgeEvent())
	_ = b.Publish(context.Background(), edgeEvent())
	cancel()
	if err := b.Publish(ctx, edgeEvent()); err != context.Canceled {
		t.Errorf("saturated Publish with canceled ctx = %v, want context.Canceled", err)
	}
}

func TestConcurrentPublishSubscribe(t *testing.T) {
	b := eventbus.NewInProc(eventbus.Options{Logger: quietLogger()})
	defer b.Close()

	var delivered atomic.Int64
	var wg sync.WaitGroup
	// Churn subscriptions while publishing concurrently; -race guards the bus.
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sub, err := b.Subscribe(nil, func(_ context.Context, _ event.Event) error {
				delivered.Add(1)
				return nil
			})
			if err != nil {
				return
			}
			for range 20 {
				_ = b.Publish(context.Background(), edgeEvent())
			}
			sub.Unsubscribe()
		}()
	}
	wg.Wait()
	// Exact count is non-deterministic (subscriptions come and go); the point is
	// the race detector sees no data race and nothing deadlocks.
	_ = delivered.Load()
}

// TestSubscribeNamedAndStats covers the saturation snapshot: named
// subscriptions label SubscriberStats, depth/capacity reflect the queue, and
// the publish-blocked counter ticks when a publisher hits a full queue.
func TestSubscribeNamedAndStats(t *testing.T) {
	bus := eventbus.NewInProc(eventbus.Options{Logger: quietLogger(), Buffer: 1})
	defer bus.Close()

	named, ok := bus.(eventbus.NamedSubscriber)
	if !ok {
		t.Fatal("in-proc bus must implement NamedSubscriber")
	}
	sp, ok := bus.(eventbus.StatsProvider)
	if !ok {
		t.Fatal("in-proc bus must implement StatsProvider")
	}

	release := make(chan struct{})
	first := make(chan struct{}, 1)
	sub, err := named.SubscribeNamed("olivares.test", []event.Type{event.TypeEdgeObserved}, func(context.Context, event.Event) error {
		first <- struct{}{}
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Unsubscribe()

	st := sp.BusStats()
	if len(st.Subscribers) != 1 || st.Subscribers[0].Name != "olivares.test" || st.Subscribers[0].Capacity != 1 {
		t.Fatalf("stats should name the subscriber with its capacity: %+v", st.Subscribers)
	}

	// Fill: one event goes to the (blocked) handler, one sits in the buffer.
	if err := bus.Publish(context.Background(), edgeEvent()); err != nil {
		t.Fatal(err)
	}
	<-first // the handler now holds the first event
	if err := bus.Publish(context.Background(), edgeEvent()); err != nil {
		t.Fatal(err)
	}
	if got := sp.BusStats().Subscribers[0].Depth; got != 1 {
		t.Fatalf("queue depth should be 1 with the buffer full, got %d", got)
	}

	// A third publish finds the queue full → blocked counter ticks; cancel the
	// publish via ctx so the test never deadlocks.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := bus.Publish(ctx, edgeEvent()); err == nil {
		t.Fatal("publish into a full queue with an expiring ctx should return its error")
	}
	if got := sp.BusStats().PublishBlocked; got != 1 {
		t.Fatalf("publish-blocked counter: want 1, got %d", got)
	}
	close(release)
}

// TestEnqueuedHandledIsACompletionBarrier pins the property the counters exist
// for: Handled reaches Enqueued only once the handler has RETURNED, so a caller
// that snapshots Enqueued after Publish and waits for Handled knows the work is
// finished. Queue depth cannot express this — the assertions below show depth
// already reading 0 while the handler is still running, which is precisely the
// gap a "wait for depth==0, then sleep a bit" test harness falls into.
func TestEnqueuedHandledIsACompletionBarrier(t *testing.T) {
	bus := eventbus.NewInProc(eventbus.Options{Logger: quietLogger()})
	defer bus.Close()
	sp := bus.(eventbus.StatsProvider)

	entered := make(chan struct{})
	release := make(chan struct{})
	sub, err := bus.Subscribe([]event.Type{event.TypeEdgeObserved}, func(context.Context, event.Event) error {
		entered <- struct{}{}
		<-release
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Unsubscribe()

	if err := bus.Publish(context.Background(), edgeEvent()); err != nil {
		t.Fatal(err)
	}
	if got := sp.BusStats().Enqueued; got != 1 {
		t.Fatalf("Enqueued after one publish to one subscriber = %d, want 1", got)
	}
	<-entered // the handler is now RUNNING, not queued

	st := sp.BusStats()
	if st.Subscribers[0].Depth != 0 {
		t.Fatalf("depth should already be 0 with the event in flight, got %d", st.Subscribers[0].Depth)
	}
	if st.Handled != 0 {
		t.Fatalf("Handled must not tick until the handler returns, got %d", st.Handled)
	}

	close(release)
	waitUntil(t, "handler completion", func() bool { return sp.BusStats().Handled == 1 })
}

// TestHandledCountsRecoveredPanics: a panicking handler still completes its
// invocation, so it must not stall a barrier waiting on Handled.
func TestHandledCountsRecoveredPanics(t *testing.T) {
	bus := eventbus.NewInProc(eventbus.Options{Logger: quietLogger()})
	defer bus.Close()
	sp := bus.(eventbus.StatsProvider)

	if _, err := bus.Subscribe([]event.Type{event.TypeEdgeObserved}, func(context.Context, event.Event) error {
		panic("boom")
	}); err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(context.Background(), edgeEvent()); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "panicking handler counted as handled", func() bool {
		st := sp.BusStats()
		return st.Handled == 1 && st.HandlerErrors == 1
	})
}

// TestEnqueuedCountsPerMatchingSubscriber: a fan-out to N matching subscribers
// is N enqueues, so a barrier built on the pair waits for ALL of them.
func TestEnqueuedCountsPerMatchingSubscriber(t *testing.T) {
	bus := eventbus.NewInProc(eventbus.Options{Logger: quietLogger()})
	defer bus.Close()
	sp := bus.(eventbus.StatsProvider)

	var seen atomic.Int64
	for i := 0; i < 3; i++ {
		if _, err := bus.Subscribe([]event.Type{event.TypeEdgeObserved}, func(context.Context, event.Event) error {
			seen.Add(1)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// A subscriber filtering on a type nobody publishes must NOT be counted, or
	// a barrier would wait forever for an event that was never sent to it.
	if _, err := bus.Subscribe([]event.Type{event.TypeFindingReported}, func(context.Context, event.Event) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := bus.Publish(context.Background(), edgeEvent()); err != nil {
		t.Fatal(err)
	}
	if got := sp.BusStats().Enqueued; got != 3 {
		t.Fatalf("Enqueued = %d, want 3 (one per MATCHING subscriber)", got)
	}
	waitUntil(t, "all three handlers", func() bool { return sp.BusStats().Handled == 3 })
	if got := seen.Load(); got != 3 {
		t.Fatalf("handlers actually run = %d, want 3", got)
	}
}

// TestHandlerErrorDemotion: an error matching Options.DemoteError still counts
// in HandlerErrors (the SLI sees it) while the bus logs it at Debug, not Warn.
func TestHandlerErrorDemotion(t *testing.T) {
	demoted := errors.New("expected on this node")
	bus := eventbus.NewInProc(eventbus.Options{
		Logger:      quietLogger(),
		DemoteError: func(err error) bool { return errors.Is(err, demoted) },
	})
	defer bus.Close()

	_, err := bus.Subscribe([]event.Type{event.TypeEdgeObserved}, func(context.Context, event.Event) error {
		return demoted
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(context.Background(), edgeEvent()); err != nil {
		t.Fatal(err)
	}
	sp := bus.(eventbus.StatsProvider)
	waitUntil(t, "handler error counted", func() bool { return sp.BusStats().HandlerErrors == 1 })
}

// waitUntil polls cond briefly (handler accounting happens on the subscriber
// goroutine, just after the handler returns).
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
