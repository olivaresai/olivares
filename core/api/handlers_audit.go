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
func (s *Server) handleAuditList(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	tenant := mc.Tenant
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
func (s *Server) handleSystemAuditList(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	p := mc.Principal
	s.auditListInto(w, r, p, model.SystemTenantID)
}

// auditListInto dispatches filtered reads to the bounded scanner. Its unfiltered
// path walks a tenant's ledger from ?from into a page and records the read in the
// same committed transaction (a View would roll the self-audit back). The tenant
// is supplied by the caller (the resolved business tenant for /v1/audit, or the
// system tenant for /v1/audit/system) — never re-derived from the request.
func (s *Server) auditListInto(w http.ResponseWriter, r *http.Request, p auth.Principal, tenant model.TenantID) {
	from := queryInt64(r, "from", 1)
	limit := int(queryInt64(r, "limit", stableListDefaultLimit))
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

// auditFilteredListInto answers the filtered audit list. When the store offers
// the filtered walk (store.FilteredWalker — the shipped store always does), the
// filter runs in SQL over the (tenant_id, seq) walk and only matching rows are
// decoded (CUTS A3; AU2-05 measured 63.7 ms of decode for a needle filter, the
// SQL model for the same answer is 4.5 ms). The envelope is unchanged:
// next_from is the last returned match + 1 while a page is full, and the chain
// head + 1 once the ledger is scanned to its end; a legacy AuditLog without the
// capability (a test fake) takes the pre-A3 walk.
func (s *Server) auditFilteredListInto(
	w http.ResponseWriter,
	r *http.Request,
	p auth.Principal,
	tenant model.TenantID,
	from int64,
	limit int,
	filters auditFilters,
) {
	if s.filteredAuditSQL(w, r, p, tenant, from, limit, filters) {
		return
	}
	s.auditFilteredListLegacy(w, r, p, tenant, from, limit, filters)
}

// errNoFilteredWalk marks an AuditLog without the filtered-walk capability.
var errNoFilteredWalk = errors.New("filtered walk unavailable")

// storeFilter renders the handler's filter set for the store walk. The rules
// are the handler's matches(), moved to SQL without changing them.
func (f auditFilters) storeFilter() store.AuditFilter {
	out := store.AuditFilter{
		Actor:                 f.values["actor"],
		ActionPrefix:          f.values["action"],
		ExcludeActionPrefixes: f.excludeActions,
		TargetKind:            f.values["target_kind"],
		TargetID:              f.values["target_id"],
		Since:                 f.since,
		Until:                 f.until,
		Q:                     f.q,
	}
	return out
}

// filteredAuditSQL is the A3 path. It reports false only when the store cannot
// filter (the legacy path then answers).
func (s *Server) filteredAuditSQL(
	w http.ResponseWriter,
	r *http.Request,
	p auth.Principal,
	tenant model.TenantID,
	from int64,
	limit int,
	filters auditFilters,
) bool {
	out := auditListResponse{Items: []AuditEventDTO{}}
	rerr := s.st.View(r.Context(), tenant, func(sc store.Scope) error {
		fw, ok := sc.Audit().(store.FilteredWalker)
		if !ok {
			return errNoFilteredWalk
		}
		var page []model.AuditEvent
		err := fw.WalkFiltered(r.Context(), from, filters.storeFilter(), func(ev model.AuditEvent) error {
			page = append(page, ev)
			if len(page) > limit {
				return errStopWalk
			}
			return nil
		})
		if err != nil && !errors.Is(err, errStopWalk) {
			return err
		}
		if len(page) > limit {
			// The lookahead row exists: a further page follows. next_from is one
			// past the last RETURNED match — the same value the legacy walk's
			// last-examined had, since a full page always ends on a match.
			page = page[:limit]
			out.HasMore = true
			out.NextFrom = page[len(page)-1].Seq + 1
		} else {
			out.ScanComplete = true
			// A completed scan examined to the head: next_from is one past it,
			// exactly the legacy walk's last-examined value.
			head, hasHead, err := sc.Audit().Head(r.Context())
			if err != nil {
				return err
			}
			if hasHead {
				out.NextFrom = head.Seq + 1
			}
		}
		for _, ev := range page {
			out.Items = append(out.Items, toAuditDTO(ev))
		}
		return nil
	})
	if errors.Is(rerr, errNoFilteredWalk) {
		return false
	}
	if rerr != nil {
		s.writeError(w, r, rerr)
		return true
	}
	meta := map[string]any{
		"filters": filters.meta(),
		"from":    from,
		"limit":   limit,
	}
	if err := s.st.Mutate(r.Context(), tenant, func(sc store.Scope) error {
		// Same ordering rule as the legacy path: the head is read BEFORE this
		// request's own audit.read joins the chain, so head_seq describes the
		// ledger the returned page was scanned from and an empty one still answers 0.
		head, herr := auditHeadSeq(r.Context(), sc)
		if herr != nil {
			return herr
		}
		out.HeadSeq = head
		return appendAuditWithMeta(r.Context(), sc, p, "audit.read", "core.audit_event", "", meta)
	}); err != nil {
		s.writeError(w, r, err)
		return true
	}
	writeJSON(w, http.StatusOK, out)
	return true
}

// auditFilteredListLegacy is the pre-A3 path for an AuditLog without
// store.FilteredWalker (a test fake): it scans in bounded, short read
// transactions so attribute filtering never holds the store's single SQLite
// connection for an unbounded write transaction. Continuation follows the last
// examined event, not the last sparse match.
func (s *Server) auditFilteredListLegacy(
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
func (s *Server) handleAuditVerify(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
	tenant := mc.Tenant
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

// handleAuditPubkey returns the engine's audit checkpoint verification key, so an
// external party can verify exported checkpoints offline.
func (s *Server) handleAuditPubkey(w http.ResponseWriter, r *http.Request, mc ModuleContext) {
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
