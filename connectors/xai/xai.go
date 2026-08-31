// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package xai is the Olivares AI connector for xAI / Grok — a frontier provider (README.md
// §2, module X multi-vendor) — read through its Management API and inference catalog. It
// is a read-only model/provider governance source built on the shared
// connectors/modelprovider contract, like connectors/openai and connectors/codex.
//
// TWO PLANES, TWO CREDENTIALS (primary-source verified 2026-06-20):
//
//   - MANAGEMENT API (https://management-api.x.ai, a "management key" from the xAI
//     Console → Settings → Management Keys): API-key + ACL inventory
//     (GET /auth/teams/{teamId}/api-keys, masked hint only — never the secret) and
//     read-only billing (GET .../invoices, .../postpaid/invoice/preview,
//     .../prepaid/balance, .../postpaid/spending-limits). These are VERIFIED-SHAPE.
//   - AUDIT EVENTS (GET /audit/teams/{teamId}/events): admin action stream (key
//     lifecycle, team membership, settings changes) → one minimal-data
//     external_activity evidence finding per event, hashing actor PII.
//     VERIFIED-SHAPE.
//   - INFERENCE API (https://api.x.ai, a normal "xai-" key): the live model catalog
//     (GET /v1/language-models, with per-model prices) Snapshot reads. Optional —
//     without it Snapshot returns the declared offline Grok catalog.
//
// The connector emits, when a management key is configured: billed CostSamples from the
// finalized invoices + an estimated current-cycle preview (CostType="xai"), the API-key
// rotation + broad-ACL governance posture, and credit-balance / spending-limit FinOps
// posture. It exposes the Grok catalog (live or declared) through CatalogProvider.
//
// HONEST GAP: xAI's per-model/per-key TOKEN aggregation is a dashboard (Usage Explorer)
// surface; the programmatic equivalent is a POST analytics endpoint whose token-metric
// names are undocumented AND which is a POST (it would break the read-first GET-only
// guarantee). So this connector does NOT use it — cost comes from the GET billing
// endpoints (billed invoices), which is the authoritative money figure anyway, and
// per-request token cost is metered around the inference path by the gateway/PEP, not here.
//
// READ-ONLY and minimal-data (docs/SECURITY-HARDENING.md-3): every call is a GET via the shared GET-only
// modelprovider client (Bearer auth), so the connector CANNOT mutate xAI (creating /
// rotating / deleting keys and setting limits are MUTATIONS, out of scope, HITL-gated); it
// carries money, key/ACL inventory METADATA and the masked key hint — never prompts,
// completions or a key value. It imports only the SDK and the Apache modelprovider
// contract, never the engine (enforced by scripts/check-boundary.sh).
package xai

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/redact"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.xai"

// Default configuration values.
const (
	defaultManagementBaseURL = "https://management-api.x.ai"
	defaultInferenceBaseURL  = "https://api.x.ai"
	defaultKeyMaxAge         = 90 * 24 * time.Hour
	defaultMaxPages          = 20
	defaultLookbackMonths    = 2

	// costTypeXAI tags every xAI CostSample so FinOps attributes Grok spend distinctly.
	costTypeXAI = "xai"

	// Management API endpoint paths. The /auth endpoints carry NO /v1 prefix; the billing
	// endpoints DO (a documented inconsistency in the xAI API, preserved here).
	validationPath     = "/auth/management-keys/validation"
	languageModelsPath = "/v1/language-models"
)

// Source is the xAI source connector. It satisfies sdk.SourceConnector (billed cost +
// governance/FinOps posture as observations) and modelprovider.CatalogProvider (the Grok
// catalog). The management plane (keys + billing) and the inference plane (catalog) use
// separate credentials and clients.
type Source struct {
	mgmtClient *modelprovider.Client // management-api.x.ai (keys + billing)
	infClient  *modelprovider.Client // api.x.ai (live catalog)

	managementKey     string
	inferenceKey      string
	managementBaseURL string
	inferenceBaseURL  string
	teamID            string

	manageKeys    bool
	billing       bool
	keyMaxAge     time.Duration
	lookbackMonth int
	lowBalanceUSD float64
	maxPages      int

	doer modelprovider.Doer // injected transport (tests); nil => default
	now  func() time.Time   // injectable clock (tests); nil => time.Now
}

