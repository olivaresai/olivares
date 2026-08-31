// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
)

// AuditIntake is one sealed audit-ledger record handed to IngestAudit for SIEM
// forwarding. It carries ONLY the data the delivery needs: the audit event id (the
// stable idempotency key — the consumer dedups on it), the per-tenant Seq (the
// natural ledger key), the event time, the emitting source, and the already-encoded
// minimal-data Payload. Payload is opaque to the engine: it is stored verbatim and
// handed to the SinkRenderer at send time, which re-shapes it into the tower's
// dialect. The ledger forwarder (cmd) builds Payload from the sealed model.AuditEvent
// so the integrity fields (Seq/PrevHash/Hash/Sig) ride through untouched.
type AuditIntake struct {
	EventID    string
	Seq        int64
	OccurredAt time.Time
	Source     string
	Payload    []byte
}

// errAuditIntake guards the required fields.
var errAuditIntake = errors.New("eventing: audit intake requires an event id and payload")

// IngestAudit durably enqueues a sealed ledger record for SIEM forwarding WITHOUT
// the in-proc bus. It is the ledger's equivalent of onEvent/captureOnce, but it is
// fed directly off the tamper-evident ledger (by the leader-gated cursor walk in
// cmd), so it sidesteps the bus's at-most-once delivery, the storage-frugal capture
// skip's system-tenant drop, and any JSON round-trip that would lose canonical
// fidelity. It is:
//
//   - storage-frugal: it captures + enqueues ONLY when a matching enabled
//     audit.recorded subscription exists (no sink, no rows — exactly like the bus
//     capture), so an estate that forwards nothing stores nothing.
//   - exactly-once per audit event id: the unique (tenant_id, event_id) index makes
//     a re-walk after a crash (or the pump and a future tee both firing) idempotent
//     — a duplicate is recognized as already-captured, not re-enqueued.
//   - atomic: the event row and ALL its deliveries commit together (the
//     at-least-once anchor), or nothing does.
//
// It returns the number of deliveries enqueued (0 when no audit.recorded
// subscription matches — the caller then advances its cursor with nothing lost).
func (m *Module) IngestAudit(ctx context.Context, tenant model.TenantID, in AuditIntake) (int, error) {
	if m.data == nil {
		return 0, nil
	}
	if in.EventID == "" || len(in.Payload) == 0 {
		return 0, errAuditIntake
	}
	if len(in.Payload) > maxPayloadBytes {
		m.warnf("eventing: audit intake payload exceeds the capture bound; dropped",
			"event_id", in.EventID, "bytes", len(in.Payload))
		return 0, nil
	}
	occurred := in.OccurredAt
	if occurred.IsZero() {
		occurred = m.clock.Now().Time()
	}
	// Synthetic event for the subscription match (type + source filters apply
	// exactly as for a bus event), so a ledger sink subscription selects audit
	// events by source the same way it would any feed.
	matchEv := event.Event{Type: typeAuditRecorded, Source: in.Source}

	for attempt := 0; ; attempt++ {
		enqueued, err := m.ingestAuditOnce(ctx, tenant, matchEv, in, occurred)
		switch {
		case err == nil:
			if enqueued > 0 {
				m.nudgeTenant(tenant)
			}
			return enqueued, nil
		case errors.Is(err, store.ErrConflict) && attempt < maxCaptureRetries:
			retrySleep(attempt)
			continue
		default:
			return 0, err
		}
	}
}

// ingestAuditOnce runs ONE atomic intake transaction: match enabled audit.recorded
// subscriptions; if none, skip (storage-frugal); else dedup on the audit event id,
// allocate the next per-tenant eventing seq, insert the event row carrying the
// audit payload verbatim, and queue one delivery per matching subscription.
func (m *Module) ingestAuditOnce(ctx context.Context, tenant model.TenantID, matchEv event.Event, in AuditIntake, occurred time.Time) (int, error) {
	enqueued := 0
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		enqueued = 0
		subs, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		matching, err := m.matchingSubscriptions(ctx, subs, matchEv)
		if err != nil {
			return err
		}
		if len(matching) == 0 {
			return nil
		}
		events, err := sc.Ext(eventKind)
		if err != nil {
			return err
		}
		// Exactly-once by the audit event id (the re-walk/idempotency belt): an
		// existing row means a prior walk already captured this record with ALL its
		// deliveries, so this is done — not an error.
		if dup, _, err := events.List(ctx, model.Query{Filters: []model.Filter{eq(colEvEventID, in.EventID)}, Limit: 1}); err != nil {
			return err
		} else if len(dup) > 0 {
			m.debugf("eventing: audit event already captured (re-walk suppressed)", "event_id", in.EventID)
			return nil
		}
		seq, err := nextSeq(ctx, sc)
		if err != nil {
			return err
		}
		evRec, err := events.Create(ctx, model.Record{
			colEvSeq: seq, colEvEventID: in.EventID, colEvType: string(typeAuditRecorded),
			colEvSource: in.Source, colEvOccurredAt: model.NewTimestamp(occurred).String(),
			colEvPayload: string(in.Payload),
		})
		if err != nil {
			return err
		}
		deliveries, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		now := m.clock.Now()
		for _, sub := range matching {
			if _, err := deliveries.Create(ctx, model.Record{
				colDelSubRef: sub.String(model.ColID), colDelEventRef: evRec.String(model.ColID),
				colDelEventID: in.EventID, colDelEventSeq: seq, colDelEventType: string(typeAuditRecorded),
				colDelStatus: statusQueued, colDelOrigin: originLive,
				colDelAttempts: int64(0), colDelNextAt: now.String(),
			}); err != nil {
				return err
			}
			enqueued++
		}
		return nil
	})
	return enqueued, err
}
