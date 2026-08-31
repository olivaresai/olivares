// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package eventbus is the engine's decoupled distribution backbone: connectors
// publish observations (lifted to events by the runtime), modules and output
// connectors subscribe by event type and react, without any of them importing
// one another. The default implementation is in-process (Go channels); the Bus
// interface is deliberately transport-agnostic so a distributed implementation
// (NATS) can be slotted in for multi-host deployments (ARCHITECTURE.md) without
// changing a single subscriber.
package eventbus

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/sdk/event"
)

// ErrBusClosed is returned by Publish/Subscribe after the bus has been closed.
var ErrBusClosed = errors.New("event bus is closed")

// Bus distributes events from publishers to type-filtered subscribers. It is the
// stable seam between the in-process default and a future NATS transport, so it
// exposes no channel: a subscriber registers a Handler and the bus owns the
// goroutine that runs it.
type Bus interface {
	// Publish delivers e to every subscriber whose filter matches e.Type. It
	// returns ErrBusClosed if the bus is closed, or ctx.Err() if ctx is canceled
	// while applying backpressure to a saturated subscriber. Delivery to each
	// subscriber is asynchronous; Publish does not wait for handlers to run.
	Publish(ctx context.Context, e event.Event) error
	// Subscribe registers h to receive events whose Type is in types; an empty or
	// nil types means every event. It returns a Subscription whose Unsubscribe
	// stops delivery and releases the subscriber's goroutine.
	Subscribe(types []event.Type, h event.Handler) (Subscription, error)
	// Close stops the bus: it unsubscribes everyone, waits for in-flight handlers
	// to finish and rejects further Publish/Subscribe. It is idempotent.
	Close() error
}

// Subscription is a live registration. Unsubscribe is idempotent and safe to
// call from any goroutine.
type Subscription interface {
	// Unsubscribe stops delivery to this subscriber and waits for its handler
	// goroutine to drain and exit.
	Unsubscribe()
}
