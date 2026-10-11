// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The worktree start (K4.A2): a session opened from a handoff starts at the commit or
// branch the handoff names. Real git on a real repository, through createRun.

// branchWithCommit makes a branch of the repository with one commit past the current
// one that writes the files and removes the named ones, and returns that commit's id.
// The repository's own checkout stays where it was: the commit is made in a temporary
// worktree of the branch.
func branchWithCommit(t *testing.T, repo, branch string, write map[string]string, remove ...string) string {
	t.Helper()
	worktreeGitOut(t, repo, "branch", branch)
	other := filepath.Join(t.TempDir(), "sender")
	worktreeGitOut(t, repo, "worktree", "add", "--quiet", other, branch)
	for name, body := range write {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(other, name)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(other, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range remove {
		if err := os.Remove(filepath.Join(other, name)); err != nil {
			t.Fatal(err)
		}
	}
	worktreeGitOut(t, other, "add", "-A")
	worktreeGitOut(t, other, "commit", "-qm", "the handed-over work")
	worktreeGitOut(t, repo, "worktree", "remove", "--force", other)
	return worktreeGitOut(t, repo, "rev-parse", branch)
}

// handoffBranch is the branch work/handoff with one commit past main that adds
// feature.txt; it returns that commit's id.
func handoffBranch(t *testing.T, h *worktreeHarness) string {
	t.Helper()
	return branchWithCommit(t, h.repo, "work/handoff", map[string]string{"feature.txt": "handed over\n"})
}

func (h *worktreeHarness) launchFrom(from string) (runDTO, error) {
	p := h.params(true)
	p.WorktreeFrom = from
	return createProfiledTestRun(h.t, h.m, context.Background(), h.tenant, p)
}

func TestWorktreeStartsAtTheNamedCommitOrBranch(t *testing.T) {
	t.Parallel()
	for name, pick := range map[string]func(sha string) string{
		"commit id": func(sha string) string { return sha },
		"branch":    func(string) string { return "work/handoff" },
	} {
		h, _ := newWorktreeHarness(t)
		sha := handoffBranch(t, h)
		mainBefore := worktreeGitOut(t, h.repo, "rev-parse", "main")

		dto, err := h.launchFrom(pick(sha))
		if err != nil {
			t.Fatalf("%s: launch: %v", name, err)
		}

		dir := filepath.Join(h.root, dto.RunRef)
		if got := worktreeGitOut(t, dir, "rev-parse", "HEAD"); got != sha {
			t.Fatalf("%s: the worktree starts at %s, want the handoff's %s", name, got, sha)
		}
		if body, err := os.ReadFile(filepath.Join(dir, "feature.txt")); err != nil || string(body) != "handed over\n" {
			t.Fatalf("%s: the handed-over file is not in the worktree: %q %v", name, body, err)
		}
		own := "olivares/" + branchSuffix(dto.RunRef)
		if dto.WorktreeBranch != own || worktreeGitOut(t, dir, "rev-parse", "--abbrev-ref", "HEAD") != own {
			t.Fatalf("%s: the session is not on its own branch %s (record %q)", name, own, dto.WorktreeBranch)
		}
		// The sender's branch and the workspace checkout are not moved or taken over.
		if got := worktreeGitOut(t, h.repo, "rev-parse", "work/handoff"); got != sha {
			t.Fatalf("%s: the sender's branch moved to %s", name, got)
		}
		if got := worktreeGitOut(t, h.repo, "rev-parse", "main"); got != mainBefore {
			t.Fatalf("%s: the workspace branch moved to %s", name, got)
		}
		if _, err := os.Stat(filepath.Join(h.repo, "feature.txt")); err == nil {
			t.Fatalf("%s: the handed-over file appeared in the workspace checkout", name)
		}
	}
}

// Without a start the worktree begins at the workspace HEAD, exactly as before.
func TestWorktreeWithoutAStartBeginsAtTheWorkspaceHead(t *testing.T) {
	t.Parallel()
	h, _ := newWorktreeHarness(t)
	handoffBranch(t, h)

	dto, err := h.launchFrom("")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(h.root, dto.RunRef)
	if got, want := worktreeGitOut(t, dir, "rev-parse", "HEAD"), worktreeGitOut(t, h.repo, "rev-parse", "HEAD"); got != want {
		t.Fatalf("the worktree starts at %s, want the workspace HEAD %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "feature.txt")); err == nil {
		t.Fatal("an unnamed start picked up another branch's work")
	}
}

// What the person cannot honestly get is refused with the reason, and a refusal leaves
// no worktree, no branch and no directory behind. Nothing the caller wrote reaches git
// as an option or as a revision expression.
func TestWorktreeStartRefusals(t *testing.T) {
	t.Parallel()
	h, _ := newWorktreeHarness(t)
	handoffBranch(t, h)
	blob := worktreeGitOut(t, h.repo, "rev-parse", "HEAD:README")
	branchesBefore := h.branches()

	for name, c := range map[string]struct{ from, mention string }{
		"a commit that is not in the repository": {handoffRefTestSHA1, "not in the workspace repository"},
		"a branch that does not exist":           {"work/missing", "no local branch"},
		"an object that is not a commit":         {blob, ""},
		"a revision expression":                  {"main~1", "branch name"},
		"a range":                                {"main..work/handoff", "branch name"},
		"an option":                              {"--detach", "no local branch"},
		"an upload-pack option":                  {"--upload-pack=touch /tmp/x", "branch name"},
		"a path outside the refs":                {"../../etc/passwd", "branch name"},
		"a name with a control byte":             {"work/hand\x00off", "not a commit id or a branch name"},
		"a name past any ref":                    {strings.Repeat("a", 513), "not a commit id or a branch name"},
	} {
		_, err := h.launchFrom(c.from)
		t.Run(name, func(t *testing.T) { wantRunErr(t, err, http.StatusUnprocessableEntity, c.mention) })
	}
	if h.branches() != branchesBefore || h.worktreeCount() != 1 {
		t.Fatalf("a refused start left git state: %s / %d worktrees", h.branches(), h.worktreeCount())
	}
	if entries, _ := os.ReadDir(h.root); len(entries) != 0 {
		t.Fatalf("a refused start left a directory under the worktree root: %v", entries)
	}
	if len(h.fr.specs) != 0 {
		t.Fatalf("a refused start reached the runner: %d specs", len(h.fr.specs))
	}
}

// An id the object store still holds but no branch, tag or remote branch reaches is not
// work anyone handed over: an abandoned or dangling commit is refused.
func TestWorktreeStartRefusesACommitNoRefHolds(t *testing.T) {
	t.Parallel()
	h, _ := newWorktreeHarness(t)
	tree := worktreeGitOut(t, h.repo, "rev-parse", "HEAD^{tree}")
	dangling := worktreeGitOut(t, h.repo, "commit-tree", tree, "-m", "abandoned")

	_, err := h.launchFrom(dangling)

	wantRunErr(t, err, http.StatusUnprocessableEntity, "no branch or tag")
	if h.worktreeCount() != 1 || len(h.fr.specs) != 0 {
		t.Fatalf("a refused start left a worktree or reached the runner: %d worktrees, %d specs", h.worktreeCount(), len(h.fr.specs))
	}
}

// What a handoff names as a branch is the branch: a tag of the same name, which git's
// own revision rules would prefer, does not take its place.
func TestWorktreeStartPrefersTheBranchOverASameNamedTag(t *testing.T) {
	t.Parallel()
	h, _ := newWorktreeHarness(t)
	sha := handoffBranch(t, h)
	worktreeGitOut(t, h.repo, "tag", "work/handoff", "main")

	dto, err := h.launchFrom("work/handoff")
	if err != nil {
		t.Fatal(err)
	}

	if got := worktreeGitOut(t, filepath.Join(h.root, dto.RunRef), "rev-parse", "HEAD"); got != sha {
		t.Fatalf("the worktree starts at %s, want the branch's %s (the tag's is %s)", got, sha, worktreeGitOut(t, h.repo, "rev-parse", "main"))
	}
}

// A start without the worktree option would silently do nothing; it is refused.
func TestWorktreeStartNeedsTheWorktreeOption(t *testing.T) {
	t.Parallel()
	h, _ := newWorktreeHarness(t)
	sha := handoffBranch(t, h)

	p := h.params(false)
	p.WorktreeFrom = sha
	_, err := createProfiledTestRun(t, h.m, context.Background(), h.tenant, p)

	wantRunErr(t, err, http.StatusUnprocessableEntity, "worktree")
	if len(h.fr.specs) != 0 {
		t.Fatalf("a refused launch reached the runner: %d specs", len(h.fr.specs))
	}
}
