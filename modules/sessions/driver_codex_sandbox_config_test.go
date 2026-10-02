// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"slices"
	"strings"
	"testing"
)

// Where Olivares confines the session, Codex's own sandbox is off, and Codex's
// configuration says so at launch: Codex then does not look for bubblewrap at
// startup and prints no warning a user would read as a failure. A Codex that keeps
// its own sandbox is launched without it, so its warning, then true, stays.
func TestCodexLaunchTurnsCodexSandboxOffInItsConfigOnlyWhenConfined(t *testing.T) {
	sandboxOff := []string{"-c", `sandbox_mode="danger-full-access"`}
	launch := DriverLaunch{WorkDir: "/w"}

	confined, err := NewCodexDriverWithPolicy(CodexPolicy{Sandbox: CodexSandboxDangerFull})
	if err != nil {
		t.Fatal(err)
	}
	if args := confined.LaunchArgs(launch); !slices.Equal(args[:2], sandboxOff) {
		t.Fatalf("confined launch = %q, want it to start with %q", args, sandboxOff)
	}
	local := launch
	local.LocalModelEndpoint = "http://127.0.0.1:11434/v1"
	if args := strings.Join(confined.LaunchArgs(local), " "); !strings.Contains(args, strings.Join(sandboxOff, " ")) || !strings.Contains(args, "olivares_ollama") {
		t.Fatalf("confined local-model launch lost an override: %s", args)
	}

	for _, sandbox := range []string{CodexSandboxWorkspaceWrite, CodexSandboxReadOnly, ""} {
		d, err := NewCodexDriverWithPolicy(CodexPolicy{Sandbox: sandbox})
		if err != nil {
			t.Fatal(err)
		}
		if args := strings.Join(d.LaunchArgs(launch), " "); strings.Contains(args, "sandbox_mode") {
			t.Fatalf("sandbox %q: launch overrides Codex's sandbox config: %s", sandbox, args)
		}
	}
}
