// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// errStopWalk stops an audit Walk early once a page is full (a sentinel, not a
// real error).
var errStopWalk = errors.New("stop walk")

// errStopAuditRange stops an export Walk once its inclusive ?to bound has been
// passed (a sentinel, not a real error).
var errStopAuditRange = errors.New("stop audit range")

const auditScanPageSize = 1000

// auditScanCap bounds the amount of ledger history a filtered list request may
// examine. It is a variable so the honesty contract can be exercised without
// manufacturing tens of thousands of audit events in tests.
var auditScanCap = 20000

type auditFilters struct {
	values map[string]string
	since  *time.Time
	until  *time.Time
	q      string
	// excludeActions are ?exclude_action prefixes, and it is a SLICE because the
	// parameter repeats: one occurrence per action family to leave out. They are
	// matched with the same strings.HasPrefix rule as the positive ?action filter —
	// two sibling parameters that filtered by different rules would be a trap for
	// whoever reads one and assumes the other.
	excludeActions []string
}

// auditListResponse carries the standard items/has_more pair plus the ledger's own
// scan bookkeeping, so it cannot BE a ListResponse — but its items field is the same
// contract and uses the same non-nullable array type.
type auditListResponse struct {
	Items        JSONArray[AuditEventDTO] `json:"items"`
	NextFrom     int64                    `json:"next_from,omitempty"`
	ScanComplete bool                     `json:"scan_complete"`
	HasMore      bool                     `json:"has_more"`
	HeadSeq      int64                    `json:"head_seq"`
}

// auditPageResponse is the UNFILTERED ledger page: the legacy items/has_more pair
// plus head_seq, and deliberately nothing else.
//
// It is not listResponse[AuditEventDTO] any more because that envelope is shared by
// every collection route and no other collection has a chain tip. It is not
// auditListResponse either, and that is the load-bearing half: those two extra
// fields answer a question the unfiltered path never asks. `scan_complete` reports
// whether a bounded attribute scan reached the head, and an unfiltered Walk does not
// scan — serving a hardcoded `"scan_complete": false` beside a complete page would
// publish a false statement, and serving `true` beside a truncated one another.
// TestAuditUnfilteredListKeepsLegacyEnvelope pins their absence.
type auditPageResponse struct {
	Items   JSONArray[AuditEventDTO] `json:"items"`
	HasMore bool                     `json:"has_more"`
	HeadSeq int64                    `json:"head_seq"`
}

// auditHeadSeq reports the tenant's ledger head sequence — the value both list
// responses publish as head_seq, and the only thing in either response that
// addresses the END of the chain. `from` walks FORWARDS from a sequence (Walk is
// ORDER BY seq ASC), so without a head a client that wants the newest events has no
// way to name them: it can only ask for the oldest and mislabel them. That is
// exactly what the notification bell did.
//
// 0 means NO HEAD WAS EVER RECORDED — which is what an empty ledger looks like, and
// the caller must be able to receive it: "no events yet" and "one event" are different
// pages, and a client paging backwards from the head has to be able to tell them apart.
//
// The tip comes from audit_heads (store.RecordedHeadReader — what the store RECORDS)
// when the store can answer that question, and from the last surviving event
// otherwise. On a healthy chain the two agree by construction: the row insert and the
// head advance happen in one transaction (sqlstore persistEvent/advanceHead). They part
// company on damage, and in BOTH directions — a ledger emptied under a live head
// reports a recorded tip with no rows beneath it, and a store that has rows but no
// recorded head reports none. Saying "they differ in one situation" would be tidier and
// TestAuditHeadSeqPrefersTheRecordedHead exercises both, so it would also be false.
//
// The recorded tip wins because head_seq answers "how far has this chain gone", and on
// the first of those two that is the question with the honest answer. What it therefore
// is NOT is a promise that the row at head_seq is readable.
//
// The fallback is not a formality: an AuditLog is free not to implement the optional
// capability, and a caller that assumed it would silently publish head_seq 0 for every
// tenant on such a store.
func auditHeadSeq(ctx context.Context, sc store.Scope) (int64, error) {
	if recorded, ok := sc.Audit().(store.RecordedHeadReader); ok {
		head, has, err := recorded.RecordedHead(ctx)
		if err != nil {
			return 0, err
		}
		if !has {
			return 0, nil
		}
		return head.Seq, nil
	}
	head, has, err := sc.Audit().Head(ctx)
	if err != nil {
		return 0, err
	}
	if !has {
		return 0, nil
	}
	return head.Seq, nil
}

