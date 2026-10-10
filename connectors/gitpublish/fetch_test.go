// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitpublish

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sessionFixture is an empty managed server repository, a target remote and a
// session folder: a linked worktree on agent/run1 holding one new commit, the
// shape a session works in.
type sessionFixture struct {
	*gitFixture
	managed, session, main string
	commit, tree           string
}

func newSessionFixture(t *testing.T) *sessionFixture {
	t.Helper()
	f := newGitFixture(t)
	dir := t.TempDir()
	s := &sessionFixture{gitFixture: f, managed: filepath.Join(dir, "managed.git"), session: filepath.Join(dir, "session"), main: filepath.Join(dir, "main")}
	if err := f.x.InitManaged(context.Background(), s.managed); err != nil {
		t.Fatal(err)
	}
	run(t, dir, "clone", "-q", f.server, s.main)
	run(t, s.main, "worktree", "add", "-q", "-b", "agent/run1", s.session, f.base)
	writeFile(t, filepath.Join(s.session, "b.txt"), "agent\n")
	run(t, s.session, "add", "b.txt")
	run(t, s.session, "commit", "-q", "-m", "agent change")
	s.commit = run(t, s.session, "rev-parse", "HEAD")
	s.tree = run(t, s.session, "rev-parse", "HEAD^{tree}")
	return s
}

func TestFetchFeedsManagedRepositoryFromSessionWorktree(t *testing.T) {
	s := newSessionFixture(t)
	ctx := context.Background()
	if _, err := s.x.CommitTree(ctx, s.managed, s.commit); !errors.Is(err, ErrContent) {
		t.Fatalf("before the fetch: err = %v, want ErrContent", err)
	}
	if err := s.x.Fetch(ctx, s.managed, s.session, s.commit); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if tree, err := s.x.CommitTree(ctx, s.managed, s.commit); err != nil || tree != s.tree {
		t.Fatalf("after the fetch: tree = %q %v, want %s", tree, err, s.tree)
	}
	// The fetch writes objects only: no ref, no FETCH_HEAD, the managed config.
	if refs := run(t, s.managed, "for-each-ref"); refs != "" {
		t.Fatalf("the fetch wrote refs: %q", refs)
	}
	if _, err := os.Lstat(filepath.Join(s.managed, "FETCH_HEAD")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("FETCH_HEAD: %v, want none", err)
	}
	if err := s.x.CheckManagedConfig(ctx, s.managed); err != nil {
		t.Fatalf("managed config after the fetch: %v", err)
	}
	// The fed repository is a push source.
	r := s.req("refs/heads/agent/run1", "")
	r.RepoPath, r.Commit = s.managed, s.commit
	if res, err := s.x.Push(ctx, r); err != nil || res.Class != Applied {
		t.Fatalf("push from the fed repository = %+v %v", res, err)
	}
	if got := s.refAt(s.target, "refs/heads/agent/run1"); got != s.commit {
		t.Fatalf("target agent/run1 = %q, want %s", got, s.commit)
	}
}

func TestFetchSkipsAPresentCommit(t *testing.T) {
	s := newSessionFixture(t)
	ctx := context.Background()
	if err := s.x.Fetch(ctx, s.managed, s.session, s.commit); err != nil {
		t.Fatal(err)
	}
	// The session folder is gone; the commit is already in the managed repository.
	if err := os.RemoveAll(s.session); err != nil {
		t.Fatal(err)
	}
	if err := s.x.Fetch(ctx, s.managed, s.session, s.commit); err != nil {
		t.Fatalf("fetch of a present commit: %v, want nil", err)
	}
}

func TestFetchRefusesNonFileSourceBeforeGit(t *testing.T) {
	s := newSessionFixture(t)
	shim := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nprintf invoked > \"$HOME/git-invoked\"\nexit 97\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	x, err := NewExecutor(shim, s.x.home)
	if err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{
		"SSH":              "ssh://example.test/repo",
		"SCP syntax":       "example.test:repo",
		"external command": "ext::sh -c touch% /tmp/pwned",
		"HTTPS":            "https://example.test/repo.git",
		"file URL":         "file://" + s.session,
		"file helper":      "file::" + s.session,
		"relative path":    "session",
		"unclean path":     s.session + "/../session",
		"empty":            "",
	} {
		t.Run(name, func(t *testing.T) {
			if err := x.Fetch(context.Background(), s.managed, source, s.commit); !errors.Is(err, ErrSource) {
				t.Fatalf("err = %v, want ErrSource", err)
			}
			if _, err := os.Stat(filepath.Join(s.x.home, "git-invoked")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("git invocation marker: %v, want no subprocess", err)
			}
		})
	}
	if err := x.Fetch(context.Background(), s.managed, s.session, "HEAD"); !errors.Is(err, ErrContent) {
		t.Fatalf("a revision that is not an object id: err = %v, want ErrContent", err)
	}
}

