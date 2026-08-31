// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package mistral is the Olivares AI connector for Mistral AI / la Plateforme — the
// European frontier provider (README.md, module X multi-vendor). It is a read-only
// model/provider governance source built on the shared connectors/modelprovider
// contract, exactly like connectors/openai and connectors/codex.
//
// HONEST SCOPE (primary-source verified 2026-06-20, Admin API added 2026-06-28).
// Mistral's PUBLIC API surface has two tiers:
//
//   - VERIFIED-SHAPE and REAL: the Models API (GET /v1/models) returns the model
//     catalog with per-model capability booleans (completion_chat, function_calling,
//     vision, classification, …) and max_context_length. This is the connector's
//     authoritative, live core — Snapshot reads it and enriches each model with the
//     declared list pricing (catalog.go), so module X gets a real Mistral catalog.
//
//   - ADMIN API (beta, AdminApiKey): audit logs (GET /api/admin/audit-logs),
//     per-workspace usage (GET /api/admin/usage), org/workspace/key inventory
//     (GET /api/admin/users, /workspaces, /api-keys), and spend/rate limits.
//     BETA-VERIFIED (generated Admin API beta reference re-verified 2026-07-04).
//     A 403/404 degrades to a posture finding. The Admin API requires a distinct admin
//     API key (different from the inference key). The narrative docs say x-api-key with
//     base https://console.mistral.ai/api/admin; generated samples use Authorization:
//     Bearer. Admin requests therefore send BOTH headers (verified/documented
//     discrepancy, 2026-07-04). When admin_api_key is configured, these surfaces
//     supersede the UNVERIFIED-OFFLINE inventory seam for workspaces and keys.
//
//   - FALLBACK: OPT-IN UNVERIFIED-OFFLINE seam (manage_inventory, default off). When
//     admin_api_key is NOT set, the org/workspace + API-key inventory falls back to
//     the UNVERIFIED-OFFLINE seam with operator-overridable paths and Mistral's own
//     {object:"list",data:[…]} list convention. A 403/404 degrades to an honest
//     posture finding — the directory's honesty bar (connectors/codex, connectors/fal).
//
//   - INFERENCE COST: per-workspace/per-model usage from the Admin API's
//     GET /api/admin/usage is emitted as billed CostSamples (when the response includes
//     a currency field). Without an admin key, cost is metered around the inference
//     path via the exported Meter helper (provenance=estimated).
//
// Gather therefore emits, when credentialed with an API key: the honest
// "billing/usage/spending-cap is dashboard-only" coverage caveat. When additionally
// credentialed with an admin key: audit logs, billed usage CostSamples, org/user/key
// inventory, and spend/rate limit posture. When only manage_inventory is on (no admin
// key): the UNVERIFIED-OFFLINE workspace/key inventory + key-rotation posture.
//
// READ-ONLY and minimal-data (docs/SECURITY-HARDENING.md-3): every call is a GET via the shared
// GET-only modelprovider client (Bearer auth), so the connector CANNOT mutate Mistral;
// it carries token COUNTS, money and inventory METADATA — never prompts, completions or
// key values. It imports only the SDK and the Apache modelprovider contract, never the
// engine (enforced by scripts/check-boundary.sh).
package mistral

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.mistral"

// Default configuration values.
const (
	defaultBaseURL      = "https://api.mistral.ai"
	defaultAdminBaseURL = defaultBaseURL
	defaultModelsND     = "/v1/models"
	defaultKeyMaxAge    = 90 * 24 * time.Hour // rotate keys older than 90 days
	defaultMaxPages     = 20

	// costTypeMistral tags every Mistral CostSample so FinOps attributes Mistral spend
	// distinctly from other providers.
	costTypeMistral = "mistral"

	// UNVERIFIED-OFFLINE default inventory paths. Mistral publishes NO concrete REST
	// shape for org/workspace/key listing (the Admin API is documented only narratively),
	// so these are plausible defaults (Mistral's {object:"list",data:[…]} convention),
	// operator-overridable, used ONLY when manage_inventory is opted in, and a 403/404 on
	// them degrades to an honest posture finding (never a fabricated empty inventory).
	defaultWorkspacesPath = "/v1/workspaces"
	defaultKeysPath       = "/v1/api_keys"
)

