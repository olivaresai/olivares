// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import "testing"

// HU 024 on refresh 04: "Ask before each action" asked nothing, because the hook
// PEP never learned which permission preset the person chose. The launch intent
// names it, from the same facts the launch runs on, so PEP binds it to the session
// credential at create and at resume.
func TestLaunchIntentNamesThePresetThePersonChose(t *testing.T) {
	builtin := []string{"Bash", "Edit", "Read"}
	for _, tc := range []struct {
		name   string
		intent LaunchIntent
		want   string
	}{
		{"Read only", LaunchIntent{PermissionMode: "plan"}, PresetReadOnly},
		{"Ask before each action", LaunchIntent{PermissionMode: "default"}, PresetAsk},
		{"Edit files only", LaunchIntent{PermissionMode: "acceptEdits"}, PresetEditsOnly},
		{"Edit files and run commands", LaunchIntent{PermissionMode: "dontAsk", TemplateBuiltin: true, AllowedTools: builtin}, PresetEditsAndCommands},
		{"Full", LaunchIntent{PermissionMode: "bypassPermissions"}, PresetFull},
		{"a custom template's allowlist", LaunchIntent{PermissionMode: "dontAsk", AllowedTools: builtin}, PresetCustom},
		{"a built-in template with no allowlist", LaunchIntent{PermissionMode: "dontAsk", TemplateBuiltin: true}, PresetCustom},
		{"no choice made (Codex): the deployment's policy decides", LaunchIntent{}, PresetNone},
	} {
		if got := tc.intent.Preset(); got != tc.want {
			t.Errorf("%s: Preset() = %q, want %q", tc.name, got, tc.want)
		}
	}
}
