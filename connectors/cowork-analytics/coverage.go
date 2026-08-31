// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package coworkanalytics

import (
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk/model"
)

// coverage.go documents — honestly, as the product's posture demands — what the
// Cowork engagement ingest does and does NOT cover, so a buyer is never sold a
// completeness the API or the sealed contract does not deliver.

// AnalyticsGap is one HONESTLY-documented coverage limit of the engagement ingest.
type AnalyticsGap struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Owner  string `json:"owner"`
}

// analyticsGaps is the verified gap inventory (AsOf 2026-06-10).
var analyticsGaps = []AnalyticsGap{
	{
		ID:     "enterprise-gated",
		Title:  "Analytics API is Team/Enterprise-gated",
		Detail: "the /v1/organizations/analytics/* endpoints require a Team/Enterprise plan and a read:analytics admin key; without it this connector degrades honestly (offline → no engagement emitted, never fabricated).",
		Owner:  "Anthropic plan gating (documented, not closable here)",
	},
	{
		ID:     "freshness-lag",
		Title:  "Engagement data lags real usage",
		Detail: "analytics are typically available within ~4h of usage (up to 24h); each response carries data_refreshed_at — usage after that watermark is not yet reflected. The engagement finding carries the day queried; for near-real-time activity use the OTEL connector.",
		Owner:  "Anthropic export cadence (documented limitation)",
	},
	{
		ID:     "counts-not-metric-observations",
		Title:  "Activity counts ride findings, not metric observations",
		Detail: "the sealed observation set is Edge/Cost/Finding (no numeric metric). Org-wide Cowork DAU/WAU/MAU, aggregate per-user engagement, and the per-skill/per-connector Cowork breakdowns (distinct-session use counts, top names) ride engagement FindingReports; per-session detail is available from the OTEL connector's edges.",
		Owner:  "(sealed-contract honesty; a metric observation type is a future SDK change)",
	},
	{
		ID:     "cost-via-otel-not-here",
		Title:  "Cowork cost is ingested via OTEL, not this connector",
		Detail: "the analytics user_cost_report row shape IS now field-level published (REF, verified 2026-06-10: user_actor rows; amount as a decimal string in fractional cents; products[] filter includes cowork), so the ingest is no longer blocked on unverified field names. It remains DELIBERATELY excluded: per-request Cowork cost already flows through the OTEL api_request path (cost_usd), and ingesting the billed report too would double-count spend until estimated-vs-billed provenance is reconciled.",
		Owner:  "FinOps reconciliation follow-up (estimated OTEL cost vs billed user_cost_report provenance)",
	},
}

// AnalyticsGaps returns the documented coverage gaps (a copy, so a caller cannot
// mutate package state). It is the honest companion to the engagement evidence.
func AnalyticsGaps() []AnalyticsGap {
	return append([]AnalyticsGap(nil), analyticsGaps...)
}

// coverageFinding emits a single posture finding per online Gather that records the
// documented coverage gaps to the ledger (Info — documentation, not an alert).
func (s *Source) coverageFinding(at time.Time) model.FindingReport {
	ids := make([]string, 0, len(analyticsGaps))
	for _, g := range analyticsGaps {
		ids = append(ids, g.ID)
	}
	return model.FindingReport{
		Kind:        findingKindCoverage,
		Severity:    model.SeverityInfo,
		SubjectKind: "cowork_analytics",
		SubjectRef:  s.orgRef,
		Title:       "Cowork analytics ingest covers org engagement (DAU/WAU/MAU + per-user activity + per-skill/per-connector Cowork breakdowns); " + strconv.Itoa(len(ids)) + " known limits documented (Enterprise-gated/freshness/counts-as-finding/cost-via-OTEL)",
		DetailHash:  redact.Hash(s.orgRef + "|gaps|" + strings.Join(ids, ",")),
		OccurredAt:  at,
	}
}