// Source is the Mistral model/provider governance source connector. It satisfies
// sdk.SourceConnector (the honest coverage caveat + opt-in inventory posture as
// observations) and modelprovider.CatalogProvider (the live model catalog).
type Source struct {
	client      *modelprovider.Client
	adminClient *modelprovider.Client // Admin API (beta); nil when admin_api_key is empty

	credential   string
	adminAPIKey  string
	baseURL      string
	adminBaseURL string
	maxPages     int

	manageInventory bool
	workspacesPath  string
	keysPath        string
	keyMaxAge       time.Duration

	// Admin API inventory cache (populated by gatherAdminInventory, read by Snapshot).
	adminWorkspaceEntries []adminWorkspaceEntry
	adminKeyEntries       []adminAPIKeyEntry

	doer modelprovider.Doer // injected transport (tests); nil => default
	now  func() time.Time   // injectable clock (tests); nil => time.Now
}

// Compile-time proof Source satisfies both contracts.
var (
	_ sdk.SourceConnector           = (*Source)(nil)
	_ modelprovider.CatalogProvider = (*Source)(nil)
)

// New returns a Mistral source with default configuration.
func New() *Source {
	return &Source{
		baseURL:        defaultBaseURL,
		adminBaseURL:   defaultAdminBaseURL,
		maxPages:       defaultMaxPages,
		workspacesPath: defaultWorkspacesPath,
		keysPath:       defaultKeysPath,
		keyMaxAge:      defaultKeyMaxAge,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Mistral AI (catalog + admin governance + cost metering)",
		Description: "Read-only Mistral governance: live model catalog (GET /v1/models) + declared list pricing, cost metering around the inference path (Meter → estimated CostSample), and (with an Admin API key) audit logs, per-workspace billed usage, org/user/key inventory, and spend/rate limit posture. The Admin API is beta; a 403/404 degrades to a posture finding. The UNVERIFIED-OFFLINE inventory seam remains as a fallback when no admin key is set.",
		ConfigFields: []sdk.ConfigField{
			{Key: "api_key", Type: sdk.FieldString, Secret: true, Description: "Mistral API key reference (read-only Bearer; never persisted). For the opt-in inventory it must belong to a user with the org Admin role. Empty = offline catalog only."},
			{Key: "admin_api_key", Type: sdk.FieldString, Secret: true, Description: "Mistral Admin API key (beta). Enables audit logs, usage, and inventory governance. Empty = admin surfaces skipped (catalog-only mode continues)."},
			{Key: "base_url", Type: sdk.FieldString, Default: defaultBaseURL, Description: "Mistral API base URL (la Plateforme)."},
			{Key: "admin_base_url", Type: sdk.FieldString, Default: defaultAdminBaseURL, Description: "Mistral Admin API base URL. Defaults to the same base the connector has historically used. Docs disagree as of 2026-07-04: narrative says x-api-key with https://console.mistral.ai/api/admin; generated samples use Authorization: Bearer. The connector sends both headers."},
			{Key: "manage_inventory", Type: sdk.FieldBool, Default: "false", Description: "Opt in to org/workspace + API-key inventory and key-rotation posture via the UNVERIFIED-OFFLINE seam. OFF by default. When admin_api_key is set, the Admin API supersedes this for inventory."},
			{Key: "workspaces_path", Type: sdk.FieldString, Default: defaultWorkspacesPath, Description: "Override for the workspace-list path (UNVERIFIED-OFFLINE; correct per your tenant without a code change)."},
			{Key: "api_keys_path", Type: sdk.FieldString, Default: defaultKeysPath, Description: "Override for the API-key-list path (UNVERIFIED-OFFLINE). The API must return only masked key metadata, never a secret."},
			{Key: "key_max_age", Type: sdk.FieldDuration, Default: "2160h", Description: "Emit a rotation-posture finding for any inventoried key older than this (default 90 days)."},
			{Key: "max_pages", Type: sdk.FieldInt, Default: strconv.Itoa(defaultMaxPages), Description: "Pagination safety bound per gather."},
		},
	}
}

