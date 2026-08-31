// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// replayCap bounds one replay call: a span larger than this returns has_more
// and a next_seq to continue from (the caller paginates the replay, so a
// single request never builds an unbounded transaction).
const replayCap = 1000

// replayRequest asks for re-delivery of captured events from a cursor.
type replayRequest struct {
	// FromSeq is the inclusive cursor to replay from (1 = everything retained).
	FromSeq int64 `json:"from_seq"`
	// ToSeq optionally bounds the replay (inclusive); 0 = up to the newest.
	ToSeq int64 `json:"to_seq,omitempty"`
}

type replayResponse struct {
	Replayed int   `json:"replayed"`
	NextSeq  int64 `json:"next_seq"`
	HasMore  bool  `json:"has_more"`
}

// handleReplay re-enqueues deliveries for the subscription from a seq cursor:
// every retained event with seq >= from_seq (and <= to_seq, if set) that
// matches the subscription's type list and source filter gets a NEW queued
// delivery row (origin=replay). At-least-once applies: a consumer may see an
// event again — the X-Olivares-Event idempotency key is stable. The per-event
// RBAC filter still runs at delivery time; replay grants nothing.
func (m *Module) handleReplay(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var in replayRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.FromSeq < 1 {
		writeJSON(w, http.StatusBadRequest, errorBody("from_seq must be >= 1"))
		return
	}
	if in.ToSeq != 0 && in.ToSeq < in.FromSeq {
		writeJSON(w, http.StatusBadRequest, errorBody("to_seq must be >= from_seq"))
		return
	}
	var out replayResponse
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		out = replayResponse{}
		subs, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		sub, err := subs.Get(r.Context(), id)
		if err != nil {
			return err
		}
		events, err := sc.Ext(eventKind)
		if err != nil {
			return err
		}
		filters := []model.Filter{gte(colEvSeq, in.FromSeq)}
		if in.ToSeq > 0 {
			filters = append(filters, lte(colEvSeq, in.ToSeq))
		}
		recs, page, err := events.List(r.Context(), model.Query{
			Filters: filters,
			Sort:    []model.Sort{{Column: colEvSeq}},
			Limit:   replayCap,
		})
		if err != nil {
			return err
		}
		deliveries, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		now := m.clock.Now()
		lastSeq := in.FromSeq - 1
		for _, ev := range recs {
			lastSeq = ev.Int(colEvSeq)
			if !csvContains(sub.String(colSubTypes), ev.String(colEvType)) {
				continue
			}
			if !csvContains(sub.String(colSubSources), ev.String(colEvSource)) {
				continue
			}
			if _, err := deliveries.Create(r.Context(), model.Record{
				colDelSubRef: id.String(), colDelEventRef: ev.String(model.ColID),
				colDelEventID: ev.String(colEvEventID), colDelEventSeq: ev.Int(colEvSeq),
				colDelEventType: ev.String(colEvType),
				colDelStatus:    statusQueued, colDelOrigin: originReplay,
				colDelAttempts: int64(0), colDelNextAt: now.String(),
			}); err != nil {
				return err
			}
			out.Replayed++
		}
		out.NextSeq = lastSeq + 1
		out.HasMore = page.HasMore
		// Replay moves retained data to an external endpoint: audited with the
		// real principal and the span, in the same transaction (docs/SECURITY-HARDENING.md).
		return auditEvent(r.Context(), sc, mc, "eventing.subscription.replay", subscriptionKind, id,
			map[string]any{"from_seq": in.FromSeq, "to_seq": in.ToSeq, "replayed": out.Replayed})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if out.Replayed > 0 {
		m.nudgeTenant(mc.Tenant)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleListDeadLetters is a convenience endpoint that lists deliveries with
// status=dead — the dead-letter queue view. It is functionally identical to
// GET /deliveries?status=dead but more discoverable in the console and CLI.
func (m *Module) handleListDeadLetters(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.authz == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("event authorization is not configured on this deployment"))
		return
	}
	allowed := m.callerAllowedTypes(r.Context(), mc)
	q := listQuery(r)
	q.Filters = append(q.Filters, eq(colDelStatus, statusDead))
	if s := r.URL.Query().Get("subscription"); s != "" {
		q.Filters = append(q.Filters, eq(colDelSubRef, s))
	}
	if s := r.URL.Query().Get("event_type"); s != "" {
		if !allowed[s] {
			writeJSON(w, http.StatusForbidden, errorBody("you are not authorized for this event type"))
			return
		}
		q.Filters = append(q.Filters, eq(colDelEventType, s))
	}
	out := []deliveryDTO{}
	var page model.Page
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		recs, p, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		page = p
		for _, rec := range recs {
			if !allowed[rec.String(colDelEventType)] {
				continue
			}
			out = append(out, deliveryDTO{
				ID:            rec.String(model.ColID),
				Subscription:  rec.String(colDelSubRef),
				EventID:       rec.String(colDelEventID),
				EventSeq:      rec.Int(colDelEventSeq),
				EventType:     rec.String(colDelEventType),
				Status:        rec.String(colDelStatus),
				Origin:        rec.String(colDelOrigin),
				Attempts:      rec.Int(colDelAttempts),
				LastAttemptAt: rec.String(colDelLastAt),
				LastStatus:    rec.String(colDelLastStatus),
			})
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[deliveryDTO]{Items: out, Cursor: page.Cursor, HasMore: page.HasMore})
}

