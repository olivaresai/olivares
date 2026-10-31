// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// realGit runs the real git in dir, as the confined runner does minus the OS
// boundary: no hooks, no inherited configuration.
func realGit(t *testing.T, dir string) LocalGitRunner {
	t.Helper()
	return func(ctx context.Context, args []string, input string, env []string) (string, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}, env...)
		cmd.Stdin = strings.NewReader(input)
		var out, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &stderr
		if err := cmd.Run(); err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return "", &LocalCommandExit{Code: exit.ExitCode(), Err: errors.New(strings.TrimSpace(stderr.String()))}
			}
			return "", err
		}
		return out.String(), nil
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := realGit(t, dir)(context.Background(), args, "", []string{"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.test", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.test"})
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(out)
}

func newRepo(t *testing.T, branch string) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", branch)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "a.txt")
	gitIn(t, dir, "commit", "-q", "-m", "first")
	return dir
}

func TestCommitAllCommitsEveryChangeAndNamesTheHead(t *testing.T) {
	dir := newRepo(t, "olivares/feature")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := gitIn(t, dir, "rev-parse", "HEAD")
	head, err := LocalRepository{Run: realGit(t, dir)}.CommitAll(context.Background(), "ship it", "Ada", "ada@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if !head.Committed || head.Branch != "olivares/feature" {
		t.Fatalf("head = %+v, want committed on olivares/feature", head)
	}
	if head.Commit == before || head.Commit != gitIn(t, dir, "rev-parse", "HEAD") || head.Tree != gitIn(t, dir, "rev-parse", "HEAD^{tree}") {
		t.Fatalf("head = %+v does not name the new HEAD and its tree", head)
	}
	if got := gitIn(t, dir, "status", "--porcelain"); got != "" {
		t.Fatalf("a change was left behind: %q", got)
	}
	if got := gitIn(t, dir, "log", "-1", "--format=%an <%ae>|%s"); got != "Ada <ada@example.test>|ship it" {
		t.Fatalf("commit = %q", got)
	}
}

func TestCommitAllOnACleanTreeNamesTheHeadWithoutACommit(t *testing.T) {
	dir := newRepo(t, "olivares/feature")
	head, err := LocalRepository{Run: realGit(t, dir)}.CommitAll(context.Background(), "", "", "")
	if err != nil {
		t.Fatalf("a clean tree needs no message or identity: %v", err)
	}
	if head.Committed || head.Commit != gitIn(t, dir, "rev-parse", "HEAD") {
		t.Fatalf("head = %+v, want the existing HEAD uncommitted", head)
	}
}

func TestCommitAllRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("detached HEAD has no branch to publish", func(t *testing.T) {
		dir := newRepo(t, "main")
		gitIn(t, dir, "checkout", "-q", "--detach")
		if _, err := (LocalRepository{Run: realGit(t, dir)}).CommitAll(ctx, "m", "Ada", "ada@example.test"); !errors.Is(err, ErrNoBranch) {
			t.Fatalf("err = %v, want ErrNoBranch", err)
		}
	})
	t.Run("a branch without a commit has nothing to publish", func(t *testing.T) {
		dir := t.TempDir()
		gitIn(t, dir, "init", "-q", "-b", "main")
		if _, err := (LocalRepository{Run: realGit(t, dir)}).CommitAll(ctx, "m", "Ada", "ada@example.test"); !errors.Is(err, ErrNoCommit) {
			t.Fatalf("err = %v, want ErrNoCommit", err)
		}
	})
	t.Run("changes need a message and nothing is staged without one", func(t *testing.T) {
		dir := newRepo(t, "main")
		if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := (LocalRepository{Run: realGit(t, dir)}).CommitAll(ctx, " ", "Ada", "ada@example.test"); !errors.Is(err, ErrLocalInput) {
			t.Fatalf("err = %v, want ErrLocalInput", err)
		}
		if _, err := (LocalRepository{Run: realGit(t, dir)}).CommitAll(ctx, "m", "", ""); !errors.Is(err, ErrLocalInput) {
			t.Fatalf("no identity: err = %v, want ErrLocalInput", err)
		}
		if got := gitIn(t, dir, "diff", "--cached", "--name-only"); got != "" {
			t.Fatalf("a refused ship staged %q", got)
		}
	})
}
