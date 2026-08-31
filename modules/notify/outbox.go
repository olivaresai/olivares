// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
)

// The durable outbox turns notification delivery from a synchronous, best-effort send
// inside the bus handler into a persisted, retried, dead-letterable work queue. route
// enqueues one notify_outbox row per claimed delivery (in the same write tx as the
// claimed evidence-ledger row); a leader-gated composition-root pump (notifypump.go)
// then drains it: claim (queued→delivering with an optimistic version token) → deliver
// via the connector dispatcher → classify → delivered, retry-with-backoff, or dead-
// letter. It mirrors modules/eventing's proven durable engine, minus the SSRF dial
// guard and per-event RBAC (notify destinations are operator-provisioned, not tenant-
// supplied) and minus the on-wire signing (the connectors own their transport).
//
// At-least-once: a node that crashes after delivering but before recording the outcome
// leaves a "delivering" row that a later pass rescues (re-delivers) once its claim goes
// stale. The stable idempotency key (the outbox row id, sent via
// sdk.IdempotencyKeyField) lets a dedup-capable target collapse that duplicate.

const (
	// maxOutboxBatchLoop bounds a single tenant pass so it never scans unboundedly.
	maxOutboxBatchLoop = 100
	// defaultOutboxBatch is the per-scan page size (rows claimed per inner loop).
	defaultOutboxBatch = 100
	// outboxOutcomeTimeout bounds a detached outcome write (see outcomeCtx).
	outboxOutcomeTimeout = 5 * time.Second
)

// Default retry/claim tuning. maxAttempts = len(retrySchedule)+1 (the initial attempt
// plus one wait per schedule entry). Exponential-ish, capped, so a persistently
// unreachable destination dead-letters within ~40 minutes rather than churning forever.
var defaultRetrySchedule = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, 30 * time.Minute}

const defaultStaleClaim = 5 * time.Minute

// defaultOutboxDeliverTimeout bounds a single delivery attempt. It MUST stay well
// below defaultStaleClaim so an in-flight attempt can never outlive its claim and be
// stale-rescued (double-sent) by another node.
const defaultOutboxDeliverTimeout = 30 * time.Second

// outboxAttempt is everything one delivery attempt needs, gathered in the claim tx so
// the external send runs with NO store transaction open.
type outboxAttempt struct {
	id           model.ID
	destination  string
	notifyJSON   string
	attempts     int64 // after this claim's increment
	claimVersion int64 // the ownership token (the row version the claim produced)
	// Denormalized ledger-outcome fields, copied at claim so the terminal append needs
	// no second read.
	routeRef    string
	eventType   string
	kind        string
	severity    string
	subjectKind string
	subjectRef  string
	title       string
	dedupKey    string
	occurredAt  string
}

// NotifyDispatchDue runs delivery passes for tenant until no due work remains. It is
// exported for the composition-root pump. Safe to run concurrently for one tenant:
// claims are optimistic and a lost race is a skip.
func (m *Module) NotifyDispatchDue(ctx context.Context, tenant model.TenantID) error {
	if m.data == nil {
		return nil
	}
	for i := 0; i < maxOutboxBatchLoop; i++ {
		ids, err := m.scanOutboxDue(ctx, tenant)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return err
			}
			m.processOutboxOne(ctx, tenant, id)
		}
		if len(ids) < m.outboxBatch {
			return nil
		}
	}
	return nil
}

// scanOutboxDue returns ids ready for an attempt: queued rows whose next_attempt_at has
// arrived, plus delivering rows whose claim went stale (a crashed node — the at-least-
// once rescue). Two queries because the closed store query language has no OR.
func (m *Module) scanOutboxDue(ctx context.Context, tenant model.TenantID) ([]model.ID, error) {
	now := m.clock.Now()
	staleBefore := model.NewTimestamp(now.Time().Add(-m.outboxStaleClaim))
	var ids []model.ID
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(outboxKind)
		if err != nil {
			return err
		}
		due, _, err := repo.List(ctx, model.Query{
			Filters: []model.Filter{eq(colObStatus, obStatusQueued), lte(colObNextAt, now.String())},
			Sort:    []model.Sort{{Column: colObNextAt}},
			Limit:   m.outboxBatch,
		})
		if err != nil {
			return err
		}
		for _, rec := range due {
			ids = append(ids, model.ID(rec.String(model.ColID)))
		}
		stale, _, err := repo.List(ctx, model.Query{
			Filters: []model.Filter{eq(colObStatus, obStatusDelivering), lte(colObLastAt, staleBefore.String())},
			Limit:   m.outboxBatch,
		})
		if err != nil {
			return err
		}
		for _, rec := range stale {
			ids = append(ids, model.ID(rec.String(model.ColID)))
		}
		return nil
	})
	return ids, err
}

