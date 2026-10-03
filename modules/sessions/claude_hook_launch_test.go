// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigureClaudeHookPEPPreservesAccountSettings(t *testing.T) {
	dataDir := t.TempDir()
	home := t.TempDir()
	original := []byte(`{"model":"user-choice","hooks":{"Stop":[]}}`)
	settings := filepath.Join(home, "settings.json")
	if err := os.WriteFile(settings, original, 0600); err != nil {
		t.Fatal(err)
	}
	spec := LaunchSpec{Args: []string{"--print"}, Env: []EnvVar{
		{Name: "CLAUDE_CONFIG_DIR", Value: home},
		{Name: "OLIVARES_HOOK_PEP_URL", Value: "http://127.0.0.1:8447/"},
		{Name: "OLIVARES_HOOK_PEP_TOKEN", Value: "test-only-secret"},
	}}
	if err := ConfigureClaudeHookPEP(&spec, dataDir, "run-one", "/opt/Olivares AI/olivares"); err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(dataDir, "run", "run-one", "pep-settings.json")
	if len(spec.Args) != 5 || spec.Args[1] != "--settings" || spec.Args[2] != wantPath || spec.Args[3] != "--setting-sources" || spec.Args[4] != "" {
		t.Fatalf("args: %v", spec.Args)
	}
	got, err := os.ReadFile(settings)
	if err != nil || string(got) != string(original) {
		t.Fatal("user settings changed")
	}
	raw, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "test-only-secret") {
		t.Fatal("credential persisted")
	}
	var cfg struct {
		DisableAllHooks bool `json:"disableAllHooks"`
		Hooks           map[string][]struct {
			Hooks []struct {
				Type, Command string
				Timeout       int
			}
		}
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.DisableAllHooks {
		t.Fatal("hooks disabled")
	}
	for _, event := range []string{"PreToolUse", "PostToolUse", "PostToolUseFailure"} {
		groups := cfg.Hooks[event]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 || groups[0].Hooks[0].Type != "command" || groups[0].Hooks[0].Command != "'/opt/Olivares AI/olivares' claude-hook --server 'http://127.0.0.1:8447/' --timeout 2m0s --hook-event '"+event+"'" {
			t.Fatalf("%s: %+v", event, groups)
		}
		if groups[0].Hooks[0].Timeout != 180 {
			t.Fatalf("vendor timeout must exceed client deadline: %d", groups[0].Hooks[0].Timeout)
		}
	}
	info, _ := os.Stat(wantPath)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode: %v", info.Mode())
	}
}

func TestConfigureClaudeHookPEPRejectsUnsafePathAndMissingProvisioning(t *testing.T) {
	for _, run := range []string{"", "../outside", "a/b", "."} {
		spec := LaunchSpec{}
		if err := ConfigureClaudeHookPEP(&spec, t.TempDir(), run, "/usr/bin/olivares"); err == nil {
			t.Fatalf("accepted %q", run)
		}
	}
	spec := LaunchSpec{}
	if err := ConfigureClaudeHookPEP(&spec, t.TempDir(), "run", "/usr/bin/olivares"); err == nil {
		t.Fatal("accepted unprovisioned launch")
	}
}

func TestConfigureClaudeHookPEPPinsDeadlineRefusalEvent(t *testing.T) {
	dataDir := t.TempDir()
	spec := LaunchSpec{Env: []EnvVar{
		{Name: "OLIVARES_HOOK_PEP_URL", Value: "http://127.0.0.1:45678/"},
		{Name: "OLIVARES_HOOK_PEP_TOKEN", Value: "test-launch-bearer"},
	}}
	if err := ConfigureClaudeHookPEP(&spec, dataDir, "event-bound", "/opt/olivares"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dataDir, "run", "event-bound", "pep-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct{ Command string }
		}
	}
	if err := json.Unmarshal(body, &settings); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"PreToolUse", "PostToolUse", "PostToolUseFailure"} {
		groups := settings.Hooks[event]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 || !strings.Contains(groups[0].Hooks[0].Command, " --hook-event '"+event+"'") {
			t.Fatalf("%s invocation cannot render its deadline refusal without stdin: %+v", event, groups)
		}
	}
}
