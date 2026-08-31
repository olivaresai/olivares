// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package codex is the Olivares AI connector for OpenAI Codex — the coding-agent
// surface — read through its enterprise governance APIs (G4, §5). Endpoint
// locations were verified 2026-07-04 against OpenAI's Codex enterprise governance and
// admin setup docs. It turns four Codex governance surfaces into the canonical
// observation streams:
//
//   - Analytics API (https://api.chatgpt.com/v1/analytics/codex,
//     GET /workspaces/{workspace_id}/usage; 90d max lookback) →
//     one model.CostSample per user/workspace/model bucket (the Codex-attributed
//     "cost.sampled" estimate that feeds module XI / FinOps, CostType="codex"), plus
//     an adoption inventory finding.
//   - Compliance Logs Platform (https://api.chatgpt.com/v1/,
//     GET /compliance/workspaces/{workspace_id}/logs then
//     /logs/{log_file_id}; CODEX_LOG/CODEX_SECURITY_LOG, JSONL, up to 30d retention)
//     → one minimal-data external_activity FindingReport per record, appended to the
//     tamper-evident ledger as audit/eDiscovery evidence. Coverage is ChatGPT-
//     authenticated Codex usage only; API-key-authenticated Codex usage is not included.
//   - Audit Logs API (org audit events) → external_activity evidence findings.
//   - Costs API (billed org cost) → authoritative billed CostSamples (opt-in).
//
// Known but not ingested: Analytics sibling endpoints /code_reviews and
// /code_review_responses, plus Compliance /codex_tasks and /codex_environments. Their
// row-level fields are UNVERIFIED-FIELDS (2026-07-04: admin portal only), so this
// connector does not present those fields as verified.
//
// It also exposes a modelprovider.Catalog (the declared Codex model family + the
// workspace/automation-key inventory) to module X through the CatalogProvider seam.
//
// IDENTITY (never subscription). The automation credential is an OpenAI
// API key (recommended for CI/CD) OR a Codex workspace ACCESS TOKEN (workspace
// identity) — both presented as a Bearer credential. A personal ChatGPT/Codex
// CONSUMER SUBSCRIPTION is NEVER used: proxying it for third-party/programmatic use
// violates OpenAI's terms exactly as a consumer Claude subscription does for
// Anthropic. There is no subscription config field by design.
//
// READ-ONLY and minimal-data (docs/SECURITY-HARDENING.md-3): every call is a GET via the shared
// GET-only modelprovider client, so the connector CANNOT mutate Codex; it carries
// token counts, money, adoption metrics and inventory METADATA — never prompt/diff
// content or key values (the admin-keys API returns only a masked value). It imports
// only the SDK and the Apache modelprovider contract, never the engine.
//
// HONEST DEGRADATION (the session brief): Costs and Audit Logs remain on
// api.openai.com. Codex Analytics and Compliance are per-workspace ChatGPT enterprise
// APIs under api.chatgpt.com; their endpoints/params/envelopes are verified, but row
// fields beyond the public token vocabulary are marked UNVERIFIED-FIELDS. A 403/404
// degrades to an "ingest unavailable" posture finding rather than failing the whole
// gather.
package codex

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/netbind"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.codex"

// Default configuration values.
const (
	// Medido contra codex-cli 0.147.0: el exportador hace POST a la RAÍZ del endpoint
	// autorizado, no a /v1/logs.
	defaultOTLPAddr = "127.0.0.1:4318"
	defaultOTLPPath = "/"

	defaultBaseURL        = "https://api.openai.com"
	defaultChatGPTBaseURL = "https://api.chatgpt.com"
	defaultLookback       = 24 * time.Hour
	defaultMaxPages       = 20
	maxAnalyticsLookback  = 90 * 24 * time.Hour

	// authModeAPIKey / authModeAccessToken record WHICH automation identity the
	// credential is (governance clarity); both are sent as a Bearer credential.
	authModeAPIKey      = "api_key"
	authModeAccessToken = "access_token"

	// costTypeCodex tags every Codex CostSample so FinOps attributes Codex spend
	// distinctly from raw OpenAI API spend (the openai connector).
	costTypeCodex = "codex"

	// Endpoint paths. The org Costs/Audit-logs paths are the VERIFIED OpenAI org APIs;
	// Analytics/Compliance are VERIFIED 2026-07-04 ChatGPT per-workspace APIs.
	defaultAnalyticsPath  = "/v1/analytics/codex/workspaces/{workspace_id}/usage"
	defaultCompliancePath = "/v1/compliance/workspaces/{workspace_id}/logs"
	costsPath             = "/v1/organization/costs"
	auditLogsPath         = "/v1/organization/audit_logs"
	projectsPath          = "/v1/organization/projects"
	adminKeysPath         = "/v1/organization/admin_api_keys"

	// findingKindActivity is the module-XIII evidence Kind (shared with claude-compliance):
	// Codex compliance/audit records count as external_activity audit evidence.
	findingKindActivity = "external_activity"
)

