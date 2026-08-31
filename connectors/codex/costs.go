// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// costs.go implements the two VERIFIED-SHAPE OpenAI org governance APIs the Codex
// "Admin/Audit/Costs" surface rides on (G4):
//
//   - Audit Logs API (/v1/organization/audit_logs): org audit events (key created,
//     login, project changes) → one minimal-data external_activity evidence finding
//     per record, hashing the actor's user email / ip, never surfacing them.
//   - Costs API (/v1/organization/costs): the BILLED, authoritative daily cost →
//     CostSamples (provenance=billed, CostType="codex"). Opt-in (costs=true): it is
//     org-wide unless project_id scopes it to the Codex project, so it is off by
//     default to avoid silently counting non-Codex spend (the honest default).
//
// Both follow the documented OpenAI org-API conventions (bucketed pages for costs; an
// object:"list" + last_id cursor for audit logs), so they are exercised against real
// recorded shapes in the tests.
package codex

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// gatherAudit paginates the org Audit Logs API and emits one external_activity
// evidence finding per record. Cursor pagination via after/last_id. The actor's user
// email and ip_address are folded into the one-way DetailHash, never surfaced.
func (s *Source) gatherAudit(ctx context.Context, sink sdk.Sink) error {
	after := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp auditLogsResponse
		q := url.Values{"limit": {"100"}}
		// The Audit Logs API filters by effective_at; pull the lookback window.
		q.Set("effective_at[gte]", strconv.FormatInt(s.startTime().Unix(), 10))
		if after != "" {
			q.Set("after", after)
		}
		if err := s.client.GetJSON(ctx, auditLogsPath, q, &resp); err != nil {
			if isUnavailable(err) {
				return sink.Emit(ctx, s.unavailableFinding("Audit Logs API", auditLogsPath))
			}
			return err
		}
		for _, e := range resp.Data {
			if e.ID == "" {
				continue
			}
			if err := sink.Emit(ctx, s.auditFinding(e)); err != nil {
				return err
			}
		}
		if !resp.HasMore || resp.LastID == "" {
			return nil
		}
		after = resp.LastID
	}
	return nil
}

// auditFinding maps one org audit-log record to a minimal-data evidence finding. The
// SubjectRef is the audit-log id; the Title is the event type; the actor's id/email/ip
// and the project are folded into the one-way DetailHash, never surfaced (docs/SECURITY-HARDENING.md).
func (s *Source) auditFinding(e auditLogEntry) model.FindingReport {
	detail := strings.Join([]string{
		e.ID, e.Type, e.Actor.Type,
		e.Actor.Session.User.ID, e.Actor.Session.User.Email, e.Actor.Session.IPAddress,
		e.Actor.APIKey.ID, e.Project.ID, e.Project.Name,
	}, "|")
	occurred := unixTime(e.EffectiveAt)
	if occurred.IsZero() {
		occurred = s.clock().UTC()
	}
	return model.FindingReport{
		Kind:        findingKindActivity,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectAudit,
		SubjectRef:  e.ID,
		Title:       "Codex audit: " + firstNonEmpty(e.Type, "event"),
		DetailHash:  redact.Hash(detail),
		OccurredAt:  occurred,
	}
}

// gatherCosts pulls the billed Costs API across the lookback window and emits one
// authoritative (billed) CostSample per result row. The amount is the provider's billed
// money; there are no token counts (it is money, not usage). It is scoped to the Codex
// project when project_id is set; otherwise the spend is org-wide (the caveat the
// costs=false default protects against). CostType="codex" attributes it as Codex spend.
func (s *Source) gatherCosts(ctx context.Context, sink sdk.Sink) error {
	page := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp costsResponse
		q := url.Values{}
		s.setStart(q)
		q.Set("bucket_width", "1d") // Costs API supports daily granularity only
		q.Add("group_by[]", "line_item")
		q.Add("group_by[]", "project_id")
		q.Set("limit", "100")
		if s.projectID != "" {
			q.Set("project_id", s.projectID)
		}
		if page != "" {
			q.Set("page", page)
		}
		if err := s.client.GetJSON(ctx, costsPath, q, &resp); err != nil {
			if isUnavailable(err) {
				return sink.Emit(ctx, s.unavailableFinding("Costs API", costsPath))
			}
			return err
		}
		for _, bucket := range resp.Data {
			occurred := unixTime(bucket.StartTime)
			for _, r := range bucket.Results {
				if err := sink.Emit(ctx, costsSample(r, occurred)); err != nil {
					return err
				}
			}
		}
		if !resp.HasMore || resp.NextPage == "" {
			return nil
		}
		page = resp.NextPage
	}
	return nil
}

// costsSample turns one billed Costs row into an authoritative CostSample. The token
// fields are zero (Costs is money, not usage); the value is the billed amount itself,
// converted dollars→micro-USD. line_item populates CostType (falling back to "codex").
func costsSample(r costsResult, occurred time.Time) model.CostSample {
	u := modelprovider.Usage{
		ProviderRef:  modelprovider.ProviderOpenAICodex,
		WorkspaceRef: r.ProjectID,
		OccurredAt:   occurred,
		Gateway:      model.GatewayDirect,
		Provenance:   model.ProvenanceBilled,
		CostType:     firstNonEmpty(r.LineItem, costTypeCodex),
	}
	return modelprovider.ToCostSampleWithCost(u, dollarsToMicroUSD(r.Amount.Value))
}
