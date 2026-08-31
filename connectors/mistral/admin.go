// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// admin.go implements the BETA-VERIFIED Mistral Admin API governance surfaces. The Admin
// API is now a published GENERATED OpenAPI beta reference (verified 2026-07-04), no
// longer narrative-only.
//
// Mistral's Admin API (beta, requires a distinct AdminApiKey) exposes:
//
//   - AUDIT LOGS (GET /api/admin/audit-logs): admin action stream (user changes,
//     workspace changes, key lifecycle) → one minimal-data external_activity evidence
//     finding per log entry, hashing actor/target metadata.
//     BETA-VERIFIED (generated-reference currency re-verified 2026-07-04). Mistral
//     documents the endpoint as "Available on Enterprise plans. Enabled by default for
//     all Workspaces." while the narrative still says "Log export is not supported"; this
//     connector tolerates that documented contradiction and degrades honestly on 403/404.
//
//   - USAGE (GET /api/admin/usage?month=M&year=Y): per-model billed cost breakdowns with
//     token counts and currency fields → CostSamples (provenance=billed when the response
//     includes a currency, estimated otherwise).
//     BETA-VERIFIED.
//
//   - INVENTORY (GET /api/admin/users, /api/admin/workspaces, /api/admin/api-keys):
//     org-level user/workspace/key inventory → findings + catalog enrichment.
//     BETA-VERIFIED.
//
//   - POSTURE (GET /api/admin/spend-limit, /api/admin/rate-limit): FinOps posture
//     findings about spending caps and rate limits.
//     BETA-VERIFIED.
//
// Every admin surface degrades honestly on 403/404 (admin key not entitled, beta endpoint
// removed, Enterprise-gated audit logs unavailable) to a posture finding "Mistral Admin
// API [surface] unavailable" and returns nil. The existing /v1/models catalog continues
// working regardless.
//
// READ-ONLY and minimal-data (docs/SECURITY-HARDENING.md-3): every call is a GET via the shared GET-only
// modelprovider client. Mistral docs disagree on admin auth (narrative: x-api-key with
// https://console.mistral.ai/api/admin; generated samples: Authorization: Bearer),
// verified 2026-07-04, so admin requests send both headers. Actor metadata and emails are
// hashed into DetailHash, never surfaced.
package mistral

import (
	"context"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Admin API endpoint paths (BETA-VERIFIED).
const (
	adminAuditLogsPath  = "/api/admin/audit-logs"
	adminUsagePath      = "/api/admin/usage"
	adminUsersPath      = "/api/admin/users"
	adminWorkspacesPath = "/api/admin/workspaces"
	adminAPIKeysPath    = "/api/admin/api-keys"
	adminSpendLimitPath = "/api/admin/spend-limit"
	adminRateLimitPath  = "/api/admin/rate-limit"
)

// Finding subjects for the Admin API surfaces.
const (
	subjectAuditLog      = "mistral.audit_log"
	subjectAdminUser     = "mistral.admin_user"
	subjectAdminPosture  = "mistral.admin_posture"
	subjectAdminCoverage = "mistral.admin_coverage"
)

// gatherAuditLogs reads the Mistral Admin audit-log stream and emits one
// external_activity evidence finding per log entry. Actor metadata is hashed
// into DetailHash, never surfaced. On a 403/404 it degrades to an honest
// posture finding.
func (s *Source) gatherAuditLogs(ctx context.Context, sink sdk.Sink) error {
	var resp adminAuditLogsResponse
	if err := s.adminClient.GetJSON(ctx, adminAuditLogsPath, nil, &resp); err != nil {
		if isUnavailable(err) {
			return sink.Emit(ctx, s.adminUnavailableFinding("audit logs", adminAuditLogsPath))
		}
		return err
	}
	for _, entry := range resp.Data {
		logID := entry.id()
		if logID == "" {
			continue
		}
		eventType := entry.eventType()
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "external_activity",
			Severity:    model.SeverityInfo,
			SubjectKind: subjectAuditLog,
			SubjectRef:  logID,
			Title:       "Mistral audit: " + firstNonEmpty(eventType, "event"),
			DetailHash: redact.Hash(strings.Join([]string{
				logID, eventType, entry.ActorType,
				entry.ActorMetadata.String(), entry.EventMetadata.String(),
				entry.TargetType, entry.TargetMetadata.String(),
				entry.CreatedAt,
			}, "|")),
			OccurredAt: parseTime(entry.CreatedAt),
		}); err != nil {
			return err
		}
	}
	return nil
}

