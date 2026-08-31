// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventbus

import "github.com/olivaresai/olivares/sdk/event"

// DeliveryClass is a subscriber's QoS lane. It decides what the bus does when the
// subscriber's queue is full: apply backpressure to the publisher (durable lanes)
// or drop the event with a counter (optional-output lanes). The class is declared
// at Subscribe time and is visible in Stats, so an operator can see — in config and
// in metrics — which lane a slow subscriber is on. The invariant it buys: a slow,
// wedged or panicking OPTIONAL subscriber (telemetry/notify) can never stall the
// Publish loop that also feeds the DURABLE subscribers (enforcement/state).
type DeliveryClass int

const (
	// ClassEnforcement — a security/PEP decision, kill-switch, or audit-ledger
	// write: losing the event weakens enforcement, so the lane is durable/block (a
	// full queue applies backpressure to the publisher, which waits honoring ctx).
	// This is the DEFAULT for a plain Subscribe/SubscribeNamed, preserving the
	// pre-QoS behavior so no existing subscriber silently becomes droppable.
	ClassEnforcement DeliveryClass = iota
	// ClassState — a durable, idempotent state projection (inventory, catalog,
	// store materialization). Loss corrupts state, so it is also durable/block.
	ClassState
	// ClassTelemetry — metrics, traces, cost counters, dashboards. Bounded and
	// droppable: a full queue drops the event and counts it (Stats.DroppedTelemetry)
	// and NEVER blocks a publisher.
	ClassTelemetry
	// ClassNotify — notifications/alerts/webhooks/SIEM sinks. These want a durable
	// OUTBOX (delivered by a separate session); until that lands the lane is
	// bounded/drop like telemetry but counted separately (Stats.DroppedNotify) so
	// the outbox seam stays visible rather than being silently conflated.
	ClassNotify
)

func (c DeliveryClass) String() string {
	switch c {
	case ClassEnforcement:
		return "enforcement"
	case ClassState:
		return "state"
	case ClassTelemetry:
		return "telemetry"
	case ClassNotify:
		return "notify"
	default:
		return "unknown"
	}
}

// blocks reports whether the class applies backpressure (durable) rather than
// dropping (optional output). Enforcement and state block; telemetry and notify drop.
func (c DeliveryClass) blocks() bool {
	return c == ClassEnforcement || c == ClassState
}

// valid reports whether c is a known delivery class.
func (c DeliveryClass) valid() bool {
	return c >= ClassEnforcement && c <= ClassNotify
}

// ClassSubscriber is the optional QoS extension: a subscriber declares its
// delivery class so the bus can isolate the optional-output lanes (telemetry,
// notify) from the durable lanes (enforcement, state). Additive — the Bus
// interface is unchanged; a caller that does not use it (a plain Subscribe)
// registers as ClassEnforcement (durable/block), and a Bus implementation that
// does not offer it is used exactly as before.
type ClassSubscriber interface {
	SubscribeClass(class DeliveryClass, name string, types []event.Type, h event.Handler) (Subscription, error)
}
