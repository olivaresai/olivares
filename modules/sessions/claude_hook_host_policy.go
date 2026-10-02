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
	if dir := strings.TrimSpace(os.Getenv("CLAUDE_CODE_MANAGED_SETTINGS_PATH")); dir != "" {
		return checkClaudeHookHostPolicyDir(dir)
	}
	dir := "/etc/claude-code"
	switch runtime.GOOS {
	case "darwin":
		dir = "/Library/Application Support/ClaudeCode"
	case "windows":
		dir = `C:\Program Files\ClaudeCode`
	}
	return checkClaudeHookHostPolicyDir(dir)
}

func checkClaudeHookHostPolicyDir(dir string) error {
	files := []string{filepath.Join(dir, "managed-settings.json")}
	fragments, err := os.ReadDir(filepath.Join(dir, "managed-settings.d"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("this host's Claude Code managed settings cannot be checked at %s; Olivares hooks require readable host policy", filepath.Join(dir, "managed-settings.d"))
	}
	for _, file := range fragments { // ReadDir is sorted: later scalar values win.
		if !file.IsDir() && !strings.HasPrefix(file.Name(), ".") && strings.HasSuffix(file.Name(), ".json") {
			files = append(files, filepath.Join(dir, "managed-settings.d", file.Name()))
		}
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
