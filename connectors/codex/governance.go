// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// governance.go implements the two Codex ENTERPRISE governance surfaces (G4) verified 2026-07-04:
//
//   - Analytics API: per-workspace Codex usage (90d max lookback). Each row yields
//     a Codex CostSample (estimated, CostType="codex", attributed to the developer +
//     workspace) and, when there is adoption activity, an inventory finding.
//   - Compliance Logs Platform: per-workspace CODEX_LOG/CODEX_SECURITY_LOG files
//     (up to 30d retention). Each record yields a minimal-data external_activity
//     evidence finding, hashing actor PII (email/ip), never surfacing it.
//
// HONEST DEGRADATION: a 403/404 (not entitled / unavailable on this workspace) emits an
// "ingest unavailable" posture finding and returns nil, so gather continues with the
// org APIs rather than hard-failing. Row-level JSON fields are UNVERIFIED-FIELDS
// (2026-07-04: full reference behind ChatGPT admin portal; endpoint/params/envelope
// verified), so parsing is deliberately tolerant.
package codex

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/internal/textscan"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Finding subjects for the Codex governance posture/evidence findings.
const (
	subjectSurface           = "codex.surface"
	subjectAdoption          = "codex.adoption"
	subjectCompliance        = "codex.compliance"
	subjectComplianceContent = "codex.compliance_content"
	subjectAudit             = "codex.audit_log"
)

