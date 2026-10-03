// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestManagedSettingsPublishedExplicitTimeoutKeepsDenyClosedHook(t *testing.T) {
	root := newRootCmd()
	root.SetArgs([]string{"agent", "managed-settings", "--timeout", "5"})
	var out, warnings bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&warnings)
	if err := root.Execute(); err != nil {
		t.Fatalf("published invocation no longer succeeds: %v", err)
	}
	var policy struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(out.Bytes(), &policy); err != nil {
		t.Fatal(err)
	}
	entries := policy.Hooks["PreToolUse"]
	if len(entries) != 1 || len(entries[0].Hooks) != 1 {
		t.Fatal("published render lost its PreToolUse gate")
	}
	hook := entries[0].Hooks[0]
	parts := strings.Fields(hook.Command)
	if hook.Type != "command" || hook.Timeout != 5 || len(parts) < 2 || parts[0] != "olivares" || parts[1] != "claude-hook" {
		t.Fatal("rendered hook is not the published Olivares command/outer timeout")
	}
	client := newClaudeHookCmd()
	if err := client.ParseFlags(parts[2:]); err != nil {
		t.Fatal(err)
	}
	deadline, _ := client.Flags().GetDuration("timeout")
	event, _ := client.Flags().GetString("hook-event")
	if deadline <= 0 || deadline >= 5*time.Second || event != "PreToolUse" {
		t.Fatalf("client cannot refuse before the outer timeout: %v event=%q", deadline, event)
	}
	if strings.Count(strings.TrimSpace(warnings.String()), "\n") != 0 || !strings.Contains(warnings.String(), "WARNING:") || !strings.Contains(warnings.String(), "denied") {
		t.Fatalf("expected one warning about longer approvals being denied, got %q", warnings.String())
	}
	// Run the rendered client without provisioning: its stdout must carry a
	// native PreToolUse denial, rather than an exit-code-only failure.
	t.Setenv("OLIVARES_HOOK_PEP_URL", "")
	t.Setenv("OLIVARES_HOOK_PEP_TOKEN", "")
	var decision, diagnostics bytes.Buffer
	client.SetArgs(parts[2:])
	client.SetIn(strings.NewReader(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"true"}}`))
	client.SetOut(&decision)
	client.SetErr(&diagnostics)
	if err := client.Execute(); err != nil {
		t.Fatal(err)
	}
	var refusal struct {
		Output struct {
			Event    string `json:"hookEventName"`
			Decision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(decision.Bytes(), &refusal); err != nil || refusal.Output.Event != "PreToolUse" || refusal.Output.Decision != "deny" {
		t.Fatal("rendered command is not deny-closed", err, decision.String())
	}
}
