// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/olivaresai/olivares/connectors/siemsink"
	"github.com/olivaresai/olivares/connectors/splunkhec"

	"github.com/olivaresai/olivares/connectors/webhook"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/egress"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/siemwire"
)

// Delivery header names. Timestamp and signature are the scheme verbatim
// (the receiver verifies with connectors/webhook.VerifyWithin); the event id is
// the consumer's idempotency key — STABLE across every retry and replay of one
// event — and the delivery id distinguishes the delivery (a replay is a new
// delivery of the same event).
const (
	headerTimestamp = "X-Olivares-Timestamp"
	headerSignature = "X-Olivares-Signature"
	headerEvent     = "X-Olivares-Event"
	headerEventType = "X-Olivares-Event-Type"
	headerDelivery  = "X-Olivares-Delivery"

	deliveryUserAgent = "olivares-eventing/1"
)

// Short, NON-SENSITIVE outcome classes recorded on the delivery row. Never an
// error string (it could embed the endpoint URL) and never response content.
const (
	outcomeTimeout       = "timeout"
	outcomeNetwork       = "network"
	outcomeDenied        = "rbac_denied"
	outcomeSecretGone    = "secret_unavailable"
	outcomeSubDeleted    = "subscription_deleted"
	outcomeEventExpired  = "event_expired"
	outcomeSubDisabled   = "subscription_disabled"
	outcomeKillSwitched  = "killswitch_parked" // parked by an estate stop; resumes on re-enable
	outcomeBadEndpoint   = "endpoint_invalid"
	outcomeNoRenderer    = "sink_renderer_unwired" // a sink sub but no SinkRenderer wired (parked)
	outcomeRenderFailed  = "sink_render_failed"    // the renderer refused (bad kind/format/opts) — retry+DLQ
	maxDispatchBatchLoop = 100                     // belt: a single pass never scans unboundedly
)

// wireEvent is the on-the-wire body: the documented sdk/event.Event envelope
// (Go field names — the SDK is the contract, like the AsyncAPI document) plus
// the per-tenant Seq cursor, with the typed payload under Payload.
type wireEvent struct {
	ID      string
	Type    string
	Tenant  string
	Source  string
	Time    time.Time
	Seq     int64
	Payload json.RawMessage
}

// attempt carries everything one delivery attempt needs, gathered in the claim
// transaction so the HTTP call runs with NO transaction open.
type attempt struct {
	deliveryID model.ID
	subID      string
	// purpose distinguishes an ordinary delivery from an operator-triggered probe. Both
	// are sends and the destination rules are identical; recording which one asked keeps
	// the decision vocabulary honest rather than making "send" cover two things.
	purpose    EgressPurpose
	role       string
	endpoint   string
	sealed     string
	eventID    string
	eventType  string
	seq        int64
	source     string
	occurredAt time.Time
	payload    []byte
	attempts   int64 // after this claim's increment
	// claimVersion is the row version the claim's Update produced — the
	// ownership token. An outcome writer that no longer matches it lost the row
	// (a stale-claim rescuer or an admin redeliver took over) and must not
	// write: last-writer-wins here would clobber the new owner's state.
	claimVersion int64
	// per-subscription auth header and retry policy.
	authType        string // none | bearer | basic | header
	authHeaderName  string // custom header name (for type "header")
	sealedAuth      string // sealed auth credential (opened at send time)
	maxAttempts     int64  // 0 = module default
	initialInterval int64  // 0 = module default (seconds)
	// SIEM-sink profile (empty sinkKind = the unchanged generic webhook).
	// Resolved from the subscription's 1:1 sink side row in the claim tx, so the
	// send needs no further reads. sealedSinkCred is opened at send time (the
	// SIEM token/key/bearer), distinct from the HMAC secret.
	sinkKind       string
	sinkFormat     string
	sinkOpts       string // raw JSON of non-secret routing options
	sealedSinkCred string
}

// DispatchDue runs delivery passes for tenant until no due work remains. It is
// exported for the composition-root pump (cmd/olivares/eventingpump.go);
// the in-process nudge calls it too. Safe to run concurrently for the same
// tenant: claims are optimistic and a lost race is a skip.
func (m *Module) DispatchDue(ctx context.Context, tenant model.TenantID) error {
	if m.data == nil {
		return nil
	}
	if m.authz == nil {
		// Deny-closed park: without the authorizer the per-event RBAC filter
		// cannot run; Start already warned loudly.
		m.debugf("eventing: dispatch skipped, no authorizer wired", "tenant", tenant.String())
		return nil
	}
	// Estate kill switch: one verdict per pass, read OUTSIDE any store
	// transaction (single-writer discipline). While the tenant is stopped,
	// non-exempt deliveries are PARKED inside claim (pre-attempt, never consuming
	// the retry ladder) — re-enable resumes the stream with nothing lost. A gate
	// error parks everything this pass: an unreadable stop state never means
	// "deliver" (the composition-root adapter normally absorbs read errors into
	// Paused+governance-exemptions itself).
	pause := DeliveryPause{}
	if m.gate != nil {
		p, gerr := m.gate.Check(ctx, tenant)
		if gerr != nil {
			m.debugf("eventing: kill-switch gate error; parking this pass (deny-closed)", "tenant", tenant.String(), "err", gerr)
			p = DeliveryPause{Paused: true}
		}
		pause = p
	}
	// Unit G: the egress control's own readiness, checked ONCE per pass and
	// BEFORE any delivery is claimed.
	//
	// It is here rather than only at the send seam because of where the send seam sits.
	// A claim increments attempts first; a retryable refusal is then requeued, and an
	// exhausted ladder dead-letters. So a control plane that could not read its own
	// rollout state — or could not draw a tenant's compatibility record — would spend
	// the retry ladder on its own outage and destroy the evidence it was carrying. That
	// is the same class of defect as unit F's "an unreadable policy dead-lettered",
	// fixed once at the wrong altitude.
	//
	// Parking is the kill switch's semantics: pre-attempt, nothing consumed, the stream
	// resumes where it stopped. The per-delivery retryable branch in send() stays as a
	// backstop for the cases that are genuinely per-destination (a name that does not
	// resolve right now).
	egressPark := m.egressReadiness(ctx, tenant)
	if egressPark != "" {
		m.log.Warn("eventing: parking this tenant's deliveries — the egress destination control cannot decide yet, and spending the retry ladder on that would dead-letter evidence",
			"tenant", tenant.String(), "outcome", egressPark)
	}
	for i := 0; i < maxDispatchBatchLoop; i++ {
		ids, err := m.scanDue(ctx, tenant)
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
			m.processOne(ctx, tenant, id, pause, egressPark)
		}
		if len(ids) < m.batch {
			return nil
		}
	}
	return nil
}

