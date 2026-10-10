// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"
)

// session show names the mode the tool itself reported (#427), and a session whose
// tool has not reported one shows no mode row rather than a guess.
func TestSessionShowNamesTheModeTheToolReported(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": "r1", "name": "alpha", "state": "running", "provider_driver": "claude", "permission_mode": "default", "tool_mode": "acceptEdits"},
		{"run_ref": "r2", "name": "quiet", "state": "running", "provider_driver": "claude", "permission_mode": "default"},
	}
	show := func(name string) string {
		t.Helper()
		out, errb, err := execSessionCLI(t, nil, append([]string{"session", "show", name}, sessionCreds(f.URL)...)...)
		if err != nil {
			t.Fatalf("show %s: %v\n%s", name, err, errb)
		}
		return out
	}
	// The MODE row carries the tool's word, not the requested permission mode.
	modeRow := ""
	for _, l := range strings.Split(show("alpha"), "\n") {
		if strings.Contains(l, "MODE") {
			modeRow = l
		}
	}
	if !strings.Contains(modeRow, "acceptEdits") || strings.Contains(modeRow, "default") {
		t.Fatalf("the MODE row = %q, want the tool's acceptEdits", modeRow)
	}
	if out := show("quiet"); strings.Contains(out, "MODE") {
		t.Fatalf("a session whose tool reported no mode shows a mode row:\n%s", out)
	}
}
