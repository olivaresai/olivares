// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package vertex

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.vertex"

// version is the connector's own semantic version.
const version = "0.1.0"

// Configuration keys (declared in the Descriptor, read in Open).
const (
	cfgCredentialsJSON          = "credentials_json"
	cfgCredentialsFile          = "credentials_file"
	cfgAccessToken              = "access_token"
	cfgTokenURI                 = "token_uri"
	cfgProject                  = "project"
	cfgPublishers               = "publishers"
	cfgEnableUsage              = "enable_usage"
	cfgEnableModelArmor         = "enable_model_armor"
	cfgEnableSanitizationIngest = "enable_sanitization_ingest"
	cfgModelArmorOrg            = "model_armor_org"
	cfgModelArmorFolders        = "model_armor_folders"
	cfgExpectFloorEnforce       = "expect_floor_enforcement"
	cfgExpectFloorBlock         = "expect_floor_block"
	cfgExpectFloorLogging       = "expect_floor_logging"
	cfgCostExportURL            = "cost_export_url"
	cfgUsageLookback            = "usage_lookback"
	cfgUsageAlignment           = "usage_alignment"
	cfgSanitizationLookback     = "sanitization_lookback"
	cfgModelArmorLocations      = "model_armor_locations"
	cfgMaxPages                 = "max_pages"
	cfgAIPlatformEndpoint       = "aiplatform_endpoint"
	cfgMonitoringEndpoint       = "monitoring_endpoint"
	cfgLoggingEndpoint          = "logging_endpoint"
	cfgModelArmorEndpoint       = "model_armor_endpoint"
	cfgModelArmorGlobalURL      = "model_armor_global_endpoint"
	cfgTimeout                  = "timeout"
)

// Defaults.
const (
	defaultAIPlatformEndpoint = "https://aiplatform.googleapis.com"
	defaultMonitoringEndpoint = "https://monitoring.googleapis.com"
	defaultLoggingEndpoint    = "https://logging.googleapis.com"
	// defaultModelArmorEndpoint is the REGIONAL Model Armor template host pattern. The
	// literal ".rep." segment and the {location} substitution were VERIFIED 2026-07-05
	// against the Model Armor v1 discovery document (revision 20260624) and docs pages
	// last updated 2026-06-29. The global modelarmor.googleapis.com host is used only for
	// floor settings; see modelarmor.go.
	defaultModelArmorEndpoint   = "https://modelarmor.{location}.rep.googleapis.com"
	defaultModelArmorGlobalURL  = "https://modelarmor.googleapis.com"
	defaultPublishers           = "google,anthropic"
	defaultUsageLookback        = time.Hour
	defaultUsageAlignment       = time.Hour
	defaultSanitizationLookback = time.Hour
	defaultMaxPages             = 50
	defaultTimeout              = 30 * time.Second
)

// config is the resolved connector configuration. The credential fields hold secret
// values in memory only; they are never logged or emitted.
type config struct {
	tokens tokenSource // nil ⇒ offline (no credential): live catalog/usage/armor are no-ops.

	project    string   // GCP project id — required for monitoring + Model Armor reads.
	publishers []string // model-catalog publishers to enrich (google, anthropic).

	enableUsage              bool
	enableModelArmor         bool
	enableSanitizationIngest bool

	modelArmorOrg     string
	modelArmorFolders []string

	expectFloorEnforce bool
	expectFloorBlock   bool
	expectFloorLogging bool

	// costExportURL, when set, is the operator-wired billing-export RESULT the connector
	// GETs for BILLED cost (GCP has no real-time cost API). Empty ⇒ no billed-cost stream.
	costExportURL string

	usageLookback        time.Duration
	usageAlignment       time.Duration
	sanitizationLookback time.Duration

	modelArmorLocations []string // regions to read Model Armor templates for.

	maxPages int

	aiplatformEndpoint  string
	monitoringEndpoint  string
	loggingEndpoint     string
	modelArmorEndpoint  string // regional template host pattern (carries {location}).
	modelArmorGlobalURL string // global floor-setting host.

	timeout time.Duration
}