func TestFetchRefusesRedirectingSessionRepository(t *testing.T) {
	for name, plant := range map[string]func(s *sessionFixture){
		"alternates": func(s *sessionFixture) {
			writeFile(t, filepath.Join(s.main, ".git", "objects", "info", "alternates"), filepath.Join(s.evil, "objects")+"\n")
		},
		"grafts": func(s *sessionFixture) { writeFile(t, filepath.Join(s.main, ".git", "info", "grafts"), s.commit+"\n") },
		"objects symlink": func(s *sessionFixture) {
			objects := filepath.Join(s.main, ".git", "objects")
			if err := os.Rename(objects, objects+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(objects+".real", objects); err != nil {
				t.Fatal(err)
			}
		},
		"not a repository": func(s *sessionFixture) {
			if err := os.Remove(filepath.Join(s.session, ".git")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSessionFixture(t)
			plant(s)
			if err := s.x.Fetch(context.Background(), s.managed, s.session, s.commit); !errors.Is(err, ErrSource) {
				t.Fatalf("err = %v, want ErrSource", err)
			}
			if _, err := s.x.CommitTree(context.Background(), s.managed, s.commit); !errors.Is(err, ErrContent) {
				t.Fatalf("a refused source fed the managed repository: %v", err)
			}
		})
	}
}

func TestFetchRefusesTamperedManagedRepository(t *testing.T) {
	s := newSessionFixture(t)
	ctx := context.Background()
	run(t, s.managed, "config", "core.hooksPath", "/tmp")
	if err := s.x.Fetch(ctx, s.managed, s.session, s.commit); !errors.Is(err, ErrRepositoryConfig) {
		t.Fatalf("err = %v, want ErrRepositoryConfig", err)
	}
	// Refused before git fetched anything.
	run(t, s.managed, "config", "--unset", "core.hooksPath")
	if _, err := s.x.CommitTree(ctx, s.managed, s.commit); !errors.Is(err, ErrContent) {
		t.Fatalf("a tampered repository was fed: %v", err)
	}
	// A present commit is no excuse: the check comes first.
	if err := s.x.Fetch(ctx, s.managed, s.session, s.commit); err != nil {
		t.Fatal(err)
	}
	run(t, s.managed, "config", "core.hooksPath", "/tmp")
	if err := s.x.Fetch(ctx, s.managed, s.session, s.commit); !errors.Is(err, ErrRepositoryConfig) {
		t.Fatalf("present commit, tampered repository: err = %v, want ErrRepositoryConfig", err)
	}
}

// TestFetchChecksManagedConfigAfterTheFetch: a git that changes the server
// repository's config during the fetch is caught before the push.
func TestFetchChecksManagedConfigAfterTheFetch(t *testing.T) {
	s := newSessionFixture(t)
	real := gitPath(t)
	shim := filepath.Join(t.TempDir(), "git")
	script := "#!/bin/sh\nfetch=\nfor a; do [ \"$a\" = fetch ] && fetch=1; done\n\"" + real + "\" \"$@\"; rc=$?\n" +
		"[ -n \"$fetch\" ] && \"" + real + "\" config --file \"$GIT_DIR/config\" core.hooksPath /tmp\nexit $rc\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	x, err := NewExecutor(shim, s.x.home)
	if err != nil {
		t.Fatal(err)
	}
	if err := x.Fetch(context.Background(), s.managed, s.session, s.commit); !errors.Is(err, ErrRepositoryConfig) {
		t.Fatalf("err = %v, want ErrRepositoryConfig", err)
	}
}

// TestFetchLeavesNoStagingRepository: the engine-made repository that borrows
// the session's objects is removed, whatever the outcome.
func TestFetchLeavesNoStagingRepository(t *testing.T) {
	s := newSessionFixture(t)
	missing := "0123456789abcdef0123456789abcdef01234567"
	_ = s.x.Fetch(context.Background(), s.managed, s.session, missing)
	if err := s.x.Fetch(context.Background(), s.managed, s.session, s.commit); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(s.managed))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(s.managed) && e.Name() != "session" && e.Name() != "main" {
			t.Fatalf("left behind: %s", e.Name())
		}
	}
	if _, err := os.Lstat(filepath.Join(s.managed, "objects", "info", "alternates")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the server repository borrowed objects: %v", err)
	}
}

