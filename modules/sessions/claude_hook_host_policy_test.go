// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeHookHostPolicyRefusesSuppression(t *testing.T) {
	for _, body := range []string{`{"disableAllHooks":true}`, `{"allowManagedHooksOnly":true}`, `{"disableAllHooks":"invalid"}`, `{`} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "managed-settings.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := checkClaudeHookHostPolicyDir(dir); err == nil || !strings.Contains(err.Error(), "host's Claude Code managed settings") {
			t.Fatalf("suppression was not explained: %v", err)
		}
	}
}

func TestClaudeHookHostPolicyUsesManagedFragmentPrecedence(t *testing.T) {
	dir := t.TempDir()
	if err := checkClaudeHookHostPolicyDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "managed-settings.json"), []byte(`{"allowManagedHooksOnly":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	fragments := filepath.Join(dir, "managed-settings.d")
	if err := os.Mkdir(fragments, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragments, "20-session-hooks.json"), []byte(`{"allowManagedHooksOnly":false,"disableAllHooks":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkClaudeHookHostPolicyDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fragments, "30-disable.json"), []byte(`{"disableAllHooks":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkClaudeHookHostPolicyDir(dir); err == nil {
		t.Fatal("later host suppression accepted")
	}
}

func TestClaudeHookHostPolicyAcceptsPublishedManagedPEP(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "managed-settings.json")
	legacy, err := os.ReadFile("testdata/managed-settings-26.10.0.json")
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(legacy)) != "63dc3ae04639aed6dec8e839a2be86d82bec2667599b8bd7ef1c3b2aadcd2374" {
		t.Fatal("published managed-settings fixture changed", err)
	}
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CODE_MANAGED_SETTINGS_PATH", dir)
	if err := CheckClaudeHookHostPolicy(); err != nil {
		t.Fatalf("published managed PEP must keep launching after upgrade: %v", err)
	}
	if err := checkClaudeHookHostPolicyDir(dir); err != nil {
		t.Fatalf("managed-only must permit its own PEP hook: %v", err)
	}
	spec := LaunchSpec{Env: []EnvVar{{Name: "OLIVARES_HOOK_PEP_URL", Value: "http://127.0.0.1:45678/"}, {Name: "OLIVARES_HOOK_PEP_TOKEN", Value: "test-launch-bearer"}}}
	if err := ConfigureClaudeHookPEP(&spec, t.TempDir(), "legacy", "/opt/olivares"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, env := range spec.Env {
		if env.Name == "CLAUDE_CODE_MANAGED_SETTINGS_PATH" && env.Value == dir {
			found = true
		}
	}
	if !found {
		t.Fatal("vendor launch would read a different host policy than preflight")
	}
}

func TestClaudeHookHostPolicyRejectsManagedOnlyWithoutFullPEP(t *testing.T) {
	for _, hooks := range []string{
		`{}`, `{"PreToolUse":[{"hooks":[{"type":"command","command":"echo olivares claude-hook"}]}]}`,
		`{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"olivares claude-hook"}]}]}`,
		`{"PostToolUse":[{"hooks":[{"type":"command","command":"olivares claude-hook"}]}]}`,
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "managed-settings.json")
		if err := os.WriteFile(path, []byte(`{"allowManagedHooksOnly":true,"hooks":`+hooks+`}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := checkClaudeHookHostPolicyDir(dir); err == nil || !strings.Contains(err.Error(), path) {
			t.Fatal("suppression must name the managed file", err)
		}
	}
}

func TestConfigureClaudeHookPEPExcludesMutableHookSources(t *testing.T) {
	t.Setenv("CLAUDE_CODE_MANAGED_SETTINGS_PATH", t.TempDir())
	spec := LaunchSpec{Env: []EnvVar{
		{Name: "OLIVARES_HOOK_PEP_URL", Value: "http://127.0.0.1:45678/"},
		{Name: "OLIVARES_HOOK_PEP_TOKEN", Value: "test-launch-bearer"},
	}}
	if err := ConfigureClaudeHookPEP(&spec, t.TempDir(), "sources", "/opt/olivares"); err != nil {
		t.Fatal(err)
	}
	found := false
	for i, arg := range spec.Args {
		if arg == "--setting-sources" && i+1 < len(spec.Args) && spec.Args[i+1] == "" {
			found = true
		}
	}
	if !found {
		t.Fatal("user/project/local hook sources can rewrite PEP-approved input")
	}
}