// scanDue returns the ids of deliveries ready for an attempt: queued rows whose
// next_attempt_at has arrived, plus delivering rows whose claim went stale (a
// crashed node — the at-least-once rescue). Two queries because the closed
// store query language has no OR.
func (m *Module) scanDue(ctx context.Context, tenant model.TenantID) ([]model.ID, error) {
	now := m.clock.Now()
	staleBefore := model.NewTimestamp(now.Time().Add(-m.staleClaim))
	var ids []model.ID
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		due, _, err := repo.List(ctx, model.Query{
			Filters: []model.Filter{eq(colDelStatus, statusQueued), lte(colDelNextAt, now.String())},
			Sort:    []model.Sort{{Column: colDelNextAt}},
			Limit:   m.batch,
		})
		if err != nil {
			return err
		}
		for _, rec := range due {
			ids = append(ids, model.ID(rec.String(model.ColID)))
		}
		stale, _, err := repo.List(ctx, model.Query{
			Filters: []model.Filter{eq(colDelStatus, statusDelivering), lte(colDelLastAt, staleBefore.String())},
			Limit:   m.batch,
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

// processOne drives one delivery id through claim → RBAC filter → signed POST →
// outcome. Every failure path records an outcome; only a lost claim race or a
// deferral is silent (someone else owns the row / it is not due anymore).
func (m *Module) processOne(ctx context.Context, tenant model.TenantID, id model.ID, pause DeliveryPause, egressPark string) {
	at, ok, err := m.claim(ctx, tenant, id, pause, egressPark)
	if err != nil {
		if !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrNotFound) {
			m.debugf("eventing: claim failed", "delivery", id.String(), "err", err)
		}
		return
	}
	if !ok {
		return
	}

	// The deny-closed per-event RBAC filter: the subscription's recorded role,
	// evaluated through the FULL pipeline (RBAC ∩ ABAC) right before sending.
	info, known := typeInfo(event.Type(at.eventType))
	if !known {
		m.finishOwned(ctx, tenant, at, statusDenied, outcomeDenied)
		return
	}
	principal := auth.ScopedPrincipal(model.ID(at.subID), "event-subscription", tenant, at.role)
	if !m.authz.Allowed(ctx, principal, info.Permission, tenant) {
		m.finishOwned(ctx, tenant, at, statusDenied, outcomeDenied)
		return
	}

	secret, err := m.sealer.Open(ctx, tenant, at.sealed)
	if err != nil {
		m.recordRetry(ctx, tenant, at, outcomeSecretGone)
		return
	}

	// unseal the auth credential (if any) outside any store transaction.
	var authValue string
	if at.authType != "" && at.authType != authTypeNone && at.sealedAuth != "" {
		av, err := m.sealer.Open(ctx, tenant, at.sealedAuth)
		if err != nil {
			m.recordRetry(ctx, tenant, at, outcomeSecretGone)
			return
		}
		authValue = string(av)
	}

	status, outcome := m.send(ctx, tenant, at, string(secret), authValue)
	switch status {
	case statusDelivered:
		m.finishOwned(ctx, tenant, at, statusDelivered, outcome)
	case statusDead:
		m.finishOwned(ctx, tenant, at, statusDead, outcome)
	case statusParked:
		// The pass-level readiness check said the egress control could decide, and by the time
		// the bytes were about to leave it could not. That window is real — availability can
		// change between the two, and a long pass can outlive the five-second disposition cache —
		// and paying for it out of the retry ladder was the same defect the pass-level check was
		// added to fix, one instant later. So this restores the attempt the claim consumed and
		// parks, instead of advancing the ladder toward a dead letter.
		m.parkOwned(ctx, tenant, at, outcome)
	default: // retryable
		m.recordRetry(ctx, tenant, at, outcome)
	}
}

// parkOwned returns a claimed delivery to the queue WITHOUT spending a ladder step.
//
// It restores attempts to what it was before the claim, which is what makes this a park rather
// than a fast retry: nothing about the delivery was attempted, the plane simply could not say
// whether it was allowed to. A caller must only reach here for a PLANE-level indeterminacy — an
// unreadable disposition, an unreadable policy, a damaged compatibility record — never for
// anything that is a property of the destination.
func (m *Module) parkOwned(ctx context.Context, tenant model.TenantID, at attempt, outcome string) {
	wctx, cancel := outcomeCtx(ctx)
	defer cancel()
	next := model.NewTimestamp(m.clock.Now().Time().Add(egressParkRecheck))
	if err := m.data.Mutate(wctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(wctx, at.deliveryID)
		if err != nil {
			return err
		}
		rec[colDelStatus] = statusQueued
		rec[colDelNextAt] = next.String()
		rec[colDelLastStatus] = outcome
		// The claim incremented this; a park did not use it.
		if n := rec.Int(colDelAttempts); n > 0 {
			rec[colDelAttempts] = n - 1
		}
		_, err = repo.Update(wctx, rec)
		return err
	}); err != nil {
		m.debugf("eventing: parking a claimed delivery failed", "delivery", at.deliveryID.String(), "err", err)
	}
}

// claim atomically takes ownership of a due delivery. In the SAME transaction
// it resolves the subscription and the event row, so the attempt needs no
// further reads. ok=false (with nil error) means "not ours": not due anymore,
// deferred (disabled subscription), or terminally resolved here (deleted
// subscription, pruned event).
func (m *Module) claim(ctx context.Context, tenant model.TenantID, id model.ID, pause DeliveryPause, egressPark string) (attempt, bool, error) {
	var at attempt
	claimed := false
	now := m.clock.Now()
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		claimed = false
		repo, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, id)
		if err != nil {
			return err
		}
		switch rec.String(colDelStatus) {
		case statusQueued:
			if rec.String(colDelNextAt) > now.String() {
				return nil // no longer due (deferred since the scan)
			}
		case statusDelivering:
			last := rec.String(colDelLastAt)
			if last == "" || last > model.NewTimestamp(now.Time().Add(-m.staleClaim)).String() {
				return nil // live claim held elsewhere
			}
		default:
			return nil // terminal since the scan
		}

		subs, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		sub, err := subs.Get(ctx, model.ID(rec.String(colDelSubRef)))
		if errors.Is(err, store.ErrNotFound) {
			rec[colDelStatus], rec[colDelLastStatus] = statusDead, outcomeSubDeleted
			rec[colDelLastAt] = now.String()
			_, err = repo.Update(ctx, rec)
			return err
		}
		if err != nil {
			return err
		}
		if !sub.Bool(colSubEnabled) {
			// Parked, not consumed: re-enable resumes the stream from here. The
			// status goes (back) to queued so the park is governed by the due
			// predicate — a stale "delivering" row left in place would re-match
			// the stale-rescue scan (which ignores next_attempt_at) on every
			// pass and churn writes forever.
			rec[colDelStatus] = statusQueued
			rec[colDelNextAt] = model.NewTimestamp(now.Time().Add(disabledRecheck)).String()
			rec[colDelLastStatus] = outcomeSubDisabled
			_, err = repo.Update(ctx, rec)
			return err
		}
		// Estate kill switch: park (the disabled-subscription semantics —
		// pre-attempt, the ladder is never consumed) every non-exempt delivery
		// while the tenant is stopped. The exempt set is the governance channel
		// (approval/kill-switch event types): a stop must not silence the rail
		// its own dual-control re-enable is decided through.
		if pause.Paused {
			if _, exempt := pause.Exempt[rec.String(colDelEventType)]; !exempt {
				rec[colDelStatus] = statusQueued
				rec[colDelNextAt] = model.NewTimestamp(now.Time().Add(disabledRecheck)).String()
				rec[colDelLastStatus] = outcomeKillSwitched
				_, err = repo.Update(ctx, rec)
				return err
			}
		}
		// Unit G: the egress control could not DECIDE — its durable disposition, the
		// operator's policy, or this tenant's compatibility record was unreadable. Park on
		// exactly the kill-switch's terms: pre-attempt, the ladder untouched, the reason
		// recorded on the row, and the stream resumes where it stopped.
		//
		// Parking here rather than refusing at the send seam is the whole point. Refusing
		// there is "retryable", which still means this attempt was claimed and the ladder
		// advanced — so a long enough outage of the control plane's OWN state dead-lettered
		// the evidence it was carrying. The refusal was already correct; it was being paid
		// for out of the wrong budget.
		if egressPark != "" {
			rec[colDelStatus] = statusQueued
			rec[colDelNextAt] = model.NewTimestamp(now.Time().Add(egressParkRecheck)).String()
			rec[colDelLastStatus] = egressPark
			_, err = repo.Update(ctx, rec)
			return err
		}

		events, err := sc.Ext(eventKind)
		if err != nil {
			return err
		}
		ev, err := events.Get(ctx, model.ID(rec.String(colDelEventRef)))
		if errors.Is(err, store.ErrNotFound) {
			rec[colDelStatus], rec[colDelLastStatus] = statusDead, outcomeEventExpired
			rec[colDelLastAt] = now.String()
			_, err = repo.Update(ctx, rec)
			return err
		}
		if err != nil {
			return err
		}

		rec[colDelStatus] = statusDelivering
		rec[colDelAttempts] = rec.Int(colDelAttempts) + 1
		rec[colDelLastAt] = now.String()
		owned, err := repo.Update(ctx, rec)
		if err != nil {
			return err
		}

		occurred := now.Time()
		if ts, err := model.ParseTimestamp(ev.String(colEvOccurredAt)); err == nil {
			occurred = ts.Time()
		}
		// resolve the OPTIONAL 1:1 SIEM-sink profile in the same claim tx, so
		// send() needs no further reads. Absent = the unchanged generic webhook.
		sink, err := m.resolveSinkProfile(ctx, sc, model.ID(sub.String(model.ColID)))
		if err != nil {
			return err
		}
		at = attempt{
			deliveryID:      id,
			subID:           sub.String(model.ColID),
			role:            sub.String(colSubRole),
			endpoint:        sub.String(colSubEndpoint),
			sealed:          sub.String(colSubSecret),
			eventID:         rec.String(colDelEventID),
			eventType:       rec.String(colDelEventType),
			seq:             rec.Int(colDelEventSeq),
			source:          ev.String(colEvSource),
			occurredAt:      occurred,
			payload:         []byte(ev.String(colEvPayload)),
			attempts:        owned.Int(colDelAttempts),
			claimVersion:    owned.Int(model.ColVersion),
			authType:        sub.String(colSubAuthType),
			authHeaderName:  sub.String(colSubAuthHeaderName),
			sealedAuth:      sub.String(colSubAuthValSealed),
			maxAttempts:     sub.Int(colSubMaxAttempts),
			initialInterval: sub.Int(colSubInitInterval),
			sinkKind:        sink.kind,
			sinkFormat:      sink.format,
			sinkOpts:        sink.opts,
			sealedSinkCred:  sink.sealedCred,
		}
		claimed = true
		return nil
	})
	return at, claimed, err
}

