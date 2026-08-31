// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// governance.go implements the non-cost governance streams:
//
//   - Spend roll-up (POST /teams/spend): per-member current-cycle spend → a budget-posture
//     FindingReport when a member's overall spend approaches (>=80%) or exceeds (>=100%)
//     their per-user monthly limit. A member with no limit (monthlyLimitDollars null) is
//     skipped — there is nothing to breach.
//   - Audit Logs (GET /teams/audit-logs): the team audit feed → one minimal-data
//     external_activity evidence FindingReport per record (actor email / ip hashed).
//   - The honest "plan-gated/UNVERIFIED" posture finding emitted when a stream returns
//     403/404 (the Admin API docs disagree on whether the reads are Enterprise-gated).
package cursor

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Finding subjects for the Cursor governance posture/evidence findings.
const (
	subjectSurface = "cursor.surface"
	subjectMember  = "cursor.member"
	subjectBudget  = "cursor.budget"
	subjectAudit   = "cursor.audit_log"
)

// gatherSpend paginates the per-member spend roll-up and emits a budget-posture finding for
// each member near or over their monthly limit. On a plan-gated surface it degrades to a
// posture finding and returns nil.
func (s *Source) gatherSpend(ctx context.Context, sink sdk.Sink) error {
	for page := 1; page <= s.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp spendResponse
		if err := s.client.postJSON(ctx, spendPath, spendRequest{Page: page, PageSize: s.pageSize}, &resp); err != nil {
			if isUnavailable(err) {
				return sink.Emit(ctx, s.unavailableFinding("Spend", spendPath))
			}
			return err
		}
		for _, e := range resp.TeamMemberSpend {
			f, ok := s.budgetFinding(e)
			if !ok {
				continue
			}
			if err := sink.Emit(ctx, f); err != nil {
				return err
			}
		}
		if len(resp.TeamMemberSpend) == 0 || (resp.TotalPages > 0 && page >= resp.TotalPages) {
			return nil
		}
		if page == s.maxPages && resp.TotalPages > s.maxPages {
			// Known truncation (TotalPages reported beyond our bound): surface it.
			return sink.Emit(ctx, s.coverageFinding("Spend", spendPath))
		}
	}
	return nil
}

// budgetFinding records a budget-posture finding for one member whose overall cycle spend
// reaches budgetWarnRatio of their monthly limit. ok is false when the member has no limit
// (nothing to breach) or is comfortably under it. Severity is High at/over 100%, Medium at
// the warn threshold. The email is hashed; the SubjectRef is the stable member id.
func (s *Source) budgetFinding(e spendEntry) (model.FindingReport, bool) {
	if e.MonthlyLimitDollars == nil || *e.MonthlyLimitDollars <= 0 {
		return model.FindingReport{}, false // no cap configured → nothing to breach
	}
	limitCents := *e.MonthlyLimitDollars * 100
	ratio := e.OverallSpendCents / limitCents
	if ratio < budgetWarnRatio {
		return model.FindingReport{}, false
	}
	sev := model.SeverityMedium
	state := "approaching"
	if ratio >= 1.0 {
		sev = model.SeverityHigh
		state = "over"
	}
	detail := strings.Join([]string{
		e.UserID, e.Email, e.Role,
		strconv.FormatFloat(e.SpendCents, 'f', -1, 64),
		strconv.FormatFloat(e.OverallSpendCents, 'f', -1, 64),
		strconv.FormatFloat(*e.MonthlyLimitDollars, 'f', -1, 64),
	}, "|")
	return model.FindingReport{
		Kind:        "posture",
		Severity:    sev,
		SubjectKind: subjectBudget,
		SubjectRef:  firstNonEmpty(e.UserID, redact.Hash(e.Email)),
		Title:       fmt.Sprintf("Cursor member %s monthly spend limit (%.0f%% of $%.2f)", state, ratio*100, *e.MonthlyLimitDollars),
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}, true
}