// Compile-time proof Source satisfies both contracts.
var (
	_ sdk.SourceConnector           = (*Source)(nil)
	_ modelprovider.CatalogProvider = (*Source)(nil)
)

// New returns an xAI source with default configuration.
func New() *Source {
	return &Source{
		managementBaseURL: defaultManagementBaseURL,
		inferenceBaseURL:  defaultInferenceBaseURL,
		manageKeys:        true,
		billing:           true,
		keyMaxAge:         defaultKeyMaxAge,
		lookbackMonth:     defaultLookbackMonths,
		maxPages:          defaultMaxPages,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "xAI / Grok (governance + billing)",
		Description: "Read-only xAI governance: API-key + ACL inventory and rotation/least-privilege posture (Management API), billed cost + credit-balance + spending-limit posture, and the live/declared Grok catalog. Management key (keys+billing) and inference key (catalog) are distinct credentials.",
		ConfigFields: []sdk.ConfigField{
			{Key: "management_key", Type: sdk.FieldString, Secret: true, Description: "xAI Management API key reference (Console → Settings → Management Keys; read-only Bearer; never persisted). Drives key/ACL inventory + billing. Empty = those streams are skipped."},
			{Key: "api_key", Type: sdk.FieldString, Secret: true, Description: "xAI inference API key reference (xai-…; read-only Bearer) for the live catalog (GET /v1/language-models). Empty = offline declared catalog."},
			{Key: "management_base_url", Type: sdk.FieldString, Default: defaultManagementBaseURL, Description: "xAI Management API base URL."},
			{Key: "inference_base_url", Type: sdk.FieldString, Default: defaultInferenceBaseURL, Description: "xAI inference API base URL (for the live catalog)."},
			{Key: "team_id", Type: sdk.FieldString, Description: "xAI team id to scope keys/billing to. Empty = discovered via /auth/management-keys/validation."},
			{Key: "manage_keys", Type: sdk.FieldBool, Default: "true", Description: "Inventory API keys + ACLs and emit rotation + broad-ACL posture findings."},
			{Key: "billing", Type: sdk.FieldBool, Default: "true", Description: "Pull billed cost (invoices + current-cycle preview) and credit-balance / spending-limit posture."},
			{Key: "key_max_age", Type: sdk.FieldDuration, Default: "2160h", Description: "Emit a rotation-posture finding for any active key older than this (default 90 days)."},
			{Key: "lookback_months", Type: sdk.FieldInt, Default: strconv.Itoa(defaultLookbackMonths), Description: "How many months of finalized invoices to pull (billed cost)."},
			{Key: "low_balance_usd", Type: sdk.FieldString, Default: "0", Description: "Emit a low-balance posture finding when prepaid credit falls below this USD threshold (0 = off)."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per gather."},
		},
	}
}

// Open reads configuration and builds the two read-only Bearer clients. It never fails for
// a missing credential: with neither key it runs offline (Snapshot returns the declared
// catalog; Gather emits nothing).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := strings.TrimRight(cfg.Get("management_base_url"), "/"); v != "" {
		s.managementBaseURL = v
	}
	if v := strings.TrimRight(cfg.Get("inference_base_url"), "/"); v != "" {
		s.inferenceBaseURL = v
	}
	s.teamID = strings.TrimSpace(cfg.Get("team_id"))
	s.manageKeys = cfg.GetBool("manage_keys", s.manageKeys)
	s.billing = cfg.GetBool("billing", s.billing)
	s.keyMaxAge = cfg.GetDuration("key_max_age", s.keyMaxAge)
	s.lookbackMonth = cfg.GetInt("lookback_months", s.lookbackMonth)
	if s.lookbackMonth <= 0 {
		s.lookbackMonth = defaultLookbackMonths
	}
	s.lowBalanceUSD = parseFloat(cfg.Get("low_balance_usd"))
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	if s.maxPages <= 0 {
		s.maxPages = defaultMaxPages
	}
	s.managementKey = cfg.Get("management_key")
	s.inferenceKey = cfg.Get("api_key")

	s.mgmtClient = modelprovider.NewClient(s.managementBaseURL, s.doer, modelprovider.AuthBearer, s.managementKey, nil)
	s.infClient = modelprovider.NewClient(s.inferenceBaseURL, s.doer, modelprovider.AuthBearer, s.inferenceKey, nil)
	return nil
}

