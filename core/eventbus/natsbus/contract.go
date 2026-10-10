// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package natsbus

import (
	"github.com/olivaresai/olivares/sdk/event"
	"log/slog"
)

type Options struct {
	// Logger receives bridge lifecycle and (throttled) error logs. nil uses
	// slog.Default().
	Logger *slog.Logger
	// DemoteError is forwarded to the embedded in-proc bus (expected
	// steady-state handler errors log at Debug; see eventbus.Options).
	DemoteError func(error) bool
	// Decoders extends DefaultDecoders with module-owned payload types the
	// composition root can import but this package cannot (license boundary).
	Decoders map[event.Type]PayloadDecoder
	// BridgeExclude, when non-nil and returning true for an event Type, keeps that
	// type's events LOCAL on the Core-NATS bridge: they still fan out to local
	// subscribers (and the wildcard subscription never receives them from this
	// node), but Publish does not bridge them over Core NATS. It exists so a
	// distributed backend that carries some types out-of-band can EMBED this bus
	// for the rest without double-delivering those types (the enterprise durable
	// JetStream bus: the durable set travels JetStream, everything else
	// travels this best-effort bridge). nil — the default and the only value the
	// open binary ever uses — bridges every type, exactly as before.
	BridgeExclude func(event.Type) bool
}

type BridgeStats struct {
	Connected bool
	// SubscriptionConfirmed is the question Connected does not answer: has the SERVER
	// confirmed this bridge's subscription? A connected bridge whose subscription was
	// refused, or not yet re-registered after a reconnect, is reachable and not routable.
	SubscriptionConfirmed bool
	PendingMsgs           int
	PendingBytes          int
	// Dropped is the subscription's cumulative slow-consumer drop count (the
	// client library's own counter — error callbacks can coalesce, this cannot).
	Dropped int64
	// PublishErrors counts bridge publishes that failed (encode or send) —
	// those events stayed node-local.
	PublishErrors uint64
	// DecodeErrors counts bridged events dropped because they did not decode.
	DecodeErrors uint64
	// GateSkipped counts remote events not injected because this node was not
	// the leader (normal on a standby).
	GateSkipped uint64
	// InvalidSubject counts events whose type cannot be a NATS subject (stayed
	// node-local).
	InvalidSubject uint64
}

type PayloadDecoder func([]byte) (any, error)