// handleRedeliver requeues ONE delivery that already reached a terminal state
// (delivered, dead or denied — the DLQ drain primitive). The attempt counter
// restarts so the full retry ladder applies again; the idempotency key does
// not change.
func (m *Module) handleRedeliver(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		switch rec.String(colDelStatus) {
		case statusDelivered, statusDead, statusDenied:
		default:
			return validationError("only a finished delivery (delivered, dead or denied) can be redelivered")
		}
		rec[colDelStatus] = statusQueued
		rec[colDelAttempts] = int64(0)
		rec[colDelNextAt] = m.clock.Now().String()
		rec[colDelLastStatus] = "requeued"
		if _, err := repo.Update(r.Context(), rec); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "eventing.delivery.redeliver", deliveryKind, id,
			map[string]any{"event_type": rec.String(colDelEventType), "event_seq": rec.Int(colDelEventSeq)})
	})
	if msg, ok := asValidation(err); ok {
		writeJSON(w, http.StatusBadRequest, errorBody(msg))
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	m.nudgeTenant(mc.Tenant)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": statusQueued})
}

// testResultDTO is the synchronous outcome of a test delivery.
type testResultDTO struct {
	Delivered bool   `json:"delivered"`
	Outcome   string `json:"outcome"` // the same non-sensitive class a delivery row records
}

