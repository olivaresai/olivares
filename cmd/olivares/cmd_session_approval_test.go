// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// A launch that needs a person's approval is created and waits (the engine answers 202,
// state waiting_approval). The CLI read the 202 as a refusal and never said who decides.

func TestSessionStartThatWaitsForApprovalSaysWhereToDecide(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.launchWaits = true
	f.workspaces = []map[string]any{{"workspace_ref": "ws-1", "root_path": t.TempDir(), "state": "active"}}
	dir := str(f.workspaces[0], "root_path")
	out, errb, err := execSessionCLI(t, nil, append([]string{"session", "start", dir, "fix the tests"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("start: %v\n%s", err, errb)
	}
	for _, want := range []string{"waits for approval before it starts", f.URL + "/permissions?tab=approvals",
		"Your message was not sent"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in\n%s", want, out)
		}
	}
	if strings.Contains(out, "Started ") || len(f.postsTo(sessionRunsPath+"/01a0f6dc-0000-7000-8000-000000000009/input")) != 0 {
		t.Fatalf("a waiting session was reported started or sent input:\n%s", out)
	}
}

func TestSessionSendToASessionWaitingForApprovalSaysSo(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{{"run_ref": "r1", "name": "notes", "state": "waiting_approval", "provider_driver": "claude",
		"approval_ref": "apr-1"}}
	_, _, err := execSessionCLI(t, nil, append([]string{"session", "send", "notes", "hi"}, sessionCreds(f.URL)...)...)
	want := "notes is waiting for approval before it starts. Send once an approver accepts it: " + f.URL + "/permissions?tab=approvals"
	if exitcode.From(err) != exitcode.Conflict || err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestSessionListSaysWhichSessionsNeedApproval(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": "r1", "name": "notes", "state": "waiting_approval", "provider_driver": "claude", "approval_ref": "apr-1"},
		{"run_ref": "r2", "name": "docs", "state": "running", "provider_driver": "codex"},
	}
	out, _, err := execSessionCLI(t, nil, append([]string{"session", "ls"}, sessionCreds(f.URL)...)...)
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	lines := strings.Split(out, "\n")
	if !strings.Contains(lines[1], "notes") || !strings.Contains(lines[1], "needs approval") {
		t.Fatalf("ls =\n%s", out)
	}
	if !strings.Contains(out, "1 session needs approval: "+f.URL+"/permissions?tab=approvals") {
		t.Fatalf("ls does not say where to approve:\n%s", out)
	}
}
