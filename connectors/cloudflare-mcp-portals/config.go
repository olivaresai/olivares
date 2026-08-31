// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cfmcpportals

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

const Name = "olivares.cloudflare-mcp-portals"

const version = "0.1.0"

const (
	cfgAPIToken        = "api_token"
	cfgAccountID       = "account_id"
	cfgApprovedServers = "approved_servers"
	cfgAPIBase         = "api_base"
	cfgTimeout         = "timeout"
)

const defaultAPIBase = "https://api.cloudflare.com/client/v4"
const defaultTimeout = 30 * time.Second

type config struct {
	apiToken        string
	accountID       string
	approvedServers map[string]struct{}
	apiBase         string
	timeout         time.Duration
}

func (c config) shadowEnabled() bool { return len(c.approvedServers) > 0 }

func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Cloudflare One MCP Portals (inventory + shadow detection)",
		Description: "Polls the Cloudflare One Zero Trust MCP servers and portals API for inventory discovery and shadow MCP detection. Read-only, minimal-data.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgAPIToken, Type: sdk.FieldString, Required: true, Secret: true, Description: "scoped read-only Cloudflare API token (Access Read)"},
			{Key: cfgAccountID, Type: sdk.FieldString, Required: true, Description: "Cloudflare account id"},
			{Key: cfgApprovedServers, Type: sdk.FieldString, Description: `JSON array of approved MCP server name refs (e.g. ["docs-mcp","api-mcp"]); enables shadow detection when set`},
			{Key: cfgAPIBase, Type: sdk.FieldString, Default: defaultAPIBase, Description: "Cloudflare REST API base URL (override for testing)"},
			{Key: cfgTimeout, Type: sdk.FieldDuration, Default: defaultTimeout.String(), Description: "HTTP timeout for the discovery pass"},
		},
	}
}

func loadConfig(cfg sdk.Config) (config, error) {
	c := config{
		apiToken:  cfg.Get(cfgAPIToken),
		accountID: cfg.Get(cfgAccountID),
		apiBase:   strings.TrimRight(cfg.Get(cfgAPIBase), "/"),
		timeout:   cfg.GetDuration(cfgTimeout, defaultTimeout),
	}
	if c.apiToken == "" {
		return config{}, fmt.Errorf("cfmcpportals: %q is required", cfgAPIToken)
	}
	if c.accountID == "" {
		return config{}, fmt.Errorf("cfmcpportals: %q is required", cfgAccountID)
	}
	if c.apiBase == "" {
		c.apiBase = defaultAPIBase
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}
	raw := cfg.Get(cfgApprovedServers)
	if raw != "" {
		var names []string
		if err := json.Unmarshal([]byte(raw), &names); err != nil {
			return config{}, fmt.Errorf("cfmcpportals: %q must be a JSON array of strings: %w", cfgApprovedServers, err)
		}
		c.approvedServers = make(map[string]struct{}, len(names))
		for _, n := range names {
			n = strings.TrimSpace(n)
			if n != "" {
				c.approvedServers[n] = struct{}{}
			}
		}
	}
	return c, nil
}
