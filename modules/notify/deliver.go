// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Delivery ledger statuses. statusClaimed is the only non-terminal one: a
// CLAIM row is appended in a write transaction BEFORE the external send
// (claim-then-send) and the outcome is a SECOND appended row — the
// ledger entity is AppendOnly (immutable notification evidence, docs/SECURITY-HARDENING.md),
// so an outcome can never be rewritten, only recorded. Claim-then-send is what
// makes external delivery single-send under concurrency: a concurrent
// duplicate of the same finding (two handler goroutines today; two HA nodes
// receiving the same event over the NATS bridge tomorrow) is suppressed by the
// claim row, and on a standby the claim Mutate itself fails closed on the
// write gate — a node that cannot record a delivery never performs one. A
// crashed claimant's orphan row ages out of its dedup window (bounded
// suppression, self-healing) and remains in the ledger as honest evidence of a
// send with an unknown outcome. Dedup/throttle SUPPRESSIONS are still not
// recorded (they would defeat the dedup and flood the ledger) — debug-log only.
const (
	statusClaimed      = "claimed"
	statusDelivered    = "delivered"
	statusFailed       = "failed"
	statusNoDispatcher = "no_dispatcher"
	statusUnknownDest  = "unknown_destination"
	// statusRejected is a DETERMINISTIC refusal by the destination: it read the
	// payload and will not take it, so re-sending identical bytes cannot change the
	// answer. It is separated from statusFailed because the two demand opposite
	// handling — one must stop immediately, the other must be retried — and because
	// an operator reading the ledger needs to tell "the destination refused this"
	// apart from "we could not reach the destination".
	statusRejected = "rejected"
)

// signal is the normalized, minimal-data view of an inbound finding the router
// matches and delivers. No payload field by construction.
type signal struct {
	eventType   string
	kind        string
	severity    sdkmodel.Severity
	source      string
	subjectKind string
	subjectRef  string
	title       string
	detailHash  string
	at          time.Time
	// The finding's multi-taxonomy axes, carried verbatim from the
	// FindingReport so buildNotification can project them onto the SIEM Fields. A
	// finding with no framework reference leaves them nil (no taxonomy keys emitted).
	owaspLLM []string
	owaspASI []string
	atlas    []string
	// approval, when non-nil, marks this signal as an approval lifecycle event
	// rather than a finding. Opened approvals render as interactive approve/deny
	// cards; resolved approvals render as terminal notices. Minimal-data: ids and
	// the decision parameters only — never the requester's free-text reason,
	// decision note or subject reference (all deliberately absent from the event
	// payload, docs/SECURITY-HARDENING.md).
	approval *approvalSignal
}

// approvalSignal is the decision-parameter subset of an approval lifecycle event
// that the notification renders. It is the minimal-data SDK event payload, not
// the full approval record.
type approvalSignal struct {
	approvalID        string
	action            string
	riskTier          string
	outcome           string
	requiredApprovals int64
	approveCount      int64
	rejectCount       int64
	policyRef         string
	expiresAt         time.Time
	decidedAt         time.Time
	resolved          bool
}

// buildApprovalSignal normalizes an approval.requested event into the router's
// signal. The action drives kind (route glob + dedup), the approval id is the
// per-approval dedup ref, and the risk tier maps onto the severity scale so a
// route's min-severity still filters approvals.
func buildApprovalSignal(e event.Event, ar event.ApprovalRequest) signal {
	return signal{
		eventType:   string(e.Type),
		kind:        ar.Action,
		severity:    riskTierSeverity(ar.RiskTier),
		source:      e.Source,
		subjectKind: ar.SubjectKind,
		subjectRef:  ar.ApprovalID, // dedup keys per approval, not per subject
		title:       approvalTitle(ar.Action),
		at:          e.Time,
		approval: &approvalSignal{
			approvalID:        ar.ApprovalID,
			action:            ar.Action,
			riskTier:          ar.RiskTier,
			requiredApprovals: ar.RequiredApprovals,
			policyRef:         ar.PolicyRef,
			expiresAt:         ar.ExpiresAt,
		},
	}
}