// send performs ONE signed POST and classifies the result into a delivery
// status (delivered / dead / queued-for-retry) and an outcome class. It owns
// the per-attempt deadline; redirects are never followed (a redirect would
// re-route the signed body — terminal).
//
// The body and destination are shaped by the subscription's sink profile: an
// empty sink kind is the unchanged generic webhook (the wireEvent JSON to the
// subscription endpoint); a SIEM sink kind asks the renderer to re-shape the SAME
// event into the tower's dialect+envelope. EITHER way the body is then
// HMAC-signed (so provenance survives even for token-authed sinks) and POSTed
// through the SAME SSRF-guarded client.
func (m *Module) send(ctx context.Context, tenant model.TenantID, at attempt, secret, authValue string) (string, string) {
	var url string
	var body []byte
	header := map[string]string{"Content-Type": "application/json"}

	if at.sinkKind == "" {
		b, err := json.Marshal(wireEvent{
			ID: at.eventID, Type: at.eventType, Tenant: tenant.String(), Source: at.source,
			Time: at.occurredAt, Seq: at.seq, Payload: json.RawMessage(at.payload),
		})
		if err != nil {
			return statusDead, outcomeBadEndpoint
		}
		url, body = at.endpoint, b
	} else {
		m.warnOTLPRemapOnce(at.subID, at.eventType, at.sinkFormat)
		u, b, h, status, outcome := m.renderSink(ctx, tenant, at)
		if status != "" {
			return status, outcome
		}
		url, body, header = u, b, h
	}

	ts := strconv.FormatInt(m.clock.Now().Time().Unix(), 10)
	header[headerTimestamp] = ts
	header[headerSignature] = "t=" + ts + ",v1=" + webhook.Sign(secret, ts, body)
	header[headerEvent] = at.eventID
	header[headerEventType] = at.eventType
	header[headerDelivery] = at.deliveryID.String()

	// per-subscription auth header (in addition to HMAC signing).
	if authValue != "" {
		switch at.authType {
		case authTypeBearer:
			header["Authorization"] = "Bearer " + authValue
		case authTypeBasic:
			header["Authorization"] = "Basic " + authValue
		case authTypeHeader:
			if at.authHeaderName != "" {
				header[at.authHeaderName] = authValue
			}
		}
	}

	attemptCtx, cancel := context.WithTimeout(ctx, defaultHTTPTimeout)
	defer cancel()

	// The transport invariant on the URL that is ACTUALLY about to be dialed: https
	// (or loopback http under the development switch), no credentials in the URL. It
	// runs BEFORE the policy and independently of whether one exists, because it is
	// not an authorization question — a body, an HMAC signature and any bearer or
	// basic credential are about to travel, and they must not travel in cleartext
	// whatever the policy says.
	//
	// Authoring already applies this rule, and authoring is not authoritative for it:
	// the stored row may predate the rule, and the URL a SinkRenderer returns is not
	// the stored endpoint at all — the interface permits any URL and says so.
	if msg := validateEndpointURL(url, m.allowLoopback); msg != "" {
		m.debugf("eventing: refusing to send to an endpoint that fails the transport rule",
			"subscription", at.subID, "reason", msg)
		return statusDead, outcomeBadEndpoint
	}

	// THE AUTHORITATIVE destination check, on the URL that is actually about to be
	// dialed. It sits here rather than only in validateSubscription because this is
	// the single point every path converges on, and each of the others leaks:
	//
	//   - the SIEM branch above dials `rendered.URL`, which a SinkRenderer produces
	//     and no one re-examines. Today's renderer preserves the host by
	//     construction, but that is a property of connectors/siemsink, not an
	//     invariant this engine holds — the interface permits any URL;
	//   - a policy authored AFTER a subscription would grandfather it forever;
	//   - the /test handler sends with the stored endpoint on a path of its own.
	//
	// A refusal is statusDead, not statusQueued: a destination the operator has not
	// authorized will not become authorized by waiting, and burning the retry ladder
	// on it only delays the dead-letter that tells someone.
	decision, derr := m.authorizeDestination(attemptCtx, egressRequest{
		Tenant: tenant, Purpose: at.sendPurpose(), URL: url, SubscriptionRef: model.ID(at.subID),
	})
	if !decision.Permitted {
		if derr != nil {
			m.log.Warn("eventing: destination refused by the egress policy",
				"tenant", tenant.String(), "subscription", at.subID, "code", decision.Code, "err", derr)
		}
		if planeIndeterminate(decision.Code) {
			// The PLANE could not decide — not this destination. It is an outage, and it must
			// not be charged to the delivery: park it with the attempt restored.
			return statusParked, egressDenialOutcome(decision.Code)
		}
		if decision.Retryable() {
			// A name that does not resolve right now is an OUTAGE rather than a refusal, but it
			// IS a property of this destination, so it is a genuine retry and walks the ladder.
			// Treating it as terminal would dead-letter evidence because a lookup timed out.
			return statusQueued, egressDenialOutcome(decision.Code)
		}
		return statusDead, egressDenialOutcome(decision.Code)
	}
	// Pin the addresses the decision covered, so the dialer connects to the machine
	// that was authorized rather than re-resolving the name and taking whatever
	// answer comes back the second time.
	attemptCtx = egress.WithPin(attemptCtx, decision.Pin)
	// The addresses the operator authorized BY ADDRESS travel too, and only those may
	// be reached past the reserved-address floor. Without this an air-gapped
	// deployment could not ship its evidence anywhere: its SIEM is on RFC 1918 by
	// definition, so a policy naming the collector produced a permit here and an
	// unexplained network failure at the socket, forever. The floor is there to
	// constrain TENANT-authored destinations; the operator who configures the box is
	// not one — but naming a HOST is not naming an address, so a name that resolves
	// somewhere reserved authorizes nothing on its own.
	attemptCtx = egress.WithReservedAuthorization(attemptCtx, decision.ReservedAuthorized)

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return statusDead, outcomeBadEndpoint
	}
	// The request carries the CANONICAL host, not the spelling that was stored. The
	// policy authorized "soc.example.com"; without this the Host header and the TLS
	// server name went out as "soc.example.com." — the trailing dot the
	// canonicalization strips — so the name that was authorized and the name the
	// destination is asked for were different strings. Not a bypass, since the pin
	// governs which machine is reached, but a destination should be addressed by the
	// name it was approved under.
	if canonical, cerr := egress.CanonicalHost(req.URL.Hostname()); cerr == nil && canonical != req.URL.Hostname() {
		if port := req.URL.Port(); port != "" {
			req.URL.Host = net.JoinHostPort(canonical, port)
		} else {
			req.URL.Host = canonical
		}
		req.Host = req.URL.Host
	}
	req.Header.Set("User-Agent", deliveryUserAgent)
	for k, v := range header {
		req.Header.Set(k, v)
	}

	resp, err := m.doer.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || attemptCtx.Err() != nil {
			return statusQueued, outcomeTimeout
		}
		return statusQueued, outcomeNetwork
	}
	// Read the body rather than discarding it. Discarding it made this path
	// classify purely on HTTP status, and every destination it serves can answer 200
	// while refusing the payload: Splunk HEC returns a non-zero code, Elasticsearch
	// _bulk returns per-item failures, OTLP returns partial_success. This is the
	// route that ships the EVIDENCE LEDGER to a SIEM, so a refusal recorded as
	// "delivered" is a gap in exactly the record that is supposed to be complete.
	//
	// One byte past the budget so a body that exactly fills it is distinguishable
	// from one that overflows it: a truncated rejection decodes as garbage, and a
	// verdict must not be drawn from bytes we did not see whole.
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxDispatchBodyExcerpt+1))
	bodyComplete := readErr == nil && len(body) <= maxDispatchBodyExcerpt
	if len(body) > maxDispatchBodyExcerpt {
		body = body[:maxDispatchBodyExcerpt]
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	outcome := "http_" + strconv.Itoa(resp.StatusCode)
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if verdict, named := classifyDispatchBody(at.sinkKind, body, bodyComplete); named {
			return verdict, outcome + "_" + verdict
		}
		return statusDelivered, outcome
	case resp.StatusCode == http.StatusRequestTimeout, // 408
		resp.StatusCode == http.StatusTooEarly,        // 425
		resp.StatusCode == http.StatusTooManyRequests, // 429
		resp.StatusCode >= 500:
		return statusQueued, outcome
	default:
		return statusDead, outcome
	}
}

