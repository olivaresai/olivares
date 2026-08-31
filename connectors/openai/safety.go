// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// This file adds the read-only AI-SAFETY POSTURE surface for OpenAI. It is
// the honest, minimal-data answer to a question a regulated buyer must evidence:
// "is content moderation applied to this org's OpenAI traffic?".
//
// The honesty boundary is the whole point. OpenAI exposes NO org-level, API-readable
// moderation/safety POLICY object — moderation is a stateless, free, OPT-IN per-call
// endpoint (POST /v1/moderations), and OpenAI's own platform-side abuse enforcement
// is not configurable or readable. So there is nothing to "inventory" as config, and
// fabricating an empty policy list would be a lie (docs/SECURITY-HARDENING.md; the same discipline
// claude-api/governance.go applies to the non-existent spend-limit-set endpoint).
//
// The ONE API-derived posture signal is USAGE: the Usage Admin API has a moderations
// bucket (GET /v1/organization/usage/moderations) reporting num_model_requests, which
// reveals whether the org actually CALLS moderation. (Cost cannot: moderation is free,
// so it never appears on the costs report.) We read that, and emit a single posture
// finding — moderation observed (Info) or not observed (Low) — carrying the honest
// caveat that absence is not "unsafe" (platform-level safety still applies) and that
// no readable policy surface exists. Minimal-data: counts and references only, never
// the inspected text or scores of any individual moderation call.
//
// Verified 2026-07-04: OpenAI's Safety Usage Dashboard visibility for blocked
// Responses requests grouped by safety_identifier is dashboard-only. safety_identifier
// is not an org usage-report group_by dimension and there is no blocked-requests API,
// so this connector emits a coverage-honesty posture finding instead of speculative
// ingest.
package openai

