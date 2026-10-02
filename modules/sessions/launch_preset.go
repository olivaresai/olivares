// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import "strings"

// The permission presets a person chooses for a session (the console's New
// session "What the agent may do", and "Full"). A preset is not stored on its
// own: it is the launch's permission mode plus, for "Edit files and run
// commands", the engine's built-in template allowlist, and Preset reads it from
// those facts so every consumer (the hook PEP binds it to the session credential)
// sees the same answer at create and at resume.
const (
	PresetNone             = ""                   // no choice made: the deployment's policy decides
	PresetReadOnly         = "read_only"          // permission mode plan
	PresetAsk              = "ask"                // permission mode default: ask before each action
	PresetEditsOnly        = "edits_only"         // permission mode acceptEdits
	PresetEditsAndCommands = "edits_and_commands" // dontAsk under a built-in template's allowlist
	PresetFull             = "full"               // bypassPermissions (a run administrator's choice)
	PresetCustom           = "custom"             // dontAsk under any other allowlist
)

func launchPreset(p CreateRunParams) string {
	return (LaunchIntent{PermissionMode: p.PermissionMode, TemplateBuiltin: p.TemplateBuiltin, AllowedTools: p.AllowedTools}).Preset()
}

// Preset names the permission preset this launch runs under.
func (i LaunchIntent) Preset() string {
	switch strings.TrimSpace(i.PermissionMode) {
	case "":
		return PresetNone
	case "plan":
		return PresetReadOnly
	case "default":
		return PresetAsk
	case "acceptEdits":
		return PresetEditsOnly
	case permModeBypass:
		return PresetFull
	case "dontAsk":
		if i.TemplateBuiltin && len(i.AllowedTools) > 0 {
			return PresetEditsAndCommands
		}
	}
	return PresetCustom
}
