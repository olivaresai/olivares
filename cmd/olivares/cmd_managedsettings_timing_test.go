// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions"
)

func TestManagedSettingsDefaultHookPinsDeadlineAndInvocation(t *testing.T) {
	cmd := newAgentManagedSettingsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	// The protected longer-approval policy is an explicit choice; the omitted
	// flags retain the published command bytes and 5s outer timeout.
	cmd.SetArgs([]string{"--pep-command", "olivares claude-hook --timeout 2m0s", "--timeout", "180", "--pin-hook-events"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(out.Bytes(), &policy) != nil {
		t.Fatal("invalid static hook settings")
	}
	for _, event := range []string{"PreToolUse", "PostToolUse"} {
		entries := policy.Hooks[event]
		if len(entries) != 1 || len(entries[0].Hooks) != 1 {
			t.Fatal("default static hook coverage changed")
		}
		hook := entries[0].Hooks[0]
		parts := strings.Fields(hook.Command)
		if len(parts) < 2 || parts[0] != "olivares" || parts[1] != "claude-hook" {
			t.Fatal("default command is not the Olivares hook client")
		}
		client := newClaudeHookCmd()
		if err := client.ParseFlags(parts[2:]); err != nil {
			t.Fatal("emitted command is not understood by the hook client", err)
		}
		deadline, _ := client.Flags().GetDuration("timeout")
		invocation, _ := client.Flags().GetString("hook-event")
		if deadline != sessions.ClaudeHookPEPClientTimeout || hook.Timeout != int(sessions.ClaudeHookPEPCommandTimeout/time.Second) || invocation != event || time.Duration(hook.Timeout)*time.Second <= deadline {
			t.Fatal("explicit static hook lacks the protected120s/180s pinned-event contract", event, deadline, hook.Timeout, invocation)
		}
	}
}

func TestManagedSettingsKeepsExplicitCustomHookCommands(t *testing.T) {
	for _, custom := range []string{"olivares claude-hook", "custom-pep --mode literal"} {
		for _, explicitTimeout := range []int{0, 37} {
			cmd := newAgentManagedSettingsCmd()
			args := []string{"--pep-command", custom}
			wantTimeout := 5
			if explicitTimeout != 0 {
				args = append(args, "--timeout", strconv.Itoa(explicitTimeout))
				wantTimeout = explicitTimeout
			}
			cmd.SetArgs(args)
			var out bytes.Buffer
			cmd.SetOut(&out)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			var policy struct {
				Hooks map[string][]struct {
					Hooks []struct {
						Command string `json:"command"`
						Timeout int    `json:"timeout"`
					} `json:"hooks"`
				} `json:"hooks"`
			}
			if json.Unmarshal(out.Bytes(), &policy) != nil {
				t.Fatal("invalid custom hook settings")
			}
			for _, event := range []string{"PreToolUse", "PostToolUse"} {
				entries := policy.Hooks[event]
				if len(entries) != 1 || len(entries[0].Hooks) != 1 || entries[0].Hooks[0].Command != custom || entries[0].Hooks[0].Timeout != wantTimeout {
					t.Fatal("custom PEP command or timeout was altered", event)
				}
			}
		}
	}
}

func TestManagedSettingsDefaultHookRefusesOuterDeadlineInsideClient(t *testing.T) {
	for _, seconds := range []int{0, 1, 5, 6, 120, 180} {
		cmd := newAgentManagedSettingsCmd()
		// Match the product root: refusal emits no settings or usage text.
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		cmd.SetArgs([]string{"--timeout", strconv.Itoa(seconds)})
		var out, warnings bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&warnings)
		if err := cmd.Execute(); err != nil {
			t.Fatal("published explicit timeout was refused", seconds, err)
		}
		var policy struct {
			Hooks map[string][]struct {
				Hooks []struct {
					Command string `json:"command"`
					Timeout int    `json:"timeout"`
				} `json:"hooks"`
			} `json:"hooks"`
		}
		if err := json.Unmarshal(out.Bytes(), &policy); err != nil {
			t.Fatal(err)
		}
		for _, event := range []string{"PreToolUse", "PostToolUse"} {
			entries := policy.Hooks[event]
			if len(entries) != 1 || len(entries[0].Hooks) != 1 {
				t.Fatal("explicit timeout lost hook coverage", seconds, event)
			}
			hook := entries[0].Hooks[0]
			client := newClaudeHookCmd()
			if err := client.ParseFlags(strings.Fields(hook.Command)[2:]); err != nil {
				t.Fatal(err)
			}
			deadline, _ := client.Flags().GetDuration("timeout")
			outer := time.Duration(seconds) * time.Second
			if seconds == 0 { // Omitted JSON timeout keeps Claude's 600s default.
				outer = 600 * time.Second
			}
			if hook.Timeout != seconds || deadline <= 0 || deadline >= outer {
				t.Fatal("default Olivares hook emitted a timeout that can discard its refusal", seconds, deadline)
			}
		}
	}
}