// warnOTLPRemapOnce is the pre-1.0 breaking-correction safeguard for the format-
// catalog remap: a STORED sink_format of "otlp" delivers the complete request
// envelope for ledger events now, where it delivered the bare LogRecord
// projection before (one token, one wire shape; the alias spelling always meant
// the envelope and is unaffected, so only the exact spelling "otlp" is gated).
// Policy: no rewrite of operator configuration, no acknowledgement gate — one
// conspicuous structured warning per affected subscription, so an operator whose
// downstream parser expected the old shape learns WHICH subscription to check
// and what to read instead. Deliberately process-local (review-noted trade-off):
// the note repeats once per subscription per PROCESS lifetime, because marking
// it durably would need a schema change for a one-time upgrade notice; the map
// is bounded by the distinct otlp/audit.recorded subscriptions one process
// sees. The message states the TOKEN's history, which is true whether the
// subscription predates the remap or not — a subscription's own creation date
// is not knowable here, so the text must not claim this one ever delivered the
// old shape.
func (m *Module) warnOTLPRemapOnce(subID, eventType, sinkFormat string) {
	if sinkFormat != string(siemwire.TokenOTLP) || eventType != "audit.recorded" {
		return
	}
	if _, seen := m.otlpRemapWarned.LoadOrStore(subID, struct{}{}); seen || m.log == nil {
		return
	}
	m.log.Warn("eventing: sink_format \"otlp\" delivers the complete OTLP request envelope for audit.recorded events; before the format-catalog remap this token delivered the bare LogRecord projection (now the pull-export format \"otlp_log_record\") — verify the downstream parser expects the envelope",
		"subscription", subID,
		"pre_remap_shape", "bare LogRecord projection",
		"delivered_shape", "ExportLogsServiceRequest envelope")
}

