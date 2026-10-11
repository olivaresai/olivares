// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gitlayout

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is a repository at root/repo and its linked worktree at root/wt, in
// git worktree's layout: wt/.git names repo/.git/worktrees/wt, whose gitdir
// links back and whose commondir names repo/.git.
type fixture struct {
	root, repo, common, wt, entry string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{root: root, repo: filepath.Join(root, "repo"), wt: filepath.Join(root, "wt")}
	f.common = filepath.Join(f.repo, ".git")
	f.entry = filepath.Join(f.common, "worktrees", "wt")
	write(t, filepath.Join(f.common, "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(f.entry, "HEAD"), "ref: refs/heads/wt\n")
	write(t, filepath.Join(f.entry, "commondir"), "../..\n")
	write(t, filepath.Join(f.entry, "gitdir"), filepath.Join(f.wt, ".git")+"\n")
	write(t, filepath.Join(f.wt, ".git"), "gitdir: "+f.entry+"\n")
	return f
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// otherRepository is a second repository the folder must not reach.
func (f fixture) otherRepository(t *testing.T) string {
	t.Helper()
	other := filepath.Join(f.root, "other", ".git")
	write(t, filepath.Join(other, "HEAD"), "ref: refs/heads/main\n")
	return other
}

// sized pads a pointer to path with leading slashes, which still name path,
// to size bytes with its newline.
func sized(path string, size int) string {
	return strings.Repeat("/", size-len(path)-1) + path + "\n"
}

// entryNamed moves the worktree's entry to worktrees/<name>, linked both ways
// as git would, and returns the worktree.
func entryNamed(t *testing.T, f fixture, name string) string {
	t.Helper()
	entry := filepath.Join(filepath.Dir(f.entry), name)
	if err := os.Rename(f.entry, entry); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(f.wt, ".git"), "gitdir: "+entry+"\n")
	return f.wt
}

func TestReadAdmitsGitsOwnLayout(t *testing.T) {
	for name, tc := range map[string]struct {
		plant  func(t *testing.T, f fixture) string
		linked bool
	}{
		"a linked worktree": {plant: func(t *testing.T, f fixture) string { return f.wt }, linked: true},
		"a linked worktree through a linked folder": {plant: func(t *testing.T, f fixture) string {
			alias := filepath.Join(f.root, "alias")
			if err := os.Symlink(f.wt, alias); err != nil {
				t.Fatal(err)
			}
			return alias
		}, linked: true},
		"relative pointers": {plant: func(t *testing.T, f fixture) string {
			write(t, filepath.Join(f.wt, ".git"), "gitdir: ../repo/.git/worktrees/wt\r\n")
			write(t, filepath.Join(f.entry, "gitdir"), "../../../../wt/.git\n")
			return f.wt
		}, linked: true},
		"pointers through links": {plant: func(t *testing.T, f fixture) string {
			if err := os.Symlink(filepath.Dir(f.entry), filepath.Join(f.root, "entries")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(f.wt, filepath.Join(f.root, "alias")); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(f.wt, ".git"), "gitdir: "+filepath.Join(f.root, "entries", "wt")+"\n")
			write(t, filepath.Join(f.entry, "gitdir"), filepath.Join(f.root, "alias", ".git")+"\n")
			return f.wt
		}, linked: true},
		"a backlink at the 8 KiB cap": {plant: func(t *testing.T, f fixture) string {
			write(t, filepath.Join(f.entry, "gitdir"), sized(filepath.Join(f.wt, ".git"), 8192))
			return f.wt
		}, linked: true},
		"a repository": {plant: func(t *testing.T, f fixture) string { return f.repo }},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			got, ok := Read(tc.plant(t, f))
			want := Layout{GitDir: f.common, CommonDir: f.common}
			if tc.linked {
				want.GitDir = f.entry
			}
			if !ok || got != want || got.Linked() != tc.linked {
				t.Fatalf("Read = %+v %v (linked %v), want %+v (linked %v)", got, ok, got.Linked(), want, tc.linked)
			}
		})
	}
}