// buildApprovalResolutionSignal normalizes an approval.resolved event into the
// router's signal. Resolution notices share the same route dimensions as opened
// approvals, but they carry an outcome and no interactive actions.
func buildApprovalResolutionSignal(e event.Event, ar event.ApprovalResolution) signal {
	return signal{
		eventType:   string(e.Type),
		kind:        ar.Action,
		severity:    riskTierSeverity(ar.RiskTier),
		source:      e.Source,
		subjectKind: ar.SubjectKind,
		subjectRef:  ar.ApprovalID,
		title:       approvalResolutionTitle(ar.Outcome),
		at:          e.Time,
		approval: &approvalSignal{
			approvalID:        ar.ApprovalID,
			action:            ar.Action,
			riskTier:          ar.RiskTier,
			outcome:           ar.Outcome,
			requiredApprovals: ar.RequiredApprovals,
			approveCount:      ar.ApproveCount,
			rejectCount:       ar.RejectCount,
			policyRef:         ar.PolicyRef,
			decidedAt:         ar.DecidedAt,
			resolved:          true,
		},
	}
}

// riskTierSeverity maps an risk tier onto the shared severity scale so the
// existing route min-severity filter and severity-aware destinations work for
// approvals. An empty/unknown tier maps to Medium (never Info): an approval that
// needs a human must not be silently buried below a route's min-severity floor.
func riskTierSeverity(tier string) sdkmodel.Severity {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "critical":
		return sdkmodel.SeverityCritical
	case "high":
		return sdkmodel.SeverityHigh
	case "low":
		return sdkmodel.SeverityLow
	case "medium":
		return sdkmodel.SeverityMedium
	default:
		return sdkmodel.SeverityMedium
	}
}

// approvalTitle is the short, non-sensitive card title for an opened approval.
func approvalTitle(action string) string {
	if action == "" {
		return "Approval needed"
	}
	return "Approval needed: " + action
}

// approvalResolutionTitle is the short, non-sensitive card title for a terminal
// approval resolution.
func approvalResolutionTitle(outcome string) string {
	outcome = strings.TrimSpace(outcome)
	if outcome == "" {
		outcome = "unknown"
	}
	return "Approval resolved: " + outcome
}

// buildSignal normalizes a finding event into the router's signal.
func buildSignal(e event.Event, report sdkmodel.FindingReport) signal {
	title := report.Title
	if title == "" {
		title = report.Kind
	}
	at := report.OccurredAt
	if at.IsZero() {
		at = e.Time
	}
	return signal{
		eventType:   string(e.Type),
		kind:        report.Kind,
		severity:    report.Severity,
		source:      e.Source,
		subjectKind: report.SubjectKind,
		subjectRef:  report.SubjectRef,
		title:       title,
		detailHash:  report.DetailHash,
		at:          at,
		owaspLLM:    report.OWASPLLM,
		owaspASI:    report.OWASPASI,
		atlas:       report.ATLAS,
	}
}

// pending is one matched route ready to deliver, captured during the read-only
// evaluation phase so the network delivery happens OUTSIDE any store transaction
// (its claim row is appended by claimDeliveries before the send).
type pending struct {
	routeID     model.ID
	routeName   string
	destination string
	dedupKey    string
	notifyJSON  string // the rendered sdk.Notification, marshaled once at route time
}

// evaluateRoute reports the predicate dimensions that REJECT the signal (empty
// = the route selects it). Every dimension is ANDed; an empty match set / empty
// min-severity means "any". The delivery path (matchesRoute) and the dry-run
// (POST /routes/evaluate) share this one predicate so they cannot drift.
func evaluateRoute(rec model.Record, s signal) []string {
	var out []string
	if !csvContains(rec.String(colMatchTypes), s.eventType) {
		out = append(out, "type")
	}
	if !s.severity.AtLeast(parseSeverity(rec.String(colMinSeverity))) {
		out = append(out, "severity")
	}
	if !globMatchAny(rec.String(colMatchKinds), s.kind) {
		out = append(out, "kind")
	}
	if !csvContains(rec.String(colMatchSources), s.source) {
		out = append(out, "source")
	}
	if !csvContains(rec.String(colMatchSubjects), s.subjectKind) {
		out = append(out, "subject_kind")
	}
	return out
}

// matchesRoute reports whether a route's predicate selects the signal.
func matchesRoute(rec model.Record, s signal) bool {
	return len(evaluateRoute(rec, s)) == 0
}

