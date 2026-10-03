// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"
)

// HU2-22: `session start <dir> "prompt"` without --tool always ran Claude Code, even when
// only OpenCode and a local model were set up. It now runs the first tool, in the
// console's order, that the engine's preview says is ready; none ready is Claude Code,
// whose refusal says what to set up; an explicit --tool is kept.
func TestSessionStartRunsTheFirstReadyTool(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ready map[string]bool
		args  []string
		want  string
	}{
		{"only the local model", map[string]bool{"opencode": true}, nil, "opencode"},
		{"the console's order", map[string]bool{"opencode": true, "codex": true}, nil, "codex"},
		{"none ready", map[string]bool{}, nil, "claude"},
		{"an explicit tool is kept", map[string]bool{"opencode": true}, []string{"--tool", "grok"}, "grok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSessionEngine(t)
			f.ready = tc.ready
			args := append(append([]string{"session", "start", t.TempDir()}, tc.args...), sessionCreds(f.URL)...)
			if _, errb, err := execSessionCLI(t, nil, args...); err != nil {
				t.Fatalf("start: %v\n%s", err, errb)
			}
			resolves := f.postsTo(profilesPath + "/resolve")
			if len(resolves) != 1 || resolves[0]["driver"] != tc.want {
				t.Fatalf("the start resolved %v, want the %s profile", resolves, tc.want)
			}
		})
	}
}
