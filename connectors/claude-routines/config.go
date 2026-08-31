// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package clauderoutines

import (
	"fmt"
	"strings"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique dotted identifier.
const Name = "olivares.claude-routines"

const version = "0.1.0"

const (
	defaultBaseURL           = "https://api.claude.ai"
	defaultRefresh           = 5 * time.Minute
	defaultMaxCadenceSeconds = 3600 // 1 hour — the API's documented minimum interval
	defaultReviewAfterDays   = 30
	defaultMaxPages          = 20
)

const (
	cfgAPIKey            = "api_key"
	cfgBaseURL           = "base_url"
	cfgOrganizationID    = "organization_id"
	cfgRefresh           = "refresh_interval"
	cfgMaxCadenceSeconds = "max_cadence_seconds"
	cfgReviewAfterDays   = "require_review_after_days"
	cfgMaxPages          = "max_pages"
)

type config struct {
	apiKey            string
	baseURL           string
	organizationID    string
	refresh           time.Duration
	maxCadenceSeconds int
	reviewAfterDays   int
	maxPages          int
}

func descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     version,
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Claude Code Routines (scheduled triggers) inventory",
		Description: "Inventories Claude Code Routines (scheduled triggers / cron agents) via the Claude Code Remote API. Read-only: every API call is a GET. Emits inventory edges and governance findings (excessive cadence, unreviewed routines, anonymous triggers). Prompt content is never stored — only hashed.",
		ConfigFields: []sdk.ConfigField{
			{Key: cfgAPIKey, Type: sdk.FieldString, Secret: true, Required: true, Description: "Claude Code API key for the read-only GET inventory (never persisted, never logged)."},
			{Key: cfgBaseURL, Type: sdk.FieldString, Default: defaultBaseURL, Description: "Claude Code Remote API base URL."},
			{Key: cfgOrganizationID, Type: sdk.FieldString, Description: "Organization whose routines to inventory (carried on edges as the workspace subject)."},
			{Key: cfgRefresh, Type: sdk.FieldDuration, Default: defaultRefresh.String(), Description: "Poll cadence for the routine inventory."},
			{Key: cfgMaxCadenceSeconds, Type: sdk.FieldInt, Default: "3600", Description: "Policy floor: minimum interval (seconds) a routine should have. A routine firing more often raises a HIGH finding."},
			{Key: cfgReviewAfterDays, Type: sdk.FieldInt, Default: "30", Description: "Routines older than this many days without a recorded review raise a MEDIUM finding."},
			{Key: cfgMaxPages, Type: sdk.FieldInt, Default: "20", Description: "Pagination safety bound per poll pass."},
		},
	}
}

func loadConfig(cfg sdk.Config) (config, error) {
	c := config{
		apiKey:            strings.TrimSpace(cfg.Get(cfgAPIKey)),
		baseURL:           firstNonEmpty(strings.TrimSpace(cfg.Get(cfgBaseURL)), defaultBaseURL),
		organizationID:    strings.TrimSpace(cfg.Get(cfgOrganizationID)),
		refresh:           cfg.GetDuration(cfgRefresh, defaultRefresh),
		maxCadenceSeconds: cfg.GetInt(cfgMaxCadenceSeconds, defaultMaxCadenceSeconds),
		reviewAfterDays:   cfg.GetInt(cfgReviewAfterDays, defaultReviewAfterDays),
		maxPages:          cfg.GetInt(cfgMaxPages, defaultMaxPages),
	}
	if c.apiKey == "" {
		return config{}, fmt.Errorf("claude-routines: %s is required", cfgAPIKey)
	}
	if c.refresh <= 0 {
		c.refresh = defaultRefresh
	}
	if c.maxCadenceSeconds <= 0 {
		c.maxCadenceSeconds = defaultMaxCadenceSeconds
	}
	if c.reviewAfterDays <= 0 {
		c.reviewAfterDays = defaultReviewAfterDays
	}
	if c.maxPages <= 0 {
		c.maxPages = defaultMaxPages
	}
	return c, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
