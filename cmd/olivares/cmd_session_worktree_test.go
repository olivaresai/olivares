// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// The opt-in worktree per session (K4.A1) at the CLI: `session start --worktree` asks
// the engine for one, `session rm --discard-worktree` is the confirmation to remove it
// with unmerged work, and without either flag the requests are what they were before.

func TestSessionStartAsksForAWorktreeOnlyWhenTold(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		asked bool
	}{
		{"default", nil, false},
		{"worktree", []string{"--worktree"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSessionEngine(t)
			_, errb, err := execSessionCLI(t, nil, append(append([]string{"session", "start", t.TempDir()}, tc.args...), sessionCreds(f.URL)...)...)
			if err != nil {
				t.Fatalf("start: %v\n%s", err, errb)
			}
			runs := f.postsTo(sessionRunsPath)
			if len(runs) != 1 {
				t.Fatalf("%d launches", len(runs))
			}
			got, present := runs[0]["worktree"]
			if tc.asked && got != true {
				t.Fatalf("--worktree sent worktree=%v", got)
			}
			if !tc.asked && present {
				t.Fatalf("a start without --worktree sent worktree=%v; the request must be what it was", got)
			}
		})
	}
}

// `session start --worktree-from` (K4.A2) opens the work a handoff names: it sends the
// start and implies the worktree, and a start without it sends neither key.
func TestSessionStartSendsTheWorktreeStartOnlyWhenTold(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct {
		name         string
		args         []string
		worktree     bool
		worktreeFrom string
	}{
		{"default", nil, false, ""},
		{"worktree alone", []string{"--worktree"}, true, ""},
		{"a commit", []string{"--worktree-from", sha}, true, sha},
		{"a branch with the worktree flag", []string{"--worktree", "--worktree-from", "work/handoff"}, true, "work/handoff"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSessionEngine(t)
			_, errb, err := execSessionCLI(t, nil, append(append([]string{"session", "start", t.TempDir()}, tc.args...), sessionCreds(f.URL)...)...)
			if err != nil {
				t.Fatalf("start: %v\n%s", err, errb)
			}
			runs := f.postsTo(sessionRunsPath)
			if len(runs) != 1 {
				t.Fatalf("%d launches", len(runs))
			}
			if got, present := runs[0]["worktree"]; present != tc.worktree || (present && got != true) {
				t.Fatalf("worktree = %v (present %v), want present=%v", got, present, tc.worktree)
			}
			got, present := runs[0]["worktree_from"]
			if tc.worktreeFrom == "" && present {
				t.Fatalf("a start without --worktree-from sent worktree_from=%v; the request must be what it was", got)
			}
			if tc.worktreeFrom != "" && got != tc.worktreeFrom {
				t.Fatalf("worktree_from = %v, want %q", got, tc.worktreeFrom)
			}
		})
	}
}

func TestSessionRemoveConfirmsTheWorktreeDiscardOnlyWhenTold(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		discard bool
	}{
		{"default", nil, false},
		{"discard", []string{"--discard-worktree"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeSessionEngine(t)
			f.runs = []map[string]any{{"run_ref": "r1", "name": "hr", "state": "stopped", "provider_driver": "claude"}}
			out, errb, err := execSessionCLI(t, nil, append(append([]string{"session", "rm", "hr"}, tc.args...), sessionCreds(f.URL)...)...)
			if err != nil || strings.TrimSpace(out) != "Removed hr." {
				t.Fatalf("rm: out=%q err=%v stderr=%q", out, err, errb)
			}
			posts := f.postsTo(sessionRunsPath + "/r1/cleanup")
			if len(posts) != 1 {
				t.Fatalf("%d cleanups", len(posts))
			}
			got, present := posts[0]["discard_worktree"]
			if tc.discard && got != true {
				t.Fatalf("--discard-worktree sent discard_worktree=%v", got)
			}
			if !tc.discard && present {
				t.Fatalf("a rm without the flag sent discard_worktree=%v", got)
			}
		})
	}
}

func TestSessionShowNamesTheBranchOfAWorktreeSession(t *testing.T) {
	f := newFakeSessionEngine(t)
	f.runs = []map[string]any{
		{"run_ref": "r1", "name": "feat", "state": "running", "provider_driver": "claude", "worktree_branch": "olivares/ab12cd34"},
		{"run_ref": "r2", "name": "plain", "state": "running", "provider_driver": "claude"},
	}
	show := func(name string) string {
		t.Helper()
		out, errb, err := execSessionCLI(t, nil, append([]string{"session", "show", name}, sessionCreds(f.URL)...)...)
		if err != nil {
			t.Fatalf("show %s: %v\n%s", name, err, errb)
		}
		return out
	}
	if out := show("feat"); !strings.Contains(out, "BRANCH") || !strings.Contains(out, "olivares/ab12cd34") {
		t.Fatalf("a worktree session's branch is missing:\n%s", out)
	}
	if out := show("plain"); strings.Contains(out, "BRANCH") {
		t.Fatalf("a session without a worktree shows a branch row:\n%s", out)
	}
}

// A release the engine refuses for a worktree names the flag that confirms it; any
// other refusal, and a session without a worktree, is printed as the engine said it.
func TestSessionRemoveSaysWhichFlagConfirmsAWorktreeRefusal(t *testing.T) {
	refusal := "branch olivares/ab12cd34 has work that is not merged into the workspace's current branch; merge or commit the work, or confirm to discard the session's worktree and branch"
	f := newFakeSessionEngine(t)
	f.cleanupRefusal = refusal
	f.runs = []map[string]any{
		{"run_ref": "r1", "name": "feat", "state": "stopped", "provider_driver": "claude", "worktree_branch": "olivares/ab12cd34"},
		{"run_ref": "r2", "name": "plain", "state": "stopped", "provider_driver": "claude"},
	}

	_, _, err := execSessionCLI(t, nil, append([]string{"session", "rm", "feat"}, sessionCreds(f.URL)...)...)
	if err == nil || !strings.Contains(err.Error(), refusal) || !strings.Contains(err.Error(), "--discard-worktree") {
		t.Fatalf("a worktree refusal does not name the confirming flag: %v", err)
	}
	if exitcode.From(err) != exitcode.Conflict {
		t.Fatalf("exit code = %d, want conflict", exitcode.From(err))
	}

	_, _, err = execSessionCLI(t, nil, append([]string{"session", "rm", "plain"}, sessionCreds(f.URL)...)...)
	if err == nil || !strings.Contains(err.Error(), refusal) || strings.Contains(err.Error(), "--discard-worktree") ||
		exitcode.From(err) != exitcode.Conflict {
		t.Fatalf("a session without a worktree was given the worktree flag, or lost the engine's sentence: %v", err)
	}
}