// outcomeCtx detaches an outcome write from shutdown cancellation: the attempt
// has already happened, so the system persists what it knows even while
// stopping — otherwise every restart would strand in-flight rows in
// "delivering" for the stale window, double-sending acknowledged deliveries
// and silently consuming ladder slots.
func outcomeCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
}

// buildCustomSchedule computes an exponential-backoff retry ladder from per-
// subscription settings. maxAttempts is the total delivery attempts (including
// the initial); the schedule length is maxAttempts-1 (waits between attempts).
func buildCustomSchedule(maxAttempts, initialIntervalSec int) []time.Duration {
	retries := maxAttempts - 1
	if retries <= 0 {
		return nil
	}
	base := time.Duration(initialIntervalSec) * time.Second
	if base <= 0 {
		base = 30 * time.Second
	}
	const maxDelay = 8 * time.Hour
	sched := make([]time.Duration, 0, retries)
	d := base
	for i := 0; i < retries; i++ {
		sched = append(sched, d)
		d *= 2
		if d > maxDelay {
			d = maxDelay
		}
	}
	return sched
}

// recordRetry schedules the next attempt on the backoff ladder, or
// dead-letters an exhausted delivery. at.attempts is 1-based (the attempt just
// made), so schedule[attempts-1] is the wait before the NEXT one.
func (m *Module) recordRetry(ctx context.Context, tenant model.TenantID, at attempt, outcome string) {
	// per-subscription retry schedule overrides the module default.
	schedule := m.retrySchedule
	if at.maxAttempts > 0 || at.initialInterval > 0 {
		ma := int(at.maxAttempts)
		if ma <= 0 {
			ma = len(m.retrySchedule) + 1
		}
		ii := int(at.initialInterval)
		schedule = buildCustomSchedule(ma, ii)
	}
	idx := int(at.attempts) - 1
	if idx >= len(schedule) {
		m.finishOwned(ctx, tenant, at, statusDead, outcome)
		return
	}
	next := m.clock.Now().Time().Add(jitter(schedule[idx]))
	wctx, cancel := outcomeCtx(ctx)
	defer cancel()
	err := m.data.Mutate(wctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(wctx, at.deliveryID)
		if err != nil {
			return err
		}
		if !ownsClaim(rec, at) {
			return nil // a rescuer or an admin redeliver took over; their state wins
		}
		rec[colDelStatus] = statusQueued
		rec[colDelNextAt] = model.NewTimestamp(next).String()
		rec[colDelLastStatus] = outcome
		_, err = repo.Update(wctx, rec)
		return err
	})
	if err != nil {
		m.debugf("eventing: retry scheduling failed", "delivery", at.deliveryID.String(), "err", err)
	}
}

