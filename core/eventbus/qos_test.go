// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventbus

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/event"
)

// classBus returns the in-proc bus as a ClassSubscriber (the QoS extension).
func classBus(t *testing.T, buf int) (Bus, ClassSubscriber) {
	t.Helper()
	b := NewInProc(Options{Buffer: buf})
	cs, ok := b.(ClassSubscriber)
	if !ok {
		t.Fatal("in-proc bus must implement ClassSubscriber")
	}
	return b, cs
}

const evT = event.Type("qos.test")

// TestQoS_SlowOptionalDoesNotStallEnforcement is the core invariant: a
// telemetry subscriber that is WEDGED (its handler never returns, so its queue
// fills) must not stall the Publish loop that also feeds an enforcement
// subscriber. With the class-aware fan-out, telemetry is a drop lane (non-blocking
// send, dropped-with-counter), so every enforcement event is still delivered and
// Publish never blocks; without it, Publish would block on the full telemetry
// queue and the enforcement subscriber would starve.
func TestQoS_SlowOptionalDoesNotStallEnforcement(t *testing.T) {
	const buf = 2
	b, cs := classBus(t, buf)
	defer b.Close()

	// Telemetry handler: wedged forever (simulates a hung/slow optional consumer).
	wedge := make(chan struct{})
	defer close(wedge)
	_, err := cs.SubscribeClass(ClassTelemetry, "telemetry", []event.Type{evT}, func(ctx context.Context, _ event.Event) error {
		<-wedge // never returns until the test ends
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Enforcement handler: fast, counts what it receives.
	var enforced atomic.Int64
	_, err = cs.SubscribeClass(ClassEnforcement, "enforcement", []event.Type{evT}, func(ctx context.Context, _ event.Event) error {
		enforced.Add(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	const n = 50
	done := make(chan error, 1)
	go func() {
		for i := 0; i < n; i++ {
			if err := b.Publish(context.Background(), event.Event{Type: evT}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()

	// If a wedged telemetry lane stalls Publish, this times out (the bug).
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("publish error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Publish stalled — a wedged telemetry subscriber blocked the enforcement fan-out")
	}

	// Every enforcement event must have been delivered (block lane, fast handler).
	deadline := time.Now().Add(2 * time.Second)
	for enforced.Load() < n && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := enforced.Load(); got != n {
		t.Fatalf("enforcement delivered %d/%d events (must lose none)", got, n)
	}

	// Telemetry must have DROPPED (bounded lane), and the drop must be counted.
	sp, ok := b.(StatsProvider)
	if !ok {
		t.Fatal("bus must implement StatsProvider")
	}
	st := sp.BusStats()
	if st.DroppedTelemetry == 0 {
		t.Fatalf("telemetry saturation must be counted; DroppedTelemetry=0")
	}
}

// TestQoS_DefaultClassBlocks pins that a plain Subscribe (no class) keeps the
// original durable/block behavior — a slow anonymous subscriber still applies
// backpressure, so no existing subscriber silently becomes droppable.
func TestQoS_DefaultClassBlocks(t *testing.T) {
	b, _ := classBus(t, 1)
	defer b.Close()

	var got atomic.Int64
	_, err := b.Subscribe([]event.Type{evT}, func(ctx context.Context, _ event.Event) error {
		got.Add(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	const n = 20
	for i := 0; i < n; i++ {
		if err := b.Publish(context.Background(), event.Event{Type: evT}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for got.Load() < n && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got.Load() != n {
		t.Fatalf("default (block) subscriber must lose no events: got %d/%d", got.Load(), n)
	}
	// A default subscriber is a block class → never counted as a telemetry/notify drop.
	if sp, ok := b.(StatsProvider); ok {
		if st := sp.BusStats(); st.DroppedTelemetry != 0 || st.DroppedNotify != 0 {
			t.Fatalf("default subscriber must not drop as telemetry/notify: %+v", st)
		}
	}
}

// TestQoS_PanicInOptionalIsolatedFromEnforcement: a panicking telemetry handler
// must not take down the bus or stall enforcement delivery.
func TestQoS_PanicInOptionalIsolatedFromEnforcement(t *testing.T) {
	b, cs := classBus(t, 4)
	defer b.Close()

	_, err := cs.SubscribeClass(ClassTelemetry, "panicky", []event.Type{evT}, func(ctx context.Context, _ event.Event) error {
		panic("boom")
	})
	if err != nil {
		t.Fatal(err)
	}
	var enforced atomic.Int64
	_, err = cs.SubscribeClass(ClassEnforcement, "enforcement", []event.Type{evT}, func(ctx context.Context, _ event.Event) error {
		enforced.Add(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	const n = 20
	for i := 0; i < n; i++ {
		if err := b.Publish(context.Background(), event.Event{Type: evT}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for enforced.Load() < n && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if enforced.Load() != n {
		t.Fatalf("a panicking telemetry handler must not starve enforcement: got %d/%d", enforced.Load(), n)
	}
}

// TestQoS_SlowOptionalDoesNotPacePublisher covers the "slow" (not fully wedged)
// case: a telemetry subscriber that drains slowly must not pace the publisher, so
// the whole publish burst completes promptly and enforcement receives every event.
func TestQoS_SlowOptionalDoesNotPacePublisher(t *testing.T) {
	const buf = 4
	b, cs := classBus(t, buf)
	defer b.Close()

	_, err := cs.SubscribeClass(ClassTelemetry, "slow", []event.Type{evT}, func(ctx context.Context, _ event.Event) error {
		time.Sleep(3 * time.Millisecond) // a consumer that lags behind the publish rate
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var enforced atomic.Int64
	_, err = cs.SubscribeClass(ClassEnforcement, "enforcement", []event.Type{evT}, func(ctx context.Context, _ event.Event) error {
		enforced.Add(1)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	const n = 200
	done := make(chan error, 1)
	go func() {
		for i := 0; i < n; i++ {
			if err := b.Publish(context.Background(), event.Event{Type: evT}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	// 200 events at 3ms each would be ~600ms if the slow telemetry lane paced the
	// publisher; with the drop lane the burst is bounded only by the enforcement
	// handler (near-instant), so a generous 3s ceiling still catches a regression.
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("publish error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("publish was paced by a slow telemetry consumer")
	}
	deadline := time.Now().Add(3 * time.Second)
	for enforced.Load() < n && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if enforced.Load() != n {
		t.Fatalf("enforcement delivered %d/%d under a slow telemetry consumer", enforced.Load(), n)
	}
}