// processOutboxOne drives one outbox id through claim → deliver → outcome.
func (m *Module) processOutboxOne(ctx context.Context, tenant model.TenantID, id model.ID) {
	at, ok, err := m.claimOutbox(ctx, tenant, id)
	if err != nil {
		if !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrNotLeader) {
			m.debugf("notify: outbox claim failed", "outbox", id.String(), "err", err)
		}
		return
	}
	if !ok {
		return
	}

	var n sdk.Notification
	if err := json.Unmarshal([]byte(at.notifyJSON), &n); err != nil {
		// A row whose stored notification cannot be decoded can never be delivered —
		// dead-letter it (deterministic) rather than retry forever.
		m.finishOutbox(ctx, tenant, at, obStatusDead, statusFailed, "corrupt_notification")
		return
	}
	// Stable idempotency key across every retry of this delivery = the outbox row id.
	if n.Fields == nil {
		n.Fields = map[string]string{}
	}
	n.Fields[sdk.IdempotencyKeyField] = at.id.String()

	// Bound EACH attempt with a deadline strictly below the stale-claim window: a hung
	// destination (TCP-accepted but unresponsive) must not (a) freeze the shared, serial
	// cross-tenant pump — head-of-line blocking would starve every other tenant/route —
	// or (b) outlive outboxStaleClaim and become eligible for a stale-rescue re-delivery
	// while still in flight (the double-send the stale window is meant to bound). The
	// connectors honor ctx cancellation, so this caps the send regardless of a
	// connector client's own (possibly absent) timeout. Mirrors eventing send()'s
	// per-attempt context.
	dctx, cancel := context.WithTimeout(ctx, m.outboxDeliverTimeout)
	defer cancel()

	// dispatchOne returns the granular delivery status, which is ALSO the terminal
	// ledger status — so the evidence trail keeps recording delivered / unknown_
	// destination / no_dispatcher / failed exactly as the synchronous path did.
	status, detail := m.dispatchOne(dctx, tenant, at.destination, n)
	switch status {
	case statusDelivered:
		m.finishOutbox(ctx, tenant, at, obStatusDelivered, statusDelivered, "")
	case statusUnknownDest:
		// A route pointing at a destination the dispatcher does not know is a
		// deterministic misconfiguration: dead-letter it so it surfaces in the DLQ for
		// the operator to fix the route (or provision the destination) and redeliver —
		// retrying an unknown name only burns the ladder.
		m.finishOutbox(ctx, tenant, at, obStatusDead, statusUnknownDest, detail)
	case statusNoDispatcher:
		// No transport is wired: retrying cannot help within this process — dead-letter
		// it (the DLQ shows the un-wired seam), matching the synchronous path's terminal.
		m.finishOutbox(ctx, tenant, at, obStatusDead, statusNoDispatcher, detail)
	case statusRejected:
		// The destination READ the payload and refused it, or accepted only part of it.
		// Retrying re-sends bytes it already rejected — and for OTLP the specification
		// forbids it outright — so this dead-letters at once. The operator sees the
		// refusal in the DLQ while it is still connected to the change that caused it,
		// instead of forty minutes later behind four failed attempts.
		//
		// This is the one outcome where a FAST dead-letter is a behavior change an
		// operator will notice. It is the right default because the alternative is not
		// "maybe it succeeds later" but "the same refusal, four more times"; a
		// destination whose refusal really is temporary is reporting the wrong outcome,
		// and that is a bug in the destination's classifier, visible here rather than
		// hidden by a retry that papers over it.
		m.finishOutbox(ctx, tenant, at, obStatusDead, statusRejected, detail)
	default: // statusFailed — a transient connector error; back off and retry.
		m.recordOutboxRetry(ctx, tenant, at, statusFailed, detail)
	}
}