// handleAuditList returns a page of the tenant's ledger from ?from (default 1).
// The legacy unfiltered path keeps its read and self-audit in one committed
// transaction; filtered requests use bounded Views and a separate Mutate.
func (s *Server) handleAuditList(w http.ResponseWriter, r *http.Request) {
	p, tenant, ok := s.authzTenant(w, r, "audit:read")
	if !ok {
		return
	}
	s.auditListInto(w, r, p, tenant)
}

// handleSystemAuditList reads the SYSTEM-tenant evidence ledger — the chain where the
// superadmin's auth-partition operations land via AuthMutate (user provisioning,
// membership grants, session login/refresh; authenticator.go, accounts.go). Note that
// org creation records org.create in the NEW org's own chain, not here. The
// tenant-scoped /v1/audit cannot reach the system chain:
// resolveTenant deliberately refuses the reserved system tenant (middleware.go), so
// those durably-written events were unreadable over HTTP. This is the only
// read path into that chain and it is superadmin-only: authzSystem authorizes against
// model.SystemTenantID with system:admin, which authorizer.go grants ONLY to the
// superadmin flag — a tenant-bound principal (even one holding audit:read in its own
// tenant) gets 403, never cross-tenant visibility.
func (s *Server) handleSystemAuditList(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, auth.PermSystemAdmin)
	if !ok {
		return
	}
	s.auditListInto(w, r, p, model.SystemTenantID)
}

