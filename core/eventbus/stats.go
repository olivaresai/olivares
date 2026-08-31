// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventbus

import "github.com/olivaresai/olivares/sdk/event"

// SubscriberStats is one subscriber's queue state at a point in time. Name is
// the identity passed to SubscribeNamed ("" for an anonymous Subscribe); Types
// is the subscription's filter (empty = all types).
type SubscriberStats struct {
	Name     string
	Class    DeliveryClass // QoS lane: enforcement/state (block) vs telemetry/notify (drop)
	Types    []event.Type
	Depth    int // events queued, waiting for the handler
	Capacity int // the bounded buffer size
}

// Stats is a point-in-time snapshot of a bus's saturation signals (docs/17 §5:
// the per-subscriber queue-depth gauge and the blocked/dropped-publish
// counters). Counters are cumulative since the bus was built.
type Stats struct {
	Subscribers []SubscriberStats
	// PublishBlocked counts sends that found a subscriber's queue full and had
	// to wait (the in-proc backpressure event — the publisher stalled).
	PublishBlocked uint64
	// Dropped counts publishes that lost the race with an unsubscribe (the send
	// was in flight when the subscriber quit). Events already QUEUED in a
	// subscriber's buffer at unsubscribe/close are also dropped but are not
	// counted here — shutdown drops are by-design, not a saturation signal.
	Dropped uint64
	// DroppedTelemetry and DroppedNotify count optional-output events dropped
	// because a bounded telemetry/notify queue was full (QoS). This is the lane
	// that keeps a slow/wedged optional subscriber from stalling the durable
	// enforcement/state lanes; a non-zero value is expected under load and is the
	// visible signal that an optional consumer is not keeping up.
	DroppedTelemetry uint64
	DroppedNotify    uint64
	// HandlerErrors counts handler invocations that returned an error or
	// panicked (demoted or not — see Options.DemoteError).
	HandlerErrors uint64
	// Enqueued counts events successfully placed on a subscriber's queue (one
	// per subscriber per publish — a fan-out to N matching subscribers adds N).
	// A publish that lost the race with an unsubscribe never lands, so it is
	// counted in Dropped, not here.
	Enqueued uint64
	// Handled counts handler invocations that have RETURNED (a recovered panic
	// counts too — the invocation is over either way).
	//
	// Enqueued/Handled are a completion barrier, not just a gauge: Depth alone
	// drops to 0 the moment an event leaves the queue, i.e. when its handler
	// STARTS, so "queue drained" says nothing about the in-flight invocation.
	// A reader that snapshots Enqueued after Publish returns and waits for
	// Handled to reach it knows every one of those handlers ran to completion.
	// That is a real barrier; sleeping for a fixed "grace period" and hoping is
	// not, and is what this pair replaced.
	Handled uint64
}

// StatsProvider is the optional introspection extension a Bus implementation
// may offer. It is deliberately NOT part of the Bus interface (the S02 contract
// froze it); the composition root type-asserts it to wire the scrape-time
// gauges.
type StatsProvider interface {
	BusStats() Stats
}

// NamedSubscriber is the optional extension that attaches a stable identity to
// a subscription, so the per-subscriber queue-depth gauge can label series by
// subscriber (module name) instead of by anonymous filter. Additive: the Bus
// interface is unchanged; callers fall back to Subscribe when a bus does not
// implement it.
type NamedSubscriber interface {
	SubscribeNamed(name string, types []event.Type, h event.Handler) (Subscription, error)
}