// defaultComplianceLogTypes is the verified Codex Compliance stream set.
var defaultComplianceLogTypes = []string{"CODEX_LOG", "CODEX_SECURITY_LOG"}

// Source is the Codex governance source connector. It satisfies sdk.SourceConnector
// (analytics/cost/compliance/audit as observations) and modelprovider.CatalogProvider
// (the Codex model + inventory catalog).
type Source struct {
	client           *modelprovider.Client
	analyticsClient  *modelprovider.Client
	complianceClient *modelprovider.Client

	credential        string
	authMode          string
	baseURL           string
	analyticsBaseURL  string
	complianceBaseURL string
	workspaceID       string
	projectID         string
	lookback          time.Duration

	// Receptor OTLP (AGT-02). Nil mientras no se habilite: un receptor apagado no debe reservar
	// puerto ni aparecer como superficie activa.
	otlp            *otlpReceiver
	otlpEnabled     bool
	otlpAddr        string
	otlpPath        string
	otlpAllowPublic bool
	otlpLis         net.Listener
	maxPages        int
	bucketWidth     string

	analytics      bool
	compliance     bool
	audit          bool
	costs          bool
	attributeEmail bool // when true, the Analytics Actor ref is the user email; default is the stable user_id

	analyticsPath        string
	compliancePath       string
	complianceLogTypes   []string
	compliancePromptScan bool

	doer modelprovider.Doer // injected transport (tests); nil => default
	now  func() time.Time   // injectable clock (tests); nil => time.Now
}

// Compile-time proof Source satisfies both contracts.
var (
	_ sdk.SourceConnector           = (*Source)(nil)
	_ modelprovider.CatalogProvider = (*Source)(nil)
)

