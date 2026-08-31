// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
)

// Seq-assignment contention bounds (the hot-row pattern): a concurrent
// capture for the same tenant loses the (tenant_id, seq) unique race as
// store.ErrConflict and re-reads from scratch.
const maxCaptureRetries = 10

// retrySleep backs off a conflicting capture: 2^attempt ms, capped. Real time
// on purpose (not the module clock): contention is a real-time phenomenon and
// tests with a frozen clock must not spin.
func retrySleep(attempt int) {
	d := time.Duration(1<<uint(attempt)) * time.Millisecond
	if d > 50*time.Millisecond {
		d = 50 * time.Millisecond
	}
	time.Sleep(d)
}

// onEvent is the bus handler: it durably captures a cataloged event for the
// tenants/subscriptions that want it. Errors are returned to the bus (which
// only logs them) — the capture transaction is the durability boundary, so a
// failed capture is a lost event exactly like notify's lost alert; the bus
// itself is at-most-once (S02) and replay starts AT capture.
func (m *Module) onEvent(ctx context.Context, e event.Event) error {
	if m.data == nil {
		return nil
	}
	if _, ok := typeInfo(e.Type); !ok {
		return nil // uncataloged: never enters the platform (deny-closed)
	}
	tenant, ok := tenantOf(e.Tenant)
	if !ok {
		return nil // system/unparseable tenant: platform events are tenant-scoped
	}
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		m.warnf("eventing: payload not serializable; event dropped", "event_type", string(e.Type), "err", err)
		return nil
	}
	if len(payload) > maxPayloadBytes {
		m.warnf("eventing: payload exceeds the capture bound; event dropped",
			"event_type", string(e.Type), "bytes", len(payload))
		return nil
	}
	// The engine stamps IDs on Host.Publish, but an event constructed in-proc
	// before publication may carry none (sdk/event contract); the capture id is
	// the stable idempotency key for every retry and replay of this event.
	eventID := e.ID
	if eventID == "" {
		eventID = model.NewID().String()
	}
	occurred := e.Time
	if occurred.IsZero() {
		occurred = m.clock.Now().Time()
	}

	// Cheap read-only pre-check: most bus events have no matching enabled
	// subscription, and a leader-gated WRITE transaction per uncaptured event
	// would be pure churn. The authoritative match re-runs inside the capture
	// transaction (a subscription created in the window is simply "from now
	// on", which is its semantics anyway).
	anyMatch := false
	err = m.data.View(ctx, tenant, func(sc store.Scope) error {
		subs, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		matching, err := m.matchingSubscriptions(ctx, subs, e)
		anyMatch = len(matching) > 0
		return err
	})
	if err != nil {
		return err
	}
	if !anyMatch {
		return nil
	}

	for attempt := 0; ; attempt++ {
		enqueued, err := m.captureOnce(ctx, tenant, e, eventID, occurred, payload)
		switch {
		case err == nil:
			if enqueued > 0 {
				m.nudgeTenant(tenant)
			}
			return nil
		case errors.Is(err, store.ErrConflict) && attempt < maxCaptureRetries:
			retrySleep(attempt)
			continue
		default:
			return err
		}
	}
}

// captureOnce runs ONE atomic capture transaction: list the matching enabled
// subscriptions; if none, skip (storage-frugal — see doc.go); otherwise insert
// the event row with the next per-tenant seq and one queued delivery per
// matching subscription. Atomicity is the at-least-once anchor: either the
// event and ALL its deliveries exist, or none do.
func (m *Module) captureOnce(ctx context.Context, tenant model.TenantID, e event.Event, eventID string, occurred time.Time, payload []byte) (int, error) {
	return m.captureEventOnce(ctx, tenant, e, eventID, occurred, payload, false)
}

