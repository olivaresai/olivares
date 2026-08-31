// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// governance.go implements the OpenAI governance surfaces:
//
//   - Org Users inventory: the org user graph (role distribution, user count).
//   - Audit Logs: org audit events as minimal-data evidence findings.
//   - Data Retention / ZDR posture: org-level and per-project data retention.
//   - Costs API: billed CostSamples (opt-in, CostType="openai").
//
// Every surface follows HONEST DEGRADATION: a 403/404 (permission or entitlement)
// emits a posture finding and returns nil — never fails the whole gather.
//
// Minimal-data (docs/SECURITY-HARDENING.md): actor email, IP, user-id are folded into the one-way
// DetailHash via redact.Hash(), NEVER surfaced in the Title or SubjectRef.
package openai

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Finding subjects for the OpenAI governance posture/evidence findings.
const (
	subjectOrgUsers      = "openai.org_users"
	subjectAuditLog      = "openai.audit_log"
	subjectAuditPosture  = "openai.audit_posture"
	subjectDataRetention = "openai.data_retention"
	subjectSurface       = "openai.surface"
)

// costTypeOpenAI tags every OpenAI CostSample so FinOps attributes OpenAI spend
// distinctly from codex's CostType="codex".
const costTypeOpenAI = "openai"

// findingKindActivity is the evidence Kind for external_activity findings
// (shared with codex).
const findingKindActivity = "external_activity"

// ---------- Org Users (GET /v1/organization/users) ----------

// gatherOrgGraph paginates the org user list and emits a single inventory finding
// summarizing the user count and role distribution. On a 403/404 it degrades to
// an honest posture finding.
func (s *Source) gatherOrgGraph(ctx context.Context, sink sdk.Sink) error {
	roleCounts := map[string]int{}
	totalUsers := 0
	after := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp orgUsersResponse
		q := url.Values{"limit": {"100"}}
		if after != "" {
			q.Set("after", after)
		}
		if err := s.client.GetJSON(ctx, "/v1/organization/users", q, &resp); err != nil {
			if isUnavailable(err) {
				return sink.Emit(ctx, s.unavailableFinding("org_users", "/v1/organization/users"))
			}
			return err
		}
		for _, u := range resp.Data {
			totalUsers++
			role := u.Role
			if role == "" {
				role = "unknown"
			}
			roleCounts[role]++
		}
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	return sink.Emit(ctx, s.orgGraphFinding(totalUsers, roleCounts))
}

// orgGraphFinding builds the inventory finding from the collected user graph.
// The DetailHash hashes the full count + role distribution, never individual PII.
func (s *Source) orgGraphFinding(total int, roles map[string]int) model.FindingReport {
	// Build a deterministic role summary for the title.
	var parts []string
	for role, count := range roles {
		parts = append(parts, fmt.Sprintf("%s=%d", role, count))
	}
	roleSummary := strings.Join(parts, ", ")

	detail := fmt.Sprintf("openai org_users total=%d roles=[%s]", total, roleSummary)

	return model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectOrgUsers,
		SubjectRef:  "organization",
		Title:       fmt.Sprintf("OpenAI org users: %d user(s), roles: %s", total, roleSummary),
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

// ---------- Audit Logs (GET /v1/organization/audit_logs) ----------

// gatherAuditLogs paginates the org audit-log API and emits one external_activity
// evidence finding per record. Actor email/IP/user-id are hashed, never surfaced.
// On a 403/404 it degrades to an honest posture finding.
func (s *Source) gatherAuditLogs(ctx context.Context, sink sdk.Sink) error {
	after := ""
	start := s.clock().Add(-s.lookback).UTC()
	wifCounts := map[string]int{}
	tunnelCounts := map[string]int{}
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp orgAuditLogsResponse
		q := url.Values{"limit": {"100"}}
		q.Set("effective_at[gte]", strconv.FormatInt(start.Unix(), 10))
		if after != "" {
			q.Set("after", after)
		}
		if err := s.client.GetJSON(ctx, "/v1/organization/audit_logs", q, &resp); err != nil {
			if isUnavailable(err) {
				return sink.Emit(ctx, s.unavailableFinding("audit_logs", "/v1/organization/audit_logs"))
			}
			return err
		}
		for _, e := range resp.Data {
			if e.ID == "" {
				continue
			}
			recordDerivedAuditEvent(e.Type, wifCounts, tunnelCounts)
			if err := sink.Emit(ctx, s.auditLogFinding(e)); err != nil {
				return err
			}
		}
		if !resp.HasMore || resp.LastID == "" {
			break
		}
		after = resp.LastID
	}
	if len(wifCounts) > 0 {
		if err := sink.Emit(ctx, s.wifAuditActivityFinding(wifCounts)); err != nil {
			return err
		}
	}
	if len(tunnelCounts) > 0 {
		if err := sink.Emit(ctx, s.tunnelAuditActivityFinding(tunnelCounts)); err != nil {
			return err
		}
	}
	return nil
}

