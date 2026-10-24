// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"reflect"
	"testing"
)

// #2888: a session on OpenCode's own login got none of the record-bound switches.
// Measured on 1.19.4 behind a refusing proxy, one turn with a fixture key: the own
// login reached models.opencode.ai, opencode.ai and registry.npmjs.org (ChatGPT:
// chatgpt.com instead of opencode.ai). With the switches below and enabled_providers
// set to the provider its model names, it reached only that provider's host, and
// OpenCode's own background plugin install reached the npm registry.
// The model catalog fetch stays on: the model picker lists what that catalog names.
// npm_config_offline stays off: OpenCode's shell runs the agent's commands with its
// own environment, so it would make the agent's npm install fail offline (review of
// #3053, measured: ENOTCACHED).
func TestOpenCodeOwnLoginGetsTheSwitchesThatNeedNoRecord(t *testing.T) {
	for _, tc := range []struct{ model, provider string }{
		{"opencode/glm-5.3-flash-free", "opencode"},
		{"opencode-go/glm-5.3-flash", "opencode-go"},
		{"openai/gpt-5.4-mini", "openai"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			env := envMap(openCodeDriver{}.LaunchEnv(DriverLaunch{Model: tc.model, Preset: PresetAsk}))
			if env[envOpenCodeDisableShare] != "1" || env[envOpenCodeDisableLSPDownload] != "1" {
				t.Errorf("share %q, LSP download %q, want both off", env[envOpenCodeDisableShare], env[envOpenCodeDisableLSPDownload])
			}
			for _, name := range []string{envOpenCodeDisableModelsFetch, envNPMConfigOffline} {
				if _, set := env[name]; set {
					t.Errorf("the own login sets %s: the catalog and the agent's npm commands stay online", name)
				}
			}
			cfg := openCodeInline(t, env)
			if cfg["share"] != "disabled" || cfg["disabled_providers"] != nil || !reflect.DeepEqual(cfg["enabled_providers"], []any{tc.provider}) {
				t.Errorf("inline configuration = %v, want share disabled and only %s enabled", cfg, tc.provider)
			}
		})
	}
}
