// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudemanagedagents

import (
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/sdk"
)

func cfgFrom(settings map[string]string) (config, error) {
	return loadConfig(sdk.Config{Settings: settings})
}

func TestLoadConfigWebhookOnly(t *testing.T) {
	c, err := cfgFrom(map[string]string{cfgWebhookSecret: testSecret})
	if err != nil {
		t.Fatalf("webhook-only config: %v", err)
	}
	if !c.webhookEnabled() || c.pollEnabled() {
		t.Errorf("webhook-only: webhookEnabled=%v pollEnabled=%v", c.webhookEnabled(), c.pollEnabled())
	}
}

func TestLoadConfigPollOnly(t *testing.T) {
	c, err := cfgFrom(map[string]string{cfgAPIKey: "sk-ant-xxx"})
	if err != nil {
		t.Fatalf("poll-only config: %v", err)
	}
	if !c.pollEnabled() || c.webhookEnabled() {
		t.Errorf("poll-only: pollEnabled=%v webhookEnabled=%v", c.pollEnabled(), c.webhookEnabled())
	}
}

func TestLoadConfigNothingToObserve(t *testing.T) {
	if _, err := cfgFrom(map[string]string{}); err == nil {
		t.Error("a config with no api_key and no webhook_secret must be rejected (nothing to observe)")
	}
	// An api_key but every observe toggle off is also nothing to poll.
	if _, err := cfgFrom(map[string]string{
		cfgAPIKey:           "sk-ant-xxx",
		cfgObserveVaults:    "false",
		cfgObserveMemory:    "false",
		cfgObserveWorkQueue: "false",
		cfgObserveSkills:    "false",
		cfgObserveSessions:  "false",
		cfgObserveDreams:    "false",
	}); err == nil {
		t.Error("an api_key with all observe toggles off must be rejected")
	}
}

func TestLoadConfigRejectsBadSecretPrefix(t *testing.T) {
	if _, err := cfgFrom(map[string]string{cfgWebhookSecret: "not-a-whsec-secret"}); err == nil {
		t.Error("a webhook secret without the whsec_ prefix must be rejected")
	}
}

func TestLoadConfigRefusesPublicBind(t *testing.T) {
	base := map[string]string{cfgWebhookSecret: testSecret, cfgWebhookAddr: "0.0.0.0:8842"}
	if _, err := cfgFrom(base); err == nil {
		t.Error("a non-loopback webhook bind without allow_public_bind must be refused")
	}
	base[cfgAllowPublic] = "true"
	if _, err := cfgFrom(base); err != nil {
		t.Errorf("a non-loopback bind WITH allow_public_bind should be allowed: %v", err)
	}
}

func TestSplitList(t *testing.T) {
	cases := map[string][]string{
		"":                       nil,
		"env_a":                  {"env_a"},
		"env_a,env_b":            {"env_a", "env_b"},
		`["env_a", "env_b"]`:     {"env_a", "env_b"},
		"env_a env_b , env_c":    {"env_a", "env_b", "env_c"},
		`[ "env_a" , "env_b" ] `: {"env_a", "env_b"},
	}
	for in, want := range cases {
		if got := splitList(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitList(%q) = %v, want %v", in, got, want)
		}
	}
}
