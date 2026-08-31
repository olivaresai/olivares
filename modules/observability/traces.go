// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package observability

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The trace read-model's single source of truth is the audit ledger's
// trace_id/span_id Meta stamps (core/observability/trace/meta.go:16-22),
// written at the store's Append chokepoint
// (core/internal/store/sqlstore/audit.go:56-63). CRITICAL: Walk returns events
// with Meta=nil by design — the meta is only reachable through the optional
// store.CanonicalWalker capability (core/store/audit.go:43-48), so the walk
// type-asserts it and parses each stored canonical meta string.

// traceWalkCap bounds how many ledger events one trace read inspects (the
// window is the chain tail), so a busy tenant chain cannot turn one read into
// an unbounded scan — same posture as modules/recording's replayWalkCap.
const traceWalkCap = int64(20000)

// traceSpanCap bounds the span rows one trace detail carries. In practice
// engine traces are tiny (a handful of API hops); a trace that exceeds the cap
// is truncated and logged — the cap is a DoS bound, not an expected path.
const traceSpanCap = 500

// traceServiceName is the one service that writes this ledger — a mirror of
// the engine's unexported OTel resource service.name (defaultServiceName,
// core/observability/trace/config.go:40). The ledger stores no service
// dimension; the single-writer constant is the honest value, not a guess.
const traceServiceName = "olivares"

// traceStatusUnset is the only status this read-model can report: the ledger
// stores no OTel span status, so claiming ok/error would be fabrication.
const traceStatusUnset = "unset"

// traceSpanKind is the widened SpanKind label for a ledger-derived "span": an
// honest non-OTel value (the web type is `| string`-widened and renders it
// verbatim) marking that these rows are groups of ledger events, not spans.
const traceSpanKind = "ledger"

// List paging bounds.
const (
	traceListDefaultLimit = 50
	traceListMaxLimit     = 200
)

// ledgerTraceEvent is one ledger event that carries a valid trace_id — the
// minimal projection the read-model needs. Remaining meta keys are
// deliberately NOT retained: even though ledger meta is already redacted by
// contract (core/model/audit.go:36-37), the response emits only synthesized
// ledger.* attributes, never raw meta passthrough.
type ledgerTraceEvent struct {
	seq        int64
	occurredAt time.Time
	action     string
	actor      string
	actorKind  string
	targetKind string
	targetID   string
	spanID     string // "" when the event carried no valid span_id
}

// walkTraceWindow walks the tail window of the tenant's ledger chain (the last
// traceWalkCap events) inside ONE read transaction, yielding every event whose
// canonical meta carries a valid W3C trace_id. ok=false (with err=nil) means
// the store's ledger does not expose CanonicalWalker — the caller degrades to
// an empty result, never a 500 (a deployment without the capability has no
// readable correlation data; that is an honest empty, warned once per process).
func (m *Module) walkTraceWindow(r *http.Request, mc api.ModuleContext, fn func(traceID string, ev ledgerTraceEvent)) (ok bool, err error) {
	ok = true
	err = mc.Data.View(r.Context(), func(sc store.Scope) error {
		head, hasHead, herr := sc.Audit().Head(r.Context())
		if herr != nil {
			return herr
		}
		if !hasHead {
			return nil // empty chain: nothing to correlate
		}
		cw, isCW := sc.Audit().(store.CanonicalWalker)
		if !isCW {
			ok = false
			m.walkerWarnOnce.Do(func() {
				if m.log != nil {
					m.log.Warn("observability: audit ledger exposes no canonical walker; trace correlation view degrades to empty")
				}
			})
			return nil
		}
		fromSeq := head.Seq - traceWalkCap + 1
		if fromSeq < 1 {
			fromSeq = 1
		}
		return cw.WalkCanonical(r.Context(), fromSeq, func(ev model.AuditEvent, metaCanonical string, _ []byte) error {
			traceID, spanID, okMeta := traceIDsFromMeta(metaCanonical)
			if !okMeta {
				return nil // no (valid) trace correlation on this event — skip
			}
			fn(traceID, ledgerTraceEvent{
				seq:        ev.Seq,
				occurredAt: ev.OccurredAt.Time(),
				action:     ev.Action,
				actor:      ev.Actor,
				actorKind:  ev.ActorKind,
				targetKind: string(ev.TargetKind),
				targetID:   ev.TargetID.String(),
				spanID:     spanID,
			})
			return nil
		})
	})
	return ok, err
}

