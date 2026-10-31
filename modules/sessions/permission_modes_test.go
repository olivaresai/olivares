// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

// The four names are a view over the permission_mode values: a name, an older CLI
// name and a console label each resolve to the value the launch sends.
func TestResolvePermission_NamesAndAliases(t *testing.T) {
	for _, tc := range []struct {
		in       string
		wantName string
		wantMode string
		template bool
	}{
		{"supervised", "supervised", "default", false},
		{"ask", "supervised", "default", false},
		{"Ask before each action", "supervised", "default", false},
		{"Supervised", "supervised", "default", false},
		{"auto-accept-edits", "auto-accept-edits", "acceptEdits", false},
		{"edits-only", "auto-accept-edits", "acceptEdits", false},
		{"Edit files only", "auto-accept-edits", "acceptEdits", false},
		{"auto", "auto", "auto", false},
		{"full", "full", "bypassPermissions", false},
		{"read-only", "read-only", "plan", false},
		{"edits-and-commands", "edits-and-commands", "", true},
		{"  auto \n", "auto", "auto", false},
		{"AUTO-ACCEPT-EDITS", "auto-accept-edits", "acceptEdits", false},
	} {
		got, ok := ResolvePermission(tc.in)
		if !ok || got.Name != tc.wantName || got.Mode != tc.wantMode || got.Template != tc.template {
			t.Errorf("ResolvePermission(%q) = %+v, %v; want name %q mode %q template %v",
				tc.in, got, ok, tc.wantName, tc.wantMode, tc.template)
		}
	}
	for _, bad := range []string{"", "yolo", "bypassPermissions", "dontAsk"} {
		if got, ok := ResolvePermission(bad); ok {
			t.Errorf("ResolvePermission(%q) = %+v, true; want unknown", bad, got)
		}
	}
}

// Every name is a mode the engine accepts, so the view can never name a value the
// launch would refuse.
func TestPermissionNames_AreValidEngineModes(t *testing.T) {
	for _, n := range PermissionNames() {
		if n.Mode != "" && !validPermissionModes[n.Mode] {
			t.Errorf("%s sends %q, which the engine does not accept", n.Name, n.Mode)
		}
	}
}

// A tool without an automatic mode asks instead; only Claude Code has one. The
// launch applies this once, where the driver is known, so the run, the hook policy
// and the driver all see the same mode.
func TestLaunchPermissionMode_AutoAsksWhereTheToolHasNoAutoMode(t *testing.T) {
	for _, tc := range []struct{ driver, mode, want string }{
		{providerDriverClaude, "auto", "auto"},
		{"", "auto", "auto"}, // a run from before profiles is Claude Code's
		{providerDriverCodex, "auto", "default"},
		{providerDriverGrok, "auto", "default"},
		{providerDriverOpenCode, "auto", "default"},
		{providerDriverGemini, "auto", "default"},
		{providerDriverCodex, "acceptEdits", "acceptEdits"},
		{providerDriverCodex, "bypassPermissions", "bypassPermissions"},
		{providerDriverCodex, "", ""},
	} {
		if got := launchPermissionMode(tc.driver, tc.mode); got != tc.want {
			t.Errorf("launchPermissionMode(%s, %q) = %q, want %q", tc.driver, tc.mode, got, tc.want)
		}
	}
}