// descriptor is the connector's stable self-description.
func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Gemini Enterprise Agent Platform (formerly Vertex AI) catalog + usage + cost + Model Armor",
		Description: "Read-only Gemini Enterprise Agent Platform — formerly Vertex AI — (the enterprise surface, distinct from AI Studio): the Gemini + Claude-on-platform foundation-model catalog (declared + live launch-stage enrichment), per-model token usage from Cloud Monitoring emitted as CostSamples (token counts only, cost derived from list pricing — never a prompt/completion), opt-in billed cost from an operator-wired billing-export result (GCP has no real-time cost API), and opt-in Model Armor safety posture (templates + floor settings: RAI filters, prompt-injection/jailbreak, malicious-URI, Sensitive-Data-Protection — config state only, never the paid data-plane). IAM and audit-log activity are deferred to the gcp-audit connector.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgCredentialsJSON, Type: sdk.FieldString, Secret: true, Description: "service-account key JSON (inline). Empty + no credentials_file/access_token ⇒ offline (declared catalog only)."},
			{Key: cfgCredentialsFile, Type: sdk.FieldString, Description: "path to a service-account key JSON file (used when credentials_json is empty)."},
			{Key: cfgAccessToken, Type: sdk.FieldString, Secret: true, Description: "pre-issued OAuth2 access token (WIF/ADC sidecar). Overrides the service-account key when set."},
			{Key: cfgTokenURI, Type: sdk.FieldString, Description: "OAuth2 token endpoint override (defaults to the key's token_uri or oauth2.googleapis.com/token)."},
			{Key: cfgProject, Type: sdk.FieldString, Description: "GCP project id — required for Cloud Monitoring usage and Model Armor reads (the catalog publisher-model reads are global and need only a credential)."},
			{Key: cfgPublishers, Type: sdk.FieldString, Default: defaultPublishers, Description: "comma-separated model-catalog publishers to enrich (google = Gemini, anthropic = Claude on Vertex)."},
			{Key: cfgEnableUsage, Type: sdk.FieldBool, Default: "true", Description: "read per-model token usage from Cloud Monitoring (token_count on the PublisherModel resource) and emit CostSamples with derived cost."},
			{Key: cfgEnableModelArmor, Type: sdk.FieldBool, Default: "false", Description: "read Model Armor templates + project/org/folder floor settings as safety_posture findings (opt-in; off by default). VERIFIED 2026-07-05 against the Model Armor v1 discovery document revision 20260624 and docs pages last updated 2026-06-29. Reads config state only, never paid sanitization; read-only posture reads do not consume the per-sanitized-token meter. Inline Gemini generateContent floors can apply even when modelArmorConfig is omitted; request templates override floors and are mutually exclusive with safety_settings. streamGenerateContent coverage is UNVERIFIED, the Gemini Developer API is not covered, and the integration is documented fail-open. Image-modality screening and streaming sanitization Preview surfaces are deliberately not read."},
			{Key: cfgEnableSanitizationIngest, Type: sdk.FieldBool, Default: "false", Description: "read Model Armor sanitization RESULTS from Cloud Logging (verdict/filter metadata only — never the logged prompt/response text). Requires upstream logging enabled (floor enableCloudLogging or template logSanitizeOperations), uses the read-only Cloud Logging entries:list POST, and entries carry no end-user principal."},
			{Key: cfgModelArmorOrg, Type: sdk.FieldString, Description: "optional organization ID whose global Model Armor floor setting is read as a conformance baseline. Empty ⇒ skip. Org/folder floors are baselines; runtime integratedServices enforcement is project-level only."},
			{Key: cfgModelArmorFolders, Type: sdk.FieldString, Description: "comma-separated folder IDs whose global Model Armor floor settings are read as conformance baselines in CSV order. Empty ⇒ skip. Lower levels take precedence: project > folder > org."},
			{Key: cfgExpectFloorEnforce, Type: sdk.FieldBool, Default: "false", Description: "declared-baseline drift check for the project runtime floor: it must exist, enable floor-setting enforcement, and bind AI_PLATFORM. Emits policy_drift when violated."},
			{Key: cfgExpectFloorBlock, Type: sdk.FieldBool, Default: "false", Description: "declared-baseline drift check requiring the project AI Platform floor leg to inspect-and-block. Also treats a missing floor as drift."},
			{Key: cfgExpectFloorLogging, Type: sdk.FieldBool, Default: "false", Description: "declared-baseline drift check requiring aiPlatformFloorSetting.enableCloudLogging=true for the project floor. Also treats a missing floor as drift; Cloud Logging ingest is separate from this read-only connector."},
			{Key: cfgCostExportURL, Type: sdk.FieldString, Description: "optional URL of an operator-wired billing-export result (GCP has no real-time cost API; actual Vertex cost lives only in BigQuery billing export). Empty ⇒ no billed-cost stream (the derived-cost usage stream still stands)."},
			{Key: cfgUsageLookback, Type: sdk.FieldDuration, Default: defaultUsageLookback.String(), Description: "Cloud Monitoring lookback window for token usage."},
			{Key: cfgUsageAlignment, Type: sdk.FieldDuration, Default: defaultUsageAlignment.String(), Description: "Cloud Monitoring per-bucket alignment period (token_count is a DELTA metric, summed per bucket)."},
			{Key: cfgSanitizationLookback, Type: sdk.FieldDuration, Default: defaultSanitizationLookback.String(), Description: "Cloud Logging entries:list timestamp window for Model Armor sanitization-result ingest."},
			{Key: cfgModelArmorLocations, Type: sdk.FieldString, Description: "comma-separated regions to read Model Armor templates for (e.g. us-central1,europe-west4). Empty ⇒ templates are skipped and only the global floor setting is read."},
			{Key: cfgMaxPages, Type: sdk.FieldInt, Default: fmt.Sprintf("%d", defaultMaxPages), Description: "max API pages per list operation (pagination safety bound)."},
			{Key: cfgAIPlatformEndpoint, Type: sdk.FieldString, Default: defaultAIPlatformEndpoint, Description: "Gemini Enterprise Agent Platform (aiplatform) endpoint base URL — global host for publisher-model catalog reads (override for testing)."},
			{Key: cfgMonitoringEndpoint, Type: sdk.FieldString, Default: defaultMonitoringEndpoint, Description: "Cloud Monitoring endpoint base URL (override for testing)."},
			{Key: cfgLoggingEndpoint, Type: sdk.FieldString, Default: defaultLoggingEndpoint, Description: "Cloud Logging endpoint base URL for Model Armor sanitization-result entries:list reads (override for testing)."},
			{Key: cfgModelArmorEndpoint, Type: sdk.FieldString, Default: defaultModelArmorEndpoint, Description: "Model Armor REGIONAL template host pattern; {location} is substituted per region (override for testing)."},
			{Key: cfgModelArmorGlobalURL, Type: sdk.FieldString, Default: defaultModelArmorGlobalURL, Description: "Model Armor GLOBAL endpoint base URL for floor settings (override for testing)."},
			{Key: cfgTimeout, Type: sdk.FieldDuration, Default: defaultTimeout.String(), Description: "per-request HTTP timeout."},
		},
	}
}