// New returns a Codex source with default configuration.
func New() *Source {
	return &Source{
		authMode:           authModeAPIKey,
		baseURL:            defaultBaseURL,
		analyticsBaseURL:   defaultChatGPTBaseURL,
		complianceBaseURL:  defaultChatGPTBaseURL,
		lookback:           defaultLookback,
		maxPages:           defaultMaxPages,
		bucketWidth:        "1d",
		analytics:          true,
		compliance:         true,
		audit:              true,
		costs:              false,
		analyticsPath:      defaultAnalyticsPath,
		compliancePath:     defaultCompliancePath,
		complianceLogTypes: defaultComplianceLogTypes,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "OpenAI Codex (governance)",
		Description: "Reads OpenAI Codex usage/adoption, per-workspace ChatGPT Compliance logs, audit logs and billed cost via verified enterprise governance APIs (read-only; endpoints verified 2026-07-04). Auth = Platform API key with Codex enterprise scopes or workspace access token; never a consumer subscription.",
		ConfigFields: []sdk.ConfigField{
			{Key: "api_key", Type: sdk.FieldString, Secret: true, Description: "Codex automation credential reference: an OpenAI API key OR a Codex workspace access token (read-only; never persisted). A consumer ChatGPT/Codex subscription is NOT supported (ToS). Empty = offline catalog only."},
			{Key: "auth_mode", Type: sdk.FieldString, Default: authModeAPIKey, Description: "Identity type of the credential for governance clarity: api_key or access_token. Both are sent as a Bearer credential."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "OpenAI/Codex API base URL."},
			{Key: "workspace_id", Type: sdk.FieldString, Description: "ChatGPT workspace id. Required for Codex Analytics and Compliance because those APIs are per-workspace."},
			{Key: "otlp_http", Type: sdk.FieldBool, Default: "false", Description: "serve the OTLP/HTTP log receiver for Codex (AGT-02; off by default — it opens a port)"},
			{Key: "otlp_http_addr", Type: sdk.FieldString, Default: defaultOTLPAddr, Description: "OTLP/HTTP listen address for the Codex receiver"},
			{Key: "otlp_path", Type: sdk.FieldString, Default: defaultOTLPPath, Description: "path the Codex exporter POSTs to — MEASURED as \"/\", not /v1/logs: the authorized endpoint is used as given"},
			{Key: "otlp_allow_public_bind", Type: sdk.FieldBool, Default: "false", Description: "DANGEROUS: allow binding the OTLP receiver off loopback; the ingest is UNAUTHENTICATED"},
			{Key: "analytics_base_url", Type: sdk.FieldString, Default: defaultChatGPTBaseURL, Description: "Base URL for Codex Analytics (default https://api.chatgpt.com)."},
			{Key: "compliance_base_url", Type: sdk.FieldString, Default: defaultChatGPTBaseURL, Description: "Base URL for ChatGPT Compliance logs (default https://api.chatgpt.com)."},
			{Key: "project_id", Type: sdk.FieldString, Description: "Optional Codex project/workspace filter for analytics and billed cost (scopes the Costs API to Codex spend)."},
			{Key: "lookback", Type: sdk.FieldDuration, Default: "24h", Description: "How far back to pull analytics/cost/audit/compliance on each Gather."},
			{Key: "bucket_width", Type: sdk.FieldString, Default: "1d", Description: "Legacy compatibility field. Codex Analytics /usage is queried with group_by=day."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per gather."},
			{Key: "analytics", Type: sdk.FieldBool, Default: "true", Description: "Pull the Codex Analytics API (usage/adoption → Codex CostSamples + adoption findings)."},
			{Key: "compliance", Type: sdk.FieldBool, Default: "true", Description: "Pull the Compliance Logs Platform (Codex Usage/Auth/Admin Audit JSONL → audit evidence)."},
			{Key: "compliance_prompt_scan", Type: sdk.FieldBool, Default: "false", Description: "When true, transiently scan raw Compliance JSONL lines for secret-shaped tokens, instruction-injection markers and invisible Unicode; emits structural posture findings only, never raw content."},
			{Key: "audit", Type: sdk.FieldBool, Default: "true", Description: "Pull the org Audit Logs API (audit events → audit evidence)."},
			{Key: "costs", Type: sdk.FieldBool, Default: "false", Description: "Also pull the billed Costs API (authoritative, daily). Off by default: it is org-wide unless project_id scopes it to Codex — enable with project_id to avoid counting non-Codex spend."},
			{Key: "attribute_email", Type: sdk.FieldBool, Default: "false", Description: "Use the developer email as the cost Actor ref. Default false: the stable user_id is used so per-developer chargeback carries an id, not PII (docs/08 §3)."},
			{Key: "compliance_log_types", Type: sdk.FieldString, Default: strings.Join(defaultComplianceLogTypes, ","), Description: "Comma-separated Compliance event_type values to pull: CODEX_LOG,CODEX_SECURITY_LOG."},
			{Key: "analytics_path", Type: sdk.FieldString, Default: defaultAnalyticsPath, Description: "Override for the Analytics API path, relative to analytics_base_url. Supports {workspace_id} substitution."},
			{Key: "compliance_path", Type: sdk.FieldString, Default: defaultCompliancePath, Description: "Override for the Compliance Logs list path, relative to compliance_base_url. Supports {workspace_id} substitution; downloads append /{log_file_id}."},
		},
	}
}

// Open reads configuration and builds the read-only Bearer client. It never fails for
// a missing credential: with no api_key the connector runs in offline catalog mode
// (Snapshot returns the declared catalog; Gather emits nothing).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := strings.TrimRight(cfg.Get("base_url"), "/"); v != "" {
		s.baseURL = v
	}
	if v := strings.TrimRight(cfg.Get("analytics_base_url"), "/"); v != "" {
		s.analyticsBaseURL = v
	}
	if v := strings.TrimRight(cfg.Get("compliance_base_url"), "/"); v != "" {
		s.complianceBaseURL = v
	}
	if v := cfg.Get("auth_mode"); v == authModeAccessToken {
		s.authMode = authModeAccessToken
	} else {
		s.authMode = authModeAPIKey
	}
	// ⛔ EL RECEPTOR SE LIGA EN Open, no en Gather, y por la misma razón que en `claude`: un puerto
	//    ocupado tiene que fallar ANTES, donde el SDK espera el error, y no a mitad de una recogida.
	s.otlpEnabled = cfg.GetBool("otlp_http", false)
	s.otlpAddr = strings.TrimSpace(cfg.Get("otlp_http_addr"))
	if s.otlpAddr == "" {
		s.otlpAddr = defaultOTLPAddr
	}
	s.otlpPath = strings.TrimSpace(cfg.Get("otlp_path"))
	if s.otlpPath == "" {
		s.otlpPath = defaultOTLPPath
	}
	s.otlpAllowPublic = cfg.GetBool("otlp_allow_public_bind", false)
	if s.otlpEnabled {
		lis, err := netbind.Listen(context.Background(), "tcp", s.otlpAddr, netbind.Policy{
			Component:   Name,
			Purpose:     "OTLP/HTTP receiver",
			AllowPublic: s.otlpAllowPublic,
			OptIn:       "otlp_allow_public_bind",
		})
		if err != nil {
			return fmt.Errorf("codex: bind OTLP/HTTP %s: %w", s.otlpAddr, err)
		}
		s.otlpLis = lis
		s.otlp = newOTLPReceiver(s.otlpPath, 0, nil)
		s.otlp.serve(lis)
	}
	s.workspaceID = strings.TrimSpace(cfg.Get("workspace_id"))
	s.projectID = strings.TrimSpace(cfg.Get("project_id"))
	s.lookback = cfg.GetDuration("lookback", s.lookback)
	if v := cfg.Get("bucket_width"); v != "" {
		s.bucketWidth = v
	}
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	if s.maxPages <= 0 {
		s.maxPages = defaultMaxPages
	}
	s.analytics = cfg.GetBool("analytics", s.analytics)
	s.compliance = cfg.GetBool("compliance", s.compliance)
	s.compliancePromptScan = cfg.GetBool("compliance_prompt_scan", s.compliancePromptScan)
	s.audit = cfg.GetBool("audit", s.audit)
	s.costs = cfg.GetBool("costs", s.costs)
	s.attributeEmail = cfg.GetBool("attribute_email", s.attributeEmail)
	if v := strings.TrimSpace(cfg.Get("analytics_path")); v != "" {
		s.analyticsPath = v
	}
	if v := strings.TrimSpace(cfg.Get("compliance_path")); v != "" {
		s.compliancePath = v
	}
	if v := strings.TrimSpace(cfg.Get("compliance_log_types")); v != "" {
		s.complianceLogTypes = splitCSV(v)
	}
	for _, t := range s.complianceLogTypes {
		if !knownComplianceLogType(t) {
			return fmt.Errorf("codex: compliance_log_types contains %q (allowed: CODEX_LOG,CODEX_SECURITY_LOG)", t)
		}
	}
	s.credential = cfg.Get("api_key")

	s.client = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthBearer, s.credential, nil)
	s.analyticsClient = modelprovider.NewClient(s.analyticsBaseURL, s.doer, modelprovider.AuthBearer, s.credential, nil)
	s.complianceClient = modelprovider.NewClient(s.complianceBaseURL, s.doer, modelprovider.AuthBearer, s.credential, nil)
	return nil
}