// The rewrite is part of the launch, not only a function: a launch through the real
// profile resolution of each tool stores and runs the mode the tool can honor.
func TestLaunchProfileMakesAutoAskOnAToolWithoutOne(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t, WithProviderDriver(NewCodexDriver()), WithProviderDriver(NewGrokDriver()),
		WithProviderDriver(NewOpenCodeDriver()), WithProviderDriver(NewGeminiDriver()))
	m.UseExecutionEnvironmentRef(testEnvRef)
	for _, tc := range []struct{ driver, want, wantPreset string }{
		{providerDriverClaude, "auto", PresetAuto},
		{providerDriverCodex, "default", PresetAsk},
		{providerDriverGrok, "default", PresetAsk},
		{providerDriverOpenCode, "default", PresetAsk},
		{providerDriverGemini, "default", PresetAsk},
	} {
		profile := mustCreateProfile(t, m, tenant, CreateProfileInput{
			Driver: tc.driver, ConfigHome: t.TempDir(), UserHome: t.TempDir(),
			DisplayName: "auto on " + tc.driver, AuthSource: AuthSourceAccountHome,
		})
		p := CreateRunParams{ProviderProfileRef: profile.Ref, PermissionMode: "auto"}
		if err := m.resolveLaunchProfileInto(context.Background(), tenant, &p); err != nil {
			t.Errorf("%s: a launch naming auto was refused: %v", tc.driver, err)
			continue
		}
		if p.PermissionMode != tc.want || launchPreset(p) != tc.wantPreset {
			t.Errorf("%s: auto launches as mode %q preset %q, want %q %q", tc.driver, p.PermissionMode, launchPreset(p), tc.want, tc.wantPreset)
		}
	}
}

// A launch path that missed the rewrite must not start a native tool whose mappers have no
// case for Auto (no sandbox, no permission rule): it is refused, loudly.
func TestNativeToolRefusesAutoThatWasNotRewritten(t *testing.T) {
	p := CreateRunParams{PermissionMode: "auto"}
	for _, driver := range []string{providerDriverCodex, providerDriverGrok, providerDriverOpenCode, providerDriverGemini} {
		if err := refuseNativeCustomPreset(p, driver); statusOf(err) != http.StatusBadRequest {
			t.Errorf("%s: auto that kept its mode = %v, want 400", driver, err)
		}
	}
	for _, driver := range []string{providerDriverClaude, ""} {
		if err := refuseNativeCustomPreset(p, driver); err != nil {
			t.Errorf("%q: auto on Claude Code = %v, want none", driver, err)
		}
	}
}

func TestLaunchIntentNamesAutoAsItsOwnPreset(t *testing.T) {
	if got := (LaunchIntent{PermissionMode: "auto"}).Preset(); got != PresetAuto {
		t.Errorf("Preset() for auto = %q, want %q", got, PresetAuto)
	}
}