// loadConfig resolves the connector configuration, applying defaults and parsing the
// credential. A MISSING credential is offline-safe (no token source ⇒ live reads are
// no-ops; the declared catalog and any no-auth cost export still work), mirroring the
// gemini/azure connectors. Secret values are read here and held in memory only.
func loadConfig(cfg sdk.Config, client *http.Client) (config, error) {
	c := config{
		project:                  strings.TrimSpace(cfg.Get(cfgProject)),
		publishers:               splitCSVLower(firstNonEmpty(cfg.Get(cfgPublishers), defaultPublishers)),
		enableUsage:              cfg.GetBool(cfgEnableUsage, true),
		enableModelArmor:         cfg.GetBool(cfgEnableModelArmor, false),
		enableSanitizationIngest: cfg.GetBool(cfgEnableSanitizationIngest, false),
		modelArmorOrg:            strings.TrimSpace(cfg.Get(cfgModelArmorOrg)),
		modelArmorFolders:        splitCSV(cfg.Get(cfgModelArmorFolders)),
		expectFloorEnforce:       cfg.GetBool(cfgExpectFloorEnforce, false),
		expectFloorBlock:         cfg.GetBool(cfgExpectFloorBlock, false),
		expectFloorLogging:       cfg.GetBool(cfgExpectFloorLogging, false),
		costExportURL:            strings.TrimSpace(cfg.Get(cfgCostExportURL)),
		usageLookback:            cfg.GetDuration(cfgUsageLookback, defaultUsageLookback),
		usageAlignment:           cfg.GetDuration(cfgUsageAlignment, defaultUsageAlignment),
		sanitizationLookback:     cfg.GetDuration(cfgSanitizationLookback, defaultSanitizationLookback),
		modelArmorLocations:      splitCSV(cfg.Get(cfgModelArmorLocations)),
		maxPages:                 cfg.GetInt(cfgMaxPages, defaultMaxPages),
		aiplatformEndpoint:       firstNonEmpty(strings.TrimSpace(cfg.Get(cfgAIPlatformEndpoint)), defaultAIPlatformEndpoint),
		monitoringEndpoint:       firstNonEmpty(strings.TrimSpace(cfg.Get(cfgMonitoringEndpoint)), defaultMonitoringEndpoint),
		loggingEndpoint:          firstNonEmpty(strings.TrimSpace(cfg.Get(cfgLoggingEndpoint)), defaultLoggingEndpoint),
		modelArmorEndpoint:       firstNonEmpty(strings.TrimSpace(cfg.Get(cfgModelArmorEndpoint)), defaultModelArmorEndpoint),
		modelArmorGlobalURL:      firstNonEmpty(strings.TrimSpace(cfg.Get(cfgModelArmorGlobalURL)), defaultModelArmorGlobalURL),
		timeout:                  cfg.GetDuration(cfgTimeout, defaultTimeout),
	}
	if c.usageLookback <= 0 {
		c.usageLookback = defaultUsageLookback
	}
	if c.usageAlignment <= 0 {
		c.usageAlignment = defaultUsageAlignment
	}
	if c.sanitizationLookback <= 0 {
		c.sanitizationLookback = defaultSanitizationLookback
	}
	if c.maxPages <= 0 {
		c.maxPages = defaultMaxPages
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}

	ts, err := resolveTokenSource(cfg, client)
	if err != nil {
		return config{}, err
	}
	c.tokens = ts
	return c, nil
}