func TestFetchOfACommitTheSessionLacksIsContent(t *testing.T) {
	s := newSessionFixture(t)
	missing := "0123456789abcdef0123456789abcdef01234567"
	if err := s.x.Fetch(context.Background(), s.managed, s.session, missing); !errors.Is(err, ErrContent) {
		t.Fatalf("err = %v, want ErrContent", err)
	}
}

// TestFetchNeverRunsSessionConfiguredCommands: the session writes its own
// repository config. A promisor remote over ext:: and a missing object make a
// git that serves the session repository run a lazy fetch; the fetch must not
// read that config at all.
func TestFetchNeverRunsSessionConfiguredCommands(t *testing.T) {
	s := newSessionFixture(t)
	marker := filepath.Join(t.TempDir(), "ran")
	common := filepath.Join(s.main, ".git")
	run(t, s.main, "config", "core.repositoryformatversion", "1")
	run(t, s.main, "config", "extensions.partialClone", "origin")
	run(t, s.main, "config", "remote.origin.promisor", "true")
	run(t, s.main, "config", "remote.origin.url", "ext::sh -c touch% "+marker)
	run(t, s.main, "config", "protocol.ext.allow", "always")
	run(t, s.main, "config", "uploadpack.packObjectsHook", "touch "+marker+";")
	run(t, s.main, "config", "core.sshCommand", "touch "+marker)
	blob := run(t, s.session, "rev-parse", s.commit+":b.txt")
	if err := os.Remove(filepath.Join(common, "objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	if err := s.x.Fetch(context.Background(), s.managed, s.session, s.commit); !errors.Is(err, ErrContent) {
		t.Fatalf("err = %v, want ErrContent (the blob is missing)", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a command from the session's config ran: %v", err)
	}
}

// TestFetchRefusesAGitfileIntoAnotherRepository: a session that rewrites its
// .git file to point at another repository the engine can read is refused,
// because that repository does not link the worktree back.
func TestFetchRefusesAGitfileIntoAnotherRepository(t *testing.T) {
	s := newSessionFixture(t)
	other := filepath.Join(t.TempDir(), "other")
	run(t, filepath.Dir(other), "init", "-q", other)
	writeFile(t, filepath.Join(other, "secret.txt"), "secret\n")
	run(t, other, "add", "secret.txt")
	run(t, other, "commit", "-q", "-m", "secret")
	secret := run(t, other, "rev-parse", "HEAD")
	for name, gitdir := range map[string]string{
		"main repository":         filepath.Join(other, ".git"),
		"another linked worktree": filepath.Join(s.main, ".git", "worktrees", "elsewhere"),
	} {
		t.Run(name, func(t *testing.T) {
			if name == "another linked worktree" {
				run(t, s.main, "worktree", "add", "-q", "--detach", filepath.Join(t.TempDir(), "elsewhere"), s.base)
			}
			writeFile(t, filepath.Join(s.session, ".git"), "gitdir: "+gitdir+"\n")
			if err := s.x.Fetch(context.Background(), s.managed, s.session, secret); !errors.Is(err, ErrSource) {
				t.Fatalf("err = %v, want ErrSource", err)
			}
		})
	}
}

// TestFetchRefusesAForgedWorktreeEntry: the session writes both ends of the
// worktree link inside its own folder, and the entry's commondir names
// another repository. Only an entry inside that repository's worktrees
// directory is git's own.
func TestFetchRefusesAForgedWorktreeEntry(t *testing.T) {
	s := newSessionFixture(t)
	other := filepath.Join(t.TempDir(), "other")
	run(t, filepath.Dir(other), "init", "-q", other)
	writeFile(t, filepath.Join(other, "secret.txt"), "secret\n")
	run(t, other, "add", "secret.txt")
	run(t, other, "commit", "-q", "-m", "secret")
	secret := run(t, other, "rev-parse", "HEAD")
	fake := filepath.Join(s.session, "fake")
	writeFile(t, filepath.Join(fake, "HEAD"), s.commit+"\n")
	writeFile(t, filepath.Join(fake, "gitdir"), filepath.Join(s.session, ".git")+"\n")
	writeFile(t, filepath.Join(fake, "commondir"), filepath.Join(other, ".git")+"\n")
	writeFile(t, filepath.Join(s.session, ".git"), "gitdir: "+fake+"\n")
	if err := s.x.Fetch(context.Background(), s.managed, s.session, secret); !errors.Is(err, ErrSource) {
		t.Fatalf("err = %v, want ErrSource", err)
	}
	if _, err := s.x.CommitTree(context.Background(), s.managed, secret); !errors.Is(err, ErrContent) {
		t.Fatalf("a forged entry fed another repository's commit: %v", err)
	}
}

func TestFetchFromAPlainRepositoryAndNotASubdirectory(t *testing.T) {
	s := newSessionFixture(t)
	ctx := context.Background()
	sub := filepath.Join(s.main, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.x.Fetch(ctx, s.managed, sub, s.commit); !errors.Is(err, ErrSource) {
		t.Fatalf("a subdirectory: err = %v, want ErrSource", err)
	}
	if err := s.x.Fetch(ctx, s.managed, s.main, s.commit); err != nil {
		t.Fatalf("a plain repository: %v", err)
	}
	if _, err := s.x.CommitTree(ctx, s.managed, s.commit); err != nil {
		t.Fatalf("after the fetch: %v", err)
	}
}

// TestFetchRefusesSymlinksInsideSessionObjects: a session links pack files,
// its pack directory or loose objects to another repository the engine can
// read. git follows those links with the engine's rights, so any link below
// objects/ is refused, not only objects itself.
func TestFetchRefusesSymlinksInsideSessionObjects(t *testing.T) {
	for name, plant := range map[string]func(t *testing.T, objects, other string){
		"pack directory": func(t *testing.T, objects, other string) {
			if err := os.RemoveAll(filepath.Join(objects, "pack")); err != nil {
				t.Fatal(err)
			}
			symlink(t, filepath.Join(other, "pack"), filepath.Join(objects, "pack"))
		},
		"pack files": func(t *testing.T, objects, other string) {
			linkEntries(t, filepath.Join(other, "pack"), filepath.Join(objects, "pack"))
		},
		"loose fan-out directories": func(t *testing.T, objects, other string) {
			linkEntries(t, other, objects)
		},
		"loose object files": func(t *testing.T, objects, other string) {
			entries, err := os.ReadDir(other)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range entries {
				if err := os.MkdirAll(filepath.Join(objects, e.Name()), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			linkEntries(t, other, objects)
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSessionFixture(t)
			other := filepath.Join(t.TempDir(), "other")
			run(t, filepath.Dir(other), "init", "-q", other)
			writeFile(t, filepath.Join(other, "secret.txt"), "secret\n")
			run(t, other, "add", "secret.txt")
			run(t, other, "commit", "-q", "-m", "secret")
			if strings.HasPrefix(name, "pack") {
				run(t, other, "repack", "-q", "-a", "-d")
			}
			secret := run(t, other, "rev-parse", "HEAD")
			plant(t, filepath.Join(s.main, ".git", "objects"), filepath.Join(other, ".git", "objects"))
			if err := s.x.Fetch(context.Background(), s.managed, s.session, secret); !errors.Is(err, ErrSource) {
				t.Fatalf("err = %v, want ErrSource", err)
			}
			if _, err := s.x.CommitTree(context.Background(), s.managed, secret); !errors.Is(err, ErrContent) {
				t.Fatalf("a link fed another repository's commit: %v", err)
			}
		})
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// linkEntries links each pack file or loose fan-out directory of src into dst:
// a whole fan-out directory when dst lacks it, else each object in it.
func linkEntries(t *testing.T, src, dst string) {
	t.Helper()
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if name == "info" || name == "pack" {
			continue
		}
		from, to := filepath.Join(src, name), filepath.Join(dst, name)
		if !e.IsDir() {
			symlink(t, from, to)
			continue
		}
		if _, err := os.Lstat(to); err != nil {
			symlink(t, from, to)
			continue
		}
		linkEntries(t, from, to)
	}
}

// TestFetchFromAPackedSessionWithACommitGraph: a session store git has
// packed, indexed and graphed is plain files, not a refusal.
func TestFetchFromAPackedSessionWithACommitGraph(t *testing.T) {
	s := newSessionFixture(t)
	run(t, s.main, "repack", "-q", "-a", "-d")
	run(t, s.main, "commit-graph", "write", "--reachable")
	run(t, s.main, "multi-pack-index", "write")
	if err := s.x.Fetch(context.Background(), s.managed, s.session, s.commit); err != nil {
		t.Fatal(err)
	}
	if _, err := s.x.CommitTree(context.Background(), s.managed, s.commit); err != nil {
		t.Fatalf("after the fetch: %v", err)
	}
}

// TestFetchOfAnUnreadableSessionStoreIsNotARefusal: an object directory the
// engine cannot read is a failure to retry, not a refused source.
func TestFetchOfAnUnreadableSessionStoreIsNotARefusal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode 000 directory")
	}
	s := newSessionFixture(t)
	pack := filepath.Join(s.main, ".git", "objects", "pack")
	if err := os.Chmod(pack, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(pack, 0o755) })
	err := s.x.Fetch(context.Background(), s.managed, s.session, s.commit)
	if err == nil || errors.Is(err, ErrSource) || errors.Is(err, ErrContent) {
		t.Fatalf("err = %v, want an unclassified read error", err)
	}
}

// TestFetchReadsOnlyGitsOwnSessionLayout pins which session layouts the fetch
// reads: git's own linked worktree with relative pointers or a backlink
// through a linked folder is fed; a .git that is a link, a .git directory
// whose commondir names another repository, and a repository whose path holds
// a newline (one line of the alternates file) are refused.
func TestFetchReadsOnlyGitsOwnSessionLayout(t *testing.T) {
	for name, plant := range map[string]func(t *testing.T, s *sessionFixture, session, entry string){
		"relative pointers": func(t *testing.T, s *sessionFixture, session, entry string) {
			rel := func(from, to string) string {
				r, err := filepath.Rel(from, to)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			writeFile(t, filepath.Join(s.session, ".git"), "gitdir: "+rel(session, entry)+"\n")
			writeFile(t, filepath.Join(entry, "gitdir"), rel(entry, filepath.Join(session, ".git"))+"\n")
		},
		"a backlink through a linked folder": func(t *testing.T, s *sessionFixture, session, entry string) {
			alias := filepath.Join(t.TempDir(), "alias")
			symlink(t, session, alias)
			writeFile(t, filepath.Join(entry, "gitdir"), filepath.Join(alias, ".git")+"\n")
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSessionFixture(t)
			session, err := filepath.EvalSymlinks(s.session)
			if err != nil {
				t.Fatal(err)
			}
			plant(t, s, session, run(t, s.session, "rev-parse", "--absolute-git-dir"))
			if err := s.x.Fetch(context.Background(), s.managed, s.session, s.commit); err != nil {
				t.Fatalf("fetch: %v", err)
			}
			if _, err := s.x.CommitTree(context.Background(), s.managed, s.commit); err != nil {
				t.Fatalf("after the fetch: %v", err)
			}
		})
	}
	for name, plant := range map[string]func(t *testing.T, s *sessionFixture, other string) string{
		"a linked .git": func(t *testing.T, s *sessionFixture, _ string) string {
			dotgit := filepath.Join(s.session, ".git")
			moved := filepath.Join(t.TempDir(), "pointer")
			if err := os.Rename(dotgit, moved); err != nil {
				t.Fatal(err)
			}
			symlink(t, moved, dotgit)
			return s.session
		},
		"a .git directory naming another common directory": func(t *testing.T, s *sessionFixture, other string) string {
			writeFile(t, filepath.Join(s.main, ".git", "commondir"), filepath.Join(other, ".git")+"\n")
			return s.main
		},
		"a repository path with a newline": func(t *testing.T, _ *sessionFixture, other string) string {
			clone := filepath.Join(t.TempDir(), "a\nb")
			run(t, filepath.Dir(clone), "clone", "-q", other, clone)
			return clone
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSessionFixture(t)
			other := filepath.Join(t.TempDir(), "other")
			run(t, filepath.Dir(other), "init", "-q", other)
			writeFile(t, filepath.Join(other, "secret.txt"), "secret\n")
			run(t, other, "add", "secret.txt")
			run(t, other, "commit", "-q", "-m", "secret")
			secret := run(t, other, "rev-parse", "HEAD")
			source := plant(t, s, other)
			if err := s.x.Fetch(context.Background(), s.managed, source, secret); !errors.Is(err, ErrSource) {
				t.Fatalf("err = %v, want ErrSource", err)
			}
			if _, err := s.x.CommitTree(context.Background(), s.managed, secret); !errors.Is(err, ErrContent) {
				t.Fatalf("a refused layout fed another repository's commit: %v", err)
			}
		})
	}
}