// gatherAnalytics pulls the Codex Analytics API across the lookback window and emits a
// Codex CostSample per usage row plus an adoption finding per active row. On an
// unavailable (sales-gated/UNVERIFIED) surface it degrades to a posture finding.
func (s *Source) gatherAnalytics(ctx context.Context, sink sdk.Sink) error {
	page := ""
	path := s.analyticsPathResolved()
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp analyticsResponse
		q := url.Values{}
		q.Set("start_time", strconv.FormatInt(s.analyticsStartTime().Unix(), 10))
		q.Set("end_time", strconv.FormatInt(s.clock().UTC().Unix(), 10))
		q.Set("group_by", "day")
		q.Set("limit", "100")
		if page != "" {
			q.Set("page", page)
		}
		if err := s.analyticsClient.GetJSON(ctx, path, q, &resp); err != nil {
			if isUnavailable(err) {
				return sink.Emit(ctx, s.unavailableFinding("Analytics API", path))
			}
			return err
		}
		for _, bucket := range resp.Data {
			occurred := unixTime(bucket.StartTime)
			for _, r := range bucket.Results {
				if cs, ok := s.analyticsCostSample(r, occurred); ok {
					if err := sink.Emit(ctx, cs); err != nil {
						return err
					}
				}
				if f, ok := s.adoptionFinding(r, occurred); ok {
					if err := sink.Emit(ctx, f); err != nil {
						return err
					}
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

// analyticsCostSample turns one Analytics row into a Codex-attributed CostSample. The
// monetary amount is the provider's OWN estimated_cost (an authoritative estimate, so
// provenance=estimated and the cost is NOT re-derived from tokens — there is no token
// price ambiguity); the developer (user_id, or email when attribute_email is set) and
// workspace are carried for per-developer/per-team chargeback, and CostType="codex"
// keeps Codex spend distinct from raw OpenAI API spend. ok is false for a row with no
// cost and no tokens (nothing to attribute).
func (s *Source) analyticsCostSample(r analyticsResult, occurred time.Time) (model.CostSample, bool) {
	micro := int64(0)
	if r.EstimatedCost != nil {
		micro = dollarsToMicroUSD(r.EstimatedCost.Value)
	}
	if micro == 0 && r.InputTokens == 0 && r.OutputTokens == 0 && r.CachedInputTokens == 0 {
		return model.CostSample{}, false
	}
	actor := r.UserID
	if s.attributeEmail && r.UserEmail != "" {
		actor = r.UserEmail
	}
	u := modelprovider.Usage{
		ProviderRef:     modelprovider.ProviderOpenAICodex,
		ModelRef:        r.Model,
		InputTokens:     r.InputTokens,
		OutputTokens:    r.OutputTokens,
		CacheReadTokens: r.CachedInputTokens,
		OccurredAt:      occurred,
		WorkspaceRef:    r.WorkspaceID,
		Actor:           actor,
		Gateway:         model.GatewayDirect,
		Provenance:      model.ProvenanceEstimated,
		CostType:        costTypeCodex,
	}
	return modelprovider.ToCostSampleWithCost(u, micro), true
}

// adoptionFinding records Codex adoption for one user/workspace/period as an inventory
// finding (code reviews, suggestion acceptance, lines accepted). ok is false when the
// row shows no adoption activity (so the ledger is not flooded with empty rows). The
// metrics are non-sensitive counts; user PII is folded into the hash, never the title.
func (s *Source) adoptionFinding(r analyticsResult, occurred time.Time) (model.FindingReport, bool) {
	if r.CodeReviews == 0 && r.SuggestionsAccepted == 0 && r.LinesAccepted == 0 && r.ActiveUsers == 0 && r.Threads == 0 && r.Turns == 0 {
		return model.FindingReport{}, false
	}
	if occurred.IsZero() {
		occurred = s.clock().UTC()
	}
	subj := firstNonEmpty(r.WorkspaceID, r.UserID, "codex")
	detail := strings.Join([]string{
		r.UserID, r.UserEmail, r.WorkspaceID, r.Model,
		strconv.FormatInt(r.CodeReviews, 10), strconv.FormatInt(r.SuggestionsShown, 10),
		strconv.FormatInt(r.SuggestionsAccepted, 10), strconv.FormatInt(r.LinesAccepted, 10),
		strconv.FormatInt(r.Threads, 10), strconv.FormatInt(r.Turns, 10),
	}, "|")
	return model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectAdoption,
		SubjectRef:  subj,
		Title:       fmt.Sprintf("Codex adoption: %d code review(s), %d suggestion(s) accepted, %d line(s) accepted", r.CodeReviews, r.SuggestionsAccepted, r.LinesAccepted),
		DetailHash:  redact.Hash(detail),
		OccurredAt:  occurred,
	}, true
}

// gatherCompliance pulls Compliance log-file lists by event_type, downloads each JSONL
// file, and emits one external_activity evidence finding per record. Downloads are capped
// by max_pages per Gather to bound cost. Optional compliance_prompt_scan applies the
// DLP posture at the connector boundary using Apache-side primitives
// only: prompt text is read transiently from the raw JSONL line and never stored.
func (s *Source) gatherCompliance(ctx context.Context, sink sdk.Sink) error {
	path := s.compliancePathResolved()
	downloaded := 0
	for _, logType := range s.complianceLogTypes {
		if err := ctx.Err(); err != nil {
			return err
		}
		q := url.Values{}
		q.Set("event_type", logType)
		q.Set("after", s.startTime().Format(time.RFC3339))
		var listed complianceLogFilesResponse
		if err := s.complianceClient.GetJSON(ctx, path, q, &listed); err != nil {
			if isUnavailable(err) {
				return sink.Emit(ctx, s.unavailableFinding("Compliance Logs Platform", path))
			}
			return err
		}
		for _, file := range listed.Files {
			if downloaded >= s.maxPages {
				return nil
			}
			if strings.TrimSpace(file.ID) == "" {
				continue
			}
			body, err := s.complianceClient.GetText(ctx, complianceDownloadPath(path, file.ID), nil)
			if err != nil {
				if isUnavailable(err) {
					return sink.Emit(ctx, s.unavailableFinding("Compliance Logs Platform", complianceDownloadPath(path, file.ID)))
				}
				return err
			}
			downloaded++
			for _, parsed := range parseComplianceJSONLLines(body) {
				rec := parsed.Record
				if rec.ID == "" {
					continue
				}
				if err := sink.Emit(ctx, s.complianceFinding(rec, firstNonEmpty(file.EventType, logType))); err != nil {
					return err
				}
				if s.compliancePromptScan {
					if f, ok := s.complianceContentFinding(rec, parsed.RawLine); ok {
						if err := sink.Emit(ctx, f); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	return nil
}

// complianceFinding maps one JSONL compliance record to a minimal-data evidence
// finding. The SubjectRef is the non-sensitive record id (the auditor's handle back to
// the Compliance Logs Platform); the Title is the log type + event type; the actor's
// id/email/ip and the structural fields are folded into the one-way DetailHash and
// NEVER surfaced (docs/SECURITY-HARDENING.md). Severity is Info: this is evidence, not an alert.
func (s *Source) complianceFinding(rec complianceRecord, logType string) model.FindingReport {
	detail := strings.Join([]string{
		rec.ID, rec.LogType, rec.Type, rec.Timestamp, rec.WorkspaceID,
		rec.Actor.Type, rec.Actor.ID, rec.Actor.Email, rec.Actor.IPAddress,
	}, "|")
	occurred := parseTime(rec.Timestamp)
	if occurred.IsZero() {
		occurred = s.clock().UTC()
	}
	return model.FindingReport{
		Kind:        findingKindActivity,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectCompliance,
		SubjectRef:  rec.ID,
		Title:       "Codex compliance (" + firstNonEmpty(logType, rec.LogType) + "): " + firstNonEmpty(rec.Type, "record"),
		DetailHash:  redact.Hash(detail),
		OccurredAt:  occurred,
	}
}

func complianceDownloadPath(listPath, fileID string) string {
	return strings.TrimRight(listPath, "/") + "/" + url.PathEscape(strings.TrimSpace(fileID))
}

// complianceContentFinding reports structural DLP/injection signals from a raw
// Compliance JSONL line without storing or echoing the line. Severity is MEDIUM when a
// secret-shaped token is present, LOW when only textscan classes are present.
func (s *Source) complianceContentFinding(rec complianceRecord, rawLine string) (model.FindingReport, bool) {
	secret := redact.ContainsSecret(rawLine)
	injection := textscan.ScanInjection(rawLine)
	invisibleClasses, invisibleCount := textscan.ScanInvisible(rawLine)
	if !secret && len(injection) == 0 && invisibleCount == 0 {
		return model.FindingReport{}, false
	}
	sev := model.SeverityLow
	secretCount := 0
	if secret {
		sev = model.SeverityMedium
		secretCount = 1
	}
	parts := []string{
		fmt.Sprintf("secret-shape=%d", secretCount),
		fmt.Sprintf("injection-markers=%d", len(injection)),
		fmt.Sprintf("invisible-runes=%d", invisibleCount),
	}
	if len(invisibleClasses) > 0 {
		parts = append(parts, "invisible-classes="+strings.Join(invisibleClasses, ","))
	}
	fingerprint := strings.Join([]string{
		rec.ID,
		"secret=" + strconv.FormatBool(secret),
		"injection=" + strings.Join(injection, ","),
		"invisible=" + strings.Join(invisibleClasses, ","),
		"invisible_count=" + strconv.Itoa(invisibleCount),
	}, "|")
	return model.FindingReport{
		Kind:        "posture",
		Severity:    sev,
		SubjectKind: subjectComplianceContent,
		SubjectRef:  rec.ID,
		Title:       "Codex compliance record flagged: " + strings.Join(parts, ", "),
		DetailHash:  redact.Hash(fingerprint),
		OccurredAt:  s.clock().UTC(),
	}, true
}

func (s *Source) workspaceIDMissingFinding() model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectSurface,
		SubjectRef:  "Codex Analytics/Compliance",
		Title:       "workspace_id not configured; Codex Analytics/Compliance APIs are per-workspace",
		DetailHash:  redact.Hash("codex workspace_id missing for analytics/compliance"),
		OccurredAt:  s.clock().UTC(),
	}
}

// unavailableFinding is the honest "ingest unavailable" posture finding for a
// sales-gated/UNVERIFIED Codex enterprise surface. It records WHICH
// surface and the path tried, so an operator can correct the path or obtain
// entitlement — never a fabricated empty inventory.
func (s *Source) unavailableFinding(surface, path string) model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectSurface,
		SubjectRef:  surface,
		Title:       "Codex " + surface + " ingest unavailable (workspace entitlement/path returned 403/404)",
		DetailHash:  redact.Hash("codex surface=" + surface + " path=" + path + " returned 403/404; enterprise governance may require workspace entitlement"),
		OccurredAt:  s.clock().UTC(),
	}
}
