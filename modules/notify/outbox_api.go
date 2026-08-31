// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// outboxDTO is the operator-facing view of a durable-outbox row: the delivery state
// machine plus the same displayable metadata as the ledger. It never carries the
// rendered notification body (minimal-data; the ledger/title already summarize it).
type outboxDTO struct {
	ID          string `json:"id"`
	Status      string `json:"status"` // queued | delivering | delivered | dead
	Attempts    int64  `json:"attempts"`
	Destination string `json:"destination"`
	EventType   string `json:"event_type"`
	FindingKind string `json:"finding_kind"`
	Severity    string `json:"severity,omitempty"`
	SubjectRef  string `json:"subject_ref,omitempty"`
	Title       string `json:"title,omitempty"`
	LastDetail  string `json:"last_detail,omitempty"`
	NextAttempt string `json:"next_attempt_at,omitempty"`
	LastAttempt string `json:"last_attempt_at,omitempty"`
	OccurredAt  string `json:"occurred_at"`
	RouteRef    string `json:"route_ref,omitempty"`
}

func toOutboxDTO(rec model.Record) outboxDTO {
	return outboxDTO{
		ID:          rec.String(model.ColID),
		Status:      rec.String(colObStatus),
		Attempts:    rec.Int(colObAttempts),
		Destination: rec.String(colObDestination),
		EventType:   rec.String(colObEventType),
		FindingKind: rec.String(colObKind),
		Severity:    rec.String(colObSeverity),
		SubjectRef:  rec.String(colObSubjectRef),
		Title:       rec.String(colObTitle),
		LastDetail:  rec.String(colObLastDetail),
		NextAttempt: rec.String(colObNextAt),
		LastAttempt: rec.String(colObLastAt),
		OccurredAt:  rec.String(colObOccurredAt),
		RouteRef:    rec.String(colObRouteRef),
	}
}

// handleListOutbox lists the durable outbox, optionally filtered by ?status (e.g.
// status=dead for the dead-letter queue view) and ?destination. Read-tier.
func (m *Module) handleListOutbox(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	q := listQuery(r)
	if s := r.URL.Query().Get("status"); s != "" {
		q.Filters = append(q.Filters, eq(colObStatus, s))
	}
	if d := r.URL.Query().Get("destination"); d != "" {
		q.Filters = append(q.Filters, eq(colObDestination, d))
	}
	out := []outboxDTO{}
	var page model.Page
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(outboxKind)
		if err != nil {
			return err
		}
		recs, p, err := repo.List(r.Context(), q)
		if err != nil {
			return err
		}
		page = p
		for _, rec := range recs {
			out = append(out, toOutboxDTO(rec))
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse[outboxDTO]{Items: out, Cursor: page.Cursor, HasMore: page.HasMore})
}

// handleRedeliverOutbox requeues a TERMINAL outbox row (dead-letter drain / ack-and-
// retry). It requeues attempts=0, next_attempt_at=now, so the next pump re-delivers.
// Only a terminal row may be redelivered: a queued/delivering row is in-flight and a
// requeue would race the owner's outcome write (409). Admin-tier + audited.
func (m *Module) handleRedeliverOutbox(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id, ok := chiID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid id"))
		return
	}
	now := m.clock.Now()
	conflict := false
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		conflict = false
		repo, err := sc.Ext(outboxKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), id)
		if err != nil {
			return err
		}
		switch rec.String(colObStatus) {
		case obStatusDelivered, obStatusDead:
			// terminal — safe to requeue
		default:
			conflict = true
			return nil // in-flight (queued/delivering); do not race the owner
		}
		rec[colObStatus] = obStatusQueued
		rec[colObAttempts] = int64(0)
		rec[colObNextAt] = now.String()
		rec[colObLastAt] = now.String()
		rec[colObLastDetail] = "redelivered"
		if _, err := repo.Update(r.Context(), rec); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "notify.outbox.redeliver", outboxKind, id, map[string]any{
			"destination": rec.String(colObDestination),
		})
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errorBody("not found"))
		return
	case err != nil:
		writeStoreError(w, err)
		return
	case conflict:
		writeJSON(w, http.StatusConflict, errorBody("delivery is in flight; only a terminal (delivered/dead) row can be redelivered"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id.String(), "status": obStatusQueued})
}