// claimOutbox atomically takes ownership of a due row (queued→delivering, attempts++,
// last_attempt_at=now), capturing the ownership version and the send/ledger fields.
// ok=false (nil error) means "not ours": not due anymore, or a live claim held
// elsewhere, or terminal since the scan.
func (m *Module) claimOutbox(ctx context.Context, tenant model.TenantID, id model.ID) (outboxAttempt, bool, error) {
	var at outboxAttempt
	claimed := false
	now := m.clock.Now()
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		claimed = false
		repo, err := sc.Ext(outboxKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		switch rec.String(colObStatus) {
		case obStatusQueued:
			if rec.String(colObNextAt) > now.String() {
				return nil // no longer due (deferred since the scan)
			}
		case obStatusDelivering:
			last := rec.String(colObLastAt)
			if last == "" || last > model.NewTimestamp(now.Time().Add(-m.outboxStaleClaim)).String() {
				return nil // live claim held elsewhere
			}
		default:
			return nil // terminal since the scan
		}

		// Re-resolve the route at DELIVERY time (like eventing re-reads its subscription
		// each attempt) so a route disabled or retargeted during the async window is
		// honored, not delivered from the enqueue-time snapshot. A deleted or disabled
		// route dead-letters the row (deterministic — retrying cannot help); an enabled
		// route's CURRENT destination is used (a retarget takes effect on the next
		// attempt). A row with no route ref (defensive) keeps its snapshot destination.
		if ref := rec.String(colObRouteRef); ref != "" {
			routeRepo, rerr := sc.Ext(routeKind)
			if rerr != nil {
				return rerr
			}
			route, gerr := routeRepo.Get(ctx, model.ID(ref))
			switch {
			case errors.Is(gerr, store.ErrNotFound):
				rec[colObStatus], rec[colObLastDetail] = obStatusDead, "route_deleted"
				_, uerr := repo.Update(ctx, rec)
				return uerr // claimed stays false: do not deliver a deleted route's notification
			case gerr != nil:
				return gerr
			case !route.Bool(colEnabled):
				rec[colObStatus], rec[colObLastDetail] = obStatusDead, "route_disabled"
				_, uerr := repo.Update(ctx, rec)
				return uerr // claimed stays false: an operator disabled this route
			default:
				rec[colObDestination] = route.String(colDestination) // deliver to the CURRENT target
			}
		}

		rec[colObStatus] = obStatusDelivering
		rec[colObAttempts] = rec.Int(colObAttempts) + 1
		rec[colObLastAt] = now.String()
		owned, err := repo.Update(ctx, rec)
		if err != nil {
			return err
		}
		at = outboxAttempt{
			id:           id,
			destination:  owned.String(colObDestination),
			notifyJSON:   owned.String(colObNotifyJSON),
			attempts:     owned.Int(colObAttempts),
			claimVersion: owned.Int(model.ColVersion),
			routeRef:     owned.String(colObRouteRef),
			eventType:    owned.String(colObEventType),
			kind:         owned.String(colObKind),
			severity:     owned.String(colObSeverity),
			subjectKind:  owned.String(colObSubjectKind),
			subjectRef:   owned.String(colObSubjectRef),
			title:        owned.String(colObTitle),
			dedupKey:     owned.String(colObDedupKey),
			occurredAt:   owned.String(colObOccurredAt),
		}
		claimed = true
		return nil
	})
	return at, claimed, err
}

// recordOutboxRetry schedules the next attempt on the backoff ladder, or dead-letters
// an exhausted delivery. at.attempts is 1-based (the attempt just made), so
// schedule[attempts-1] is the wait before the NEXT one.
func (m *Module) recordOutboxRetry(ctx context.Context, tenant model.TenantID, at outboxAttempt, ledgerStatus, detail string) {
	idx := int(at.attempts) - 1
	if idx >= len(m.outboxRetrySchedule) {
		m.finishOutbox(ctx, tenant, at, obStatusDead, ledgerStatus, detail)
		return
	}
	next := m.clock.Now().Time().Add(jitter(m.outboxRetrySchedule[idx]))
	wctx, cancel := outcomeCtx(ctx)
	defer cancel()
	err := m.data.Mutate(wctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(outboxKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(wctx, at.id)
		if err != nil {
			return err
		}
		if !ownsOutboxClaim(rec, at) {
			return nil // a rescuer or an admin redeliver took over; their state wins
		}
		rec[colObStatus] = obStatusQueued
		rec[colObNextAt] = model.NewTimestamp(next).String()
		rec[colObLastDetail] = detail
		_, err = repo.Update(wctx, rec)
		return err
	})
	if err != nil {
		m.debugf("notify: outbox retry scheduling failed", "outbox", at.id.String(), "err", err)
	}
}

