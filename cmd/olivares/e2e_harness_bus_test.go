// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/eventbus"
	"github.com/olivaresai/olivares/sdk/event"
)

// delayedCostBus delays the FIRST cost.sampled handler invocation of every
// subscriber by delay, then forwards normally. It models a healthy pipeline
// under load: the seed source finishes publishing while the read-model handlers
// are still working, so events arrive later than a fixed grace period allows.
// Every subscription path wraps the handler, and the real delivery class is
// forwarded, so telemetry stays a droppable lane exactly as in production.
type delayedCostBus struct {
	eventbus.Bus
	stats   eventbus.StatsProvider
	named   eventbus.NamedSubscriber
	class   eventbus.ClassSubscriber
	delay   time.Duration
	delayed atomic.Bool
}

func (b *delayedCostBus) BusStats() eventbus.Stats { return b.stats.BusStats() }

func (b *delayedCostBus) Subscribe(types []event.Type, h event.Handler) (eventbus.Subscription, error) {
	return b.Bus.Subscribe(types, b.wrap(h))
}

func (b *delayedCostBus) SubscribeNamed(name string, types []event.Type, h event.Handler) (eventbus.Subscription, error) {
	return b.named.SubscribeNamed(name, types, b.wrap(h))
}

func (b *delayedCostBus) SubscribeClass(class eventbus.DeliveryClass, name string, types []event.Type, h event.Handler) (eventbus.Subscription, error) {
	return b.class.SubscribeClass(class, name, types, b.wrap(h))
}

// wrap delays this subscription's first cost.sampled invocation. The once is
// per subscription, so every read-model writer is delayed; Publish itself never
// blocks, which is what lets the seed source return while these handlers run.
func (b *delayedCostBus) wrap(h event.Handler) event.Handler {
	var once sync.Once
	return func(ctx context.Context, e event.Event) error {
		if e.Type == event.TypeCostSampled {
			once.Do(func() {
				b.delayed.Store(true)
				time.Sleep(b.delay)
			})
		}
		return h(ctx, e)
	}
}

// TestHarnessSettlesWhenBusDeliveryOutlastsAFixedGracePeriod reproduces
// dev#2592: the seeded estate was healthy, but the first cost.sampled handlers
// were still running after the old fixed 10s grace period, so the harness failed
// with "model/provider catalog not yet materialized (cost samples not
// drained)". The seed source has already returned when those handlers run
// (Publish does not wait for handlers), so settling must follow the bus's
// Enqueued/Handled completion barrier, not the source status or wall-clock luck.
func TestHarnessSettlesWhenBusDeliveryOutlastsAFixedGracePeriod(t *testing.T) {
	inner := eventbus.NewInProc(eventbus.Options{})
	bus := &delayedCostBus{
		Bus:   inner,
		stats: inner.(eventbus.StatsProvider),
		named: inner.(eventbus.NamedSubscriber),
		class: inner.(eventbus.ClassSubscriber),
		delay: 15 * time.Second,
	}
	h := newHarnessWithBus(t, bus, nil)
	if !bus.delayed.Load() {
		t.Fatal("no cost.sampled handler was ever delayed; the reproducer did not exercise late delivery")
	}
	if h.tenantA == "" {
		t.Fatal("harness returned without a seeded tenant")
	}
}
