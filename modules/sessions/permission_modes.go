// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"slices"
	"strings"

	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
)

// The permission names a person picks from: a view over the permission_mode values
// the engine already accepts, never a second setting. A name, an older CLI name or
// the console's label resolves (ResolvePermission) to the value the launch sends.
// The table of what each name hands each tool (PermissionTable) is built by calling
// the same functions that build the launch, so it cannot say anything the launch
// does not do.

// DefaultPermission is the permission the console's quick start and the CLI choose
// when none is named. It is never Full: Full is chosen on purpose, per session or
// for a folder, and a command line takes it from a folder only with --permission full.
const DefaultPermission = "edits-and-commands"

// engineDefaultPermissionMode is the mode a launch that names none runs under: the
// tool asks before each action.
const engineDefaultPermissionMode = "default"

// PermissionName is one permission a person can choose.
type PermissionName struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Summary string `json:"summary"`
	// Mode is the permission_mode the launch sends ("" for the built-in template,
	// which carries dontAsk with its allowlist).
	Mode string `json:"mode"`
	// Template says the launch also names the engine's built-in "Edits and commands"
	// template, the allowlist that dontAsk confines.
	Template bool `json:"template,omitempty"`
	// Aliases are older names that keep working.
	Aliases []string `json:"aliases,omitempty"`
}

// permissionNames is ordered from the least to the most the agent may do.
var permissionNames = []PermissionName{
	{Name: "read-only", Label: "Read only", Mode: "plan",
		Summary: "Reads and plans. Every change is refused."},
	{Name: "supervised", Label: "Supervised", Mode: "default", Aliases: []string{"ask", "Ask before each action"},
		Summary: "Asks before every action that is not a read."},
	{Name: "auto-accept-edits", Label: "Auto-accept edits", Mode: "acceptEdits", Aliases: []string{"edits-only", "Edit files only"},
		Summary: "Reads and edits files without asking. Everything else asks."},
	{Name: "edits-and-commands", Label: "Edit files and run commands", Mode: "", Template: true,
		Summary: "Reads, edits files and runs commands without asking. Other tools are refused."},
	{Name: "auto", Label: "Auto", Mode: "auto",
		Summary: "The tool's own reviewed automatic mode decides. A tool without one asks, as in Supervised. Needs run administration."},
	{Name: "full", Label: "Full", Mode: permModeBypass,
		Summary: "Nothing asks. Olivares still confines the session and records every action. Needs run administration."},
}

// PermissionNames returns the permissions a person can choose, least to most.
func PermissionNames() []PermissionName { return slices.Clone(permissionNames) }

// ResolvePermission finds a permission by its name, an older name or its label,
// ignoring case. Raw permission_mode values are not names: the API takes them as it
// always did.
func ResolvePermission(s string) (PermissionName, bool) {
	s = strings.TrimSpace(s)
	for _, n := range permissionNames {
		if strings.EqualFold(s, n.Name) || strings.EqualFold(s, n.Label) ||
			slices.ContainsFunc(n.Aliases, func(a string) bool { return strings.EqualFold(s, a) }) {
			return n, true
		}
	}
	return PermissionName{}, false
}

// normalizeDefaultPermission is the one validation of a folder's default: empty takes
// the default away, anything else is a permission by its name, an older name or its
// label, and is stored by its name.
func normalizeDefaultPermission(s string) (PermissionName, error) {
	if strings.TrimSpace(s) == "" {
		return PermissionName{}, nil
	}
	n, ok := ResolvePermission(s)
	if !ok {
		return PermissionName{}, badRequest("invalid default_permission (want read-only|supervised|auto-accept-edits|edits-and-commands|auto|full, or empty for none)")
	}
	return n, nil
}

// refuseUnrestrictedFolderDefault keeps Full and Auto a run administrator's choice
// when it is made for a folder: whoever may only administer the folder could otherwise
// leave a run administrator to start in either without having chosen it.
func refuseUnrestrictedFolderDefault(n PermissionName, mayRunUnrestricted bool) error {
	if needsRunAdministration(n.Mode) && !mayRunUnrestricted {
		return forbiddenErr("only an administrator can make " + n.Name + " the default permission of a folder")
	}
	return nil
}

