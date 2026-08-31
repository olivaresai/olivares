// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package cursor is the Olivares AI connector for Cursor — the AI code-editor agent
// surface — read through its Team/Enterprise Admin API (https://api.cursor.com,
// §5.1). It is the observe+govern peer of the codex connector for the
// "Cursor" agent surface: it turns the Admin API into the canonical observation streams:
//
//   - Filtered Usage Events (POST /teams/filtered-usage-events) → one billed
//     model.CostSample per chargeable agent event (CostType="cursor", ProviderRef=
//     "cursor", attributed to the developer/service account + model), the authoritative
//     Cursor-attributed spend feeding module XI / FinOps.
//   - Audit Logs (GET /teams/audit-logs) → one minimal-data external_activity
//     FindingReport per record (actor email / ip hashed), appended to the tamper-evident
//     ledger as audit evidence.
//   - Team Members (GET /teams/members) → an inventory FindingReport per member (role,
//     active/removed) AND the email→id map that attributes usage to a stable, non-PII id.
//   - Spend roll-up (POST /teams/spend) → budget-posture FindingReports when a member's
//     cycle spend approaches or exceeds their per-user monthly limit.
//
// AUTH (VERIFIED, https://cursor.com/docs/account/teams/admin-api). The credential is a
// Cursor Admin API key, minted by a team admin (Dashboard → Settings → Advanced → Admin
// API Keys). It is presented as the HTTP Basic-auth USERNAME with an EMPTY password — NOT
// a Bearer token (Bearer is only the separate Cloud Agents API). There is no consumer
// credential and no per-user OAuth: a control plane reads team governance with an admin
// key, exactly as for Codex/Claude.
//
// READ-ONLY and minimal-data (docs/SECURITY-HARDENING.md-3): the client exposes only the documented READ
// endpoints (the query-with-body POSTs are reads), so the connector CANNOT mutate Cursor
// — it never names the user-spend-limit / remove-member / group-write endpoints. It
// carries token counts, billed money, model ids, role/actor identifiers and audit
// metadata — never prompt/diff content or key values. It imports only the SDK and the
// Apache modelprovider contract, never the engine.
//
// HONEST DEGRADATION (the session brief). The Admin API docs disagree on plan gating: the
// overview labels the whole Admin API "Enterprise teams" while the per-endpoint reference
// gates only two MUTATING endpoints as Enterprise-only and leaves the reads ungated. So a
// 403/404 on any stream degrades to a "plan-gated/UNVERIFIED" posture finding and does NOT
// abort the gather — the connector reports what it could read and names what it could not,
// never a fabricated empty inventory.
package cursor

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.cursor"

// Default configuration values.
const (
	defaultBaseURL  = "https://api.cursor.com"
	defaultLookback = 24 * time.Hour
	defaultPageSize = 100
	defaultMaxPages = 20

	// costTypeCursor tags every Cursor CostSample so FinOps attributes Cursor agent
	// spend distinctly from the underlying model providers Cursor routes to.
	costTypeCursor = "cursor"

	// findingKindActivity is the module-XIII evidence Kind (shared with codex/claude-
	// compliance): Cursor audit records count as external_activity audit evidence.
	findingKindActivity = "external_activity"

	// budgetWarnRatio is the fraction of a member's monthly limit at which the connector
	// raises a budget-posture finding (>=100% is High, >=warn is Medium).
	budgetWarnRatio = 0.80
)

// Endpoint paths (VERIFIED Cursor Admin API).
const (
	membersPath = "/teams/members"
	usagePath   = "/teams/filtered-usage-events"
	spendPath   = "/teams/spend"
	auditPath   = "/teams/audit-logs"
)