func TestReadRefusesWhatIsNotGitsOwn(t *testing.T) {
	for name, plant := range map[string]func(t *testing.T, f fixture) string{
		"no .git": func(t *testing.T, f fixture) string {
			if err := os.Remove(filepath.Join(f.wt, ".git")); err != nil {
				t.Fatal(err)
			}
			return f.wt
		},
		"a missing folder": func(t *testing.T, f fixture) string { return filepath.Join(f.root, "missing") },
		"a subdirectory": func(t *testing.T, f fixture) string {
			sub := filepath.Join(f.repo, "sub")
			if err := os.Mkdir(sub, 0o700); err != nil {
				t.Fatal(err)
			}
			return sub
		},
		"a copied pointer": func(t *testing.T, f fixture) string {
			foreign := filepath.Join(f.root, "foreign")
			write(t, filepath.Join(foreign, ".git"), "gitdir: "+f.entry+"\n")
			return foreign
		},
		"a linked .git file": func(t *testing.T, f fixture) string {
			foreign := filepath.Join(f.root, "foreign")
			if err := os.Mkdir(foreign, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(f.wt, ".git"), filepath.Join(foreign, ".git")); err != nil {
				t.Fatal(err)
			}
			return foreign
		},
		"a linked .git directory": func(t *testing.T, f fixture) string {
			foreign := filepath.Join(f.root, "foreign")
			if err := os.Mkdir(foreign, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(f.common, filepath.Join(foreign, ".git")); err != nil {
				t.Fatal(err)
			}
			return foreign
		},
		"a .git directory naming another common directory": func(t *testing.T, f fixture) string {
			write(t, filepath.Join(f.common, "commondir"), f.otherRepository(t)+"\n")
			return f.repo
		},
		"a pointer into another repository": func(t *testing.T, f fixture) string {
			write(t, filepath.Join(f.wt, ".git"), "gitdir: "+f.otherRepository(t)+"\n")
			return f.wt
		},
		"another worktree's entry": func(t *testing.T, f fixture) string {
			other := filepath.Join(f.common, "worktrees", "other")
			write(t, filepath.Join(other, "commondir"), "../..\n")
			write(t, filepath.Join(other, "gitdir"), filepath.Join(f.root, "elsewhere", ".git")+"\n")
			write(t, filepath.Join(f.root, "elsewhere", ".git"), "gitdir: "+other+"\n")
			write(t, filepath.Join(f.wt, ".git"), "gitdir: "+other+"\n")
			return f.wt
		},
		"an entry forged inside the folder": func(t *testing.T, f fixture) string {
			fake := filepath.Join(f.wt, "fake")
			write(t, filepath.Join(fake, "HEAD"), "ref: refs/heads/main\n")
			write(t, filepath.Join(fake, "gitdir"), filepath.Join(f.wt, ".git")+"\n")
			write(t, filepath.Join(fake, "commondir"), f.otherRepository(t)+"\n")
			write(t, filepath.Join(f.wt, ".git"), "gitdir: "+fake+"\n")
			return f.wt
		},
		"an entry without commondir": func(t *testing.T, f fixture) string {
			if err := os.Remove(filepath.Join(f.entry, "commondir")); err != nil {
				t.Fatal(err)
			}
			return f.wt
		},
		"an entry without backlink": func(t *testing.T, f fixture) string {
			if err := os.Remove(filepath.Join(f.entry, "gitdir")); err != nil {
				t.Fatal(err)
			}
			return f.wt
		},
		"a pointer without its prefix": func(t *testing.T, f fixture) string {
			write(t, filepath.Join(f.wt, ".git"), f.entry+"\n")
			return f.wt
		},
		// The entry is git's own and links back; only the line break in its
		// name, which would end a line of an alternates file, refuses it.
		"an entry path with a newline": func(t *testing.T, f fixture) string { return entryNamed(t, f, "wt\nx") },
		"an entry path with a carriage return": func(t *testing.T, f fixture) string {
			return entryNamed(t, f, "wt\rx")
		},
		"a backlink one byte over the 8 KiB cap": func(t *testing.T, f fixture) string {
			// Read whole, this still names the worktree: only the cap refuses it.
			write(t, filepath.Join(f.entry, "gitdir"), sized(filepath.Join(f.wt, ".git"), 8193))
			return f.wt
		},
		"a backlink that is a directory": func(t *testing.T, f fixture) string {
			backlink := filepath.Join(f.entry, "gitdir")
			if err := os.Remove(backlink); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(backlink, 0o700); err != nil {
				t.Fatal(err)
			}
			return f.wt
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if got, ok := Read(plant(t, f)); ok {
				t.Fatalf("Read = %+v, want a refusal", got)
			}
		})
	}
}