// traceIDsFromMeta parses one stored canonical meta JSON string and returns
// its trace_id/span_id, validating the W3C forms (32/16 lowercase hex). An
// event without a valid trace_id is skipped entirely; a valid trace_id with a
// missing/invalid span_id still joins the trace window (spanID "").
func traceIDsFromMeta(metaCanonical string) (traceID, spanID string, ok bool) {
	if metaCanonical == "" || metaCanonical == "{}" ||
		!strings.Contains(metaCanonical, `"trace_id"`) {
		return "", "", false // fast path: most ledger events carry no trace context
	}
	var meta struct {
		TraceID string `json:"trace_id"`
		SpanID  string `json:"span_id"`
	}
	if err := json.Unmarshal([]byte(metaCanonical), &meta); err != nil {
		return "", "", false // a malformed canonical string is skipped, never a 500
	}
	if !isLowerHex(meta.TraceID, 32) {
		return "", "", false
	}
	if isLowerHex(meta.SpanID, 16) {
		spanID = meta.SpanID
	}
	return meta.TraceID, spanID, true
}

// traceAgg is one trace's aggregate while walking the window.
type traceAgg struct {
	id       string
	started  time.Time
	ended    time.Time
	rootSeq  int64
	rootName string
	spanIDs  map[string]struct{}
	actors   map[string]struct{}
}

// fold merges one event into the aggregate.
func (a *traceAgg) fold(ev ledgerTraceEvent) {
	if a.rootSeq == 0 || ev.seq < a.rootSeq {
		a.rootSeq, a.rootName = ev.seq, ev.action
	}
	if a.started.IsZero() || ev.occurredAt.Before(a.started) {
		a.started = ev.occurredAt
	}
	if ev.occurredAt.After(a.ended) {
		a.ended = ev.occurredAt
	}
	if ev.spanID != "" {
		if a.spanIDs == nil {
			a.spanIDs = make(map[string]struct{})
		}
		a.spanIDs[ev.spanID] = struct{}{}
	}
	if ev.actor != "" {
		if a.actors == nil {
			a.actors = make(map[string]struct{})
		}
		a.actors[ev.actor] = struct{}{}
	}
}