// finishOutbox records a TERMINAL outcome (delivered | dead) for a claimed attempt,
// writing only if this attempt still owns the row (version check). On a terminal
// outcome it ALSO appends the evidence-ledger outcome row (the claim-then-send
// evidence trail: the claimed row was appended at enqueue, this is its resolution).
func (m *Module) finishOutbox(ctx context.Context, tenant model.TenantID, at outboxAttempt, obStatus, ledgerStatus, detail string) {
	wctx, cancel := outcomeCtx(ctx)
	defer cancel()
	wrote := false
	err := m.data.Mutate(wctx, tenant, func(sc store.Scope) error {
		wrote = false
		repo, err := sc.Ext(outboxKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(wctx, at.id)
		if err != nil {
			return err
		}
		if !ownsOutboxClaim(rec, at) {
			return nil
		}
		rec[colObStatus] = obStatus
		rec[colObLastDetail] = detail
		if _, err := repo.Update(wctx, rec); err != nil {
			return err
		}
		wrote = true
		return nil
	})
	if err != nil {
		m.debugf("notify: outbox terminal write failed", "outbox", at.id.String(), "err", err)
		return
	}
	if wrote {
		// Only the owner appends the ledger outcome, so a stale rescuer that lost the
		// version check does not double-append.
		m.appendOutboxLedger(wctx, tenant, at, ledgerStatus, detail)
	}
}

// appendOutboxLedger appends the terminal notify_delivery evidence row for a finished
// outbox delivery, from the denormalized fields (no second read). ledgerStatus is the
// granular delivery status (delivered | unknown_destination | no_dispatcher | failed)
// so the evidence trail matches the synchronous path. Best-effort: a failed append
// leaves the claimed ledger row latest (it ages out of its dedup window).
func (m *Module) appendOutboxLedger(ctx context.Context, tenant model.TenantID, at outboxAttempt, ledgerStatus, detail string) {
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		rec := model.Record{
			colDelDestination: at.destination,
			colDelEventType:   at.eventType,
			colDelKind:        at.kind,
			colDelSeverity:    at.severity,
			colDelSubjectKind: at.subjectKind,
			colDelSubjectRef:  at.subjectRef,
			colDelTitle:       at.title,
			colDelDedupKey:    at.dedupKey,
			colDelStatus:      ledgerStatus,
			colDelOccurredAt:  m.clock.Now().String(),
		}
		if detail != "" {
			rec[colDelDetail] = detail
		}
		if at.routeRef != "" {
			rec[colDelRouteRef] = at.routeRef
		}
		_, e := repo.Create(ctx, rec)
		return e
	})
	if err != nil {
		m.debugf("notify: outbox ledger outcome append failed", "outbox", at.id.String(), "err", err)
	}
}

// ownsOutboxClaim reports whether the row still belongs to this attempt's claim: still
// "delivering" and the exact version the claim produced. A stale-claim rescuer or an
// admin redeliver bumps the version, so a writer that outlived its claim backs off.
func ownsOutboxClaim(rec model.Record, at outboxAttempt) bool {
	return rec.String(colObStatus) == obStatusDelivering && rec.Int(model.ColVersion) == at.claimVersion
}

// outboxRecord builds a fresh queued outbox row for one claimed delivery, ready to be
// created in the claim transaction. next_attempt_at = now, so the first attempt fires
// on the next pump tick (or an immediate nudge).
func outboxRecord(s signal, p pending, notifyJSON string, now time.Time) model.Record {
	nowStr := model.NewTimestamp(now).String()
	rec := model.Record{
		colObStatus:      obStatusQueued,
		colObAttempts:    int64(0),
		colObNextAt:      nowStr,
		colObLastAt:      nowStr,
		colObDestination: p.destination,
		colObNotifyJSON:  notifyJSON,
		colObEventType:   s.eventType,
		colObKind:        s.kind,
		colObSeverity:    string(s.severity),
		colObSubjectKind: s.subjectKind,
		colObSubjectRef:  clamp(s.subjectRef, maxRefLen),
		colObTitle:       clamp(s.title, maxNameLen),
		colObDedupKey:    p.dedupKey,
		colObOccurredAt:  nowStr,
	}
	if !p.routeID.IsZero() {
		rec[colObRouteRef] = p.routeID.String()
	}
	return rec
}

// outcomeCtx detaches an outcome write from shutdown cancellation: the attempt already
// happened, so the system persists what it knows even while stopping — otherwise a
// restart strands rows in "delivering" for the stale window, re-sending acknowledged
// deliveries.
func outcomeCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), outboxOutcomeTimeout)
}

// jitter spreads a backoff delay ±20% so synchronized retries do not thunder.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	f := 0.8 + 0.4*rand.Float64() // #nosec G404 -- scheduling jitter, not key material
	return time.Duration(float64(d) * f)
}
