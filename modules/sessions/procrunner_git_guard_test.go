// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// guardScript is what a session might do to the Git metadata of its folder, and
// what Git itself must still be able to do. Each line says blocked or writable.
const guardScriptTemplate = `
export G="$(git rev-parse --absolute-git-dir)" C="$(cd "$(git rev-parse --git-common-dir)" && pwd)"
w() { if sh -c "$2" 2>/dev/null; then echo "$1=writable"; else echo "$1=blocked"; fi; }
w hook 'echo "#!/bin/sh" > "$(git rev-parse --git-common-dir)/hooks/pre-commit"'
w config 'git config user.name planted'
w config-append 'echo "[core]" >> "$(git rev-parse --git-common-dir)/config"'
w newfile 'echo y > top-level-file'
w common-commondir 'echo ../x > "$C/commondir"'
w config-worktree 'echo "[core]" > "$G/config.worktree"'
w common-config-worktree 'echo "[core]" > "$C/config.worktree"'
w move-gitdir 'mv "$G" "$G.moved"'
LINKED
git switch -q -c tracked origin/main 2>/dev/null
echo "track-upstream=$(git config --get branch.tracked.remote 2>/dev/null)"
echo "track-branch=$(git rev-parse --abbrev-ref HEAD)"
git add top-level-file
git -c user.name=fixture -c user.email=f@example.invalid -c commit.gpgsign=false commit -qm "guarded commit"
echo "commit=$(git log -1 --format=%s)"
`

// linkedChecks are the extra writes that matter only in a linked worktree: the
// links that say where the common directory is, from both sides, and the
// directory that holds every worktree's entry.
const linkedChecks = `
w commondir 'echo ../x > "$(git rev-parse --absolute-git-dir)/commondir"'
w backlink 'echo /x > "$(git rev-parse --absolute-git-dir)/gitdir"'
w pointer 'echo "gitdir: /x" > .git'
w pointer-move 'mv .git .git.bak'
w worktrees-move 'd="$(dirname "$(git rev-parse --absolute-git-dir)")"; mv "$d" "$d.x"'
`

func guardScript(linked bool) string {
	if linked {
		return strings.Replace(guardScriptTemplate, "LINKED", linkedChecks, 1)
	}
	return strings.Replace(guardScriptTemplate, "LINKED", "", 1)
}

// hostLacksMounts reports a host that gives a session no mount namespace.
func hostLacksMounts(t *testing.T) bool {
	t.Helper()
	probe := &exec.Cmd{Path: "/bin/true", Args: []string{"/bin/true", confine.HelperArg, "--", "/bin/true"}}
	return errors.Is(confine.WrapMounts(probe, confine.Mounts{Pin: []string{t.TempDir()}}), confine.ErrMountsUnavailable)
}

// withRemoteTrackingBranch gives the repository an origin and origin/main, so
// that `git switch -c <name> origin/main` has an upstream to record.
func withRemoteTrackingBranch(t *testing.T, repo string) {
	t.Helper()
	worktreeGit(t, repo, "config", "remote.origin.url", "https://example.invalid/repository.git")
	worktreeGit(t, repo, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	worktreeGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
}

func lines(out string) map[string]string {
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name, value, _ := strings.Cut(line, "=")
		got[name] = value
	}
	return got
}