// Gather pulls the enabled Codex governance streams and emits their observations. It
// is a batch source: it returns nil when the windows are drained. With no credential
// it returns nil immediately (offline → nothing pulled, an honest absence). A 403/404
// on the UNVERIFIED Codex enterprise surfaces (Analytics/Compliance) degrades to a
// posture finding and does NOT abort the run; a transient error is returned so the
// engine retries.
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.credential == "" || s.client == nil {
		return nil // offline mode: nothing to pull
	}
	workspaceSurfaces := s.analytics || s.compliance
	workspaceReady := strings.TrimSpace(s.workspaceID) != ""
	if workspaceSurfaces && !workspaceReady {
		if err := sink.Emit(ctx, s.workspaceIDMissingFinding()); err != nil {
			return err
		}
	}
	if s.analytics && workspaceReady {
		if err := s.gatherAnalytics(ctx, sink); err != nil {
			return err
		}
	}
	if s.compliance && workspaceReady {
		if err := s.gatherCompliance(ctx, sink); err != nil {
			return err
		}
	}
	if s.audit {
		if err := s.gatherAudit(ctx, sink); err != nil {
			return err
		}
	}
	if s.costs {
		if err := s.gatherCosts(ctx, sink); err != nil {
			return err
		}
	}
	// ⛔ EL DRENAJE VA AL FINAL Y FUERA DEL `credential == ""` DE ARRIBA. El receptor no necesita
	//    credencial —lo que llega, llega— así que un despliegue SIN clave de API sigue recibiendo
	//    telemetría y debe poder entregarla. Colocarlo arriba lo habría atado al modo con
	//    credencial y el receptor habría quedado mudo justo en el caso air-gapped, que es donde más
	//    valor tiene.
	s.drainOTLP()
	return nil
}

