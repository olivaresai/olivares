// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// audit.go implements xAI audit event ingestion from the Management API. The audit
// endpoint exposes admin actions (key lifecycle, team membership changes, settings
// changes) as a paginated event stream. The live audit shape and pagination were
// re-verified against docs.x.ai on 2026-07-04.
//
//   - AUDIT EVENTS (GET /audit/teams/{teamId}/events): admin action stream (key
//     lifecycle, team membership, settings changes) → one minimal-data
//     external_activity evidence finding per event, hashing actor PII.
//     VERIFIED-SHAPE (currency re-verified 2026-07-04). There is no eventType
//     field; semantics live in description. eventFilter.* and orderBy are documented,
//     but this connector deliberately pulls the full window as read-only evidence.
//
// Each event is emitted as a model.FindingReport with Kind="external_activity" and
// SubjectKind="xai.audit_event". Actor email and identity are folded into the one-way
// DetailHash (never surfaced in the title or ref). On a 403/404 the endpoint is
// honestly degraded to a posture finding rather than failing the gather.
package xai

import (
	"context"
	"net/url"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// subjectAuditEvent is the finding subject for audit event evidence.
const subjectAuditEvent = "xai.audit_event"

// gatherAuditEvents reads the xAI audit event stream for the team and emits one
// external_activity evidence finding per event. Actor email is hashed into DetailHash,
// never surfaced. On a 403/404 it degrades to an honest posture finding.
func (s *Source) gatherAuditEvents(ctx context.Context, sink sdk.Sink, team string) error {
	cursor := ""
	cursorMode := ""
	path := "/audit/teams/" + url.PathEscape(team) + "/events"
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		q := url.Values{"pageSize": {"100"}}
		if cursor != "" {
			q.Set("cursor", cursor)
			switch cursorMode {
			case "live":
				q.Set("pageToken", cursor)
			case "pagination":
				q.Set("paginationToken", cursor)
			}
		}
		var resp auditEventsResponse
		if err := s.mgmtClient.GetJSON(ctx, path, q, &resp); err != nil {
			if isUnavailable(err) {
				return sink.Emit(ctx, s.auditUnavailableFinding(team))
			}
			return err
		}
		for _, ev := range resp.Events {
			eventID := ev.id()
			if eventID == "" {
				continue
			}
			description := ev.description()
			eventTime := ev.time()
			if err := sink.Emit(ctx, model.FindingReport{
				Kind:        "external_activity",
				Severity:    model.SeverityInfo,
				SubjectKind: subjectAuditEvent,
				SubjectRef:  eventID,
				Title:       "xAI audit: " + description,
				DetailHash:  redact.Hash(strings.Join(ev.detailParts(eventID, eventTime, description), "|")),
				OccurredAt:  parseTime(eventTime),
			}); err != nil {
				return err
			}
		}
		next, mode := resp.nextCursor()
		if next == "" {
			break
		}
		cursor = next
		cursorMode = mode
	}
	return nil
}

func (r auditEventsResponse) nextCursor() (string, string) {
	if r.NextPageToken != "" {
		return r.NextPageToken, "live"
	}
	if r.PaginationToken != "" {
		return r.PaginationToken, "pagination"
	}
	if r.HasMore && r.Cursor != "" {
		return r.Cursor, "cursor"
	}
	return "", ""
}

func (e auditEvent) id() string {
	return firstNonEmpty(e.EventID, e.ID)
}

func (e auditEvent) time() string {
	return firstNonEmpty(e.EventTime, e.Time, e.Timestamp)
}

func (e auditEvent) description() string {
	return firstNonEmpty(e.Description, e.Message, e.Action, "event")
}

func (e auditEvent) detailParts(eventID, eventTime, description string) []string {
	parts := []string{eventID, eventTime, description}
	parts = append(parts, e.User.detailParts("user")...)
	parts = append(parts, e.Actor.detailParts("actor")...)
	return parts
}

func (u auditUser) detailParts(prefix string) []string {
	return []string{
		prefix + ".id=" + firstNonEmpty(u.UserID, u.ID),
		prefix + ".email=" + u.Email,
		prefix + ".given_name=" + u.GivenName,
		prefix + ".family_name=" + u.FamilyName,
		prefix + ".name=" + u.Name,
		prefix + ".profile_image=" + u.ProfileImage,
		prefix + ".profile_image_url=" + u.ProfileImageURL,
	}
}

// auditUnavailableFinding is the honest degrade when the audit event endpoint returns
// 403/404 (the management key is not entitled, or the team does not have audit enabled).
func (s *Source) auditUnavailableFinding(team string) model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectAuditEvent,
		SubjectRef:  "xai",
		Title:       "xAI Audit Events ingest unavailable (management key not entitled / wrong team)",
		DetailHash:  redact.Hash("xai audit events for team=" + team + " base=" + s.managementBaseURL + " returned 403/404; the management key may lack scope or audit is not enabled for this team"),
		OccurredAt:  s.clock().UTC(),
	}
}