// resolveTokenSource picks the credential: a pre-issued access_token (static), else a
// service-account key (inline credentials_json, else credentials_file). Anything less is
// offline (nil). A malformed inline key is a config error surfaced in Open.
func resolveTokenSource(cfg sdk.Config, client *http.Client) (tokenSource, error) {
	if tok := strings.TrimSpace(cfg.Get(cfgAccessToken)); tok != "" {
		return staticTokenSource{tok: tok}, nil
	}
	tokenURI := strings.TrimSpace(cfg.Get(cfgTokenURI))
	if raw := strings.TrimSpace(cfg.Get(cfgCredentialsJSON)); raw != "" {
		return newSATokenSource([]byte(raw), client, tokenURI)
	}
	if path := strings.TrimSpace(cfg.Get(cfgCredentialsFile)); path != "" {
		data, err := os.ReadFile(path) //nolint:gosec // operator-configured credential path
		if err != nil {
			return nil, fmt.Errorf("vertex: read credentials_file: %w", err)
		}
		return newSATokenSource(data, client, tokenURI)
	}
	return nil, nil // offline (no credential)
}

// splitCSV trims and splits a comma-separated config value, dropping blanks.
func splitCSV(raw string) []string {
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// splitCSVLower is splitCSV with each element lower-cased (publisher ids).
func splitCSVLower(raw string) []string {
	out := splitCSV(raw)
	for i := range out {
		out[i] = strings.ToLower(out[i])
	}
	return out
}