// auditListInto dispatches filtered reads to the bounded scanner. Its unfiltered
// path walks a tenant's ledger from ?from into a page and records the read in the
// same committed transaction (a View would roll the self-audit back). The tenant
// is supplied by the caller (the resolved business tenant for /v1/audit, or the
// system tenant for /v1/audit/system) — never re-derived from the request.
func (s *Server) auditListInto(w http.ResponseWriter, r *http.Request, p auth.Principal, tenant model.TenantID) {
	from := queryInt64(r, "from", 1)
	limit := int(queryInt64(r, "limit", 100))
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	filters, filtered, ferr := parseAuditFilters(r)
	if ferr != nil {
		s.badRequest(w, r, ferr.Error())
		return
	}
	if filtered {
		s.auditFilteredListInto(w, r, p, tenant, from, limit, filters)
		return
	}
	out := auditPageResponse{Items: []AuditEventDTO{}}
	err := s.st.Mutate(r.Context(), tenant, func(sc store.Scope) error {
		werr := sc.Audit().Walk(r.Context(), from, func(ev model.AuditEvent) error {
			if len(out.Items) >= limit {
				out.HasMore = true
				return errStopWalk
			}
			out.Items = append(out.Items, toAuditDTO(ev))
			return nil
		})
		if werr != nil && !errors.Is(werr, errStopWalk) {
			return werr
		}
		// BEFORE the self-audit, and the order is the contract, not tidiness: this
		// read appends its own audit.read event to the very chain it is reporting on.
		// Read the head afterwards and an EMPTY ledger answers head_seq 1 — the read's
		// own footprint — which is the one value the empty case must never return, and
		// head_seq would name a sequence that is not in items on every single call.
		head, herr := auditHeadSeq(r.Context(), sc)
		if herr != nil {
			return herr
		}
		out.HeadSeq = head
		return appendAudit(r.Context(), sc, p, "audit.read", "core.audit_event", "")
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// auditFilteredListInto scans in bounded, short read transactions so attribute
// filtering never holds the store's single SQLite connection for an unbounded
// write transaction. Continuation follows the last examined event, not the last
// sparse match.
func (s *Server) auditFilteredListInto(
	w http.ResponseWriter,
	r *http.Request,
	p auth.Principal,
	tenant model.TenantID,
	from int64,
	limit int,
	filters auditFilters,
) {
	out := auditListResponse{Items: []AuditEventDTO{}}
	cursor := from
	examined := 0
	scanCap := auditScanCap

	for examined < scanCap && len(out.Items) < limit {
		pageLimit := auditScanPageSize
		if remaining := scanCap - examined; remaining < pageLimit {
			pageLimit = remaining
		}

		var page []model.AuditEvent
		rerr := s.st.View(r.Context(), tenant, func(sc store.Scope) error {
			return sc.Audit().Walk(r.Context(), cursor, func(ev model.AuditEvent) error {
				if len(page) >= pageLimit {
					return errStopWalk
				}
				page = append(page, ev)
				return nil
			})
		})
		full := errors.Is(rerr, errStopWalk)
		if rerr != nil && !full {
			s.writeError(w, r, rerr)
			return
		}

		stoppedAtLimit := false
		for i, ev := range page {
			examined++
			out.NextFrom = ev.Seq + 1
			if filters.matches(ev) {
				out.Items = append(out.Items, toAuditDTO(ev))
			}
			if len(out.Items) >= limit {
				reachedHead := !full && i == len(page)-1
				out.ScanComplete = reachedHead
				out.HasMore = !reachedHead
				stoppedAtLimit = true
				break
			}
		}
		if stoppedAtLimit {
			break
		}
		if !full {
			out.ScanComplete = true
			break
		}
		cursor = page[len(page)-1].Seq + 1
	}
	if !out.ScanComplete && examined >= scanCap {
		out.HasMore = true
	}

	meta := map[string]any{
		"filters": filters.meta(),
		"from":    from,
		"limit":   limit,
	}
	if err := s.st.Mutate(r.Context(), tenant, func(sc store.Scope) error {
		// Same ordering rule as the unfiltered path: the head is read BEFORE this
		// request's own audit.read joins the chain, so head_seq describes the ledger
		// the returned page was scanned from and an empty one still answers 0.
		head, herr := auditHeadSeq(r.Context(), sc)
		if herr != nil {
			return herr
		}
		out.HeadSeq = head
		return appendAuditWithMeta(r.Context(), sc, p, "audit.read", "core.audit_event", "", meta)
	}); err != nil {
		s.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAuditVerify verifies the chain structurally AND verifies every signed
// checkpoint's Ed25519 signature against the engine key. It does NOT self-audit
// (verification is an observer; auditing it would grow the chain it inspects).
func (s *Server) handleAuditVerify(w http.ResponseWriter, r *http.Request) {
	_, tenant, ok := s.authzTenant(w, r, "audit:read")
	if !ok {
		return
	}
	from := queryInt64(r, "from", 1)
	var structural store.VerifyReport
	var checks audit.CheckpointReport
	err := s.st.View(r.Context(), tenant, func(sc store.Scope) error {
		rep, err := sc.Audit().Verify(r.Context(), from)
		if err != nil {
			return err
		}
		structural = rep
		cr, err := audit.VerifyCheckpoints(r.Context(), sc.Audit(), s.signer.PublicKey())
		if err != nil {
			return err
		}
		checks = cr
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// A young chain has no checkpoints yet: "pending" is not a verification FAILURE
	// (structural verification already proves chain integrity), only a corrupt
	// checkpoint (bad sig/link) is. But an EMPTY chain must not report success —
	// Checked==0 verifies nothing, and calling that "ok" is the vacuous-truth
	// shape.
	//
	// `checkpoints.ok` stays the strict boolean it has always been (false until
	// something has actually been attested — flipping it to true for a virgin chain
	// would be the same lie in the other direction, and would hide a ledger whose
	// checkpoints really are gone). `checkpoints.status` carries the THIRD answer
	// the boolean cannot: a renderer keys off it so "not yet" is never painted as
	// "broken". Anything the audit layer cannot name lands on "failed".
	cpStatus := checks.Status()
	checkpointsTrustworthy := cpStatus != audit.CheckpointStatusFailed
	verified := structural.OK && structural.Checked > 0 && checkpointsTrustworthy
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": verified,
		"chain": map[string]any{
			"ok": structural.OK, "checked": structural.Checked,
			"break_at": structural.BreakAt, "reason": structural.Reason,
		},
		"checkpoints": map[string]any{
			"ok": checks.OK, "status": string(cpStatus), "count": checks.Checkpoints,
			"latest_attested_seq": checks.LatestAttestedSeq,
			"first_bad_seq":       checks.FirstBadSeq, "reason": checks.Reason,
		},
	})
}

// handleAuditExport streams the tenant's ledger in any format audit.Formats()
// lists — CEF, LEEF, RFC5424 syslog, the bare OTLP-logs projection, a complete
// OTLP/HTTP export request, or an OCSF v1.8.0 API Activity projection — carrying
// the chain-integrity fields so an external WORM/SIEM holds an independently
// verifiable copy. The export action is itself self-audited
// (best effort, after streaming).
func (s *Server) handleAuditExport(w http.ResponseWriter, r *http.Request) {
	p, tenant, ok := s.authzTenant(w, r, "audit:read")
	if !ok {
		return
	}
	format := audit.Format(r.URL.Query().Get("format"))
	if format == "" {
		format = audit.DefaultFormat()
	}
	if !audit.ValidFormat(format) {
		// Built from the engine's own registry: a list typed by hand here is how the
		// CLI ended up telling operators that two working formats did not exist.
		s.badRequest(w, r, "unknown export format (use "+audit.FormatList()+")")
		return
	}
	from := queryInt64(r, "from", 1)
	to, hasTo, terr := queryOptionalInt64(r, "to")
	if terr != nil {
		s.badRequest(w, r, terr.Error())
		return
	}
	if hasTo && to < from {
		s.badRequest(w, r, "to must be greater than or equal to from")
		return
	}
	filters, _, ferr := parseAuditFilters(r)
	if ferr != nil {
		s.badRequest(w, r, ferr.Error())
		return
	}
	ct := "text/plain; charset=utf-8"
	if jsonPerLineFormat(format) {
		// JSON-object-per-line formats stream as NDJSON.
		ct = "application/x-ndjson; charset=utf-8"
	}

	// Page through the ledger in bounded, SHORT read transactions, writing each
	// page to the client only AFTER its transaction closes — so a slow client
	// never holds the single SQLite connection open (a wedge DoS otherwise). The
	// chain is gap-free, so seq-keyset paging is exact.
	const pageSize = 1000
	cursor := from
	var total, lastSeq int64
	wrote := false
	for {
		var page []model.AuditEvent
		// Export, not View: this is the customer's copy of their own ledger, so it
		// survives a withdrawal of service — and is recorded on their chain when it is
		// taken during one (core/suspension).
		rerr := s.st.Export(r.Context(), tenant, func(sc store.ExportScope) error {
			return sc.Audit().Walk(r.Context(), cursor, func(ev model.AuditEvent) error {
				if hasTo && ev.Seq > to {
					return errStopAuditRange
				}
				if len(page) >= pageSize {
					return errStopWalk
				}
				page = append(page, ev)
				return nil
			})
		})
		full := errors.Is(rerr, errStopWalk)
		rangeComplete := errors.Is(rerr, errStopAuditRange)
		if rerr != nil && !full && !rangeComplete {
			if !wrote {
				s.writeError(w, r, rerr)
			} else {
				s.log.Error("api: audit export failed mid-stream", "err", rerr, "tenant", tenant)
			}
			return
		}
		if !wrote {
			w.Header().Set("Content-Type", ct)
			wrote = true
		}
		for _, ev := range page {
			if !filters.matches(ev) {
				continue
			}
			line, ferr := audit.FormatEvent(ev, format)
			if ferr != nil {
				s.log.Error("api: audit export format failed", "err", ferr)
				return
			}
			if _, werr := w.Write([]byte(line + "\n")); werr != nil {
				return // client gone
			}
			total, lastSeq = total+1, ev.Seq
		}
		if rangeComplete || !full {
			break
		}
		cursor = page[len(page)-1].Seq + 1
	}
	// A completion terminator makes a truncated export detectable: its ABSENCE
	// tells a SIEM the stream was cut short.
	_, _ = w.Write([]byte(exportTerminator(format, total, lastSeq) + "\n"))

	// Best-effort self-audit of the extraction (separate committed tx).
	meta := map[string]any{
		"format": string(format),
		"from":   from,
	}
	if hasTo {
		meta["to"] = to
	}
	if filterMeta := filters.meta(); len(filterMeta) > 0 {
		meta["filters"] = filterMeta
	}
	if err := s.st.Mutate(r.Context(), tenant, func(sc store.Scope) error {
		return appendAuditWithMeta(r.Context(), sc, p, "audit.export", "core.audit_event", "", meta)
	}); err != nil {
		s.log.Error("api: failed to record audit export", "err", err)
	}
}

// jsonPerLineFormat reports whether an export format emits one JSON object per
// line (NDJSON) rather than a text record. It decides both the Content-Type and
// the shape of the stream terminator, so the two can never disagree — a JSON
// stream whose last line is a `#` comment breaks precisely the consumers that
// parse every line.
func jsonPerLineFormat(f audit.Format) bool {
	switch f {
	case audit.FormatOTLP, audit.FormatOTLPEnvelope, audit.FormatOTLPLogRecord, audit.FormatOCSF:
		return true
	default:
		return false
	}
}

// exportTerminator is the final line of an audit export; its presence confirms
// the consumer received the complete stream.
func exportTerminator(f audit.Format, count, lastSeq int64) string {
	if jsonPerLineFormat(f) {
		return fmt.Sprintf(`{"export_complete":true,"count":%d,"last_seq":%d}`, count, lastSeq)
	}
	return fmt.Sprintf("# olivares-audit-export-complete count=%d last_seq=%d", count, lastSeq)
}

// handleAuditPubkey returns the engine's audit checkpoint verification key, so an
// external party can verify exported checkpoints offline.
func (s *Server) handleAuditPubkey(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.authzTenant(w, r, "audit:read"); !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"algorithm":  "ed25519",
		"public_key": base64.StdEncoding.EncodeToString(s.signer.PublicKey()),
	})
}

// queryInt64 reads an int64 query parameter with a default.
func queryInt64(r *http.Request, key string, def int64) int64 {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

// queryOptionalInt64 reads an optional int64 query parameter, rejecting a
// present but malformed value.
func queryOptionalInt64(r *http.Request, key string) (int64, bool, error) {
	values, ok := r.URL.Query()[key]
	if !ok {
		return 0, false, nil
	}
	if len(values) == 0 {
		return 0, true, fmt.Errorf("%s must be an integer", key)
	}
	n, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil {
		return 0, true, fmt.Errorf("%s must be an integer", key)
	}
	return n, true, nil
}

func parseAuditFilters(r *http.Request) (auditFilters, bool, error) {
	filters := auditFilters{values: map[string]string{}}
	query := r.URL.Query()
	for _, key := range []string{"since", "until", "actor", "action", "target_kind", "target_id", "q"} {
		values, ok := query[key]
		if !ok {
			continue
		}
		value := ""
		if len(values) > 0 {
			value = values[0]
		}
		// An EMPTY value means "filter cleared", never "match the empty string":
		// it must not flip the request onto the bounded-scan path (a ?actor=
		// from a cleared form field would otherwise scan 20k events to match
		// nothing an operator asked for).
		if strings.TrimSpace(value) == "" {
			continue
		}
		filters.values[key] = value
		switch key {
		case "since":
			parsed, err := parseAuditFilterTime(value)
			if err != nil {
				return auditFilters{}, false, errors.New("since must be RFC3339")
			}
			filters.since = &parsed
		case "until":
			parsed, err := parseAuditFilterTime(value)
			if err != nil {
				return auditFilters{}, false, errors.New("until must be RFC3339")
			}
			filters.until = &parsed
		case "q":
			filters.q = strings.ToLower(value)
		}
	}
	// exclude_action is read apart from the loop above because it is the only
	// REPEATABLE filter: every occurrence is kept, where the single-valued ones take
	// values[0] and drop the rest. Blank occurrences are skipped under the same rule
	// as the others — a cleared field means "no filter", never "exclude everything",
	// and an empty prefix would match every action and empty the ledger view.
	for _, value := range query["exclude_action"] {
		if strings.TrimSpace(value) == "" {
			continue
		}
		filters.excludeActions = append(filters.excludeActions, value)
	}
	if filters.since != nil && filters.until != nil && filters.until.Before(*filters.since) {
		return auditFilters{}, false, errors.New("until must not be before since")
	}
	return filters, len(filters.values) > 0 || len(filters.excludeActions) > 0, nil
}

func parseAuditFilterTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err == nil {
		return parsed, nil
	}
	return time.Parse(time.RFC3339, value)
}

func (f auditFilters) matches(ev model.AuditEvent) bool {
	if f.since != nil || f.until != nil {
		occurredAt, err := parseAuditFilterTime(ev.OccurredAt.String())
		if err != nil {
			return false
		}
		if f.since != nil && occurredAt.Before(*f.since) {
			return false
		}
		if f.until != nil && occurredAt.After(*f.until) {
			return false
		}
	}
	if actor, ok := f.values["actor"]; ok && ev.Actor != actor {
		return false
	}
	if action, ok := f.values["action"]; ok && !strings.HasPrefix(ev.Action, action) {
		return false
	}
	// Exclusion is a filter on the VIEW and nothing else: the event was still sealed
	// into the chain, this read still appends its own, and an unfiltered request still
	// returns every one of them. Dropping the append instead would have made the bell
	// quiet by destroying evidence, which is the one remedy that must never be the
	// cheap one.
	for _, excluded := range f.excludeActions {
		if strings.HasPrefix(ev.Action, excluded) {
			return false
		}
	}
	if targetKind, ok := f.values["target_kind"]; ok && string(ev.TargetKind) != targetKind {
		return false
	}
	targetID := idOrEmpty(ev.TargetID)
	if wantedTargetID, ok := f.values["target_id"]; ok && targetID != wantedTargetID {
		return false
	}
	if _, ok := f.values["q"]; ok {
		if !strings.Contains(strings.ToLower(ev.Action), f.q) &&
			!strings.Contains(strings.ToLower(ev.Actor), f.q) &&
			!strings.Contains(strings.ToLower(string(ev.TargetKind)), f.q) &&
			!strings.Contains(strings.ToLower(targetID), f.q) {
			return false
		}
	}
	return true
}

func (f auditFilters) meta() map[string]any {
	meta := make(map[string]any, len(f.values)+1)
	for key, value := range f.values {
		meta[key] = value
	}
	// The self-audit records what the reader ASKED FOR, so an exclusion belongs in it:
	// a page that came back short because the caller excluded a family, and one that
	// came back short because the ledger holds nothing else, are different facts.
	//
	// As a LIST, not a joined string. Nothing forbids a comma inside a prefix, so
	// joining makes three different requests — ["a,b","c"], ["a","b,c"] and ["a,b,c"] —
	// record the same "a,b,c", and the evidence stops being able to say which filter
	// the reader actually asked for. A copy, because the draft's Meta outlives this
	// call and must not alias the request's parsed filters.
	if len(f.excludeActions) > 0 {
		excluded := make([]string, len(f.excludeActions))
		copy(excluded, f.excludeActions)
		meta["exclude_action"] = excluded
	}
	return meta
}

func appendAuditWithMeta(
	ctx context.Context,
	sc store.Scope,
	p auth.Principal,
	action string,
	targetKind model.Kind,
	target model.ID,
	meta map[string]any,
) error {
	_, err := sc.Audit().Append(ctx, model.AuditDraft{
		Actor: p.Actor(), ActorKind: p.ActorKind(),
		Action: action, TargetKind: targetKind, TargetID: target, Meta: meta,
	})
	return err
}