// Source is the Cursor governance source connector. It satisfies sdk.SourceConnector:
// Gather streams billed CostSamples plus member/audit/budget findings. It is NOT a
// modelprovider.CatalogProvider — Cursor exposes no model catalog (it routes to other
// providers' models, named per usage event), so there is nothing to declare offline.
type Source struct {
	client *client

	apiKey   string
	baseURL  string
	lookback time.Duration
	pageSize int
	maxPages int

	usage          bool // filtered-usage-events → billed CostSamples
	audit          bool // audit-logs → external_activity evidence
	members        bool // members → inventory findings
	spend          bool // spend → budget-posture findings
	attributeEmail bool // when true the cost Actor is the raw email; default is the stable member id

	doer Doer             // injected transport (tests); nil => default
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof Source satisfies the contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a Cursor source with default configuration.
func New() *Source {
	return &Source{
		baseURL:  defaultBaseURL,
		lookback: defaultLookback,
		pageSize: defaultPageSize,
		maxPages: defaultMaxPages,
		usage:    true,
		audit:    true,
		members:  true,
		spend:    true,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Cursor (governance)",
		Description: "Reads Cursor team usage/spend (billed CostSamples), audit logs and member inventory via the Cursor Admin API (read-only). Auth = an admin API key as the HTTP Basic username; never a consumer credential.",
		ConfigFields: []sdk.ConfigField{
			{Key: "api_key", Type: sdk.FieldString, Secret: true, Description: "Cursor Admin API key (read-only; never persisted). Presented as the HTTP Basic username with an empty password. Empty = offline (Gather emits nothing)."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "Cursor Admin API base URL."},
			{Key: "lookback", Type: sdk.FieldDuration, Default: "24h", Description: "How far back to pull usage/audit on each Gather."},
			{Key: "page_size", Type: sdk.FieldInt, Default: strconv.Itoa(defaultPageSize), Description: "Page size for paginated endpoints."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per gather (Cursor rate-limits most reads to 20/min)."},
			{Key: "usage", Type: sdk.FieldBool, Default: "true", Description: "Pull filtered usage events → billed Cursor CostSamples (chargedCents)."},
			{Key: "audit", Type: sdk.FieldBool, Default: "true", Description: "Pull the team audit logs → external_activity evidence."},
			{Key: "members", Type: sdk.FieldBool, Default: "true", Description: "Pull team members → inventory findings (role, active/removed)."},
			{Key: "spend", Type: sdk.FieldBool, Default: "true", Description: "Pull the per-user spend roll-up → budget-posture findings near/over the monthly limit."},
			{Key: "attribute_email", Type: sdk.FieldBool, Default: "false", Description: "Use the developer email as the cost Actor ref. Default false: the stable member id is used so per-developer chargeback carries an id, not PII (docs/08 §3)."},
		},
	}
}

// Open reads configuration and builds the read-only Basic-auth client. It never fails for
// a missing credential: with no api_key the connector runs offline (Gather emits nothing).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := strings.TrimRight(strings.TrimSpace(cfg.Get("base_url")), "/"); v != "" {
		s.baseURL = v
	}
	s.lookback = cfg.GetDuration("lookback", s.lookback)
	if v := cfg.GetInt("page_size", s.pageSize); v > 0 {
		s.pageSize = v
	}
	if v := cfg.GetInt("max_pages", s.maxPages); v > 0 {
		s.maxPages = v
	}
	s.usage = cfg.GetBool("usage", s.usage)
	s.audit = cfg.GetBool("audit", s.audit)
	s.members = cfg.GetBool("members", s.members)
	s.spend = cfg.GetBool("spend", s.spend)
	s.attributeEmail = cfg.GetBool("attribute_email", s.attributeEmail)
	s.apiKey = strings.TrimSpace(cfg.Get("api_key"))

	s.client = newClient(s.baseURL, s.apiKey, s.doer)
	return nil
}

// Gather pulls the enabled Cursor governance streams. It is a batch source: it returns nil
// when the windows are drained. With no credential it returns nil immediately (offline). A
// 403/404 on a plan-gated stream degrades to a posture finding and does NOT abort the run;
// a transient error is returned so the engine retries.
//
// Members are resolved FIRST (even when member findings are off but usage is on) so usage
// events attribute to a stable, non-PII member id rather than the raw email.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.apiKey == "" || s.client == nil {
		return nil // offline mode: nothing to pull
	}

	byEmail, err := s.resolveMembers(ctx, sink)
	if err != nil {
		return err
	}
	if s.usage {
		if err := s.gatherUsage(ctx, sink, byEmail); err != nil {
			return err
		}
	}
	if s.spend {
		if err := s.gatherSpend(ctx, sink); err != nil {
			return err
		}
	}
	if s.audit {
		if err := s.gatherAudit(ctx, sink); err != nil {
			return err
		}
	}
	return nil
}