import (
	"context"
	"net/url"
	"strconv"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// safetyPostureKind is the FindingReport.Kind every provider safety-posture finding
// carries. The modules/security ingest persists it (any severity, deduped) and
// the GET /safety-posture view aggregates on it. It is namespaced apart from the
// cost/inventory observations so a consumer can query the safety surface alone. The
// value agrees by VALUE with the other provider connectors (aws, azure-activity) and
// modules/security findingKindSafetyPosture across the license boundary (no shared
// import — a connector never links the AGPL engine, LICENSING.md).
const safetyPostureKind = "safety_posture"

// subjectModeration is the SubjectKind of the OpenAI moderation posture finding.
const subjectModeration = "openai.moderation"

// subjectSafetyDashboard is the SubjectKind of the dashboard-only safety coverage
// caveat finding.
const subjectSafetyDashboard = "openai.safety_usage_dashboard"

// usageModerationsPath is the Usage Admin API moderations bucket — the only org-scoped
// signal that the org uses the (opt-in, free, stateless) Moderations API.
const usageModerationsPath = "/v1/organization/usage/moderations"

// gatherSafetyPosture emits the read-only OpenAI moderation posture finding. It reads
// the moderations USAGE buckets over the lookback window (the only API-derived signal,
// since OpenAI has no readable moderation policy and moderation is free so cost is
// silent) and emits exactly one posture finding: moderation observed (Info) or not
// observed (Low). It runs only with a credential (the caller gates on s.apiKey); the
// usage endpoint needs the same admin/org key the cost pulls already use.
func (s *Source) gatherSafetyPosture(ctx context.Context, sink sdk.Sink) error {
	// The Moderations USAGE bucket is an OpenAI-platform org endpoint. In azure-openai
	// mode the base URL is an Azure endpoint with no such endpoint, and Azure's safety
	// surface is Responsible-AI content filtering (read by the azure connector),
	// not OpenAI Moderation — so this posture does not apply. Skip honestly rather than
	// 404 a non-existent endpoint.
	if s.providerRef == modelprovider.ProviderAzureOpenAI {
		return nil
	}
	if err := sink.Emit(ctx, s.safetyDashboardCoverageFinding()); err != nil {
		return err
	}
	requests, err := s.moderationRequestCount(ctx)
	if err != nil {
		// Honest degradation (docs/SECURITY-HARDENING.md — "a gap is a signal, not silence"): a credentialed
		// read that fails (a key lacking usage scope, a transient 5xx) emits an explicit
		// "unreadable" posture finding and does NOT fail the whole Gather, so it neither
		// fabricates a green nor poisons the already-emitted cost samples — matching the
		// AWS Bedrock / Azure RAI passes. (The offline no-credential case never reaches
		// here: Gather returns before calling this.)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return sink.Emit(ctx, s.moderationUnreadableFinding())
	}
	return sink.Emit(ctx, s.moderationPostureFinding(requests))
}

// safetyDashboardCoverageFinding records the 2026-07-04 verified coverage ceiling:
// safety_identifier blocked-request visibility is dashboard-only, with no org-level
// API this connector can ingest.
func (s *Source) safetyDashboardCoverageFinding() model.FindingReport {
	return model.FindingReport{
		Kind:        safetyPostureKind,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectSafetyDashboard,
		SubjectRef:  "organization",
		Title:       "OpenAI safety_identifier blocking visibility is dashboard-only; no org-level API for Olivares ingest",
		DetailHash:  redact.Hash("openai safety_identifier blocked_requests visibility=dashboard_only verified=2026-07-04 no_org_api=true no_usage_group_by=true"),
		OccurredAt:  s.clock().UTC(),
	}
}

// moderationUnreadableFinding records that the moderation usage signal could not be
// read, so the operator sees the gap rather than an absent surface. The detail is
// hashed and carries no error text (which could embed a token).
func (s *Source) moderationUnreadableFinding() model.FindingReport {
	return model.FindingReport{
		Kind:        safetyPostureKind,
		Severity:    model.SeverityMedium,
		SubjectKind: subjectModeration,
		SubjectRef:  "organization",
		Title:       "OpenAI moderation usage unreadable (permission or availability) — posture not asserted",
		DetailHash:  redact.Hash("openai.moderation unreadable; usage signal not retrievable (key scope/availability); posture not asserted"),
		OccurredAt:  s.clock().UTC(),
	}
}

// moderationRequestCount sums num_model_requests across the moderations usage buckets
// for the lookback window, following page cursors up to the pagination bound.
func (s *Source) moderationRequestCount(ctx context.Context) (int64, error) {
	start := s.clock().Add(-s.lookback).UTC()
	var total int64
	page := ""
	for i := 0; i < s.maxPages; i++ {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		var resp usageModerationsResponse
		q := url.Values{}
		q.Set("start_time", strconv.FormatInt(start.Unix(), 10))
		q.Set("bucket_width", s.bucketWidth)
		q.Set("limit", strconv.Itoa(usageLimitFor(s.bucketWidth)))
		if page != "" {
			q.Set("page", page)
		}
		if err := s.client.GetJSON(ctx, usageModerationsPath, q, &resp); err != nil {
			return 0, err
		}
		for _, bucket := range resp.Data {
			for _, r := range bucket.Results {
				total += r.NumModelRequests
			}
		}
		if !resp.HasMore || resp.NextPage == "" {
			break
		}
		page = resp.NextPage
	}
	return total, nil
}

// moderationPostureFinding builds the posture finding from the observed moderation
// request count. The DetailHash is over a STATE-DETERMINISTIC fingerprint (present vs
// absent — never the fluctuating count, never a timestamp) so re-pulls of the same
// posture dedup in modules/security, while a real change (the org starts/stops
// moderating) produces a fresh finding. Severity is Low for "not observed" — a
// governance gap worth surfacing, NOT a definitive misconfiguration: OpenAI applies
// non-configurable platform safety regardless, so absence is "no application-level
// moderation evidence", not "unsafe" (bounded false-positives, docs/SECURITY-HARDENING.md).
func (s *Source) moderationPostureFinding(requests int64) model.FindingReport {
	at := s.clock().UTC()
	state := "absent"
	sev := model.SeverityLow
	title := "No OpenAI Moderation API usage observed over the lookback window"
	if requests > 0 {
		state = "present"
		sev = model.SeverityInfo
		title = "OpenAI Moderation API in use (application-level content moderation observed)"
	}
	detail := "openai.moderation usage=" + state +
		"; OpenAI exposes no API-readable moderation/safety policy object (moderation is a stateless, opt-in per-call /v1/moderations endpoint); " +
		"platform-level safety enforcement is applied by OpenAI but is not configurable or API-readable; absence of usage is no application-level moderation evidence, not 'unsafe'"
	return model.FindingReport{
		Kind:        safetyPostureKind,
		Severity:    sev,
		SubjectKind: subjectModeration,
		SubjectRef:  "organization",
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  at,
	}
}
