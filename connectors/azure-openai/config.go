// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureopenai

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.azure-openai"

// version is the connector's own semantic version.
const version = "0.1.0"

// Configuration keys (declared in the Descriptor, read in Open).
const (
	cfgTenantID            = "tenant_id"
	cfgClientID            = "client_id"
	cfgClientSecret        = "client_secret"
	cfgAccessToken         = "access_token"
	cfgOAuthTokenURL       = "oauth_token_url"
	cfgSubscriptions       = "subscriptions"
	cfgAccountKinds        = "account_kinds"
	cfgEnableUsage         = "enable_usage"
	cfgEnableCost          = "enable_cost"
	cfgUsageLookback       = "usage_lookback"
	cfgMetricsInterval     = "metrics_interval"
	cfgCostLookback        = "cost_lookback"
	cfgCostServiceDim      = "cost_service_dimension"
	cfgCostServiceValue    = "cost_service_value"
	cfgCostFinalizationLag = "cost_finalization_lag"
	cfgMaxPages            = "max_pages"
	cfgManagementEndpoint  = "management_endpoint"
	cfgARMAPIVersion       = "arm_api_version"
	cfgMetricsAPIVersion   = "metrics_api_version"
	cfgCostAPIVersion      = "cost_api_version"
	cfgSubsAPIVersion      = "subscriptions_api_version"
	cfgTimeout             = "timeout"
)

// Defaults.
const (
	defaultManagementEndpoint = "https://management.azure.com"
	defaultAccountKinds       = "OpenAI,AIServices"
	defaultUsageLookback      = time.Hour
	defaultMetricsInterval    = "PT1H"
	defaultCostLookback       = 30 * 24 * time.Hour
	defaultCostServiceDim     = "ServiceName"
	defaultCostServiceValue   = "Cognitive Services"
	// defaultCostFinalizationLag is how recent a day must be to still be considered
	// non-final: Azure rerates open/recent cost (up to ~5 calendar days after period end)
	// and exposes NO isFinal flag, so days within this window are Provenance=estimated.
	defaultCostFinalizationLag = 5 * 24 * time.Hour
	defaultMaxPages            = 50
	defaultTimeout             = 30 * time.Second
)

// Azure REST api-versions (pinned to verified stable GA values).
const (
	defaultARMAPIVersion     = "2024-10-01" // Cognitive Services accounts/deployments/models
	defaultMetricsAPIVersion = "2024-02-01" // Azure Monitor metrics data path
	defaultCostAPIVersion    = "2024-08-01" // Cost Management Query
	defaultSubsAPIVersion    = "2022-12-01" // subscription list
)

// config is the resolved connector configuration. The credential fields hold secret
// values in memory only; they are never logged or emitted.
type config struct {
	tokens tokenSource // nil ⇒ offline (no/partial credential): live reads are no-ops.

	tenantID      string
	subscriptions []string // explicit subscription ids; auto-listed when empty.
	accountKinds  []string // Cognitive Services account kinds that host LLM deployments.

	enableUsage bool
	enableCost  bool

	usageLookback   time.Duration
	metricsInterval string // ISO-8601 grain (e.g. PT1H) passed verbatim to Azure Monitor.
	costLookback    time.Duration

	costServiceDimension string // Cost Management filter dimension (ServiceName/MeterCategory).
	costServiceValue     string // the value isolating Azure OpenAI / Cognitive Services cost.
	costFinalizationLag  time.Duration

	maxPages           int
	managementEndpoint string

	armAPIVersion     string
	metricsAPIVersion string
	costAPIVersion    string
	subsAPIVersion    string

	timeout time.Duration
}