// Close releases resources; this connector holds none.
func (s *Source) Close(context.Context) error { return nil }

// resolveMembers fetches /teams/members once, emitting an inventory finding per member
// (when members is enabled) and returning the email→member map used to attribute usage to
// a stable id. It is also fetched when usage is on but member findings are off (for
// attribution only). A 403/404 degrades to a posture finding and an empty map; a transient
// error aborts the run for retry.
func (s *Source) resolveMembers(ctx context.Context, sink sdk.Sink) (map[string]memberEntry, error) {
	if !s.members && !s.usage {
		return nil, nil
	}
	var resp membersResponse
	if err := s.client.getJSON(ctx, membersPath, nil, &resp); err != nil {
		if isUnavailable(err) {
			return nil, sink.Emit(ctx, s.unavailableFinding("Team Members", membersPath))
		}
		return nil, err
	}
	byEmail := make(map[string]memberEntry, len(resp.TeamMembers))
	for _, m := range resp.TeamMembers {
		if m.Email != "" {
			byEmail[strings.ToLower(m.Email)] = m
		}
		if s.members {
			if err := sink.Emit(ctx, s.memberFinding(m)); err != nil {
				return nil, err
			}
		}
	}
	return byEmail, nil
}

// memberFinding records one team member as inventory. The SubjectRef is the stable
// non-PII member id; the Title carries only the role + active state; the email/name are
// folded into the one-way DetailHash, never surfaced (docs/SECURITY-HARDENING.md).
func (s *Source) memberFinding(m memberEntry) model.FindingReport {
	state := "active"
	if m.IsRemoved {
		state = "removed"
	}
	detail := strings.Join([]string{m.ID, m.Email, m.Name, m.Role, state}, "|")
	return model.FindingReport{
		Kind:        "inventory",
		Severity:    model.SeverityInfo,
		SubjectKind: subjectMember,
		SubjectRef:  firstNonEmpty(m.ID, redact.Hash(m.Email)),
		Title:       "Cursor team member (" + firstNonEmpty(m.Role, "member") + ", " + state + ")",
		DetailHash:  redact.Hash(detail),
		OccurredAt:  s.clock().UTC(),
	}
}

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// startMillis is the lookback window start as Unix milliseconds (the Cursor usage/audit
// time unit).
func (s *Source) startMillis() int64 {
	return s.clock().Add(-s.lookback).UnixMilli()
}

// nowMillis is the window end as Unix milliseconds.
func (s *Source) nowMillis() int64 {
	return s.clock().UnixMilli()
}

// centsToMicroUSD converts a cents amount (Cursor bills in cents; the value can be
// fractional, e.g. 20.18232) to integer micro-USD: 1 cent = 0.01 USD = 10_000 µUSD. A
// negative/NaN amount clamps to 0 (unknown), never a guessed cost (ARCHITECTURE.md).
func centsToMicroUSD(cents float64) int64 {
	if cents <= 0 || cents != cents { // cents!=cents is the NaN guard
		return 0
	}
	return int64(cents*10_000 + 0.5)
}

// millisTime converts a string of Unix milliseconds to UTC, returning the zero time on a
// missing/odd value so a bad provider timestamp never aborts a run.
func millisTime(ms string) time.Time {
	ms = strings.TrimSpace(ms)
	if ms == "" {
		return time.Time{}
	}
	n, err := strconv.ParseInt(ms, 10, 64)
	if err != nil || n <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(n).UTC()
}

// firstNonEmpty returns the first non-empty string, or "".
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// isUnavailable reports whether err is a plan-gated/not-found response (403/404), so the
// connector can degrade to an honest posture finding instead of failing the gather. The
// status is in the error string; this never matches a transport error (which is retried).
func isUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "status 404") || strings.Contains(msg, "status 403")
}