// gatherAudit paginates the team audit logs and emits one external_activity evidence
// finding per record. On a plan-gated surface it degrades to a posture finding and returns
// nil. The actor email and ip are folded into the one-way DetailHash, never surfaced.
func (s *Source) gatherAudit(ctx context.Context, sink sdk.Sink) error {
	for page := 1; page <= s.maxPages; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		q := url.Values{}
		s.auditWindow(q)
		q.Set("page", strconv.Itoa(page))
		q.Set("pageSize", strconv.Itoa(s.pageSize))
		var resp auditResponse
		if err := s.client.getJSON(ctx, auditPath, q, &resp); err != nil {
			if isUnavailable(err) {
				return sink.Emit(ctx, s.unavailableFinding("Audit Logs", auditPath))
			}
			return err
		}
		for _, ev := range resp.Events {
			if ev.EventID == "" {
				continue
			}
			if err := sink.Emit(ctx, s.auditFinding(ev)); err != nil {
				return err
			}
		}
		if len(resp.Events) == 0 || !resp.Pagination.HasNextPage {
			return nil
		}
	}
	// Truncated at max_pages with audit pages still pending: surface the coverage gap.
	return sink.Emit(ctx, s.coverageFinding("Audit Logs", auditPath))
}

// auditFinding maps one audit record to a minimal-data evidence finding. The SubjectRef is
// the non-sensitive event id; the Title is the event type; the actor email/ip and the
// event payload are folded into the one-way DetailHash and NEVER surfaced (docs/SECURITY-HARDENING.md).
func (s *Source) auditFinding(e auditEvent) model.FindingReport {
	detail := strings.Join([]string{
		e.EventID, e.EventType, e.UserEmail, e.IPAddress, e.Timestamp, string(e.EventData),
	}, "|")
	occurred := millisTime(e.Timestamp)
	if occurred.IsZero() {
		occurred = s.clock().UTC()
	}
	return model.FindingReport{
		Kind:        findingKindActivity,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectAudit,
		SubjectRef:  e.EventID,
		Title:       "Cursor audit: " + firstNonEmpty(e.EventType, "event"),
		DetailHash:  redact.Hash(detail),
		OccurredAt:  occurred,
	}
}

// coverageFinding is the honest "partial coverage" health finding emitted when a paginated
// stream is truncated at max_pages with more data still pending — so an under-counted spend
// roll-up or a gap in audit evidence is a VISIBLE signal, never a silent cap (the same
// no-silent-caps invariant the sibling gcp-audit/azure-activity observers honor). Low
// severity: the data emitted is correct, only incomplete.
func (s *Source) coverageFinding(surface, path string) model.FindingReport {
	return model.FindingReport{
		Kind:        "health",
		Severity:    model.SeverityLow,
		SubjectKind: subjectSurface,
		SubjectRef:  surface,
		Title:       "Cursor " + surface + " partial: stopped at max_pages — raise max_pages or narrow lookback for full coverage",
		DetailHash:  redact.Hash("cursor surface=" + surface + " path=" + path + " truncated at max_pages with more pages pending"),
		OccurredAt:  s.clock().UTC(),
	}
}

// unavailableFinding is the honest "stream unavailable" posture finding for a plan-gated /
// UNVERIFIED Cursor Admin API surface. It records WHICH stream and the path tried, so an
// operator can obtain entitlement — never a fabricated empty inventory. Medium severity:
// the Admin API docs disagree on whether reads are Enterprise-gated, so a 403/404 is a
// coverage gap to surface, not a hard failure.
func (s *Source) unavailableFinding(surface, path string) model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectSurface,
		SubjectRef:  surface,
		Title:       "Cursor " + surface + " unavailable (plan-gated/UNVERIFIED for this team)",
		DetailHash:  redact.Hash("cursor surface=" + surface + " path=" + path + " returned 403/404; the Admin API docs disagree on whether the read endpoints are Enterprise-gated, so coverage could not be verified for this team key"),
		OccurredAt:  s.clock().UTC(),
	}
}

// lowered lowercases an email for the member-map lookup.
func lowered(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// millisString renders a Unix-millis int as a base-10 string for a query param.
func millisString(ms int64) string { return strconv.FormatInt(ms, 10) }