// handleTestSubscription sends ONE synthetic "eventing.test" delivery to the
// subscription's endpoint, synchronously, signed with its real secret — the
// integrator's "is my receiver wired correctly?" probe. It is not captured in
// the event log and creates no delivery row; the type is deliberately
// uncataloged so it can never be subscribed to or replayed.
func (m *Module) handleTestSubscription(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	var endpoint, sealed, authType, authHeaderName, sealedAuth string
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		endpoint = rec.String(colSubEndpoint)
		sealed = rec.String(colSubSecret)
		authType = rec.String(colSubAuthType)
		authHeaderName = rec.String(colSubAuthHeaderName)
		sealedAuth = rec.String(colSubAuthValSealed)
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	secret, err := m.sealer.Open(r.Context(), mc.Tenant, sealed)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// unseal the auth credential for the test delivery.
	var authValue string
	if authType != "" && authType != authTypeNone && sealedAuth != "" {
		av, err := m.sealer.Open(r.Context(), mc.Tenant, sealedAuth)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		authValue = string(av)
	}
	payload, _ := json.Marshal(map[string]string{"Message": "Olivares eventing test delivery", "SubscriptionID": id.String()})
	status, outcome := m.send(r.Context(), mc.Tenant, attempt{
		deliveryID: model.NewID(),
		// The subscription's OWN id, not a synthetic one. Without it a test delivery to a
		// destination this deployment already had would be judged as a candidate that does
		// not exist yet — so under compatibility mode the probe would report a refusal for
		// an endpoint whose real deliveries succeed, and an operator would go hunting for
		// a fault that is not there.
		subID:          id.String(),
		purpose:        EgressTest,
		endpoint:       endpoint,
		eventID:        model.NewID().String(),
		eventType:      "eventing.test",
		occurredAt:     m.clock.Now().Time(),
		payload:        payload,
		authType:       authType,
		authHeaderName: authHeaderName,
	}, string(secret), authValue)
	// The probe is itself an admin action against an external endpoint.
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		return auditEvent(r.Context(), sc, mc, "eventing.subscription.test", subscriptionKind, id,
			map[string]any{"outcome": outcome})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, testResultDTO{Delivered: status == statusDelivered, Outcome: outcome})
}

// eventDTO is the API shape of a captured event.
type eventDTO struct {
	Seq        int64           `json:"seq"`
	EventID    string          `json:"event_id"`
	Type       string          `json:"type"`
	Source     string          `json:"source"`
	OccurredAt string          `json:"occurred_at"`
	Payload    json.RawMessage `json:"payload"`
}

// callerAllowedTypes evaluates the caller's per-type RBAC ONCE, BEFORE any
// store transaction opens: the authorizer's ABAC layer may reach an external
// PDP (an HTTP call), and the engine's store must never host a network call —
// or a nested authorizer read — inside an open transaction. Six catalog
// entries make this a bounded precomputation.
func (m *Module) callerAllowedTypes(ctx context.Context, mc api.ModuleContext) map[string]bool {
	allowed := make(map[string]bool, len(catalog))
	for _, e := range catalog {
		allowed[string(e.Type)] = m.authz.Allowed(ctx, mc.Principal, e.Permission, mc.Tenant)
	}
	return allowed
}

// handleListEvents lists the tenant's captured event log from a seq cursor —
// the pull-side companion of replay (find your cursor, inspect what a delivery
// carried). The per-type RBAC filter applies to the CALLER: an event whose type
// the caller's own principal may not receive is not shown (deny-closed; an
// explicitly requested forbidden ?type is an explicit 403). Requires the wired
// authorizer for the same reason deliveries do.
func (m *Module) handleListEvents(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.authz == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("event authorization is not configured on this deployment"))
		return
	}
	allowed := m.callerAllowedTypes(r.Context(), mc)
	q := model.Query{Sort: []model.Sort{{Column: colEvSeq}}, Limit: 100}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= listCap {
			q.Limit = n
		}
	}
	var sinceSeq int64
	if s := r.URL.Query().Get("since_seq"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid since_seq"))
			return
		}
		sinceSeq = n
		q.Filters = append(q.Filters, gte(colEvSeq, n))
	}
	if t := r.URL.Query().Get("type"); t != "" {
		if !allowed[t] {
			writeJSON(w, http.StatusForbidden, errorBody("you are not authorized for this event type"))
			return
		}
		q.Filters = append(q.Filters, eq(colEvType, t))
	}
	out := []eventDTO{}
	var page model.Page
	// An empty page must return the caller's own cursor, not 0 — next_seq=0
	// would reset a polling consumer to the start of the log.
	nextSeq := sinceSeq
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(eventKind)
		if err != nil {
			return err
		}
		recs, p, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		page = p
		for _, rec := range recs {
			nextSeq = rec.Int(colEvSeq) + 1
			if !allowed[rec.String(colEvType)] {
				continue // RBAC-filtered for the caller, deny-closed
			}
			out = append(out, eventDTO{
				Seq: rec.Int(colEvSeq), EventID: rec.String(colEvEventID),
				Type: rec.String(colEvType), Source: rec.String(colEvSource),
				OccurredAt: rec.String(colEvOccurredAt),
				Payload:    json.RawMessage(rec.String(colEvPayload)),
			})
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// Seq is the cursor (custom sort disables the opaque keyset cursor):
	// continue with ?since_seq=<next_seq>.
	writeJSON(w, http.StatusOK, map[string]any{
		"items": out, "next_seq": nextSeq, "has_more": page.HasMore,
	})
}

