// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"
)

// TestSessionShowGivesTheReasonAStoppedSessionStopped: the engine stops a session with a
// reason a person acts on ("Access changed for <user>; resume to continue with the new
// access"). `session show` printed a reason only for failed sessions.
func TestSessionShowGivesTheReasonAStoppedSessionStopped(t *testing.T) {
	reason := "Access changed for ana@example.com; resume to continue with the new access"
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": "r1", "name": "notes", "state": "stopped", "provider_driver": "claude", "reason": reason},
		{"run_ref": "r2", "name": "docs", "state": "stopped", "provider_driver": "claude"},
		{"run_ref": "r3", "name": "web", "state": "running", "provider_driver": "claude", "reason": "an old note"},
	}
	show := func(name string) string {
		t.Helper()
		out, errb, err := execSessionCLI(t, nil, append([]string{"session", "show", name}, sessionCreds(f.URL)...)...)
		if err != nil {
			t.Fatalf("show %s: %v\n%s", name, err, errb)
		}
		return out
	}
	if out := show("notes"); !strings.Contains(out, "REASON") || !strings.Contains(out, reason) {
		t.Fatalf("a stopped session's reason is missing:\n%s", out)
	}
	if out := show("docs"); strings.Contains(out, "REASON") {
		t.Fatalf("a stopped session without a reason shows an empty reason row:\n%s", out)
	}
	if out := show("web"); strings.Contains(out, "REASON") {
		t.Fatalf("a running session shows a reason:\n%s", out)
	}
}

// TestSessionShowPrintsTheCanonicalSessionID: a peer send names a session by
// its canonical osn_ ID and the peers list holds it, and `session show` is where a
// person reads a session. It printed only the run id, so no one could hand work over.
func TestSessionShowPrintsTheCanonicalSessionID(t *testing.T) {
	sid := "osn_01a10199-9a94-7d38-b856-ff57cca8d2b8"
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": "r1", "name": "notes", "state": "running", "provider_driver": "codex", "canonical_sid": sid},
		{"run_ref": "r2", "name": "docs", "state": "running", "provider_driver": "codex"},
	}
	show := func(name string) string {
		t.Helper()
		out, errb, err := execSessionCLI(t, nil, append([]string{"session", "show", name}, sessionCreds(f.URL)...)...)
		if err != nil {
			t.Fatalf("show %s: %v\n%s", name, err, errb)
		}
		return out
	}
	if out := show("notes"); !strings.Contains(out, "SESSION ID") || !strings.Contains(out, sid) {
		t.Fatalf("the canonical session ID is missing:\n%s", out)
	}
	if out := show("docs"); strings.Contains(out, "SESSION ID") {
		t.Fatalf("a session without a canonical ID shows an empty row:\n%s", out)
	}
}
