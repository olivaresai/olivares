// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

func worktreeFixture(t *testing.T) (repo, folder, data, outside string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable: " + err.Error())
	}
	root := t.TempDir()
	repo, folder, data = filepath.Join(root, "repository"), filepath.Join(root, "session"), filepath.Join(root, "data")
	outside = filepath.Join(root, "outside.txt")
	if err := os.MkdirAll(data, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktreeGit(t, root, "init", "-q", repo)
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktreeGit(t, repo, "add", "README")
	worktreeGit(t, repo, "commit", "-qm", "fixture base")
	worktreeGit(t, repo, "worktree", "add", "-q", "-b", "session-proof", folder)
	return
}

func worktreeGit(t *testing.T, folder string, args ...string) string {
	t.Helper()
	args = append([]string{"-c", "user.name=N1C fixture", "-c", "user.email=n1c@example.invalid", "-c", "commit.gpgsign=false", "-C", folder}, args...)
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func runWorktreeSession(t *testing.T, folder, data, preset, script string, args ...string) string {
	t.Helper()
	if state := confine.Probe(); state.Mode != confine.ModeLandlock {
		t.Skip("host cannot confine: " + state.Reason)
	}
	m := New(WithConfinement([]string{data}, true))
	proc, err := NewProcRunner().Launch(t.Context(), LaunchSpec{
		Program: "/bin/sh", Args: append([]string{"-c", script, "sh"}, args...), Dir: folder,
		Env:         []EnvVar{{Name: "GIT_CONFIG_NOSYSTEM", Value: "1"}, {Name: "GIT_CONFIG_GLOBAL", Value: "/dev/null"}},
		Confinement: m.sessionConfinement(folder, nil, preset), ConfinementRequired: true, WaitDelay: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for frame := range proc.Output() {
		out.Write(frame.Data)
	}
	if code, err := proc.Wait(); code != 0 || err != nil {
		t.Fatalf("confined worktree child: exit=%d err=%v\n%s", code, err, out.String())
	}
	return out.String()
}

func TestConfinedSessionCommitsInAGitWorktreeWithoutOpeningItsParent(t *testing.T) {
	_, folder, data, outside := worktreeFixture(t)
	out := runWorktreeSession(t, folder, data, PresetAsk, `set -e
git status --porcelain
printf 'confined edit\n' >> README
git add README
git -c user.name='N1C fixture' -c user.email=n1c@example.invalid -c commit.gpgsign=false commit -qm 'confined worktree commit'
git log -1 --format=%s
if cat "$1" 2>/dev/null; then exit 8; fi
if printf escaped > "$2" 2>/dev/null; then exit 9; fi
echo outside-denied`, outside, filepath.Join(filepath.Dir(outside), "escaped.txt"))
	if !strings.Contains(out, "confined worktree commit") || !strings.Contains(out, "outside-denied") {
		t.Fatalf("git did not complete with confinement controls: %s", out)
	}
	if got := worktreeGit(t, folder, "log", "-1", "--format=%s"); got != "confined worktree commit" {
		t.Fatalf("durable commit subject=%q", got)
	}
}

func TestReadOnlySessionReadsWorktreeGitWithoutChangingItsRefs(t *testing.T) {
	_, folder, data, _ := worktreeFixture(t)
	before := worktreeGit(t, folder, "rev-parse", "HEAD")
	out := runWorktreeSession(t, folder, data, PresetReadOnly, `set -e
git log -1 --format=%s
if git -c user.name='N1C fixture' -c user.email=n1c@example.invalid -c commit.gpgsign=false commit --allow-empty -qm 'forbidden commit' 2>/dev/null; then exit 8; fi
echo git-write-denied`)
	if !strings.Contains(out, "fixture base") || !strings.Contains(out, "git-write-denied") {
		t.Fatalf("read-only git effects=%s", out)
	}
	if got := worktreeGit(t, folder, "rev-parse", "HEAD"); got != before {
		t.Fatalf("read-only session changed HEAD: %s -> %s", before, got)
	}
}

func TestSessionDoesNotGrantAnotherWorktreesGitMetadata(t *testing.T) {
	repo, folder, data, _ := worktreeFixture(t)
	pointer, err := os.ReadFile(filepath.Join(folder, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"copy", "symlink"} {
		t.Run(method, func(t *testing.T) {
			foreign := filepath.Join(filepath.Dir(folder), "foreign-"+method)
			if err := os.Mkdir(foreign, 0o700); err != nil {
				t.Fatal(err)
			}
			if method == "copy" {
				err = os.WriteFile(filepath.Join(foreign, ".git"), pointer, 0o600)
			} else {
				err = os.Symlink(filepath.Join(folder, ".git"), filepath.Join(foreign, ".git"))
			}
			if err != nil {
				t.Fatal(err)
			}
			out := runWorktreeSession(t, foreign, data, PresetAsk, `if cat "$1" 2>/dev/null; then exit 8; fi; echo foreign-git-denied`, filepath.Join(repo, ".git", "config"))
			if !strings.Contains(out, "foreign-git-denied") {
				t.Fatalf("forged worktree pointer opened another repository: %s", out)
			}
		})
	}
}

func TestWorktreeGitGrantsDoNotOpenProtectedMetadata(t *testing.T) {
	repo, folder, _, _ := worktreeFixture(t)
	out := runWorktreeSession(t, folder, repo, PresetAsk, `if cat "$1" 2>/dev/null; then exit 8; fi; echo protected-git-denied`, filepath.Join(repo, ".git", "config"))
	if !strings.Contains(out, "protected-git-denied") {
		t.Fatalf("worktree reopened protected repository: %s", out)
	}
}

func TestSessionReadsGitThroughASymlinkedWorktreeFolder(t *testing.T) {
	_, folder, data, _ := worktreeFixture(t)
	alias := filepath.Join(filepath.Dir(folder), "folder-alias")
	if err := os.Symlink(folder, alias); err != nil {
		t.Fatal(err)
	}
	if out := runWorktreeSession(t, alias, data, PresetAsk, "git log -1 --format=%s"); !strings.Contains(out, "fixture base") {
		t.Fatalf("symlinked workspace cannot read its repository: %s", out)
	}
}