// launchPermissionMode is the mode a launch really runs under for a tool. Only
// Claude Code has an automatic mode; any other tool asks instead. It is applied once,
// where the driver is known, so the stored run, the hook policy and the driver all
// see the same mode. A run that predates profiles names no driver and is Claude
// Code's historical path.
func launchPermissionMode(driver, mode string) string {
	if strings.TrimSpace(mode) == "auto" && driver != "" && driver != providerDriverClaude {
		return engineDefaultPermissionMode
	}
	return mode
}

// PermissionCell is what one tool is handed for one permission.
type PermissionCell struct {
	Driver string `json:"driver"`
	// Receives is what the launch hands the tool, in the tool's own words.
	Receives []string `json:"receives"`
	// AsksInstead says the tool has no such mode, so it asks before each action.
	AsksInstead bool `json:"asks_instead,omitempty"`
}

// PermissionRow is one permission and what every tool receives for it.
type PermissionRow struct {
	PermissionName
	Default bool             `json:"default,omitempty"`
	Tools   []PermissionCell `json:"tools"`
}

type permissionTool struct{ Driver, Label string }

var permissionTools = []permissionTool{
	{providerDriverClaude, "Claude Code"},
	{providerDriverCodex, "Codex"},
	{providerDriverOpenCode, "OpenCode"},
	{providerDriverGrok, "Grok Build"},
	{providerDriverGemini, "Gemini CLI"},
}

// PermissionTable says, for every permission and tool, what the launch hands the
// tool and where it asks instead. Each cell comes from the function that builds that
// tool's launch.
func PermissionTable() []PermissionRow {
	rows := make([]PermissionRow, 0, len(permissionNames))
	for _, n := range permissionNames {
		row := PermissionRow{PermissionName: n, Default: n.Name == DefaultPermission}
		for _, tool := range permissionTools {
			row.Tools = append(row.Tools, permissionCell(tool.Driver, n))
		}
		rows = append(rows, row)
	}
	return rows
}

func permissionCell(driver string, n PermissionName) PermissionCell {
	mode := n.Mode
	var allowed []string
	if n.Template {
		mode, allowed = permModeDontAsk, builtinAllowedTools(EditsAndCommandsTemplate)
	}
	effective := launchPermissionMode(driver, mode)
	preset := (LaunchIntent{PermissionMode: effective, TemplateBuiltin: n.Template, AllowedTools: allowed}).Preset()
	cell := PermissionCell{Driver: driver, AsksInstead: effective != mode}
	switch driver {
	case providerDriverClaude:
		args := cliruntime.ClaudeArgs(cliruntime.LaunchRequest{PermissionMode: effective, AllowedTools: allowed})
		for i, a := range args {
			if (a == "--permission-mode" || a == "--allowedTools") && i+1 < len(args) {
				cell.Receives = append(cell.Receives, a+" "+args[i+1])
			}
		}
	case providerDriverCodex:
		policy := codexDriver{}.launchPolicy(preset, false)
		cell.Receives = []string{"sandbox " + policy.sandbox(), "approval " + policy.approval().Mode}
	case providerDriverGrok:
		if sandbox := grokSandboxFor(preset); sandbox != "" {
			cell.Receives = append(cell.Receives, "sandbox "+sandbox)
		}
		if native := grokModeFor(preset); native != "" {
			cell.Receives = append(cell.Receives, "mode "+native)
		}
	case providerDriverOpenCode:
		cell.Receives = []string{"permission " + openCodePermissionFor(preset)}
	case providerDriverGemini:
		cell.Receives = []string{"mode " + geminiModeFor(preset)}
	}
	return cell
}

// builtinAllowedTools is the allowlist of a built-in template, by name.
func builtinAllowedTools(name string) []string {
	for _, t := range builtinTemplates {
		if t.name == name && t.body.Policies != nil {
			return t.body.Policies.AllowedTools
		}
	}
	return nil
}
