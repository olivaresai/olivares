// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sdk

import (
	"context"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// Sink is how a SourceConnector hands observations to the engine. The engine
// provides it; the connector calls Emit for every fact it gathers. Emit lifts
// the observation onto the event bus. It returns an error if the engine cannot
// accept the observation (e.g. an unknown observation type, or a closed sink);
// the connector should treat a returned error as fatal to the current Gather
// run and return it.
type Sink interface {
	// Emit delivers one observation to the engine. It blocks until the engine has
	// accepted the observation or ctx is done (backpressure is intentional: a
	// slow engine slows the source rather than dropping facts).
	Emit(ctx context.Context, obs model.Observation) error
}

// SourceConnector gathers facts from one external system and emits them as
// observations. It is the read-first capture half of the amplitude moat
// (ARCHITECTURE.md). A connector imports only this SDK, never the engine, so it can
// ship under Apache-2.0 and run in-process or out-of-process over gRPC
// identically.
//
// Lifecycle: Open (configure once) → Gather (run, emitting) → Close (release).
//
// Scheduling is the engine's job, not the connector's. Gather runs on a
// goroutine the engine owns and cancels via ctx. A long-lived/streaming source
// (a tail, a receiver) blocks in Gather, emitting until ctx is canceled. A
// batch or polling source does its work and returns nil; the engine decides
// whether and when to call Gather again. Either way the connector never owns a
// ticker for its own lifecycle — it honors ctx and returns.
//
// Delivery is at-least-once: if Gather returns an error and the engine restarts
// the source, already-emitted observations may be emitted again. Consumers
// de-duplicate on the observation's natural key (e.g. EdgeObservation's
// origin/resource/mode plus ObservedAt), which the engine's idempotent
// AccessEdge upsert relies on (ARCHITECTURE.md). A connector therefore need not track
// delivery state.
type SourceConnector interface {
	// Descriptor returns the component's stable self-description.
	Descriptor() Descriptor
	// Open prepares the connector with its resolved configuration. It is called
	// once before Gather. A configuration error (missing required setting,
	// unreachable target) should be returned here, not deferred to Gather.
	Open(ctx context.Context, cfg Config) error
	// Gather runs the connector, emitting observations to sink. It returns nil
	// when a batch/poll run is complete, blocks until ctx is done for a streaming
	// source, and returns a non-nil error to signal a fault the engine may retry.
	Gather(ctx context.Context, sink Sink) error
	// Close releases the connector's resources. It is called once, after Gather
	// has returned, and must be safe to call even if Open failed.
	Close(ctx context.Context) error
}

// IdempotencyKeyField is the well-known Notification.Fields key carrying a STABLE
// per-delivery idempotency token. The durable notification outbox sets it to a value
// that is the same across every retry of one delivery, so a connector whose target
// supports deduplication (a PagerDuty dedup_key, a webhook Idempotency-Key header) can
// suppress a duplicate that at-least-once redelivery may produce after an ambiguous
// failure. A connector that cannot dedup simply ignores it (delivery stays at-least-
// once). It is opaque and non-sensitive.
const IdempotencyKeyField = "idempotency_key"

// Notification is the minimal-data message an OutputConnector delivers to an
// external system (Slack, a webhook, a SIEM, PagerDuty). It carries only
// non-sensitive, displayable fields (docs/SECURITY-HARDENING.md); forwarding rich structured
// payloads to a SIEM is a later concern.
type Notification struct {
	// Type categorizes the notification. It mirrors the originating event.Type
	// string so an output can route by it (e.g. "finding.reported").
	Type string
	// Title is a short, non-sensitive summary.
	Title string
	// Body is a non-sensitive human-readable detail.
	Body string
	// Severity grades the notification on the shared severity scale (may be empty
	// when not applicable).
	Severity model.Severity
	// Tenant is the originating tenant as a string reference.
	Tenant string
	// Fields are non-sensitive structured key/values (links, ids) for the target.
	Fields map[string]string
	// Time is when the underlying event occurred.
	Time time.Time
	// Actions, when non-empty, are interactive choices an output MAY render as
	// controls (e.g. a Slack Block Kit actions block of buttons) so a human can
	// decide directly from the message — the origination half of a chat round-trip
	// whose inbound half parses the click (the HITL approve/deny loop). It is
	// minimal-data by construction: a label, a decision verb and an opaque target
	// id, never a secret. An output that cannot render interactive controls ignores
	// Actions and falls back to Title/Body — honest, never a pretend-rendered card.
	Actions []NotificationAction
}

// NotificationAction is one interactive choice on a Notification. ID and Value
// are exactly what the inbound receiver reads back from a click: ID names the
// decision (the control's action_id / verb) and Value carries the opaque target
// the decision acts on (e.g. the approval id), optionally packed as
// "decision:id" so the value alone is self-describing. It carries no secret.
type NotificationAction struct {
	// Label is the human-facing control text (e.g. "Approve").
	Label string
	// ID is the control identifier the output sets (Slack action_id, Teams verb).
	ID string
	// Value is the opaque target the control carries (Slack button value).
	Value string
	// Style hints emphasis: "primary", "danger" or "" (default/neutral). An output
	// maps it to its own vocabulary and ignores values it does not recognize.
	Style string
}

// OutputConnector delivers notifications to one external system. It is the
// notify half of the connector SDK (ARCHITECTURE.md). Like a SourceConnector it
// imports only this SDK and runs in-process or out-of-process identically.
//
// Lifecycle: Open (configure once) → Notify (per notification) → Close (release).
type OutputConnector interface {
	// Descriptor returns the component's stable self-description.
	Descriptor() Descriptor
	// Open prepares the connector with its resolved configuration, once, before
	// any Notify.
	Open(ctx context.Context, cfg Config) error
	// Notify delivers one notification. It returns an error if delivery failed;
	// retry, rate-limiting and idempotency policy are the engine's concern, so
	// the connector simply attempts delivery and reports the outcome.
	Notify(ctx context.Context, n Notification) error
	// Close releases resources; safe to call even if Open failed.
	Close(ctx context.Context) error
}