// captureEventOnce is the shared atomic write behind the at-most-once bus
// capture and the explicit durable intake. persistUnmatched distinguishes their
// source-of-truth contracts:
//
//   - bus events remain storage-frugal and are stored only when a subscription
//     already matches; the bus is an observation fan-out, not an event archive;
//   - a durable intake is itself an acknowledgement boundary, so it stores the
//     event even when it currently has no deliveries. That durable row binds the
//     event ID to its exact content and makes a later, divergent reuse visible.
//
// Both paths still create the event and every matching delivery in one
// transaction. A duplicate durable event is checked by sameDurableEvent below;
// the bus path keeps its historical ID-only duplicate semantics.
func (m *Module) captureEventOnce(ctx context.Context, tenant model.TenantID, e event.Event, eventID string, occurred time.Time, payload []byte, persistUnmatched bool) (int, error) {
	enqueued := 0
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		enqueued = 0
		subs, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		matching, err := m.matchingSubscriptions(ctx, subs, e)
		if err != nil {
			return err
		}
		if len(matching) == 0 && !persistUnmatched {
			return nil
		}
		events, err := sc.Ext(eventKind)
		if err != nil {
			return err
		}
		// Exactly-once capture per event id: with the NATS bridge the same
		// event can reach two nodes' captures inside the leader-failover overlap.
		// An existing row means a peer (or an earlier retry) already captured it —
		// with ALL its deliveries, capture is atomic — so this duplicate is done,
		// not an error. The unique (tenant_id, event_id) index is the race belt:
		// a concurrent loser's insert collides as ErrConflict, retries, and lands
		// here.
		if dup, _, err := events.List(ctx, model.Query{Filters: []model.Filter{eq(colEvEventID, eventID)}, Limit: 1}); err != nil {
			return err
		} else if len(dup) > 0 {
			if persistUnmatched && !sameDurableEvent(dup[0], e, occurred, payload) {
				return ErrDurableEventIDConflict
			}
			m.debugf("eventing: event already captured (duplicate delivery suppressed)", "event_id", eventID)
			return nil
		}
		seq, err := nextSeq(ctx, sc)
		if err != nil {
			return err
		}
		evRec, err := events.Create(ctx, model.Record{
			colEvSeq: seq, colEvEventID: eventID, colEvType: string(e.Type),
			colEvSource: e.Source, colEvOccurredAt: model.NewTimestamp(occurred).String(),
			colEvPayload: string(payload),
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
				colDelEventID: eventID, colDelEventSeq: seq, colDelEventType: string(e.Type),
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

// sameDurableEvent proves that a stable event ID is being replayed with the
// exact content it was first bound to. The timestamp is compared in the same
// canonical representation stored in the row, so location and monotonic-clock
// metadata cannot create a false mismatch.
func sameDurableEvent(rec model.Record, e event.Event, occurred time.Time, payload []byte) bool {
	return rec.String(colEvType) == string(e.Type) &&
		rec.String(colEvSource) == e.Source &&
		rec.String(colEvOccurredAt) == model.NewTimestamp(occurred).String() &&
		rec.String(colEvPayload) == string(payload)
}

// matchingSubscriptions returns the enabled subscriptions whose type list and
// source filter match e.
func (m *Module) matchingSubscriptions(ctx context.Context, repo store.GenericRepo, e event.Event) ([]model.Record, error) {
	var out []model.Record
	q := model.Query{Filters: []model.Filter{eq(colSubEnabled, true)}, Limit: listCap}
	for {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, rec := range recs {
			if !csvContains(rec.String(colSubTypes), string(e.Type)) {
				continue
			}
			if !csvContains(rec.String(colSubSources), e.Source) {
				continue
			}
			out = append(out, rec)
		}
		if !page.HasMore || page.Cursor == "" {
			return out, nil
		}
		q.Cursor = page.Cursor
	}
}

// nextSeq allocates the tenant's next event sequence number from the
// eventing_cursor row (created on first use), bumping it in the SAME capture
// transaction. The cursor — not max(seq) over the log — is the allocator, so
// pruning the whole log never regresses the documented monotonic cursor.
// Concurrent captures collide on the row's optimistic version (or, for two
// first-captures, on the unique (tenant_id) index) as store.ErrConflict, which
// the caller's bounded retry absorbs.
func nextSeq(ctx context.Context, sc store.Scope) (int64, error) {
	cursors, err := sc.Ext(cursorKind)
	if err != nil {
		return 0, err
	}
	recs, _, err := cursors.List(ctx, model.Query{Limit: 1})
	if err != nil {
		return 0, err
	}
	if len(recs) == 0 {
		if _, err := cursors.Create(ctx, model.Record{colCurLastSeq: int64(1)}); err != nil {
			return 0, err
		}
		return 1, nil
	}
	rec := recs[0]
	seq := rec.Int(colCurLastSeq) + 1
	rec[colCurLastSeq] = seq
	if _, err := cursors.Update(ctx, rec); err != nil {
		return 0, err
	}
	return seq, nil
}
