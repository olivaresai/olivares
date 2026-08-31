// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package azureactivity

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/connectors/internal/identity"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.azure-activity"

// version is the connector's own semantic version.
const version = "0.1.0"

// Configuration keys (declared in the Descriptor, read in Open).
const (
	cfgTenantID           = "tenant_id"
	cfgClientID           = "client_id"
	cfgClientSecret       = "client_secret"
	cfgAccessToken        = "access_token"
	cfgOAuthTokenURL      = "oauth_token_url"
	cfgSubscriptions      = "subscriptions"
	cfgEnableInventory    = "enable_inventory"
	cfgEnableActivity     = "enable_activity"
	cfgEnableRAI          = "enable_rai"
	cfgRAIAPIVersion      = "rai_api_version"
	cfgLookback           = "lookback"
	cfgMaxEvents          = "max_events"
	cfgMaxPages           = "max_pages"
	cfgSharedAccounts     = "shared_accounts"
	cfgManagementEndpoint = "management_endpoint"
	cfgTimeout            = "timeout"
)

// Defaults.
const (
	defaultManagementEndpoint = "https://management.azure.com"
	defaultLookback           = time.Hour
	defaultMaxEvents          = 1000
	defaultMaxPages           = 50
	defaultTimeout            = 30 * time.Second
)

// Azure REST api-versions (pinned; verified stable GA versions).
const (
	subscriptionsAPIVersion = "2022-12-01"
	resourceGraphAPIVersion = "2022-10-01"
	activityLogAPIVersion   = "2015-04-01"
	// defaultRAIAPIVersion is the Cognitive Services management api-version used for
	// the accounts / raiPolicies / deployments reads. 2024-10-01 is the stable
	// GA version that returns the full RAI-policy shape.
	defaultRAIAPIVersion = "2024-10-01"
)

// config is the resolved connector configuration. The credential fields hold
// secret values in memory only; they are never logged or emitted.
type config struct {
	tokens tokenSource // nil ⇒ offline (no/partial credential): Gather is a silent no-op.

	tenantID      string   // for tenant ⊳ subscription edges; may be "" with a static token.
	subscriptions []string // explicit subscription ids; auto-listed when empty.

	enableInventory bool
	enableActivity  bool
	enableRAI       bool

	raiAPIVersion      string
	managementEndpoint string
	shared             identity.SharedSet

	lookback  time.Duration
	maxEvents int
	maxPages  int
	timeout   time.Duration
}

// descriptor is the connector's stable self-description.
func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Azure Activity Log + Resource Graph",
		Description: "Read-only Azure management plane: Resource Graph inventory (tenant/subscription/resource topology), Azure Monitor Activity Log control-plane activity across subscriptions, and (opt-in) Azure AI Foundry/OpenAI Responsible-AI content-filter posture (RAI policies + deployment bindings). Emits topology and identity→azure.api edges and safety-posture findings; never reads payloads, secrets or key material.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgTenantID, Type: sdk.FieldString, Description: "Microsoft Entra tenant id (directory id). Empty + no access_token ⇒ offline."},
			{Key: cfgClientID, Type: sdk.FieldString, Description: "Entra application (client) id of the read-only service principal."},
			{Key: cfgClientSecret, Type: sdk.FieldString, Secret: true, Description: "client secret reference (read-only; never persisted). Empty + no access_token ⇒ offline."},
			{Key: cfgAccessToken, Type: sdk.FieldString, Secret: true, Description: "pre-issued ARM access token (managed identity / ADC sidecar). Overrides client credentials when set."},
			{Key: cfgOAuthTokenURL, Type: sdk.FieldString, Description: "OAuth2 token endpoint override (defaults to login.microsoftonline.com/{tenant_id}/oauth2/v2.0/token)."},
			{Key: cfgSubscriptions, Type: sdk.FieldString, Description: "comma-separated subscription ids to inventory and read activity for. Empty ⇒ auto-list every subscription the principal can see."},
			{Key: cfgEnableInventory, Type: sdk.FieldBool, Default: "true", Description: "discover subscription/resource topology via Resource Graph."},
			{Key: cfgEnableActivity, Type: sdk.FieldBool, Default: "true", Description: "read the Azure Monitor Activity Log as control-plane activity."},
			{Key: cfgEnableRAI, Type: sdk.FieldBool, Default: "false", Description: "read Azure AI Foundry/OpenAI Responsible-AI (content-filter) posture: RAI policies + deployment bindings per Cognitive Services account (opt-in; off by default — enable on Azure OpenAI/Foundry estates)."},
			{Key: cfgRAIAPIVersion, Type: sdk.FieldString, Default: defaultRAIAPIVersion, Description: "Cognitive Services management api-version for the RAI reads (accounts/raiPolicies/deployments)."},
			{Key: cfgLookback, Type: sdk.FieldDuration, Default: defaultLookback.String(), Description: "Activity Log lookback window."},
			{Key: cfgMaxEvents, Type: sdk.FieldInt, Default: fmt.Sprintf("%d", defaultMaxEvents), Description: "max Activity Log events per pass (across all subscriptions)."},
			{Key: cfgMaxPages, Type: sdk.FieldInt, Default: fmt.Sprintf("%d", defaultMaxPages), Description: "max API pages per list operation (pagination safety bound)."},
			{Key: cfgSharedAccounts, Type: sdk.FieldString, Description: "comma-separated caller ids (objectId/appId/UPN) that are shared/pooled (attribution marked approximate)."},
			{Key: cfgManagementEndpoint, Type: sdk.FieldString, Default: defaultManagementEndpoint, Description: "Azure Resource Manager endpoint base URL (override for testing)."},
			{Key: cfgTimeout, Type: sdk.FieldDuration, Default: defaultTimeout.String(), Description: "per-request HTTP timeout."},
		},
	}
}

// loadConfig resolves the connector configuration, applying defaults and parsing
// the credential. A MISSING or PARTIAL client credential is offline-safe (no
// token source ⇒ Gather is a no-op), mirroring entra-agent. Secret values are
// read here and held in memory only.
func loadConfig(cfg sdk.Config, client *http.Client) (config, error) {
	c := config{
		tenantID:           strings.TrimSpace(cfg.Get(cfgTenantID)),
		subscriptions:      splitCSV(cfg.Get(cfgSubscriptions)),
		enableInventory:    cfg.GetBool(cfgEnableInventory, true),
		enableActivity:     cfg.GetBool(cfgEnableActivity, true),
		enableRAI:          cfg.GetBool(cfgEnableRAI, false),
		raiAPIVersion:      firstNonEmpty(strings.TrimSpace(cfg.Get(cfgRAIAPIVersion)), defaultRAIAPIVersion),
		managementEndpoint: firstNonEmpty(strings.TrimSpace(cfg.Get(cfgManagementEndpoint)), defaultManagementEndpoint),
		shared:             identity.ParseSharedAccounts(cfg.Get(cfgSharedAccounts)),
		lookback:           cfg.GetDuration(cfgLookback, defaultLookback),
		maxEvents:          cfg.GetInt(cfgMaxEvents, defaultMaxEvents),
		maxPages:           cfg.GetInt(cfgMaxPages, defaultMaxPages),
		timeout:            cfg.GetDuration(cfgTimeout, defaultTimeout),
	}
	if c.lookback <= 0 {
		c.lookback = defaultLookback
	}
	if c.maxEvents <= 0 {
		c.maxEvents = defaultMaxEvents
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

// resolveTokenSource picks the credential: a pre-issued access_token (static),
// else client-credentials (requires client_id + client_secret + a tenant or a
// token-url override). Anything less is offline (nil).
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
