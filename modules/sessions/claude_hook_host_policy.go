// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// CheckClaudeHookHostPolicy is shared by launch and doctor. Managed settings
// outrank --settings; a known host policy that suppresses our hooks refuses the
// launch rather than reporting a governed session without a working gate.
// This checks the documented file source, not remote policies or OS MDM stores.
// https://code.claude.com/docs/en/managed-settings
func CheckClaudeHookHostPolicy() error {
	return checkClaudeHookHostPolicyDir(claudeManagedSettingsDir)
}

// claudeManagedSettingsDir is the directory Claude Code reads its managed (policy-tier)
// settings from: the system one. The pinned Claude Code 2.1.288 does not honour
// CLAUDE_CODE_MANAGED_SETTINGS_PATH (SR2C 071 traced it to an empty override), so neither does
// this check. A variable only so tests can move it.
var claudeManagedSettingsDir = func() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/ClaudeCode"
	case "windows":
		return `C:\Program Files\ClaudeCode`
	}
	return "/etc/claude-code"
}()

// ClaudeManagedSettingsDir is that directory, for the doctor's remedy text.
func ClaudeManagedSettingsDir() string { return claudeManagedSettingsDir }

// claudeManagedSettingsFiles lists the managed settings documents in dir, in the order Claude
// Code merges them: managed-settings.json, then the managed-settings.d fragments sorted.
func claudeManagedSettingsFiles(dir string) ([]string, error) {
	files := []string{filepath.Join(dir, "managed-settings.json")}
	fragments, err := os.ReadDir(filepath.Join(dir, "managed-settings.d"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("this host's Claude Code managed settings cannot be checked at %s; Olivares hooks require readable host policy", filepath.Join(dir, "managed-settings.d"))
	}
	for _, file := range fragments { // ReadDir is sorted: later scalar values win.
		if !file.IsDir() && !strings.HasPrefix(file.Name(), ".") && strings.HasSuffix(file.Name(), ".json") {
			files = append(files, filepath.Join(dir, "managed-settings.d", file.Name()))
		}
	}
	return files, nil
}

// CheckClaudeQuietHostPolicy refuses a Claude Code session on a key from Providers when this
// host's managed settings leave either quiet switch (claudeBoundSettings) at anything but "1".
// Managed settings are Claude Code's policy tier; whether they can undo the launch's switches
// was not reproduced on 2.1.288, so this is a fail-closed policy refusal, not a measured leak.
// Each switch is judged on its effective value: the documents in merge order
// (claudeManagedSettingsFiles), env keys merging with the later document winning, naming the
// document that set it. An unreadable document refuses too. The administrator's files are not
// touched, and a session on the tool's own sign-in never asks.
func CheckClaudeQuietHostPolicy() error {
	unreadable := func(path string) error {
		return fmt.Errorf("this host's Claude Code managed settings cannot be read at %s, so a session on a key from Providers cannot be kept to its provider's endpoint under this host policy: fix its permissions or contents, or remove it, or use Claude Code's own sign-in", path)
	}
	files, err := claudeManagedSettingsFiles(claudeManagedSettingsDir)
	if err != nil {
		return unreadable(filepath.Join(claudeManagedSettingsDir, "managed-settings.d"))
	}
	type setting struct {
		value any
		path  string
	}
	effective := map[string]setting{}
	quiet := []string{"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC", "CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL"}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		var policy struct {
			Env map[string]any `json:"env"`
		}
		if err != nil || json.Unmarshal(raw, &policy) != nil {
			return unreadable(path)
		}
		for _, name := range quiet {
			if value, set := policy.Env[name]; set {
				effective[name] = setting{value, path}
			}
		}
	}
	for _, name := range quiet {
		if s, set := effective[name]; set && s.value != "1" {
			return fmt.Errorf("this host's Claude Code managed settings (%s) set %s, so a session on a key from Providers cannot be kept to its provider's endpoint under this host policy; remove the switch from the file, or use Claude Code's own sign-in", s.path, name)
		}
	}
	return nil
}

func checkClaudeHookHostPolicyDir(dir string) error {
	files, err := claudeManagedSettingsFiles(dir)
	if err != nil {
		return err
	}
	var disabled, managedOnly, managedPEP bool
	for _, path := range files {
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("this host's Claude Code managed settings cannot be checked at %s; Olivares hooks require readable host policy", path)
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		closeErr := file.Close()
		var policy struct {
			Disabled    *bool `json:"disableAllHooks"`
			ManagedOnly *bool `json:"allowManagedHooksOnly"`
			Hooks       map[string][]struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Type    string `json:"type"`
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"hooks"`
		}
		if readErr != nil || closeErr != nil || len(raw) > 1<<20 || json.Unmarshal(raw, &policy) != nil {
			return fmt.Errorf("this host's Claude Code managed settings are unreadable or invalid at %s; check host policy before launching Olivares hooks", path)
		}
		if policy.Disabled != nil {
			disabled = *policy.Disabled
		}
		if policy.ManagedOnly != nil {
			managedOnly = *policy.ManagedOnly
		}
		// Hook arrays merge across managed fragments; a later scalar does not
		// remove the managed PEP already present in an earlier document.
		for _, matcher := range policy.Hooks["PreToolUse"] {
			if matcher.Matcher != "" && matcher.Matcher != "*" {
				continue
			}
			for _, hook := range matcher.Hooks {
				parts := strings.Fields(hook.Command)
				if hook.Type == "command" && len(parts) >= 2 && parts[0] == "olivares" && parts[1] == "claude-hook" && !strings.ContainsAny(hook.Command, ";|&`$<>\r\n") {
					managedPEP = true
				}
			}
		}
	}
	if disabled || (managedOnly && !managedPEP) {
		return fmt.Errorf("this host's Claude Code managed settings block Olivares' hooks; enable hooks and include the managed Olivares PEP hook in %s", filepath.Join(dir, "managed-settings.json"))
	}
	return nil
}
