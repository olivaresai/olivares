// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cfaigateway

import (
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

const Name = "olivares.cloudflare-ai-gateway"

const version = "0.1.0"

const (
	cfgAPIToken     = "api_token"
	cfgAccountID    = "account_id"
	cfgGatewayID    = "gateway_id"
	cfgAPIBase      = "api_base"
	cfgTimeout      = "timeout"
	cfgMetadataKeys = "metadata_keys"
)

const defaultAPIBase = "https://api.cloudflare.com/client/v4"
const defaultTimeout = 30 * time.Second
const defaultMetadataKeys = "workspace,user,cost_center"

type config struct {
	apiToken     string
	accountID    string
	gatewayID    string
	apiBase      string
	timeout      time.Duration
	metadataKeys []string
}

func (c config) allGateways() bool { return c.gatewayID == "" }

func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Cloudflare AI Gateway (usage/cost)",
		Description: "Polls the Cloudflare AI Gateway REST API for per-request logs and emits gateway-side cost telemetry per model/provider with FinOps attribution from custom metadata. Read-only, minimal-data.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgAPIToken, Type: sdk.FieldString, Required: true, Secret: true, Description: "scoped read-only Cloudflare API token (AI Gateway Read)"},
			{Key: cfgAccountID, Type: sdk.FieldString, Required: true, Description: "Cloudflare account id"},
			{Key: cfgGatewayID, Type: sdk.FieldString, Description: "specific gateway id; omit to poll all gateways in the account"},
			{Key: cfgAPIBase, Type: sdk.FieldString, Default: defaultAPIBase, Description: "Cloudflare REST API base URL (override for testing)"},
			{Key: cfgTimeout, Type: sdk.FieldDuration, Default: defaultTimeout.String(), Description: "HTTP timeout for the discovery pass"},
			{Key: cfgMetadataKeys, Type: sdk.FieldString, Default: defaultMetadataKeys, Description: "comma-separated custom metadata keys to extract as FinOps attribution (workspace, user, cost_center)"},
		},
	}
}

func loadConfig(cfg sdk.Config) (config, error) {
	c := config{
		apiToken:  cfg.Get(cfgAPIToken),
		accountID: cfg.Get(cfgAccountID),
		gatewayID: cfg.Get(cfgGatewayID),
		apiBase:   strings.TrimRight(cfg.Get(cfgAPIBase), "/"),
		timeout:   cfg.GetDuration(cfgTimeout, defaultTimeout),
	}
	if c.apiToken == "" {
		return config{}, fmt.Errorf("cfaigateway: %q is required", cfgAPIToken)
	}
	if c.accountID == "" {
		return config{}, fmt.Errorf("cfaigateway: %q is required", cfgAccountID)
	}
	if c.apiBase == "" {
		c.apiBase = defaultAPIBase
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}
	raw := cfg.Get(cfgMetadataKeys)
	if raw == "" {
		raw = defaultMetadataKeys
	}
	for _, k := range strings.Split(raw, ",") {
		k = strings.TrimSpace(k)
		if k != "" {
			c.metadataKeys = append(c.metadataKeys, k)
		}
	}
	return c, nil
}