// auditLogFinding maps one org audit-log record to a minimal-data evidence
// finding. Actor email/IP/user-id and project are folded into the one-way
// DetailHash, never surfaced.
func (s *Source) auditLogFinding(e orgAuditLogEntry) model.FindingReport {
	detail := strings.Join([]string{
		e.ID, e.Type, e.Actor.Type,
		e.Actor.Session.User.ID, e.Actor.Session.User.Email, e.Actor.Session.IPAddress,
		e.Actor.APIKey.ID, e.Project.ID, e.Project.Name,
	}, "|")
	occurred := unixTime(e.EffectiveAt)
	if occurred.IsZero() {
		occurred = s.clock().UTC()
	}
	eventType := e.Type
	if eventType == "" {
		eventType = "event"
	}
	return model.FindingReport{
		Kind:        findingKindActivity,
		Severity:    model.SeverityInfo,
		SubjectKind: subjectAuditLog,
		SubjectRef:  e.ID,
		Title:       "OpenAI audit: " + eventType,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  occurred,
	}
}

func recordDerivedAuditEvent(eventType string, wifCounts, tunnelCounts map[string]int) {
	switch eventType {
	case "workload_identity_provider.created",
		"workload_identity_provider.updated",
		"workload_identity_provider.deleted",
		"workload_identity_provider_mapping.created",
		"workload_identity_provider_mapping.updated",
		"workload_identity_provider_mapping.deleted":
		wifCounts[eventType]++
	case "tunnel.created", "tunnel.updated", "tunnel.deleted":
		tunnelCounts[eventType]++
	}
}

// wifAuditActivityFinding is the WIF-config visibility ceiling for OpenAI: there
// is no CRUD read API for workload identity providers/mappings, so audit events are
// the only read-side signal. This mirrors the WIF ingest pattern only on the
// ingest side; it does not call token exchange endpoints.
func (s *Source) wifAuditActivityFinding(counts map[string]int) model.FindingReport {
	summary := eventCountSummary(counts)
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectAuditPosture,
		SubjectRef:  "workload_identity_federation",
		Title:       "OpenAI workload identity federation activity observed: " + summary,
		DetailHash:  redact.Hash("openai wif audit_counts=" + summary),
		OccurredAt:  s.clock().UTC(),
	}
}

// tunnelAuditActivityFinding records Secure MCP Tunnel lifecycle activity observed
// in audit logs. OpenAI exposes no tunnel management read API to this connector, so
// audit events are the read-side posture signal.
func (s *Source) tunnelAuditActivityFinding(counts map[string]int) model.FindingReport {
	summary := eventCountSummary(counts)
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectAuditPosture,
		SubjectRef:  "secure_mcp_tunnel",
		Title:       "OpenAI Secure MCP Tunnel activity observed: " + summary,
		DetailHash:  redact.Hash("openai tunnel audit_counts=" + summary),
		OccurredAt:  s.clock().UTC(),
	}
}

func eventCountSummary(counts map[string]int) string {
	types := make([]string, 0, len(counts))
	for eventType := range counts {
		types = append(types, eventType)
	}
	sort.Strings(types)
	parts := make([]string, 0, len(types))
	for _, eventType := range types {
		parts = append(parts, fmt.Sprintf("%s=%d", eventType, counts[eventType]))
	}
	return strings.Join(parts, ", ")
}

// ---------- Data Retention / ZDR (GET /v1/organization/data_retention) ----------

// gatherDataRetention reads the org-level data retention policy and emits a
// posture finding. It also reads per-project data retention for each known project
// and emits an additional posture finding for any project whose retention differs
// from the org default. On a 403/404 it degrades honestly.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
func (s *Source) gatherDataRetention(ctx context.Context, sink sdk.Sink) error {
	projects, err := s.fetchNonArchivedProjects(ctx)
	return s.gatherDataRetentionForProjects(ctx, sink, projects, err)
}