// gatherAdminUsage reads the Mistral Admin per-model usage for the current month
// and emits CostSamples. When the response includes a currency field, provenance is
// billed; otherwise estimated. On a 403/404 it degrades to a posture finding.
func (s *Source) gatherAdminUsage(ctx context.Context, sink sdk.Sink) error {
	now := s.clock().UTC()
	q := url.Values{}
	q.Set("month", strconv.Itoa(int(now.Month())))
	q.Set("year", strconv.Itoa(now.Year()))

	var resp adminUsageResponse
	if err := s.adminClient.GetJSON(ctx, adminUsagePath, q, &resp); err != nil {
		if isUnavailable(err) {
			return sink.Emit(ctx, s.adminUnavailableFinding("usage", adminUsagePath))
		}
		return err
	}
	for _, entry := range resp.Data {
		if entry.Model == "" {
			continue
		}
		provenance := model.ProvenanceEstimated
		if entry.Currency != "" {
			provenance = model.ProvenanceBilled
		}
		u := modelprovider.Usage{
			ProviderRef:  modelprovider.ProviderMistral,
			ModelRef:     entry.Model,
			InputTokens:  maxInt64(entry.InputTokens, 0),
			OutputTokens: maxInt64(entry.OutputTokens, 0),
			OccurredAt:   now,
			Gateway:      model.GatewayDirect,
			Provenance:   provenance,
			CostType:     costTypeMistral,
		}
		// When the admin API reports an amount with currency, use the billed figure.
		// Convert the USD amount to micro-USD (integer).
		costMicroUSD := int64(math.Round(entry.Amount * 1_000_000))
		cs := modelprovider.ToCostSampleWithCost(u, costMicroUSD)
		if err := sink.Emit(ctx, cs); err != nil {
			return err
		}
	}
	return nil
}

// gatherAdminInventory reads users, workspaces and API keys from the Admin API and
// emits inventory findings + populates the catalog. On a 403/404 each sub-surface
// degrades independently.
func (s *Source) gatherAdminInventory(ctx context.Context, sink sdk.Sink) error {
	// --- Users ---
	if err := s.gatherAdminUsers(ctx, sink); err != nil {
		return err
	}
	// --- Workspaces ---
	if err := s.gatherAdminWorkspaces(ctx, sink); err != nil {
		return err
	}
	// --- API Keys ---
	return s.gatherAdminKeys(ctx, sink)
}

// gatherAdminUsers reads the admin users endpoint and emits an inventory finding
// with user count + role distribution (emails hashed, never surfaced).
func (s *Source) gatherAdminUsers(ctx context.Context, sink sdk.Sink) error {
	var resp adminUsersResponse
	if err := s.adminClient.GetJSON(ctx, adminUsersPath, nil, &resp); err != nil {
		if isUnavailable(err) {
			return sink.Emit(ctx, s.adminUnavailableFinding("user inventory", adminUsersPath))
		}
		return err
	}
	// Count roles.
	roles := make(map[string]int)
	for _, u := range resp.Data {
		role := u.Role
		if role == "" {
			role = "unknown"
		}
		roles[role]++
	}
	// Build a role distribution summary (sorted for determinism).
	roleNames := make([]string, 0, len(roles))
	for role := range roles {
		roleNames = append(roleNames, role)
	}
	sort.Strings(roleNames)
	var roleParts []string
	for _, role := range roleNames {
		roleParts = append(roleParts, fmt.Sprintf("%s=%d", role, roles[role]))
	}
	// Hash all emails for audit trail without surfacing PII.
	var emailHashes []string
	for _, u := range resp.Data {
		emailHashes = append(emailHashes, redact.Hash(u.Email))
	}

	return sink.Emit(ctx, model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectAdminUser,
		SubjectRef:  "mistral",
		Title:       fmt.Sprintf("Mistral org: %d users (%s)", len(resp.Data), strings.Join(roleParts, ", ")),
		DetailHash: redact.Hash(fmt.Sprintf("mistral admin users count=%d roles=%s emails=%s",
			len(resp.Data), strings.Join(roleParts, ","), strings.Join(emailHashes, ","))),
		OccurredAt: s.clock().UTC(),
	})
}

// gatherAdminWorkspaces reads the admin workspaces endpoint and populates
// s.adminWorkspaces for Snapshot enrichment, plus emits an inventory finding.
func (s *Source) gatherAdminWorkspaces(ctx context.Context, sink sdk.Sink) error {
	var resp adminWorkspacesResponse
	if err := s.adminClient.GetJSON(ctx, adminWorkspacesPath, nil, &resp); err != nil {
		if isUnavailable(err) {
			return sink.Emit(ctx, s.adminUnavailableFinding("workspace inventory", adminWorkspacesPath))
		}
		return err
	}
	s.adminWorkspaceEntries = resp.Data
	for _, w := range resp.Data {
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "inventory",
			Severity:    model.SeverityInfo,
			SubjectKind: subjectWorkspace,
			SubjectRef:  w.ref(),
			Title:       "Mistral workspace inventoried (Admin API)",
			DetailHash:  redact.Hash(fmt.Sprintf("mistral admin workspace id=%s uuid=%s name=%q description=%q icon=%q is_default=%v members_count=%d raw_roles=%s", w.ID, w.UUID, w.Name, w.Description, w.Icon, w.IsDefault, w.MembersCount, w.RawRoles.String())),
			OccurredAt:  s.clock().UTC(),
		}); err != nil {
			return err
		}
		if w.SpendLimit.Present {
			if err := sink.Emit(ctx, s.spendLimitFinding("workspace_spend_limit:"+w.ref(), "Mistral workspace spend limit", w.SpendLimit, "", "workspace="+w.ref())); err != nil {
				return err
			}
		}
	}
	return nil
}

