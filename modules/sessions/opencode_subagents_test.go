// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import "testing"

// #3083: OpenCode's ACP agent drops a permission ask from a session it did not create,
// so a subagent's ask is never answered and its turn waits forever (measured on
// 1.19.4 over ACP: task -> general -> bash). Only the build agent may ask; it starts no
// subagent, and every other agent's rules (the global ones) deny instead of asking or
// allowing, so nothing it does goes unanswered or unaudited.
func TestOpenCodeSubagentsNeverAskAndBuildStartsNone(t *testing.T) {
	for _, preset := range []string{PresetReadOnly, PresetAsk, PresetEditsOnly, PresetEditsAndCommands, PresetFull} {
		t.Run(preset, func(t *testing.T) {
			cfg := openCodeInline(t, envMap(openCodeDriver{}.LaunchEnv(DriverLaunch{Model: "opencode/big-pickle", Preset: preset})))
			var walk func(path string, v any)
			walk = func(path string, v any) {
				switch v := v.(type) {
				case map[string]any:
					for k, w := range v {
						walk(path+"."+k, w)
					}
				default:
					if v != "deny" {
						t.Errorf("global rule %s = %v: an agent other than build would not be refused", path, v)
					}
				}
			}
			walk("permission", cfg["permission"])
			agents, _ := cfg["agent"].(map[string]any)
			build, _ := agents["build"].(map[string]any)
			rules, _ := build["permission"].(map[string]any)
			if rules["task"] != "deny" {
				t.Errorf("build agent task = %v, want deny: a subagent's ask would never be answered", rules["task"])
			}
		})
	}
}

// #3058: the build agent's glob, grep and list ask, so live policy decides and audits
// them as reads of the folder they search (OpenCode 1.19.4 has no list tool).
func TestOpenCodeBuildAgentSearchesAsk(t *testing.T) {
	for _, preset := range []string{PresetAsk, PresetEditsOnly, PresetEditsAndCommands, PresetFull, PresetReadOnly} {
		cfg := openCodeInline(t, envMap(openCodeDriver{}.LaunchEnv(DriverLaunch{Model: "opencode/big-pickle", Preset: preset})))
		agents, _ := cfg["agent"].(map[string]any)
		build, _ := agents["build"].(map[string]any)
		rules, _ := build["permission"].(map[string]any)
		for _, tool := range []string{"glob", "grep", "list"} {
			if rules[tool] != "ask" {
				t.Errorf("%s: build agent %s = %v, want ask", preset, tool, rules[tool])
			}
		}
	}
}
