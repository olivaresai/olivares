// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ClaudeHookPEPClientTimeout bounds the whole helper, including input, host path
// resolution and a human wait. The outer command timeout includes startup headroom:
// a vendor hook timeout discards its decision and can let PreToolUse proceed.
const ClaudeHookPEPClientTimeout = 2 * time.Minute
const ClaudeHookPEPCommandTimeout = 3 * time.Minute

// ConfigureClaudeHookPEP adds the engine's tool hooks to one Claude launch. Call
// after the launch gate has supplied Env, for both create and resume. The caller
// must expose dataDir/run/runRef read-only to the agent. Account homes are untouched;
// credentials remain exclusively in Env. olivaresBinary must be an absolute path.
func ConfigureClaudeHookPEP(spec *LaunchSpec, dataDir, runRef, olivaresBinary string) error {
	if spec == nil || !filepath.IsAbs(dataDir) || !filepath.IsAbs(olivaresBinary) || runRef == "" || runRef == "." || runRef == ".." || strings.ContainsAny(runRef, "/\\\x00") {
		return errors.New("invalid Claude hook launch path")
	}
	var endpoint, token string
	for _, v := range spec.Env {
		switch v.Name {
		case "OLIVARES_HOOK_PEP_URL":
			endpoint = v.Value
		case "OLIVARES_HOOK_PEP_TOKEN":
			token = v.Value
		}
	}
	if endpoint == "" || token == "" {
		return errors.New("Claude hook launch credential is unavailable")
	}
	if err := CheckClaudeHookHostPolicy(); err != nil {
		return err
	}
	// The child reads its managed settings from the system directory, the one the check above
	// read (claudeManagedSettingsDir). CLAUDE_CODE_MANAGED_SETTINGS_PATH is never handed to it:
	// a Claude Code that honoured the variable would read a directory the check never saw
	// (Root 2026-10-03 00:5xZ).
	for i := 0; i < len(spec.Env); i++ {
		if spec.Env[i].Name == "CLAUDE_CODE_MANAGED_SETTINGS_PATH" {
			spec.Env = append(spec.Env[:i], spec.Env[i+1:]...)
			i--
		}
	}
	// User/account settings remain writable and can set the child environment.
	// Pin the public endpoint here so those settings cannot redirect the hook;
	// the secret stays in the launch environment and is verified by this endpoint.
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	command := quote(olivaresBinary) + " claude-hook --server " + quote(endpoint) + " --timeout " + ClaudeHookPEPClientTimeout.String()
	hooks := map[string]any{}
	for _, event := range []string{"PreToolUse", "PostToolUse", "PostToolUseFailure"} {
		// Even stalled stdin must yield the denial schema this invocation honors.
		eventCommand := command + " --hook-event " + quote(event)
		hooks[event] = []any{map[string]any{"matcher": "*", "hooks": []any{map[string]any{"type": "command", "command": eventCommand, "timeout": int(ClaudeHookPEPCommandTimeout / time.Second)}}}}
	}
	settings := map[string]any{"disableAllHooks": false, "hooks": hooks}
	// A launch may already carry inline flag settings (a record-bound launch's quiet
	// switches, claudeBoundSettings). Claude Code takes one --settings, so they join this
	// file rather than being replaced by it.
	for i := 0; i+1 < len(spec.Args); i++ {
		if spec.Args[i] != claudeSettingsFlag {
			continue
		}
		inline := map[string]any{}
		if err := json.Unmarshal([]byte(spec.Args[i+1]), &inline); err != nil {
			return errors.New("the launch's own Claude settings could not be read")
		}
		for key, value := range inline {
			settings[key] = value
		}
		spec.Args = append(spec.Args[:i], spec.Args[i+2:]...)
		break
	}
	body, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	dir := filepath.Join(dataDir, "run", runRef)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".pep-settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(body)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	path := filepath.Join(dir, "pep-settings.json")
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	// Matching native hooks run in parallel. A user/project hook could return
	// updatedInput after PEP approval, so mutable setting sources cannot join
	// this governed launch. An empty value keeps managed and explicit settings.
	// https://code.claude.com/docs/en/cli-reference
	spec.Args = append(spec.Args, claudeSettingsFlag, path, "--setting-sources", "")
	return nil
}