// The expected side is written out by hand from each tool's own vocabulary: the
// table has to say what the launch builders produce, and this is the second
// opinion. A mode a tool cannot honor shows what it asks instead.
func TestPermissionTable_SaysWhatEachToolReceives(t *testing.T) {
	allowed := "Bash,Edit,Glob,Grep,LS,MultiEdit,NotebookEdit,Read,TodoWrite,Write"
	want := map[string]map[string][]string{
		"supervised": {
			providerDriverClaude:   {"--permission-mode default"},
			providerDriverCodex:    {"sandbox workspace-write", "approval untrusted"},
			providerDriverGrok:     {"sandbox workspace", "mode default"},
			providerDriverOpenCode: {"permission ask"},
			providerDriverGemini:   {"mode default"},
		},
		"auto-accept-edits": {
			providerDriverClaude:   {"--permission-mode acceptEdits"},
			providerDriverCodex:    {"sandbox workspace-write", "approval untrusted"},
			providerDriverGrok:     {"sandbox workspace", "mode default"},
			providerDriverOpenCode: {"permission ask"},
			providerDriverGemini:   {"mode default"},
		},
		"auto": {
			providerDriverClaude:   {"--permission-mode auto"},
			providerDriverCodex:    {"sandbox workspace-write", "approval untrusted"},
			providerDriverGrok:     {"sandbox workspace", "mode default"},
			providerDriverOpenCode: {"permission ask"},
			providerDriverGemini:   {"mode default"},
		},
		"full": {
			providerDriverClaude:   {"--permission-mode bypassPermissions"},
			providerDriverCodex:    {"sandbox danger-full-access", "approval untrusted"},
			providerDriverGrok:     {"mode default"},
			providerDriverOpenCode: {"permission ask"},
			providerDriverGemini:   {"mode default"},
		},
		"read-only": {
			providerDriverClaude:   {"--permission-mode plan"},
			providerDriverCodex:    {"sandbox read-only", "approval on-request"},
			providerDriverGrok:     {"sandbox read-only", "mode plan"},
			providerDriverOpenCode: {"permission deny"},
			providerDriverGemini:   {"mode plan"},
		},
		"edits-and-commands": {
			providerDriverClaude:   {"--permission-mode dontAsk", "--allowedTools " + allowed},
			providerDriverCodex:    {"sandbox workspace-write", "approval untrusted"},
			providerDriverGrok:     {"sandbox workspace", "mode default"},
			providerDriverOpenCode: {"permission ask"},
			providerDriverGemini:   {"mode default"},
		},
	}
	rows := PermissionTable()
	if len(rows) != len(want) {
		t.Fatalf("table has %d modes, want %d", len(rows), len(want))
	}
	for _, row := range rows {
		cells, ok := want[row.Name]
		if !ok {
			t.Errorf("table names a mode the test does not expect: %q", row.Name)
			continue
		}
		if len(row.Tools) != len(cells) {
			t.Errorf("%s: %d tools, want %d", row.Name, len(row.Tools), len(cells))
		}
		for _, cell := range row.Tools {
			if got := cell.Receives; !reflect.DeepEqual(got, cells[cell.Driver]) {
				t.Errorf("%s on %s: receives %q, want %q", row.Name, cell.Driver, got, cells[cell.Driver])
			}
			wantAsks := row.Name == "auto" && cell.Driver != providerDriverClaude
			if cell.AsksInstead != wantAsks {
				t.Errorf("%s on %s: AsksInstead = %v, want %v", row.Name, cell.Driver, cell.AsksInstead, wantAsks)
			}
		}
	}
}

// No surface starts a session in Full unless the person chose Full: the engine's
// default for an unnamed mode and the quick-start default of the console and CLI.
// Every tool must honor the quick-start default as it is, with no fallback.
func TestPermissionDefault_IsNeverFullAndEveryToolHonorsIt(t *testing.T) {
	def, ok := ResolvePermission(DefaultPermission)
	if !ok {
		t.Fatalf("DefaultPermission %q is not a permission name", DefaultPermission)
	}
	if def.Name == "full" || def.Mode == permModeBypass {
		t.Fatalf("the default permission is %q: no default may be Full", def.Name)
	}
	if engineDefaultPermissionMode == permModeBypass || !validPermissionModes[engineDefaultPermissionMode] {
		t.Fatalf("the engine's default for an unnamed mode is %q", engineDefaultPermissionMode)
	}
	for _, row := range PermissionTable() {
		if row.Name != def.Name {
			continue
		}
		for _, cell := range row.Tools {
			if cell.AsksInstead || len(cell.Receives) == 0 {
				t.Errorf("the default %s falls back or hands %s nothing", def.Name, cell.Driver)
			}
		}
	}
	// Every tool launches under it as it is: none refuses the preset or the built-in
	// template's allowlist the default carries.
	p := CreateRunParams{PermissionMode: permModeDontAsk, TemplateBuiltin: true, AllowedTools: builtinAllowedTools(EditsAndCommandsTemplate)}
	if got := launchPreset(p); got != PresetEditsAndCommands {
		t.Fatalf("the default launches as preset %q, want %q", got, PresetEditsAndCommands)
	}
	for _, tool := range permissionTools {
		if err := refuseNativeCustomPreset(p, tool.Driver); err != nil {
			t.Errorf("%s refuses the default: %v", tool.Driver, err)
		}
		if name := nativeACPToolName(tool.Driver); name != "" {
			if err := refuseACPUnsupportedControls(p, name); err != nil {
				t.Errorf("%s refuses the default's controls: %v", name, err)
			}
		}
	}
}