// Gather pulls the enabled management-plane streams. It is a batch source: it returns nil
// when done. With no management key it returns nil immediately (the catalog half lives in
// Snapshot, not Gather). It resolves the team (config or /auth/management-keys/validation),
// then emits key/ACL posture and billed cost + FinOps posture. A 403/404 on a billing
// sub-surface (e.g. a prepaid endpoint on a postpaid team) degrades to a skip, not a
// failure.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.managementKey == "" || s.mgmtClient == nil {
		return nil // offline / catalog-only: nothing to pull from the management plane
	}
	team, err := s.resolveTeam(ctx)
	if err != nil {
		return err
	}
	if team == "" {
		return sink.Emit(ctx, s.teamUnresolvedFinding())
	}
	if s.manageKeys {
		if err := s.gatherKeyPosture(ctx, sink, team); err != nil {
			return err
		}
	}
	if s.billing {
		if err := s.gatherBilling(ctx, sink, team); err != nil {
			return err
		}
	}
	if err := s.gatherAuditEvents(ctx, sink, team); err != nil {
		return err
	}
	return nil
}

// Close releases resources; this connector holds none.
func (s *Source) Close(context.Context) error { return nil }

// resolveTeam returns the configured team id, or discovers it from the management key's
// validation endpoint (teamId, falling back to scopeId for an org-scoped key). An empty
// result means the team could not be resolved (the caller emits an honest posture finding).
func (s *Source) resolveTeam(ctx context.Context) (string, error) {
	if s.teamID != "" {
		return s.teamID, nil
	}
	var v validationResponse
	if err := s.mgmtClient.GetJSON(ctx, validationPath, nil, &v); err != nil {
		if isUnavailable(err) {
			return "", nil // not entitled / path differs: caller emits the unresolved finding
		}
		return "", err
	}
	return firstNonEmpty(v.TeamID, v.ScopeID), nil
}

// teamUnresolvedFinding is the honest degrade when no team id is configured and the
// validation endpoint did not yield one (so the team-scoped key/billing calls cannot run).
func (s *Source) teamUnresolvedFinding() model.FindingReport {
	return model.FindingReport{
		Kind:        "posture",
		Severity:    model.SeverityMedium,
		SubjectKind: subjectTeam,
		SubjectRef:  "xai",
		Title:       "xAI team not resolved; key/billing inventory skipped (set team_id or grant the management key validation access)",
		DetailHash:  redact.Hash("xai management key could not resolve a teamId via " + validationPath + "; set team_id in config to scope the key/ACL inventory and billing reads"),
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

// parseTime parses an RFC3339 timestamp, returning the zero time on any error.
func parseTime(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(v))
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// parseFloat parses a non-negative float, returning 0 on any error (no guessed value).
func parseFloat(v string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || f < 0 || f != f {
		return 0
	}
	return f
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

// isUnavailable reports whether err is a 403/404 (not entitled / wrong billing mode / path
// differs), so the connector can degrade to a skip or an honest posture finding instead of
// failing the gather. It never matches a transport error (which the engine retries).
func isUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "status 404") || strings.Contains(msg, "status 403")
}