// deliveryDTO is the API shape of a delivery row (routing metadata and outcome
// classes only — the payload lives on the event, RBAC-gated there).
type deliveryDTO struct {
	ID            string `json:"id"`
	Subscription  string `json:"subscription"`
	EventID       string `json:"event_id"`
	EventSeq      int64  `json:"event_seq"`
	EventType     string `json:"event_type"`
	Status        string `json:"status"`
	Origin        string `json:"origin"`
	Attempts      int64  `json:"attempts"`
	NextAttemptAt string `json:"next_attempt_at,omitempty"`
	LastAttemptAt string `json:"last_attempt_at,omitempty"`
	LastStatus    string `json:"last_status,omitempty"`
}

// handleListDeliveries lists delivery state, optionally filtered by
// ?subscription, ?status (status=dead is the DLQ view), ?origin (live|replay)
// and ?event_type. The
// same per-type RBAC filter as the event log applies to the CALLER: a delivery
// row is metadata, but which privileged events exist is itself privileged
// (deny-closed; an explicitly requested forbidden ?event_type is a 403).
func (m *Module) handleListDeliveries(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.authz == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody("event authorization is not configured on this deployment"))
		return
	}
	allowed := m.callerAllowedTypes(r.Context(), mc)
	q := listQuery(r)
	if s := r.URL.Query().Get("subscription"); s != "" {
		q.Filters = append(q.Filters, eq(colDelSubRef, s))
	}
	if s := r.URL.Query().Get("status"); s != "" {
		q.Filters = append(q.Filters, eq(colDelStatus, s))
	}
	// the console's replay flow links straight to the rows a replay
	// produced. Origin is per-row metadata already shown to this caller.
	if s := r.URL.Query().Get("origin"); s != "" {
		q.Filters = append(q.Filters, eq(colDelOrigin, s))
	}
	if s := r.URL.Query().Get("event_type"); s != "" {
		if !allowed[s] {
			writeJSON(w, http.StatusForbidden, errorBody("you are not authorized for this event type"))
			return
		}
		q.Filters = append(q.Filters, eq(colDelEventType, s))
	}
	out := []deliveryDTO{}
	var page model.Page
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		recs, p, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		page = p
		for _, rec := range recs {
			if !allowed[rec.String(colDelEventType)] {
				continue // RBAC-filtered for the caller, deny-closed
			}
			out = append(out, deliveryDTO{
				ID:           rec.String(model.ColID),
				Subscription: rec.String(colDelSubRef),
				EventID:      rec.String(colDelEventID),
				EventSeq:     rec.Int(colDelEventSeq),
				EventType:    rec.String(colDelEventType),
				Status:       rec.String(colDelStatus),
				Origin:       rec.String(colDelOrigin),
				Attempts:     rec.Int(colDelAttempts),
				NextAttemptAt: func() string {
					if rec.String(colDelStatus) == statusQueued {
						return rec.String(colDelNextAt)
					}
					return ""
				}(),
				LastAttemptAt: rec.String(colDelLastAt),
				LastStatus:    rec.String(colDelLastStatus),
			})
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[deliveryDTO]{Items: out, Cursor: page.Cursor, HasMore: page.HasMore})
}