// Open reads configuration and builds the read-only Bearer client. It never fails for a
// missing credential: with no api_key the connector runs in offline catalog mode
// (Snapshot returns the declared catalog; Gather emits nothing).
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	if v := strings.TrimRight(cfg.Get("base_url"), "/"); v != "" {
		s.baseURL = v
	}
	s.adminBaseURL = s.baseURL
	if v := normalizeAdminBaseURL(cfg.Get("admin_base_url")); v != "" {
		s.adminBaseURL = v
	}
	s.manageInventory = cfg.GetBool("manage_inventory", s.manageInventory)
	if v := strings.TrimSpace(cfg.Get("workspaces_path")); v != "" {
		s.workspacesPath = v
	}
	if v := strings.TrimSpace(cfg.Get("api_keys_path")); v != "" {
		s.keysPath = v
	}
	s.keyMaxAge = cfg.GetDuration("key_max_age", s.keyMaxAge)
	s.maxPages = cfg.GetInt("max_pages", s.maxPages)
	if s.maxPages <= 0 {
		// A zero/negative bound would make every pull loop run zero times; floor it so a
		// misconfiguration never silently degrades a read (the openai/aws clamp pattern).
		s.maxPages = defaultMaxPages
	}
	s.credential = cfg.Get("api_key")
	s.adminAPIKey = cfg.Get("admin_api_key")

	s.client = modelprovider.NewClient(s.baseURL, s.doer, modelprovider.AuthBearer, s.credential, nil)
	if s.adminAPIKey != "" {
		s.adminClient = modelprovider.NewClient(s.adminBaseURL, s.doer, modelprovider.AuthBearer, s.adminAPIKey, map[string]string{"x-api-key": s.adminAPIKey})
	}
	return nil
}

// Gather emits the Mistral governance posture. It is a batch source: it returns nil when
// done. With no credential it returns nil immediately (offline → nothing pulled). It
// ALWAYS emits the honest coverage caveat (Mistral has no public usage/billing/cap API)
// when credentialed; only when manage_inventory is opted in does it attempt the
// UNVERIFIED-OFFLINE org/workspace/key inventory + rotation posture, degrading 403/404 to
// a posture finding rather than failing.
//
// When admin_api_key is set, the Admin API surfaces are gathered: audit logs, per-model
// billed usage, org/user/workspace/key inventory, and spend/rate limit posture. The Admin
// API supersedes the UNVERIFIED-OFFLINE inventory seam for workspaces and keys. Each admin
// surface degrades independently on 403/404 (beta/admin-key required).
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.credential == "" && s.adminAPIKey == "" {
		return nil // offline mode: nothing to pull
	}
	if s.credential != "" {
		if err := sink.Emit(ctx, s.coverageCaveat()); err != nil {
			return err
		}
	}
	// UNVERIFIED-OFFLINE inventory (legacy seam): only when manage_inventory is on AND
	// no admin key is set (admin API supersedes it when available).
	if s.manageInventory && s.adminClient == nil {
		if err := s.gatherInventoryPosture(ctx, sink); err != nil {
			return err
		}
	}
	// Admin API surfaces (BETA-VERIFIED): only when admin_api_key is configured.
	if s.adminClient != nil {
		if err := s.gatherAuditLogs(ctx, sink); err != nil {
			return err
		}
		if err := s.gatherAdminUsage(ctx, sink); err != nil {
			return err
		}
		if err := s.gatherAdminInventory(ctx, sink); err != nil {
			return err
		}
		if err := s.gatherSpendPosture(ctx, sink); err != nil {
			return err
		}
	}
	return nil
}

// Close releases resources; this connector holds none.
func (s *Source) Close(context.Context) error { return nil }

// clock returns the connector's time source (injectable for tests).
func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// unixTime converts a Unix-seconds timestamp to UTC, returning the zero time for a
// zero/absent value so a missing provider timestamp never aborts a read.
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

// isUnavailable reports whether err is a 403/404 (not entitled / path differs on this
// tenant) from the UNVERIFIED-OFFLINE inventory surface, so the connector can degrade to
// an honest posture finding instead of failing the gather. It never matches a transport
// error (which the engine retries).
func isUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "status 404") || strings.Contains(msg, "status 403")
}

func normalizeAdminBaseURL(v string) string {
	v = strings.TrimRight(strings.TrimSpace(v), "/")
	if strings.HasSuffix(v, "/api/admin") {
		return strings.TrimRight(strings.TrimSuffix(v, "/api/admin"), "/")
	}
	return v
}
