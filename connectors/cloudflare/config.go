// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cloudflare

import (
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.cloudflare"

// version is the connector's own semantic version.
const version = "0.1.0"

// Configuration keys (declared in the Descriptor, read in Open).
const (
	cfgAPIToken  = "api_token"
	cfgAccountID = "account_id"
	cfgZoneID    = "zone_id"
	cfgAPIBase   = "api_base"
	cfgTimeout   = "timeout"
)

// defaultAPIBase is the Cloudflare REST API v4 root. A test points api_base at an
// httptest server; production uses this default.
const defaultAPIBase = "https://api.cloudflare.com/client/v4"

// defaultTimeout bounds the whole discovery pass's HTTP calls.
const defaultTimeout = 30 * time.Second

// config is the resolved connector configuration. The apiToken is a secret held
// only in memory for the lifetime of the connector; it is never logged or emitted.
type config struct {
	apiToken  string
	accountID string
	zoneID    string
	apiBase   string
	timeout   time.Duration
}

// hasZone reports whether a zone is configured, which enables the zone-scoped
// discovery (Worker routes + zone Logpush jobs). An absent zone is skipped
// silently, not a finding.
func (c config) hasZone() bool { return c.zoneID != "" }

// descriptor is the connector's stable self-description. The api_token field is
// declared Secret so a UI masks it and logs never print it; the value itself is
// passed by reference and read in Open.
func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Cloudflare inventory",
		Description: "Read-only discovery of Cloudflare Workers, R2 buckets and Logpush jobs via the REST API v4; emits topology edges.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgAPIToken, Type: sdk.FieldString, Required: true, Secret: true, Description: "scoped read-only Cloudflare API token (Bearer)"},
			{Key: cfgAccountID, Type: sdk.FieldString, Required: true, Description: "Cloudflare account id to inventory"},
			{Key: cfgZoneID, Type: sdk.FieldString, Description: "optional zone id; enables Worker routes and zone Logpush discovery"},
			{Key: cfgAPIBase, Type: sdk.FieldString, Default: defaultAPIBase, Description: "Cloudflare REST API base URL (override for testing)"},
			{Key: cfgTimeout, Type: sdk.FieldDuration, Default: defaultTimeout.String(), Description: "HTTP timeout for the discovery pass"},
		},
	}
}

// loadConfig resolves and validates the connector configuration. The required
// api_token and account_id surface a configuration error here, before Gather. The
// api_base is trimmed of a trailing slash so paths join cleanly.
func loadConfig(cfg sdk.Config) (config, error) {
	c := config{
		apiToken:  cfg.Get(cfgAPIToken),
		accountID: cfg.Get(cfgAccountID),
		zoneID:    cfg.Get(cfgZoneID),
		apiBase:   strings.TrimRight(cfg.Get(cfgAPIBase), "/"),
		timeout:   cfg.GetDuration(cfgTimeout, defaultTimeout),
	}
	if c.apiToken == "" {
		return config{}, fmt.Errorf("cloudflare: %q is required", cfgAPIToken)
	}
	if c.accountID == "" {
		return config{}, fmt.Errorf("cloudflare: %q is required", cfgAccountID)
	}
	if c.apiBase == "" {
		c.apiBase = defaultAPIBase
	}
	if c.timeout <= 0 {
		c.timeout = defaultTimeout
	}
	return c, nil
}