// gatherAdminKeys reads the admin API keys endpoint and populates s.adminKeyEntries
// for Snapshot enrichment, plus emits inventory findings.
func (s *Source) gatherAdminKeys(ctx context.Context, sink sdk.Sink) error {
	var resp adminAPIKeysResponse
	if err := s.adminClient.GetJSON(ctx, adminAPIKeysPath, nil, &resp); err != nil {
		if isUnavailable(err) {
			return sink.Emit(ctx, s.adminUnavailableFinding("API-key inventory", adminAPIKeysPath))
		}
		return err
	}
	s.adminKeyEntries = resp.Data
	for _, k := range resp.Data {
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "inventory",
			Severity:    model.SeverityInfo,
			SubjectKind: subjectAPIKey,
			SubjectRef:  firstNonEmpty(k.ID, k.Name),
			Title:       "Mistral API key inventoried (Admin API)",
			DetailHash:  redact.Hash(fmt.Sprintf("mistral admin key id=%s name=%q workspace=%q", k.ID, k.Name, k.WorkspaceID)),
			OccurredAt:  s.clock().UTC(),
		}); err != nil {
			return err
		}
	}
	return nil
}

// gatherSpendPosture reads the spend-limit and rate-limit Admin API endpoints and
// emits posture findings. Each sub-surface degrades independently on 403/404.
func (s *Source) gatherSpendPosture(ctx context.Context, sink sdk.Sink) error {
	// --- Spend Limit ---
	var spend adminSpendLimitResponse
	if err := s.adminClient.GetJSON(ctx, adminSpendLimitPath, nil, &spend); err != nil {
		if !isUnavailable(err) {
			return err
		}
		if err := sink.Emit(ctx, s.adminUnavailableFinding("spend limit", adminSpendLimitPath)); err != nil {
			return err
		}
	} else {
		if err := sink.Emit(ctx, s.spendLimitFinding("spend_limit", "Mistral spend limit", spend.SpendLimit, spend.Currency, "org")); err != nil {
			return err
		}
	}

	// --- Rate Limit ---
	var rate adminRateLimitResponse
	if err := s.adminClient.GetJSON(ctx, adminRateLimitPath, nil, &rate); err != nil {
		if !isUnavailable(err) {
			return err
		}
		return sink.Emit(ctx, s.adminUnavailableFinding("rate limit", adminRateLimitPath))
	}
	return sink.Emit(ctx, model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectAdminPosture,
		SubjectRef:  "rate_limit",
		Title:       fmt.Sprintf("Mistral rate limit: %d req/min, %d tok/min", rate.RequestsPerMinute, rate.TokensPerMinute),
		DetailHash:  redact.Hash(fmt.Sprintf("mistral rate_limit rpm=%d tpm=%d", rate.RequestsPerMinute, rate.TokensPerMinute)),
		OccurredAt:  s.clock().UTC(),
	})
}

// adminUnavailableFinding is the honest degrade when a BETA-VERIFIED Admin API
// surface returns 403/404 (admin key not entitled, beta endpoint removed, or
// endpoint not available for this org plan). It records WHICH surface and the path
// tried so an operator can diagnose — never a fabricated empty result.
func (s *Source) adminUnavailableFinding(surface, path string) model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectAdminCoverage,
		SubjectRef:  surface,
		Title:       "Mistral Admin API " + surface + " unavailable (beta/admin-key required)",
		DetailHash:  redact.Hash("mistral admin surface=" + surface + " path=" + path + " base=" + s.adminBaseURL + " returned 403/404; the Admin API is beta and requires a distinct AdminApiKey with admin entitlement"),
		OccurredAt:  s.clock().UTC(),
	}
}

func (s *Source) spendLimitFinding(subjectRef, titlePrefix string, limit flexSpendLimit, fallbackCurrency, detailScope string) model.FindingReport {
	currency := firstNonEmpty(limit.Currency, fallbackCurrency)
	amount := fmt.Sprintf("%.2f", limit.Amount)
	if currency != "" {
		amount += " " + currency
	}
	title := titlePrefix + ": " + amount
	if !limit.Present || limit.Amount == 0 {
		title = titlePrefix + ": not configured"
	}
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectAdminPosture,
		SubjectRef:  subjectRef,
		Title:       title,
		DetailHash:  redact.Hash(fmt.Sprintf("mistral spend_limit=%.2f currency=%s scope=%s", limit.Amount, currency, detailScope)),
		OccurredAt:  s.clock().UTC(),
	}
}

func (e adminAuditLogEntry) id() string {
	return firstNonEmpty(e.LogID.String(), e.ID.String())
}

func (e adminAuditLogEntry) eventType() string {
	return firstNonEmpty(e.EventType.String(), e.Type.String())
}

func (w adminWorkspaceEntry) ref() string {
	return firstNonEmpty(w.ID, w.UUID, w.Name)
}