func TestSessionCannotWriteGitHooksOrConfigButStillCommits(t *testing.T) {
	type fixture func(t *testing.T) (folder, data, hooks string, linked bool)
	for name, plant := range map[string]fixture{
		"a linked worktree": func(t *testing.T) (string, string, string, bool) {
			repo, folder, data, _ := worktreeFixture(t)
			withRemoteTrackingBranch(t, repo)
			return folder, data, filepath.Join(realPath(t, filepath.Join(repo, ".git")), "hooks"), true
		},
		"a plain clone": func(t *testing.T) (string, string, string, bool) {
			repo, _, data, _ := worktreeFixture(t)
			withRemoteTrackingBranch(t, repo)
			return repo, data, filepath.Join(realPath(t, filepath.Join(repo, ".git")), "hooks"), false
		},
	} {
		t.Run(name, func(t *testing.T) {
			if hostLacksMounts(t) {
				t.Skip("this host gives a session no user and mount namespace; the next test covers it")
			}
			dir, data, hooks, linked := plant(t)
			out, state := launchWorktreeSession(t, dir, data, PresetAsk, true, guardScript(linked))
			got := lines(out)
			want := map[string]string{"hook": "blocked", "config": "blocked", "config-append": "blocked", "newfile": "writable", "move-gitdir": "blocked", "commit": "guarded commit", "track-branch": "tracked", "track-upstream": "",
				"common-commondir": "blocked", "config-worktree": "blocked", "common-config-worktree": "blocked"}
			if linked {
				for _, k := range []string{"commondir", "backlink", "pointer", "pointer-move", "worktrees-move"} {
					want[k] = "blocked"
				}
			}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q (output: %s)", k, got[k], v, out)
				}
			}
			if _, err := os.Lstat(filepath.Join(hooks, "pre-commit")); err == nil {
				t.Error("the session left a pre-commit hook in the repository")
			}
			if strings.Contains(state.String(), "stay writable") {
				t.Errorf("state names a missing protection on a host that has it: %s", state)
			}
		})
	}
}

// The same script without the mounts: every write above succeeds, so the
// guarded test is red for the mounts and for nothing else.
func TestUnguardedSessionWritesGitHooksAndConfig(t *testing.T) {
	repo, _, data, _ := worktreeFixture(t)
	out, _ := launchWorktreeSession(t, repo, data, PresetAsk, false, guardScript(false))
	got := lines(out)
	for _, k := range []string{"hook", "config", "config-append", "newfile", "common-commondir", "config-worktree", "common-config-worktree", "move-gitdir"} {
		if got[k] != "writable" {
			t.Errorf("%s = %q without the mounts, want writable (output: %s)", k, got[k], out)
		}
	}
}

// A guard the launch cannot apply is a bug, not a host limit: it refuses the
// launch before the program runs.
func TestRelativeGuardPathRefusesTheLaunch(t *testing.T) {
	if state := confine.Probe(); state.Mode != confine.ModeLandlock {
		t.Skip("host cannot confine: " + state.Reason)
	}
	m := New(WithConfinement([]string{t.TempDir()}, true))
	folder := t.TempDir()
	_, _, _, release, err := (&procRunner{}).command(t.Context(), LaunchSpec{
		Program: "/bin/true", Dir: folder, Confinement: m.sessionConfinement(folder, nil, PresetAsk), ConfinementRequired: true,
		ConfinementMounts: confine.Mounts{ReadOnly: []string{"relative/hooks"}},
	})
	if release != nil {
		release()
	}
	if err == nil || !strings.Contains(err.Error(), "not an absolute path") {
		t.Fatalf("command = %v, want the relative guard path refused", err)
	}
}

// A host without unprivileged user namespaces must keep starting sessions: the
// session runs, and its confinement reason names what it lacks.
func TestSessionStartsWithoutMountNamespacesAndSaysWhatIsMissing(t *testing.T) {
	if !hostLacksMounts(t) {
		t.Skip("this host gives a session a mount namespace; the test above covers it")
	}
	_, folder, data, _ := worktreeFixture(t)
	// Only what the session needs: guardScript's attacks succeed on this host and
	// would take the repository apart before the commit.
	const script = `
echo y > top-level-file && echo "newfile=writable"
git add top-level-file
git -c user.name=fixture -c user.email=f@example.invalid -c commit.gpgsign=false commit -qm "guarded commit"
echo "commit=$(git log -1 --format=%s)"
`
	out, state := launchWorktreeSession(t, folder, data, PresetAsk, true, script)
	if got := lines(out); got["commit"] != "guarded commit" || got["newfile"] != "writable" {
		t.Fatalf("the session did not run: %s", out)
	}
	if !strings.Contains(state.String(), "Git hooks and configuration stay writable") {
		t.Fatalf("confinement reason does not name the missing protection: %s", state)
	}
}