// descriptor is the connector's stable self-description.
func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Azure OpenAI / AI Foundry catalog + usage + cost",
		Description: "Read-only Azure OpenAI / Azure AI Foundry governance over the ARM control plane: the Cognitive Services deployment + model catalog (accounts as workspaces, deployments as the inference-callable models incl. Claude-on-Foundry, with lifecycle/retirement), per-deployment token usage from Azure Monitor metrics emitted as CostSamples (token counts only, cost derived from list pricing — never a prompt/completion), and opt-in billed cost from Azure Cost Management (money only, billed when finalized / estimated while a period rerates). Responsible-AI/content-filter posture is deferred to the azure-activity connector (enable_rai). Never reads account keys or the inference data plane.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgTenantID, Type: sdk.FieldString, Description: "Microsoft Entra tenant id (directory id). Empty + no access_token ⇒ offline."},
			{Key: cfgClientID, Type: sdk.FieldString, Description: "Entra application (client) id of the read-only service principal (Reader + Monitoring Reader + Cost Management Reader)."},
			{Key: cfgClientSecret, Type: sdk.FieldString, Secret: true, Description: "client secret reference (read-only; never persisted). Empty + no access_token ⇒ offline."},
			{Key: cfgAccessToken, Type: sdk.FieldString, Secret: true, Description: "pre-issued ARM access token (managed identity / ADC sidecar). Overrides client credentials when set."},
			{Key: cfgOAuthTokenURL, Type: sdk.FieldString, Description: "OAuth2 token endpoint override (defaults to login.microsoftonline.com/{tenant_id}/oauth2/v2.0/token)."},
			{Key: cfgSubscriptions, Type: sdk.FieldString, Description: "comma-separated subscription ids to enumerate. Empty ⇒ auto-list every subscription the principal can see."},
			{Key: cfgAccountKinds, Type: sdk.FieldString, Default: defaultAccountKinds, Description: "comma-separated Cognitive Services account kinds that host LLM deployments (filter applied client-side; kind is a free-form string)."},
			{Key: cfgEnableUsage, Type: sdk.FieldBool, Default: "true", Description: "read per-deployment token usage from Azure Monitor metrics and emit CostSamples with derived cost."},
			{Key: cfgEnableCost, Type: sdk.FieldBool, Default: "false", Description: "read billed cost from Azure Cost Management Query (opt-in; off by default)."},
			{Key: cfgUsageLookback, Type: sdk.FieldDuration, Default: defaultUsageLookback.String(), Description: "Azure Monitor metrics lookback window."},
			{Key: cfgMetricsInterval, Type: sdk.FieldString, Default: defaultMetricsInterval, Description: "Azure Monitor metrics grain (ISO-8601 duration: PT1M/PT5M/PT1H/PT6H/P1D); passed verbatim with AutoAdjustTimegrain."},
			{Key: cfgCostLookback, Type: sdk.FieldDuration, Default: defaultCostLookback.String(), Description: "Cost Management lookback window (day-bucketed)."},
			{Key: cfgCostServiceDim, Type: sdk.FieldString, Default: defaultCostServiceDim, Description: `Cost Management filter dimension isolating Azure OpenAI cost (default "ServiceName"; some account types use "MeterCategory" — resolve via Dimensions-List).`},
			{Key: cfgCostServiceValue, Type: sdk.FieldString, Default: defaultCostServiceValue, Description: `Cost Management filter value for the service dimension (default "Cognitive Services"; override if your account reports a different value).`},
			{Key: cfgCostFinalizationLag, Type: sdk.FieldDuration, Default: defaultCostFinalizationLag.String(), Description: "how recent a cost day stays Provenance=estimated (Azure rerates open periods; no isFinal flag)."},
			{Key: cfgMaxPages, Type: sdk.FieldInt, Default: fmt.Sprintf("%d", defaultMaxPages), Description: "max API pages per list operation (pagination safety bound)."},
			{Key: cfgManagementEndpoint, Type: sdk.FieldString, Default: defaultManagementEndpoint, Description: "Azure Resource Manager endpoint base URL (override for testing)."},
			{Key: cfgARMAPIVersion, Type: sdk.FieldString, Default: defaultARMAPIVersion, Description: "Cognitive Services management api-version (accounts/deployments/models)."},
			{Key: cfgMetricsAPIVersion, Type: sdk.FieldString, Default: defaultMetricsAPIVersion, Description: "Azure Monitor metrics api-version."},
			{Key: cfgCostAPIVersion, Type: sdk.FieldString, Default: defaultCostAPIVersion, Description: "Azure Cost Management Query api-version."},
			{Key: cfgSubsAPIVersion, Type: sdk.FieldString, Default: defaultSubsAPIVersion, Description: "subscription-list api-version (used only when subscriptions is empty)."},
			{Key: cfgTimeout, Type: sdk.FieldDuration, Default: defaultTimeout.String(), Description: "per-request HTTP timeout."},
		},
	}
}

