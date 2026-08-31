// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventbus

// DurableStats is the JetStream-side SLI snapshot a DURABLE bus backend exposes (the
// enterprise durablebus.Bus). It lives in this open transport-seam package — not
// the closed backend — so the AGPL composition root can scrape these gauges on
// /metrics WITHOUT importing the commercial package: a backend that satisfies
// `interface{ Durable() DurableStats }` is exported by registerBusMetrics, while the
// open binary (which never constructs such a backend) simply never matches.
//
// StreamPending is the load-bearing observability signal: a durable enforcement event
// is delivered at-least-once but ONLY on the leader (via the durable consumer), and the
// stream retains it for at most its MaxAge window. So a backlog that grows without a
// leader draining it is the precursor to retention-driven loss — this gauge makes that
// approach visible and alertable, instead of a silent drop past MaxAge.
type DurableStats struct {
	// Connected reports whether the JetStream plane connection is up.
	Connected bool
	// Leading reports whether this node currently runs the durable consumer (it is the
	// active writer); only the leader injects durable events.
	Leading bool
	// StreamPending is this node's consumer backlog (matching + delivered-unacked
	// messages waiting to be processed). It is meaningful on the leader; a sustained
	// rise toward the stream's MaxAge is the loss-approaching signal.
	StreamPending uint64
	// Published counts durable events confirmed into the stream (PubAck received).
	Published uint64
	// PublishErrors counts durable publishes that failed (NOT stored — surfaced to
	// the caller, never silently dropped).
	PublishErrors uint64
	// Injected counts durable events delivered into local subscribers (on the leader).
	Injected uint64
	// DedupSkipped counts redeliveries/overlaps suppressed at the dedup boundary.
	DedupSkipped uint64
	// InjectErrors counts inject failures (the event was left unacked for redelivery).
	InjectErrors uint64
	// DecodeErrors counts undecodable durable events that were terminated.
	DecodeErrors uint64
	// KVErrors counts dedup-KV read/record failures (degraded to the in-memory tier).
	KVErrors uint64
	// NoDedupID counts durable events delivered WITHOUT an event.ID, for which dedup
	// is impossible (the engine always stamps one, so this should stay zero; a nonzero
	// value means a publisher bypassed the durability contract — make it visible).
	NoDedupID uint64
}