// drainOTLP vacía el buffer del receptor. Hoy sólo cuenta lo recibido y lo descartado; convertir
// esos eventos en observaciones es el siguiente paso y necesita decidir su mapeo, que no se inventa
// aquí: los NOMBRES están medidos (`codex.startup_phase`, `codex.user_prompt`, `codex.api_request`,
// `codex.auth_recovery`, `codex.websocket_connect`, `codex.conversation_starts`) y lo que cada uno
// significa para el plano de sesión es una decisión de contrato, no de este fichero.
func (s *Source) drainOTLP() (int, int) {
	if s.otlp == nil {
		return 0, 0
	}
	evs, dropped := s.otlp.drain()
	return len(evs), dropped
}

// Close releases resources; this connector holds none.
func (s *Source) Close(ctx context.Context) error {
	if s.otlp != nil {
		return s.otlp.close(ctx)
	}
	if s.otlpLis != nil {
		return s.otlpLis.Close()
	}
	return nil
}

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// startTime is the lookback window start (UTC), used by the bucketed endpoints.
func (s *Source) startTime() time.Time {
	return s.clock().Add(-s.lookback).UTC()
}

// analyticsStartTime clamps Codex Analytics to the documented 90-day maximum lookback.
// This is a query-window fact, not a posture finding: the connector cannot recover older
// data from the API, so it honestly requests the maximum supported window.
func (s *Source) analyticsStartTime() time.Time {
	lookback := s.lookback
	if lookback > maxAnalyticsLookback {
		lookback = maxAnalyticsLookback
	}
	return s.clock().Add(-lookback).UTC()
}

// startQuery sets start_time on a query to the lookback window (org-API Unix-seconds).
func (s *Source) setStart(q url.Values) {
	q.Set("start_time", strconv.FormatInt(s.startTime().Unix(), 10))
}

func (s *Source) analyticsPathResolved() string {
	return workspacePath(s.analyticsPath, s.workspaceID)
}

func (s *Source) compliancePathResolved() string {
	return workspacePath(s.compliancePath, s.workspaceID)
}

func workspacePath(path, workspaceID string) string {
	return strings.ReplaceAll(path, "{workspace_id}", url.PathEscape(strings.TrimSpace(workspaceID)))
}

func knownComplianceLogType(t string) bool {
	return t == "CODEX_LOG" || t == "CODEX_SECURITY_LOG"
}

// dollarsToMicroUSD converts a major-unit (dollars) amount to integer micro-USD
// (1 USD = 1_000_000 µUSD). A negative/NaN amount clamps to 0 (unknown), never a
// guessed cost (ARCHITECTURE.md).
func dollarsToMicroUSD(value float64) int64 {
	if value <= 0 || value != value { // value!=value is the NaN guard
		return 0
	}
	return int64(value*1_000_000 + 0.5)
}

// unixTime converts Unix-seconds to UTC, returning the zero time for a zero value so a
// missing provider timestamp never aborts a run.
func unixTime(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
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

// splitCSV splits a comma-separated list, trimming spaces and dropping empties.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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

// isUnavailable reports whether err is a "not entitled / not found" response (403/404)
// from an UNVERIFIED enterprise surface, so the connector can degrade to an honest
// posture finding instead of failing the gather. The modelprovider client surfaces the
// status in the error string; this never matches a transport error (which is retried).
func isUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "status 404") || strings.Contains(msg, "status 403")
}