// finishOwned records a terminal outcome for a CLAIMED attempt, writing only if
// this attempt still owns the row (ownership check against the claim version).
func (m *Module) finishOwned(ctx context.Context, tenant model.TenantID, at attempt, status, outcome string) {
	wctx, cancel := outcomeCtx(ctx)
	defer cancel()
	err := m.data.Mutate(wctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(wctx, at.deliveryID)
		if err != nil {
			return err
		}
		if !ownsClaim(rec, at) {
			return nil
		}
		rec[colDelStatus] = status
		rec[colDelLastStatus] = outcome
		rec[colDelAttempts] = at.attempts
		_, err = repo.Update(wctx, rec)
		return err
	})
	if err != nil {
		m.debugf("eventing: outcome recording failed", "delivery", at.deliveryID.String(), "err", err)
	}
}

// ownsClaim reports whether the row still belongs to this attempt's claim: the
// status is still "delivering" and the version is exactly the one the claim
// produced. A stale-claim rescuer or an admin redeliver bumps the version, so a
// writer that outlived its claim window backs off instead of clobbering.
func ownsClaim(rec model.Record, at attempt) bool {
	return rec.String(colDelStatus) == statusDelivering && rec.Int(model.ColVersion) == at.claimVersion
}

// jitter spreads a backoff delay ±20% so synchronized retries do not thunder.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	f := 0.8 + 0.4*rand.Float64() // #nosec G404 -- scheduling jitter, not key material
	return time.Duration(float64(d) * f)
}

// guardedClient is the production outbound client: per-attempt timeout,
// TLS ≥ 1.2, redirects refused, and a dial-time SSRF guard that re-checks the
// CONCRETE resolved IP (closing the DNS-rebinding TOCTOU, the mcp/a2a
// pattern) — tenant-supplied endpoints are untrusted (docs/SECURITY-HARDENING.md), unlike
// notify's operator-provisioned destinations. No proxy, deliberately: a proxy
// would be dialed INSTEAD of the endpoint, so the dial-time guard would check
// the proxy's IP and the endpoint guard would be void (the mcp/a2a guarded
// clients omit Proxy for the same reason).
// GuardedClient is the module's outbound client, EXPORTED so the CLI sends through
// the same one instead of a copy.
//
// The copy is what this replaces, and it failed exactly the way the engine's own pin
// first failed: it refused any dial address that was not already an IP literal, while
// an http.Transport hands its dialer "hostname:port". Every hostname destination was
// unreachable. It also carried a smaller reserved-address set than checkDialIP. One
// definition removes both problems and the possibility of a third.
func GuardedClient(allowLoopback bool) *http.Client {
	return (&Module{allowLoopback: allowLoopback}).guardedClient()
}

