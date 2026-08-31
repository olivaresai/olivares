// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package clauderoutines

import (
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

func cfgFrom(settings map[string]string) (config, error) {
	return loadConfig(sdk.Config{Settings: settings})
}

func TestLoadConfigValid(t *testing.T) {
	c, err := cfgFrom(map[string]string{cfgAPIKey: "sk-ant-xxx"})
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}
	if c.apiKey != "sk-ant-xxx" {
		t.Errorf("apiKey = %q, want sk-ant-xxx", c.apiKey)
	}
	if c.baseURL != defaultBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, defaultBaseURL)
	}
	if c.refresh != defaultRefresh {
		t.Errorf("refresh = %v, want %v", c.refresh, defaultRefresh)
	}
	if c.maxCadenceSeconds != defaultMaxCadenceSeconds {
		t.Errorf("maxCadenceSeconds = %d, want %d", c.maxCadenceSeconds, defaultMaxCadenceSeconds)
	}
	if c.reviewAfterDays != defaultReviewAfterDays {
		t.Errorf("reviewAfterDays = %d, want %d", c.reviewAfterDays, defaultReviewAfterDays)
	}
}

func TestLoadConfigMissingAPIKey(t *testing.T) {
	if _, err := cfgFrom(map[string]string{}); err == nil {
		t.Error("a config with no api_key must be rejected")
	}
}

func TestLoadConfigCustomValues(t *testing.T) {
	c, err := cfgFrom(map[string]string{
		cfgAPIKey:            "sk-ant-yyy",
		cfgBaseURL:           "https://custom.api",
		cfgOrganizationID:    "org_123",
		cfgRefresh:           "10m",
		cfgMaxCadenceSeconds: "1800",
		cfgReviewAfterDays:   "14",
		cfgMaxPages:          "5",
	})
	if err != nil {
		t.Fatalf("custom config: %v", err)
	}
	if c.baseURL != "https://custom.api" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
	if c.organizationID != "org_123" {
		t.Errorf("organizationID = %q", c.organizationID)
	}
	if c.refresh.Minutes() != 10 {
		t.Errorf("refresh = %v", c.refresh)
	}
	if c.maxCadenceSeconds != 1800 {
		t.Errorf("maxCadenceSeconds = %d", c.maxCadenceSeconds)
	}
	if c.reviewAfterDays != 14 {
		t.Errorf("reviewAfterDays = %d", c.reviewAfterDays)
	}
	if c.maxPages != 5 {
		t.Errorf("maxPages = %d", c.maxPages)
	}
}

func TestLoadConfigInvalidRefreshFallsToDefault(t *testing.T) {
	c, err := cfgFrom(map[string]string{
		cfgAPIKey:  "sk-ant-xxx",
		cfgRefresh: "-1s",
	})
	if err != nil {
		t.Fatalf("negative refresh should not fail Open: %v", err)
	}
	if c.refresh != defaultRefresh {
		t.Errorf("negative refresh should fall back to default, got %v", c.refresh)
	}
}

func TestLoadConfigInvalidIntFallsToDefault(t *testing.T) {
	c, err := cfgFrom(map[string]string{
		cfgAPIKey:            "sk-ant-xxx",
		cfgMaxCadenceSeconds: "not-a-number",
		cfgReviewAfterDays:   "-5",
	})
	if err != nil {
		t.Fatalf("bad int should not fail Open: %v", err)
	}
	if c.maxCadenceSeconds != defaultMaxCadenceSeconds {
		t.Errorf("unparseable maxCadenceSeconds should fall back, got %d", c.maxCadenceSeconds)
	}
	// -5 parses as an int but is <=0 so it falls to the default
	if c.reviewAfterDays != defaultReviewAfterDays {
		t.Errorf("negative reviewAfterDays should fall back, got %d", c.reviewAfterDays)
	}
}

func TestDescriptor(t *testing.T) {
	d := New().Descriptor()
	if d.Name != Name || d.Type != sdk.TypeSource || d.APIVersion != sdk.APIVersion {
		t.Fatalf("descriptor identity = %+v", d)
	}
	if d.Version != "0.1.0" {
		t.Errorf("version = %q, want 0.1.0", d.Version)
	}
	secret := map[string]bool{}
	for _, f := range d.ConfigFields {
		secret[f.Key] = f.Secret
	}
	if !secret[cfgAPIKey] {
		t.Error("api_key must be marked Secret")
	}
}
