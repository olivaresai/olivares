// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// layoutPolicy is the confinement of a session launched in folder with protect
// as the engine's protected paths, read without blocking.
func layoutPolicy(t *testing.T, folder, protect, preset string) *confine.Policy {
	t.Helper()
	m := New(WithConfinement([]string{protect}, true))
	done := make(chan *confine.Policy, 1)
	go func() { done <- m.sessionConfinement(folder, nil, preset) }()
	select {
	case p := <-done:
		return p
	case <-time.After(20 * time.Second):
		t.Fatal("reading the session folder's git layout blocked")
		return nil
	}
}

// worktreeGrants is what a session launched in folder gets beyond the folder
// itself: the linked worktree's git metadata, or nothing.
func worktreeGrants(t *testing.T, folder, protect string) []string {
	t.Helper()
	return layoutPolicy(t, folder, protect, PresetAsk).ReadWrite[1:]
}

func realPath(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func relPath(t *testing.T, from, to string) string {
	t.Helper()
	r, err := filepath.Rel(from, to)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestSessionGitLayoutGrantsOnlyGitsOwnLinkedWorktree pins which git
// directories a session folder owns: a linked worktree's entry and its
// repository's common directory, through the folder or a link to it, with
// absolute, relative or linked pointers, never beneath a protected path. A
// plain clone owns nothing outside itself.
func TestSessionGitLayoutGrantsOnlyGitsOwnLinkedWorktree(t *testing.T) {
	for name, plant := range map[string]func(t *testing.T, repo, folder, entry string) string{
		"a linked worktree": func(t *testing.T, _, folder, _ string) string { return folder },
		"a linked folder": func(t *testing.T, _, folder, _ string) string {
			alias := filepath.Join(filepath.Dir(folder), "folder-alias")
			if err := os.Symlink(folder, alias); err != nil {
				t.Fatal(err)
			}
			return alias
		},
		// git worktree --relative-paths writes both pointers relative.
		"relative pointers": func(t *testing.T, _, folder, entry string) string {
			writeTestFile(t, filepath.Join(folder, ".git"), "gitdir: "+relPath(t, realPath(t, folder), entry)+"\n")
			writeTestFile(t, filepath.Join(entry, "gitdir"), relPath(t, entry, filepath.Join(realPath(t, folder), ".git"))+"\n")
			return folder
		},
		"pointers through links": func(t *testing.T, _, folder, entry string) string {
			root := filepath.Dir(folder)
			if err := os.Symlink(filepath.Dir(entry), filepath.Join(root, "entries")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(folder, filepath.Join(root, "folder-alias")); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, filepath.Join(folder, ".git"), "gitdir: "+filepath.Join(root, "entries", filepath.Base(entry))+"\n")
			writeTestFile(t, filepath.Join(entry, "gitdir"), filepath.Join(root, "folder-alias", ".git")+"\n")
			return folder
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo, folder, data, _ := worktreeFixture(t)
			common := realPath(t, filepath.Join(repo, ".git"))
			entry := filepath.Join(common, "worktrees", filepath.Base(folder))
			session := plant(t, repo, folder, entry)
			want := []string{entry, common}
			if got := worktreeGrants(t, session, data); !slices.Equal(got, want) {
				t.Fatalf("grants = %q, want %q", got, want)
			}
			// A session's run worktree is a real directory, never a link to one.
			err := New().isRunWorktree(t.Context(), repo, session)
			if st, _ := os.Lstat(session); st.Mode()&os.ModeSymlink != 0 {
				if err == nil || !strings.Contains(err.Error(), "not a directory") {
					t.Fatalf("isRunWorktree(linked folder) = %v, want it is not a directory", err)
				}
			} else if err != nil {
				t.Fatalf("isRunWorktree = %v", err)
			}
		})
	}
	t.Run("read-only", func(t *testing.T) {
		repo, folder, data, _ := worktreeFixture(t)
		common := realPath(t, filepath.Join(repo, ".git"))
		p := layoutPolicy(t, folder, data, PresetReadOnly)
		want := []string{folder, filepath.Join(common, "worktrees", filepath.Base(folder)), common}
		if !slices.Equal(p.ReadOnly, want) || len(p.ReadWrite) != 0 || !slices.Equal(p.Sealed, []string{folder}) {
			t.Fatalf("read-only policy = ro %q rw %q sealed %q, want ro %q", p.ReadOnly, p.ReadWrite, p.Sealed, want)
		}
	})
	t.Run("beneath a protected path", func(t *testing.T) {
		repo, folder, _, _ := worktreeFixture(t)
		if got := worktreeGrants(t, folder, repo); len(got) != 0 {
			t.Fatalf("grants beneath the protected repository = %q, want none", got)
		}
		entry := filepath.Join(realPath(t, filepath.Join(repo, ".git")), "worktrees", filepath.Base(folder))
		if got, want := worktreeGrants(t, folder, entry), []string{realPath(t, filepath.Join(repo, ".git"))}; !slices.Equal(got, want) {
			t.Fatalf("grants with the entry protected = %q, want %q", got, want)
		}
	})
	t.Run("a plain clone", func(t *testing.T) {
		repo, _, data, _ := worktreeFixture(t)
		if got := worktreeGrants(t, repo, data); len(got) != 0 {
			t.Fatalf("grants = %q, want none", got)
		}
		if err := New().isRunWorktree(t.Context(), repo, repo); err == nil {
			t.Fatal("isRunWorktree = nil, want a refusal")
		}
	})
	t.Run("a worktree of another repository", func(t *testing.T) {
		_, folder, _, _ := worktreeFixture(t)
		other, _, _, _ := worktreeFixture(t)
		if err := New().isRunWorktree(t.Context(), other, folder); err == nil || !strings.Contains(err.Error(), "another repository") {
			t.Fatalf("isRunWorktree = %v, want it belongs to another repository", err)
		}
	})
}

// TestSessionGitLayoutRefusesWhatIsNotGitsOwn pins the refusals: a pointer
// another folder copied or linked, an entry forged inside the session folder
// that names another repository, and pointer files that are not short regular
// files (read without blocking).
func TestSessionGitLayoutRefusesWhatIsNotGitsOwn(t *testing.T) {
	fifo := func(t *testing.T, path string) {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		// A writer holding it open makes a blocking open return and a read wait.
		w, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { w.Close() })
	}
	for name, plant := range map[string]func(t *testing.T, repo, folder, entry string) string{
		"a copied pointer": func(t *testing.T, _, folder, _ string) string {
			foreign := filepath.Join(filepath.Dir(folder), "foreign")
			pointer, err := os.ReadFile(filepath.Join(folder, ".git"))
			if err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, filepath.Join(foreign, ".git"), string(pointer))
			return foreign
		},
		"a linked .git": func(t *testing.T, _, folder, _ string) string {
			foreign := filepath.Join(filepath.Dir(folder), "foreign")
			if err := os.Mkdir(foreign, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(folder, ".git"), filepath.Join(foreign, ".git")); err != nil {
				t.Fatal(err)
			}
			return foreign
		},
		"a pointer into another repository": func(t *testing.T, _, folder, _ string) string {
			other := filepath.Join(filepath.Dir(folder), "other")
			worktreeGit(t, filepath.Dir(folder), "init", "-q", other)
			writeTestFile(t, filepath.Join(folder, ".git"), "gitdir: "+filepath.Join(other, ".git")+"\n")
			return folder
		},
		"a forged entry in the folder": func(t *testing.T, _, folder, _ string) string {
			other := filepath.Join(filepath.Dir(folder), "other")
			worktreeGit(t, filepath.Dir(folder), "init", "-q", other)
			fake := filepath.Join(folder, "fake")
			writeTestFile(t, filepath.Join(fake, "HEAD"), "ref: refs/heads/main\n")
			writeTestFile(t, filepath.Join(fake, "gitdir"), filepath.Join(realPath(t, folder), ".git")+"\n")
			writeTestFile(t, filepath.Join(fake, "commondir"), filepath.Join(other, ".git")+"\n")
			writeTestFile(t, filepath.Join(folder, ".git"), "gitdir: "+fake+"\n")
			return folder
		},
		"a FIFO .git": func(t *testing.T, _, folder, _ string) string {
			fifo(t, filepath.Join(folder, ".git"))
			return folder
		},
		"a FIFO backlink": func(t *testing.T, _, folder, entry string) string {
			fifo(t, filepath.Join(entry, "gitdir"))
			return folder
		},
		"a FIFO commondir": func(t *testing.T, _, folder, entry string) string {
			fifo(t, filepath.Join(entry, "commondir"))
			return folder
		},
		"an oversized backlink": func(t *testing.T, _, folder, entry string) string {
			// Leading slashes still name the worktree: only the size cap refuses it.
			writeTestFile(t, filepath.Join(entry, "gitdir"), strings.Repeat("/", 8192)+filepath.Join(realPath(t, folder), ".git")+"\n")
			return folder
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo, folder, data, _ := worktreeFixture(t)
			entry := filepath.Join(realPath(t, filepath.Join(repo, ".git")), "worktrees", filepath.Base(folder))
			session := plant(t, repo, folder, entry)
			if got := worktreeGrants(t, session, data); len(got) != 0 {
				t.Fatalf("grants = %q, want none", got)
			}
			if err := New().isRunWorktree(t.Context(), repo, session); err == nil {
				t.Fatal("isRunWorktree = nil, want a refusal")
			}
		})
	}
}