func (m *Module) guardedClient() *http.Client {
	allowLoopback := m.allowLoopback
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("eventing: refusing dial to unparseable address")
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("eventing: refusing dial to non-IP address")
			}
			return checkDialIP(ip, allowLoopback)
			// NOTE: the operator-authorization lift is applied in DialContext below,
			// which has the context this hook does not.
		},
	}
	// liftedDialer keeps every guard EXCEPT the reserved-address refusal, and is
	// selected per address by dialerFor.
	liftedDialer := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Client{
		Timeout: defaultHTTPTimeout,
		Transport: &http.Transport{
			// The pin check runs BEFORE the dial, on the concrete address the
			// transport is about to connect to. The Control hook above cannot do
			// this job: it is handed no context, so it cannot know which request an
			// address belongs to, and the transport is one shared instance for every
			// tenant. Together they answer two different questions — Control asks
			// "is this address category ever dialable", this asks "is this the
			// machine THIS delivery was authorized to reach".
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				// The floor yields PER ADDRESS, and only for one the operator wrote a
				// CIDR rule for. DialPinned substitutes a pinned address, so the
				// decision is made on the concrete address about to be dialed rather
				// than on a property of the request.
				return egress.DialPinned(ctx, dialerFor(ctx, dialer, liftedDialer), network, addr)
			},
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			// Connection reuse is DISABLED on this lane, and it is the pin that
			// requires it rather than a preference for fresh sockets.
			//
			// A pooled connection is keyed by host:port and skips DialContext
			// entirely, so the pin — which lives in the request context and is read
			// by the dialer — is simply not consulted for a reused connection. That
			// is not theoretical: the first version of this transport bounded reuse
			// with an idle timeout instead, and the pin test demonstrated the hole
			// by connecting a request whose pin did not cover the address, on a
			// connection an earlier authorized request had opened.
			//
			// An idle timeout does not fix it, it only shortens it. The cost of
			// closing it properly is a handshake per delivery, which this lane can
			// afford: deliveries are durable, retried and measured in events per
			// minute, and an egress control that a connection pool can bypass is not
			// a control.
			DisableKeepAlives: true,
		},
		// Redirects are refused, and that is LOAD-BEARING for the destination
		// policy rather than merely tidy: the policy decides the first hop. If this
		// ever returned nil, a 302 would carry the delivery to a host nothing
		// authorized — the pin would refuse an off-pin address, but a redirect to
		// another name resolving inside the pin would not be caught by it.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// checkDialIP rejects reserved/internal destinations: private (RFC 1918 / ULA),
// link-local (the cloud metadata service lives there), multicast, unspecified,
// the reservedCIDRs above (CGNAT/TEST-NET/benchmark/class-E/NAT64), and
// loopback unless explicitly allowed (tests / single-box development).
// IPv4-mapped IPv6 is unmapped first so ::ffff:10.0.0.1 cannot mask a private
// IPv4 target.
func checkDialIP(ip net.IP, allowLoopback bool) error {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() {
		if allowLoopback {
			return nil
		}
		return fmt.Errorf("eventing: refusing dial to loopback address")
	}
	// The classification is core/egress's, not a copy. This file used to hold its own
	// table while the policy layer held a NARROWER one, so an operator could write a
	// rule the policy accepted and this guard refused forever, with nothing saying why.
	if egress.ReservedAddress(ip) {
		return fmt.Errorf("eventing: refusing dial to reserved address")
	}
	return nil
}

// validateEndpointURL statically validates a subscription endpoint at authoring
// time (clear 400s; the dial-time guard remains authoritative). HTTPS is
// required; plain HTTP is allowed only for loopback hosts when loopback is
// allowed at all. Credentials in the URL are refused: the signing secret is the
// authentication mechanism, and a userinfo would end up in stored config.
func validateEndpointURL(raw string, allowLoopback bool) string {
	if raw == "" {
		return "endpoint is required"
	}
	if len(raw) > maxEndpointLen {
		return "endpoint too long"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "endpoint must be an absolute URL"
	}
	if u.User != nil {
		return "endpoint must not carry credentials"
	}
	host := u.Hostname()
	isLoopbackHost := host == "localhost" || func() bool {
		ip := net.ParseIP(host)
		return ip != nil && ip.IsLoopback()
	}()
	switch strings.ToLower(u.Scheme) {
	case "https":
	case "http":
		if !allowLoopback || !isLoopbackHost {
			return "endpoint must use https"
		}
	default:
		return "endpoint must use https"
	}
	if isLoopbackHost && !allowLoopback {
		return "endpoint host is not allowed"
	}
	if ip := net.ParseIP(host); ip != nil {
		if err := checkDialIP(ip, allowLoopback); err != nil {
			return "endpoint host is not allowed"
		}
	}
	return ""
}

// maxDispatchBodyExcerpt bounds how much of a 2xx response body is inspected for a
// logical rejection. It matches the connector-side budget so the two paths agree
// about what "too large to judge" means.
const maxDispatchBodyExcerpt = 2 << 10 // 2 KiB