// processFinding routes one finding in three phases (claim-then-send):
//
//  1. a READ transaction pre-matches routes and pre-checks dedup/throttle —
//     cheap, so the common no-route finding never touches the writer;
//  2. ONE write transaction re-checks dedup/throttle and CLAIMS a ledger row
//     per surviving match. The re-check inside the tx is what closes the
//     read-then-send race (two concurrent duplicates both passed phase 1; only
//     one claims). On an HA standby this Mutate fails on the write gate —
//     deny-closed: a node that cannot record a delivery never sends one;
//  3. each claimed match delivers OUTSIDE any transaction (network I/O must
//     never hold the store writer) and its claim row is finalized to the
//     terminal outcome.
//
// Best-effort throughout: a failure logs and is not propagated (a handler
// error only gets logged by the bus and must not stop delivery to other
// subscribers).
func (m *Module) processFinding(ctx context.Context, tenant model.TenantID, e event.Event, report sdkmodel.FindingReport) error {
	return m.route(ctx, tenant, buildSignal(e, report))
}

// processApproval routes an opened approval (event.TypeApprovalRequested):
// it normalizes the ApprovalRequest into an actionable signal and runs the same
// claim-then-send delivery as a finding, so an approval opened by the API reaches
// a human channel as an interactive approve/deny card. Closing the HITL chat
// round-trip whose inbound half is the receiver in cmd/olivares/hitl.go.
func (m *Module) processApproval(ctx context.Context, tenant model.TenantID, e event.Event, ar event.ApprovalRequest) error {
	if ar.ApprovalID == "" {
		return nil // nothing actionable without an id to decide on
	}
	return m.route(ctx, tenant, buildApprovalSignal(e, ar))
}

// processApprovalResolution routes a terminal approval lifecycle notice
// (event.TypeApprovalResolved): same route/permission model as the opened
// approval, but rendered as a non-interactive resolution notification.
func (m *Module) processApprovalResolution(ctx context.Context, tenant model.TenantID, e event.Event, ar event.ApprovalResolution) error {
	if ar.ApprovalID == "" {
		return nil
	}
	return m.route(ctx, tenant, buildApprovalResolutionSignal(e, ar))
}

// route runs the three-phase claim-then-send delivery for ANY normalized
// signal — a finding or an approval. The phases and their concurrency/HA
// guarantees are unchanged; only the signal source differs.
func (m *Module) route(ctx context.Context, tenant model.TenantID, s signal) error {
	now := m.clock.Now().Time()

	var todo []pending
	err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		routeRepo, err := sc.Ext(routeKind)
		if err != nil {
			return err
		}
		routes, err := listAll(ctx, routeRepo, eq(colEnabled, true))
		if err != nil {
			return err
		}
		sortRoutes(routes)
		delRepo, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		for _, route := range routes {
			if !matchesRoute(route, s) {
				continue
			}
			routeID := model.ID(route.String(model.ColID))
			// The event type is a dedup dimension: an approval's opened card
			// (approval.requested) and its terminal notice (approval.resolved)
			// share kind (the action) and subjectRef (the approval id) but are
			// distinct notifications — a route matching both types must never
			// suppress the resolution as a duplicate of the request.
			dedupKey := hashHex(route.String(colName) + "|" + s.eventType + "|" + s.kind + "|" + s.subjectRef)
			if suppressed, err := m.suppressed(ctx, delRepo, route, routeID, dedupKey, s, now); err != nil {
				return err
			} else if suppressed {
				continue
			}
			p := pending{routeID: routeID, routeName: route.String(colName), destination: route.String(colDestination), dedupKey: dedupKey}
			nb, merr := json.Marshal(m.buildNotification(tenant, s, p))
			if merr != nil {
				// A notification that cannot be marshaled can never be delivered — skip
				// this route rather than enqueue an undeliverable outbox row.
				m.debugf("notify: notification marshal failed; route skipped", "route", p.routeName)
				continue
			}
			p.notifyJSON = string(nb)
			todo = append(todo, p)
		}
		return nil
	})
	if err != nil {
		m.debugf("notify: route evaluation failed", "err", err)
		return nil
	}
	if len(todo) == 0 {
		return nil
	}

	claimed, err := m.claimDeliveries(ctx, tenant, s, todo, now)
	if err != nil {
		// store.ErrNotLeader is an HA standby declining to side-effect; an
		// ErrConflict is a concurrent claimer winning the route-row serializer —
		// in both cases the right delivery happens(/ed) elsewhere.
		switch {
		case errors.Is(err, store.ErrNotLeader):
			m.debugf("notify: standby node declined delivery (the leader delivers)", "kind", s.kind)
		case errors.Is(err, store.ErrConflict):
			m.debugf("notify: lost the claim race to a concurrent duplicate (its claimer delivers)", "kind", s.kind)
		default:
			m.debugf("notify: delivery claim failed", "err", err)
		}
		return nil
	}

	// Each surviving claim was enqueued durably (a notify_outbox row) in the same tx as
	// its claimed evidence row. Delivery happens out of band: the nudge gives it a
	// low-latency first attempt (per-tenant, so it works even where the cross-tenant
	// pump cannot enumerate), and the leader-gated pump (notifypump.go) is the durable
	// backstop for retries/backoff/DLQ. No synchronous external send runs in this bus
	// handler, so a slow or down destination never blocks routing.
	if len(claimed) > 0 {
		m.nudge(tenant)
	}
	return nil
}