// loadConfig resolves the connector configuration, applying defaults and parsing the
// credential. A MISSING or PARTIAL client credential is offline-safe (no token source ⇒
// Snapshot returns the empty catalog and Gather is a no-op), mirroring azure-activity.
// Secret values are read here and held in memory only.
func loadConfig(cfg sdk.Config, client *http.Client) (config, error) {
	c := config{
		tenantID:             strings.TrimSpace(cfg.Get(cfgTenantID)),
		subscriptions:        splitCSV(cfg.Get(cfgSubscriptions)),
		accountKinds:         splitCSVLower(firstNonEmpty(cfg.Get(cfgAccountKinds), defaultAccountKinds)),
		enableUsage:          cfg.GetBool(cfgEnableUsage, true),
		enableCost:           cfg.GetBool(cfgEnableCost, false),
		usageLookback:        cfg.GetDuration(cfgUsageLookback, defaultUsageLookback),
		metricsInterval:      firstNonEmpty(strings.TrimSpace(cfg.Get(cfgMetricsInterval)), defaultMetricsInterval),
		costLookback:         cfg.GetDuration(cfgCostLookback, defaultCostLookback),
		costServiceDimension: firstNonEmpty(strings.TrimSpace(cfg.Get(cfgCostServiceDim)), defaultCostServiceDim),
		costServiceValue:     firstNonEmpty(strings.TrimSpace(cfg.Get(cfgCostServiceValue)), defaultCostServiceValue),
		costFinalizationLag:  cfg.GetDuration(cfgCostFinalizationLag, defaultCostFinalizationLag),
		maxPages:             cfg.GetInt(cfgMaxPages, defaultMaxPages),
		managementEndpoint:   firstNonEmpty(strings.TrimSpace(cfg.Get(cfgManagementEndpoint)), defaultManagementEndpoint),
		armAPIVersion:        firstNonEmpty(strings.TrimSpace(cfg.Get(cfgARMAPIVersion)), defaultARMAPIVersion),
		metricsAPIVersion:    firstNonEmpty(strings.TrimSpace(cfg.Get(cfgMetricsAPIVersion)), defaultMetricsAPIVersion),
		costAPIVersion:       firstNonEmpty(strings.TrimSpace(cfg.Get(cfgCostAPIVersion)), defaultCostAPIVersion),
		subsAPIVersion:       firstNonEmpty(strings.TrimSpace(cfg.Get(cfgSubsAPIVersion)), defaultSubsAPIVersion),
		timeout:              cfg.GetDuration(cfgTimeout, defaultTimeout),
	}
	if c.usageLookback <= 0 {
		c.usageLookback = defaultUsageLookback
	}
	if c.costLookback <= 0 {
		c.costLookback = defaultCostLookback
	}
	if c.costFinalizationLag < 0 {
		c.costFinalizationLag = defaultCostFinalizationLag
	}
	if c.maxPages <= 0 {
		c.maxPages = defaultMaxPages
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}
	c.tokens = resolveTokenSource(cfg, client)
	return c, nil
}

// resolveTokenSource picks the credential: a pre-issued access_token (static), else
// client-credentials (requires client_id + client_secret + a tenant or a token-url
// override). Anything less is offline (nil).
func resolveTokenSource(cfg sdk.Config, client *http.Client) tokenSource {
	if tok := strings.TrimSpace(cfg.Get(cfgAccessToken)); tok != "" {
		return staticTokenSource{tok: tok}
	}
	tenant := strings.TrimSpace(cfg.Get(cfgTenantID))
	clientID := strings.TrimSpace(cfg.Get(cfgClientID))
	secret := cfg.Get(cfgClientSecret)
	tokenURL := strings.TrimSpace(cfg.Get(cfgOAuthTokenURL))
	if clientID != "" && secret != "" && (tenant != "" || tokenURL != "") {
		return newCCTokenSource(tenant, clientID, secret, tokenURL, client)
	}
	return nil // offline (missing/partial credential)
}

// firstNonEmpty returns the first non-empty argument, or "" if all are empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
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

// splitCSVLower is splitCSV with each element lower-cased (account-kind matching).
func splitCSVLower(raw string) []string {
	out := splitCSV(raw)
	for i := range out {
		out[i] = strings.ToLower(out[i])
	}
	return out
}