// classifyDispatchBody looks for a logical refusal inside a 2xx response, for the
// ONE sink kind on this path whose protocol defines one.
//
// It is gated on the kind deliberately. This dispatcher POSTs to an
// operator-configured URL, and matching a body structurally without knowing what is
// on the other end manufactures false refusals: a generic collector that answers
// {"code":200,"message":"ok"} or an API that happens to carry an "errors" member
// would be dead-lettered as a rejection it never made. The other kinds here
// (https, sentinel_dcr, datadog, newrelic) either carry an opaque body or signal
// through status alone, so their 2xx stands exactly as before. Elasticsearch and
// OTLP are not sinks on this path at all — they are OutputConnectors, and their
// verdicts are drawn in their own connectors where the protocol is known.
//
// named=false means "this body says nothing I am entitled to interpret", which
// preserves the previous behavior: the 2xx stands.
func classifyDispatchBody(sinkKind string, body []byte, complete bool) (string, bool) {
	// Check the KIND first. A destination whose protocol defines no logical
	// rejection says nothing by answering at length, so requeuing its 2xx because
	// the body was large would retry a delivery that succeeded — for a reason the
	// operator could not act on. Only a kind we would have interpreted can be
	// harmed by not having read the answer.
	if sinkKind != string(siemsink.KindSplunkHEC) {
		return "", false
	}
	if !complete {
		// Queued, not dead: an unreadable answer is not evidence of refusal, and the
		// delivery may well have landed. Retrying is the safe direction here, because
		// discarding a notification over a body-size accident would be silent.
		return statusQueued, true
	}
	// The verdict comes from the CONNECTOR's table, not from a copy of it. This path
	// used to hold its own, and the two had already diverged on the cases that decide
	// whether evidence is recorded as delivered: it accepted a body only when text AND
	// code were both present (so the documented submit-with-ack response, which
	// carries an ackID, was unrecognizable), it read code 17 — a HEALTH answer, not an
	// acceptance of this event — as a delivery, and it read an EMPTY body as one too.
	// This is the lane that ships the audit ledger to a SIEM, so each of those was a
	// record claiming a delivery nobody confirmed.
	outcome, _, ok := splunkhec.HECVerdict(body)
	if !ok {
		// Not a HEC status document at all. Requeue rather than dead-letter: this
		// lane's fail-safe points toward keeping the notification, because losing one
		// here is silent while a duplicate is reconcilable.
		return statusQueued, true
	}
	switch {
	case outcome.Accepted():
		return "", false
	case outcome.Retryable():
		return statusQueued, true
	default:
		return statusDead, true
	}
}

// dialerFor picks the guarded dialer for ONE address: the lifted one when the
// operator authorized that exact address by CIDR, the floor-enforcing one otherwise.
//
// The choice is per address rather than per request because a destination can resolve
// to several, and a rule that names one of them says nothing about the others. It
// returns an egress.Dialer whose DialContext re-reads the decision for whatever
// address DialPinned finally substitutes, so a multi-homed destination cannot get the
// lift for an address the operator did not write by trying the authorized one first.
func dialerFor(_ context.Context, floor, lifted *net.Dialer) egress.Dialer {
	return perAddressDialer{floor: floor, lifted: lifted}
}

type perAddressDialer struct{ floor, lifted *net.Dialer }

func (d perAddressDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if egress.ReservedAuthorizedFor(ctx, addr) {
		return d.lifted.DialContext(ctx, network, addr)
	}
	return d.floor.DialContext(ctx, network, addr)
}

// sendPurpose is the attempt's purpose, defaulting to an ordinary delivery.
func (at attempt) sendPurpose() EgressPurpose {
	if at.purpose == EgressCreate {
		// The zero value means "not stated", and for a send the honest default is a send.
		// It must never fall through to EgressCreate, which is the one purpose that may
		// not use a compatibility exception: a delivery judged as a create would refuse
		// every grandfathered destination.
		return EgressSend
	}
	return at.purpose
}

// egressReadiness reports the OUTCOME TOKEN a park must record, or "" when the egress
// control can decide. It is consulted once per tenant per pass, outside any store
// transaction, exactly like the kill-switch gate.
//
// It answers the three questions that are about the PLANE rather than about a
// destination: can the durable rollout state be read, can the operator's policy be
// read, and — under compatibility mode — is this tenant's record of pre-existing
// destinations in place. All three are outages that resolve on their own, and all
// three would otherwise be paid for out of the retry ladder.
//
// The POLICY belongs here for the same reason as the other two, and leaving it out was
// a real gap: unit F correctly stopped treating an unreadable policy as terminal, but
// it did that per delivery, where "retryable" still means the attempt was claimed and
// the ladder advanced. A long enough policy-store outage therefore still dead-lettered
// evidence — the defect one altitude above where it was fixed.
//
// It returns the SAME tokens the per-delivery denial path uses rather than inventing a
// park-specific one, because the question an operator asks of a delivery row is "why
// did this not go out", and the answer has not changed — only the cost of it has.
func (m *Module) egressReadiness(ctx context.Context, tenant model.TenantID) string {
	st, err := m.resolveRollout(ctx)
	if err != nil {
		return egressDenialOutcome(egress.CodeRolloutUnavailable)
	}
	if p := m.resolvePolicy(ctx, tenant); p.Unavailable {
		return egressDenialOutcome(egress.CodePolicyUnavailable)
	}
	if st.CurrentMode != store.RolloutLegacyCompat || m.compat == nil {
		return ""
	}
	if err := m.compat.ensureSeed(ctx, tenant); err != nil {
		return egressDenialOutcome(egress.CodeSeedIncomplete)
	}
	return ""
}

// planeIndeterminate reports whether a denial code means "this control plane could not decide",
// as opposed to "this destination is not reachable right now".
//
// The distinction decides who pays. A destination that does not resolve is the delivery's own
// problem and walks its retry ladder. An unreadable disposition, an unreadable policy or a damaged
// compatibility record are the PLANE's problem, and charging them to the ladder means a long enough
// outage of our own state destroys the evidence we were carrying.
func planeIndeterminate(code string) bool {
	switch code {
	case egress.CodeRolloutUnavailable, egress.CodePolicyUnavailable, egress.CodeSeedIncomplete:
		return true
	}
	return false
}
