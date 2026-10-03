// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"
)

// TestSessionRemoveTakesALaunchThatNeverStarted is J7 on refresh 06: a launch whose
// approval was rejected is "declined" (or "expired" when nobody decided), and `session
// rm` said "Stop it first", which a session that never started cannot do. The engine
// cleans up declined and expired runs like stopped and failed ones.
func TestSessionRemoveTakesALaunchThatNeverStarted(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": "r1", "name": "hr", "state": "declined", "provider_driver": "claude"},
		{"run_ref": "r2", "name": "ops", "state": "expired", "provider_driver": "claude"},
	}
	for _, name := range []string{"hr", "ops"} {
		out, errb, err := execSessionCLI(t, nil, append([]string{"session", "rm", name}, sessionCreds(f.URL)...)...)
		if err != nil || strings.TrimSpace(out) != "Removed "+name+"." {
			t.Fatalf("rm %s: out=%q err=%v stderr=%q", name, out, err, errb)
		}
	}
}