// suppressed runs the dedup and throttle window tests for one matched route.
// Claims count for BOTH tests (an in-flight delivery suppresses a concurrent
// duplicate — the point of claim-then-send); failed attempts still only count
// for throttle (a prior failure must not dedup-suppress a retry of the alert).
func (m *Module) suppressed(ctx context.Context, delRepo store.GenericRepo, route model.Record, routeID model.ID, dedupKey string, s signal, now time.Time) (bool, error) {
	if win := route.Int(colDedupWindow); win > 0 {
		if dup, err := dedupSuppressed(ctx, delRepo, now, win, dedupKey); err != nil {
			return false, err
		} else if dup {
			m.debugf("notify: dedup-suppressed", "route", route.String(colName), "kind", s.kind)
			return true, nil
		}
	}
	if win := route.Int(colThrottleWin); win > 0 {
		if thr, err := recentAttempt(ctx, delRepo, now, win, eq(colDelRouteRef, routeID.String())); err != nil {
			return false, err
		} else if thr {
			m.debugf("notify: throttle-suppressed", "route", route.String(colName), "kind", s.kind)
			return true, nil
		}
	}
	return false, nil
}

// claimDeliveries re-checks suppression and inserts one CLAIMED ledger row per
// surviving match, all in one write transaction. Matches that lost the
// re-check (a concurrent duplicate claimed first) are dropped silently — that
// is the dedup working. Returns the claims with their created rows attached.
func (m *Module) claimDeliveries(ctx context.Context, tenant model.TenantID, s signal, todo []pending, now time.Time) ([]pending, error) {
	var claimed []pending
	err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		claimed = claimed[:0] // a retried tx must not double-collect
		routeRepo, err := sc.Ext(routeKind)
		if err != nil {
			return err
		}
		delRepo, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		obRepo, err := sc.Ext(outboxKind)
		if err != nil {
			return err
		}
		for _, p := range todo {
			// Re-resolve the route inside the tx: it may have been disabled or its
			// windows retuned between the phases.
			route, ok, err := findOne(ctx, routeRepo, eq(model.ColID, p.routeID.String()))
			if err != nil {
				return err
			}
			if !ok || !route.Bool(colEnabled) {
				continue
			}
			if sup, err := m.suppressed(ctx, delRepo, route, p.routeID, p.dedupKey, s, now); err != nil {
				return err
			} else if sup {
				continue
			}
			// Serialize concurrent claimers ON THE ROUTE ROW: under READ COMMITTED
			// two overlapping claim transactions (two goroutines, or two nodes in
			// the ≤2s failover window — both still pass the write gate) would each
			// read no claim and both insert one. Bumping the route's optimistic
			// version forces a write-write conflict: the loser's Mutate fails with
			// ErrConflict and delivers nothing. Without this, the claim only
			// serializes on SQLite's single writer.
			if _, err := routeRepo.Update(ctx, route); err != nil {
				return err
			}
			if _, err := delRepo.Create(ctx, claimRecord(s, p, now)); err != nil {
				return err
			}
			// Enqueue the durable work item in the SAME tx as the claim: intent is
			// persisted before any send, so a crash after this point still delivers.
			if _, err := obRepo.Create(ctx, outboxRecord(s, p, p.notifyJSON, now)); err != nil {
				return err
			}
			claimed = append(claimed, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// dedupSuppressed is the DEDUP test: it looks at the LATEST ledger row for the
// dedup key within the trailing window and suppresses when that row is
// DELIVERED (a repeat of an already-sent alert) or CLAIMED (a delivery is in
// flight RIGHT NOW — the claim-then-send race closer). A latest row with
// a failure status does not suppress: the claim that preceded it is resolved,
// and a prior failed attempt must NOT dedup-suppress a retry of the alert.
// "Latest" orders by id — ids are UUIDv7, time-ordered — because the claim and
// its outcome can share one occurred_at under a frozen clock.
func dedupSuppressed(ctx context.Context, repo store.GenericRepo, now time.Time, winSeconds int64, dedupKey string) (bool, error) {
	since := now.Add(-time.Duration(winSeconds) * time.Second)
	rows, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{
			eq(colDelDedupKey, dedupKey),
			gte(colDelOccurredAt, model.NewTimestamp(since).String()),
		},
		// One row: the newest by id (UUIDv7 — time-ordered). Draining every row
		// for a hot key on each finding, inside the claim WRITE tx, would be
		// O(rows²) across an alert storm.
		Sort:  []model.Sort{{Column: model.ColID, Desc: true}},
		Limit: 1,
	})
	if err != nil || len(rows) == 0 {
		return false, err
	}
	st := rows[0].String(colDelStatus)
	return st == statusDelivered || st == statusClaimed, nil
}

// recentAttempt reports whether ANY delivery-attempt row (delivered OR failed)
// matching extra exists within the trailing window — the THROTTLE test. Throttle is
// outcome-agnostic so a persistently failing or misconfigured destination is still
// rate-limited, not re-attempted (and re-logged) on every finding.
func recentAttempt(ctx context.Context, repo store.GenericRepo, now time.Time, winSeconds int64, extra model.Filter) (bool, error) {
	since := now.Add(-time.Duration(winSeconds) * time.Second)
	_, ok, err := findOne(ctx, repo,
		extra,
		gte(colDelOccurredAt, model.NewTimestamp(since).String()),
	)
	return ok, err
}

// buildNotification projects the signal into the minimal-data sdk.Notification the
// transport delivers. It carries only displayable, non-sensitive fields; the
// dedup_key lets a destination (e.g. PagerDuty) coalesce repeats.
func (m *Module) buildNotification(tenant model.TenantID, s signal, p pending) sdk.Notification {
	if s.approval != nil {
		return buildApprovalNotification(tenant, s)
	}
	fields := map[string]string{
		"kind":         s.kind,
		"subject_kind": s.subjectKind,
		"subject_ref":  clamp(s.subjectRef, maxRefLen),
		"source":       s.source,
		"dedup_key":    p.dedupKey,
		"route":        p.routeName,
	}
	if s.detailHash != "" {
		fields["detail_hash"] = s.detailHash
	}
	// Project the finding's three taxonomy axes onto SIEM fields. Each axis
	// is comma-joined in its already-sorted order so siemfmt's deterministic output is
	// byte-stable; an empty axis emits no key. These flow to CEF/LEEF/syslog/OTLP as
	// extension fields and ride OCSF `unmapped` / ASIM AdditionalFields, so a SOC can
	// filter a finding by OWASP LLM, OWASP Agentic (ASI) or MITRE ATLAS.
	if v := joinTaxonomy(s.owaspLLM); v != "" {
		fields[sdkmodel.FieldOWASPLLM] = v
	}
	if v := joinTaxonomy(s.owaspASI); v != "" {
		fields[sdkmodel.FieldOWASPASI] = v
	}
	if v := joinTaxonomy(s.atlas); v != "" {
		fields[sdkmodel.FieldATLAS] = v
	}
	return sdk.Notification{
		Type:     s.eventType,
		Title:    clamp(s.title, maxNameLen),
		Body:     s.kind,
		Severity: s.severity,
		Tenant:   tenant.String(),
		Fields:   fields,
		Time:     s.at,
	}
}

// Action vocabulary the inbound HITL receiver (cmd/olivares/hitl.go) parses on a
// click: the button's VALUE packs "decision:approval_id" (the receiver's
// decisionAndID splits on the first ':'), and the action_id repeats the decision
// with the "olivares_" prefix its normalizeDecision strips. Either alone suffices;
// both make the click self-describing and resilient.
const (
	actionApproveID = "olivares_approve"
	actionDenyID    = "olivares_deny"
)

// ApprovalActions returns the canonical approve/deny action pair for an opened
// approval — the origination half of the HITL chat round-trip. An output
// that renders interactive controls (the Slack Block Kit connector) turns each
// into a button whose action_id/value the inbound receiver reads back; an output
// that cannot just ignores them. It carries no secret — only the decision verb
// and the approval id.
func ApprovalActions(approvalID string) []sdk.NotificationAction {
	return []sdk.NotificationAction{
		{Label: "Approve", ID: actionApproveID, Value: "approve:" + approvalID, Style: "primary"},
		{Label: "Deny", ID: actionDenyID, Value: "deny:" + approvalID, Style: "danger"},
	}
}

// buildApprovalNotification projects an approval signal into the minimal-data,
// sdk.Notification an output delivers. Opened approvals carry approve/deny
// actions; resolved approvals are non-interactive terminal notices. The Fields
// are decision parameters only — never the requester's reason, decision note or
// subject reference (all absent from the event by design, docs/SECURITY-HARDENING.md).
func buildApprovalNotification(tenant model.TenantID, s signal) sdk.Notification {
	a := s.approval
	fields := map[string]string{
		"approval_id":        a.approvalID,
		"action":             a.action,
		"subject_kind":       s.subjectKind,
		"risk_tier":          a.riskTier,
		"required_approvals": strconv.FormatInt(a.requiredApprovals, 10),
	}
	if a.resolved {
		fields["outcome"] = a.outcome
		fields["approve_count"] = strconv.FormatInt(a.approveCount, 10)
		fields["reject_count"] = strconv.FormatInt(a.rejectCount, 10)
	}
	if a.policyRef != "" {
		fields["policy_ref"] = a.policyRef
	}
	if !a.expiresAt.IsZero() {
		fields["expires_at"] = a.expiresAt.UTC().Format(time.RFC3339)
	}
	if !a.decidedAt.IsZero() {
		fields["decided_at"] = a.decidedAt.UTC().Format(time.RFC3339)
	}
	n := sdk.Notification{
		Type:     s.eventType,
		Title:    clamp(s.title, maxNameLen),
		Body:     approvalBody(a),
		Severity: s.severity,
		Tenant:   tenant.String(),
		Fields:   fields,
		Time:     s.at,
	}
	if !a.resolved {
		n.Actions = ApprovalActions(a.approvalID)
	}
	return n
}

// approvalBody is the one-line, non-sensitive human summary on the card.
func approvalBody(a *approvalSignal) string {
	if a.resolved {
		outcome := a.outcome
		if outcome == "" {
			outcome = "unknown"
		}
		risk := a.riskTier
		if risk == "" {
			risk = "unspecified"
		}
		return "A " + risk + "-risk action resolved as " + outcome + "."
	}
	risk := a.riskTier
	if risk == "" {
		risk = "unspecified"
	}
	n := a.requiredApprovals
	if n < 1 {
		n = 1
	}
	noun := "approval"
	if n != 1 {
		noun = "approvals"
	}
	return "A " + risk + "-risk action awaits " + strconv.FormatInt(n, 10) + " " + noun + "."
}

// joinTaxonomy renders one taxonomy axis as a deterministic, comma-joined id list for
// a SIEM field. It sorts and de-dups defensively so the output is byte-stable
// regardless of the producer's ordering (the security module already emits sorted sets,
// but notify routes findings from any producer). An empty axis yields "" (no key).
func joinTaxonomy(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	cp := append([]string(nil), ids...)
	sort.Strings(cp)
	out := cp[:0]
	for _, v := range cp {
		if v == "" {
			continue
		}
		if len(out) == 0 || v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return strings.Join(out, ",")
}

// dispatchOne delivers through the transport seam and classifies the outcome into a
// non-sensitive status + detail. It never logs or records the raw delivery error
// (a connector error can embed a destination URL/token) — only a class.
func (m *Module) dispatchOne(ctx context.Context, tenant model.TenantID, destination string, n sdk.Notification) (string, string) {
	err := m.dispatch.Deliver(ctx, tenant, destination, n)
	switch {
	case err == nil:
		return statusDelivered, ""
	case errors.Is(err, ErrUnknownDestination):
		return statusUnknownDest, "destination not provisioned"
	case errors.Is(err, errNoDispatcher):
		return statusNoDispatcher, "transport not wired"
	}
	// Ask the connector WHAT the destination did, rather than inferring "retry" from
	// the mere presence of an error. A destination can answer HTTP 200 and still
	// refuse the payload (Splunk HEC's non-zero code, Elasticsearch's per-item
	// failures, OTLP's partial_success), and the retry ladder is the wrong response
	// to a refusal: it re-sends bytes the destination already rejected, for roughly
	// forty minutes, before dead-lettering them anyway. For OTLP it is not merely
	// wasteful — the specification says the client MUST NOT retry a populated
	// partial success.
	//
	// A connector that returns a plain error carries no report and resolves to
	// OutcomeIndeterminate, which keeps the previous behavior: retry. So this
	// widens what the engine can honor without changing what an unmodified
	// connector does.
	switch report := sdk.ReportFor(err); {
	case report.Outcome == sdk.OutcomeRejected, report.Outcome == sdk.OutcomeProtocolAnomaly:
		m.debugf("notify: destination refused the payload",
			"destination", destination, "outcome", report.Outcome.String())
		// The detail is a FIXED token, never the destination's own text: it is written
		// to the delivery ledger, and a remote party must not choose what our records
		// say.
		return statusRejected, report.Outcome.String()
	case report.Outcome == sdk.OutcomePartial:
		// Part of the batch landed. Retrying would duplicate that part, so this stops
		// here and surfaces in the dead-letter queue with the ordinals the protocol
		// gave us, for an operator to resubmit selectively.
		m.debugf("notify: destination accepted the payload only in part", "destination", destination)
		return statusRejected, report.Outcome.String()
	default:
		m.debugf("notify: delivery failed", "destination", destination)
		return statusFailed, "delivery_failed"
	}
}

// claimRecord builds the CLAIMED ledger row for one matched route.
func claimRecord(s signal, p pending, now time.Time) model.Record {
	rec := model.Record{
		colDelDestination: p.destination,
		colDelEventType:   s.eventType,
		colDelKind:        s.kind,
		colDelSeverity:    string(s.severity),
		colDelSubjectKind: s.subjectKind,
		colDelSubjectRef:  clamp(s.subjectRef, maxRefLen),
		colDelTitle:       clamp(s.title, maxNameLen),
		colDelDedupKey:    p.dedupKey,
		colDelStatus:      statusClaimed,
		colDelOccurredAt:  model.NewTimestamp(now).String(),
	}
	if !p.routeID.IsZero() {
		rec[colDelRouteRef] = p.routeID.String()
	}
	return rec
}

// finalizeDelivery appends the OUTCOME row for a claim after the send — a new
// append, never an update: the ledger entity is AppendOnly (immutable
// evidence; a compromised writer can add rows, never rewrite an outcome). It
// runs on a non-cancelable context: the external send already happened, so its
// outcome must not vanish because the triggering request/handler ctx died.
// Best-effort: a failed append leaves the claim row latest — it suppresses its
// dedup window out (bounded) and the log line is the operational signal.
func (m *Module) finalizeDelivery(ctx context.Context, tenant model.TenantID, s signal, p pending, status, detail string) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	now := m.clock.Now().Time()
	err := m.data.Mutate(fctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(deliveryKind)
		if err != nil {
			return err
		}
		rec := claimRecord(s, p, now)
		rec[colDelStatus] = status
		if detail != "" {
			rec[colDelDetail] = detail
		}
		_, e := repo.Create(fctx, rec)
		return e
	})
	if err != nil {
		m.debugf("notify: outcome append failed (the claim row stays latest and ages out of its window)", "err", err)
	}
}

// sortRoutes orders routes by priority (lower first) then name, so evaluation is
// deterministic regardless of store order.
func sortRoutes(routes []model.Record) {
	sort.SliceStable(routes, func(i, j int) bool {
		pi, pj := routes[i].Int(colPriority), routes[j].Int(colPriority)
		if pi != pj {
			return pi < pj
		}
		return routes[i].String(colName) < routes[j].String(colName)
	})
}