// gatherDataRetentionForProjects reads data retention with a caller-supplied
// project list so Gather can reuse one project fetch across per-project surfaces.
// Shape verified 2026-07-04 against developers.openai.com/api/docs/api-reference/administration.
func (s *Source) gatherDataRetentionForProjects(ctx context.Context, sink sdk.Sink, projects []modelprovider.WorkspaceRef, projectErr error) error {
	var orgRetention dataRetentionResponse
	if err := s.client.GetJSON(ctx, "/v1/organization/data_retention", nil, &orgRetention); err != nil {
		if isUnavailable(err) {
			return sink.Emit(ctx, s.unavailableFinding("data_retention", "/v1/organization/data_retention"))
		}
		return err
	}
	if err := sink.Emit(ctx, s.dataRetentionFinding(orgRetention, "organization", "")); err != nil {
		return err
	}

	if projectErr != nil {
		// If we can't list projects, just skip per-project retention; the org-level
		// finding is already emitted. Don't fail the whole gather.
		return nil
	}
	for _, p := range projects {
		path := "/v1/organization/projects/" + p.ID + "/data_retention"
		var projRetention dataRetentionResponse
		if err := s.client.GetJSON(ctx, path, nil, &projRetention); err != nil {
			if isUnavailable(err) {
				// Per-project retention unavailable is expected for some plans; skip.
				continue
			}
			// Transient error on one project: skip, don't fail the gather.
			continue
		}
		// Only emit if the project's retention differs from the org default.
		if dataRetentionDiffers(projRetention, orgRetention) {
			if err := sink.Emit(ctx, s.dataRetentionFinding(projRetention, p.ID, p.Name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// dataRetentionFinding builds a posture finding describing the data retention
// policy for an org or project. subjectRef is "organization" for org-level or
// the project ID for per-project.
func (s *Source) dataRetentionFinding(dr dataRetentionResponse, subjectRef, projectName string) model.FindingReport {
	zdrStatus := "disabled"
	if dataRetentionZDR(dr, nil) {
		zdrStatus = "enabled"
	}
	scope := "org"
	if subjectRef != "organization" {
		scope = "project " + subjectRef
		if projectName != "" {
			scope = "project " + projectName
		}
	}
	title := fmt.Sprintf("OpenAI data retention (%s): ZDR %s, retention %d day(s), abuse monitoring %s",
		scope, zdrStatus, dr.RetentionDays, dr.AbuseMonitoring)
	if dr.Type != "" {
		title = fmt.Sprintf("OpenAI data retention (%s): type %s, ZDR %s", scope, dr.Type, zdrStatus)
		if dr.RetentionDays > 0 {
			title += fmt.Sprintf(", retention %d day(s)", dr.RetentionDays)
		}
		if dr.AbuseMonitoring != "" {
			title += ", abuse monitoring " + dr.AbuseMonitoring
		}
	}

	detail := fmt.Sprintf("openai data_retention ref=%s type=%s zdr=%v retention_days=%d abuse_monitoring=%s",
		subjectRef, dr.Type, dataRetentionZDR(dr, nil), dr.RetentionDays, dr.AbuseMonitoring)

	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectDataRetention,
		SubjectRef:  subjectRef,
		Title:       title,
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

func dataRetentionDiffers(project, org dataRetentionResponse) bool {
	if project.Type == "organization_default" {
		return false
	}
	return dataRetentionPolicyKey(project, &org) != dataRetentionPolicyKey(org, nil)
}

func dataRetentionPolicyKey(dr dataRetentionResponse, org *dataRetentionResponse) string {
	switch dr.Type {
	case "organization_default":
		if org != nil {
			return dataRetentionPolicyKey(*org, nil)
		}
		return dr.Type
	case "zero_data_retention", "enhanced_zero_data_retention",
		"modified_abuse_monitoring", "enhanced_modified_abuse_monitoring", "none":
		return dr.Type
	}
	if dr.Type != "" {
		return dr.Type
	}
	return fmt.Sprintf("legacy:zdr=%v:days=%d:abuse=%s", dr.ZDR, dr.RetentionDays, dr.AbuseMonitoring)
}

func dataRetentionZDR(dr dataRetentionResponse, org *dataRetentionResponse) bool {
	switch dr.Type {
	case "zero_data_retention", "enhanced_zero_data_retention":
		return true
	case "modified_abuse_monitoring", "enhanced_modified_abuse_monitoring", "none":
		return false
	case "organization_default":
		if org != nil {
			return dataRetentionZDR(*org, nil)
		}
		return false
	default:
		return dr.ZDR
	}
}

// ---------- Costs API (GET /v1/organization/costs) ----------

// gatherCosts pulls the billed Costs API across the lookback window and emits one
// authoritative (billed) CostSample per result row. CostType="openai" attributes
// it as OpenAI API spend. On a 403/404 it degrades honestly.
func (s *Source) gatherCosts(ctx context.Context, sink sdk.Sink) error {
	start := s.clock().Add(-s.lookback).UTC()
	page := ""
	for i := 0; i < s.maxPages; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var resp orgCostsResponse
		q := url.Values{}
		q.Set("start_time", strconv.FormatInt(start.Unix(), 10))
		q.Set("bucket_width", "1d") // Costs API supports daily granularity only
		q.Add("group_by[]", "line_item")
		q.Add("group_by[]", "project_id")
		q.Add("group_by[]", "api_key_id")
		q.Set("limit", "100")
		if page != "" {
			q.Set("page", page)
		}
		if err := s.client.GetJSON(ctx, "/v1/organization/costs", q, &resp); err != nil {
			if isUnavailable(err) {
				return sink.Emit(ctx, s.unavailableFinding("costs", "/v1/organization/costs"))
			}
			return err
		}
		for _, bucket := range resp.Data {
			occurred := unixTime(bucket.StartTime)
			for _, r := range bucket.Results {
				if err := sink.Emit(ctx, s.orgCostSample(r, occurred)); err != nil {
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

// orgCostSample turns one billed Costs row into a CostSample. The token fields
// are zero (Costs is money, not usage); the amount is the billed figure converted
// from dollars to micro-USD. CostType uses the Source's providerRef (openai).
func (s *Source) orgCostSample(r orgCostsResult, occurred time.Time) model.CostSample {
	lineItem := r.LineItem
	if lineItem == "" {
		lineItem = costTypeOpenAI
	}
	u := modelprovider.Usage{
		ProviderRef:  s.providerRef,
		WorkspaceRef: r.ProjectID,
		APIKeyRef:    r.APIKeyID,
		OccurredAt:   occurred,
		Gateway:      model.GatewayDirect,
		Provenance:   model.ProvenanceBilled,
		CostType:     lineItem,
	}
	sample := modelprovider.ToCostSampleWithCost(u, dollarsToMicroUSD(r.Amount))
	if r.Quantity != nil {
		sample.Labels = map[string]string{
			"line_item_quantity": strconv.FormatFloat(*r.Quantity, 'f', -1, 64),
		}
	}
	return sample
}

// ---------- Shared helpers ----------

// unavailableFinding is the honest "ingest unavailable" posture finding for a
// governance surface that returned 403/404/410 (permission, entitlement, or gone). It
// records WHICH surface and the path tried, so an operator can correct
// configuration — never a fabricated empty inventory.
func (s *Source) unavailableFinding(surface, path string) model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectSurface,
		SubjectRef:  surface,
		Title:       "OpenAI " + surface + " ingest unavailable (permission or entitlement)",
		DetailHash:  redact.Hash("openai surface=" + surface + " path=" + path + " returned 403/404/410"),
		OccurredAt:  s.clock().UTC(),
	}
}

// isUnavailable reports whether err is a "not entitled / not found / gone"
// response (403/404/410), so the connector can degrade to an honest posture
// finding instead of failing the gather. The modelprovider client surfaces the
// status in the error string; this never matches a transport error (which is retried).
func isUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "status 404") || strings.Contains(msg, "status 403") ||
		strings.Contains(msg, "status 410")
}

// dollarsToMicroUSD converts a major-unit (dollars) amount to integer micro-USD
// (1 USD = 1_000_000 uUSD). A negative/NaN amount clamps to 0 (unknown), never a
// guessed cost.
func dollarsToMicroUSD(value float64) int64 {
	if value <= 0 || value != value { // value!=value is the NaN guard
		return 0
	}
	return int64(value*1_000_000 + 0.5)
}