// handleListTraces lists the traces correlated in the ledger window, newest
// first. duration_ms is the LEDGER-EVENT WINDOW of each trace (max−min event
// time), not a span duration; status is always "unset"; services is the
// single-writer constant — see the constants above for why each is the honest
// value. Query params: ?limit (default 50, max 200), ?cursor (opaque decimal
// offset), ?service and ?status (exact-match filters).
func (m *Module) handleListTraces(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	limit, offset, ok := traceListParams(w, r)
	if !ok {
		return
	}

	aggs := make(map[string]*traceAgg)
	_, err := m.walkTraceWindow(r, mc, func(traceID string, ev ledgerTraceEvent) {
		a := aggs[traceID]
		if a == nil {
			a = &traceAgg{id: traceID}
			aggs[traceID] = a
		}
		a.fold(ev)
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}

	// Newest first; trace_id breaks ties deterministically. Sorted on the time
	// values, not the formatted strings (RFC3339Nano is not fixed-width, so a
	// lexical compare would mis-order sub-second timestamps).
	ordered := make([]*traceAgg, 0, len(aggs))
	for _, a := range aggs {
		ordered = append(ordered, a)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if !ordered[i].started.Equal(ordered[j].started) {
			return ordered[i].started.After(ordered[j].started)
		}
		return ordered[i].id < ordered[j].id
	})
	items := make([]traceListItemDTO, 0, len(ordered))
	for _, a := range ordered {
		items = append(items, traceListItemDTO{
			TraceID:    a.id,
			RootName:   a.rootName,
			StartedAt:  rfc3339(a.started),
			DurationMS: a.ended.Sub(a.started).Milliseconds(),
			SpanCount:  len(a.spanIDs),
			AgentCount: len(a.actors),
			Status:     traceStatusUnset,
			Services:   []string{traceServiceName},
		})
	}

	q := r.URL.Query()

	// Trace-id prefix search: ?q= matches trace_id prefixes so the operator
	// can paste a partial id from a log line.
	if prefix := strings.TrimSpace(q.Get("q")); prefix != "" {
		lo := strings.ToLower(prefix)
		items = filterTraces(items, func(it traceListItemDTO) bool {
			return strings.HasPrefix(it.TraceID, lo)
		})
	}

	// Time-range filters: ?from= and ?to= (RFC3339). The window is
	// [from, to] inclusive on the trace's started_at.
	if fromStr := q.Get("from"); fromStr != "" {
		from, err := time.Parse(time.RFC3339, fromStr)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid from: expected RFC3339"))
			return
		}
		items = filterTraces(items, func(it traceListItemDTO) bool {
			t, _ := time.Parse(time.RFC3339Nano, it.StartedAt)
			return !t.Before(from)
		})
	}
	if toStr := q.Get("to"); toStr != "" {
		to, err := time.Parse(time.RFC3339, toStr)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid to: expected RFC3339"))
			return
		}
		items = filterTraces(items, func(it traceListItemDTO) bool {
			t, _ := time.Parse(time.RFC3339Nano, it.StartedAt)
			return !t.After(to)
		})
	}

	// Exact-match filters. With a single writer and no stored status these can
	// only match the constants — the filter exists for contract symmetry, and
	// honestly returns nothing for any other value.
	if svc := q.Get("service"); svc != "" {
		items = filterTraces(items, func(it traceListItemDTO) bool {
			for _, s := range it.Services {
				if s == svc {
					return true
				}
			}
			return false
		})
	}
	if st := q.Get("status"); st != "" {
		items = filterTraces(items, func(it traceListItemDTO) bool { return st == it.Status })
	}

	out := listResponse[traceListItemDTO]{Items: []traceListItemDTO{}}
	if offset < len(items) {
		end := offset + limit
		if end > len(items) {
			end = len(items)
		}
		out.Items = items[offset:end]
		if end < len(items) {
			out.HasMore = true
			out.Cursor = strconv.Itoa(end)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// filterTraces keeps the items matching keep, in place.
func filterTraces(items []traceListItemDTO, keep func(traceListItemDTO) bool) []traceListItemDTO {
	out := items[:0]
	for _, it := range items {
		if keep(it) {
			out = append(out, it)
		}
	}
	return out
}

// traceListParams parses ?limit and ?cursor, rejecting unusable values (400)
// rather than silently substituting; the documented max is a server policy cap.
func traceListParams(w http.ResponseWriter, r *http.Request) (limit, offset int, ok bool) {
	limit = traceListDefaultLimit
	if l := r.URL.Query().Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 1 {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid limit"))
			return 0, 0, false
		}
		if n > traceListMaxLimit {
			n = traceListMaxLimit
		}
		limit = n
	}
	if c := r.URL.Query().Get("cursor"); c != "" {
		n, err := strconv.Atoi(c)
		if err != nil || n < 0 {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid cursor"))
			return 0, 0, false
		}
		offset = n
	}
	return limit, offset, true
}

// handleGetTrace returns one trace's detail: ONE span row per distinct
// span_id, each grouping the ledger events that engine span produced.
// Presenting each event as its own "span" would duplicate span_ids and
// over-claim — the grouping is the honest unit. An event with no valid
// span_id still widens the trace window but yields no span row.
func (m *Module) handleGetTrace(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	id := chi.URLParam(r, "id")
	if id == "" || len(id) > 64 || !isLowerHexLoose(id) {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid trace id"))
		return
	}

	var events []ledgerTraceEvent
	walked, err := m.walkTraceWindow(r, mc, func(traceID string, ev ledgerTraceEvent) {
		if traceID == id {
			events = append(events, ev)
		}
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !walked {
		// No CanonicalWalker: the window was NOT searched, so the 404 must not
		// claim it was (the "not found in the window" text would over-claim).
		writeJSON(w, http.StatusNotFound,
			errorBody("trace correlation unavailable: the ledger exposes no canonical meta"))
		return
	}
	if len(events) == 0 {
		// Unknown OR evicted from the bounded window — the message says which
		// window so the 404 is actionable, not a mystery.
		writeJSON(w, http.StatusNotFound,
			errorBody(fmt.Sprintf("trace not found in the ledger window (last %d events)", traceWalkCap)))
		return
	}

	out := traceDetailDTO{TraceID: id, Spans: m.buildSpans(events)}
	started, ended := windowOf(events)
	out.StartedAt = rfc3339(started)
	out.DurationMS = ended.Sub(started).Milliseconds()
	writeJSON(w, http.StatusOK, out)
}

// windowOf returns the min/max event times of a trace's events.
func windowOf(events []ledgerTraceEvent) (started, ended time.Time) {
	for _, ev := range events {
		if started.IsZero() || ev.occurredAt.Before(started) {
			started = ev.occurredAt
		}
		if ev.occurredAt.After(ended) {
			ended = ev.occurredAt
		}
	}
	return started, ended
}

// buildSpans groups a trace's ledger events by span_id into traceSpanDTO rows.
// Per span: the earliest (lowest-seq) event names it (with a "(+N events)"
// suffix when it groups several), start_ms is its offset from the trace start,
// duration_ms is the window covered by the span's OWN events (0 for a single
// event), and the attributes are the four synthesized ledger.* keys only.
func (m *Module) buildSpans(events []ledgerTraceEvent) []traceSpanDTO {
	started, _ := windowOf(events)

	bySpan := make(map[string][]ledgerTraceEvent)
	for _, ev := range events {
		if ev.spanID == "" {
			continue // no span identity stored — contributes to the window only
		}
		bySpan[ev.spanID] = append(bySpan[ev.spanID], ev)
	}

	spans := make([]traceSpanDTO, 0, len(bySpan))
	for spanID, evs := range bySpan {
		sort.Slice(evs, func(i, j int) bool { return evs[i].seq < evs[j].seq })
		first := evs[0]
		sStart, sEnd := windowOf(evs)

		name := first.action
		if len(evs) > 1 {
			name = fmt.Sprintf("%s (+%d events)", first.action, len(evs))
		}

		// Distinct actions in seq order, capped at 3 with an explicit marker so
		// truncation is visible, never silent.
		var actions []string
		seen := make(map[string]struct{}, len(evs))
		truncated := false
		for _, ev := range evs {
			if _, dup := seen[ev.action]; dup {
				continue
			}
			seen[ev.action] = struct{}{}
			if len(actions) == 3 {
				truncated = true
				break
			}
			actions = append(actions, ev.action)
		}
		actionsVal := strings.Join(actions, ",")
		if truncated {
			actionsVal += ",…"
		}

		span := traceSpanDTO{
			SpanID:     spanID,
			Name:       name,
			Service:    traceServiceName,
			Kind:       traceSpanKind,
			StartMS:    sStart.Sub(started).Milliseconds(),
			DurationMS: sEnd.Sub(sStart).Milliseconds(),
			Status:     traceStatusUnset,
			Actor:      clampStr(first.actor, 256),
			ActorKind:  first.actorKind,
			// Synthesized, bounded attributes (≤16 keys by construction — four —
			// values clamped to 256 chars): never raw meta passthrough.
			Attributes: map[string]string{
				"ledger.events":  strconv.Itoa(len(evs)),
				"ledger.actions": clampStr(actionsVal, 256),
				"ledger.actor":   clampStr(first.actor, 256),
				"ledger.seq":     fmt.Sprintf("%d-%d", evs[0].seq, evs[len(evs)-1].seq),
			},
		}
		if first.targetKind != "" {
			span.EntityRef = clampStr(first.targetKind+":"+first.targetID, 256)
		}
		spans = append(spans, span)
	}

	sort.Slice(spans, func(i, j int) bool {
		if spans[i].StartMS != spans[j].StartMS {
			return spans[i].StartMS < spans[j].StartMS
		}
		return spans[i].SpanID < spans[j].SpanID
	})
	if len(spans) > traceSpanCap {
		// A DoS bound, not an expected path (engine traces are tiny). The loss is
		// logged because a silently shortened waterfall would misrepresent the trace.
		m.warnf("observability: trace span rows truncated", "cap", traceSpanCap, "had", len(spans))
		spans = spans[:traceSpanCap]
	}
	return spans
}
