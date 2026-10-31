// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package gitlayout answers which git directories a worktree folder owns.
//
// A session writes its own folder, so its .git and the worktree entry it
// points to are untrusted. Session confinement (which directories a session
// may open) and the session-commit fetch (which objects the engine reads)
// both ask here, so every hardening of these reads lands once. It serves the
// product's own composition and is not part of the stable author contract
// (VERSIONING.md).
package gitlayout

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Layout is where a worktree folder's git metadata lives, links resolved.
type Layout struct {
	// GitDir holds the folder's own index and HEAD.
	GitDir string
	// CommonDir holds the repository's objects and refs: GitDir itself for a
	// repository, the main repository's git directory for a linked worktree.
	CommonDir string
}

// Linked reports whether the folder is a linked worktree, whose metadata
// lives outside it.
func (l Layout) Linked() bool { return l.GitDir != l.CommonDir }

// Read returns the layout of the worktree root folder, or false when its .git
// is not git's own. A .git directory is the repository itself and names no
// other common directory than itself. A .git file must name an entry in its common
// directory's worktrees/ whose gitdir file links back to this .git: git
// worktree's own two-way link, so neither a rewritten or copied pointer nor an
// entry forged inside the folder reaches another repository's metadata.
func Read(folder string) (Layout, bool) {
	root, err := filepath.EvalSymlinks(folder)
	if err != nil {
		return Layout{}, false
	}
	dotgit := filepath.Join(root, ".git")
	st, err := os.Lstat(dotgit)
	switch {
	case err != nil:
		return Layout{}, false
	case st.IsDir():
		// A commondir that names the directory itself is what session
		// confinement writes (a name the session must not create); another one
		// redirects git to another repository's metadata.
		if _, err := os.Lstat(filepath.Join(dotgit, "commondir")); !errors.Is(err, fs.ErrNotExist) && !NamesItself(dotgit) {
			return Layout{}, false
		}
		return Layout{GitDir: dotgit, CommonDir: dotgit}, true
	case !st.Mode().IsRegular():
		return Layout{}, false
	}
	gitDir := readPointer(dotgit, "gitdir: ")
	if gitDir == "" {
		return Layout{}, false
	}
	commonDir := readPointer(filepath.Join(gitDir, "commondir"), "")
	backlink := readPointer(filepath.Join(gitDir, "gitdir"), "")
	if commonDir == "" || backlink != dotgit || filepath.Dir(gitDir) != filepath.Join(commonDir, "worktrees") {
		return Layout{}, false
	}
	// Git keeps a linked worktree's entry in the repository: never inside the
	// worktree, and the worktree is never inside the entry. Where the two
	// overlap, the session wrote the entry itself, and with it names an
	// ancestor of the folder as the common directory.
	if within(gitDir, root) || within(root, gitDir) {
		return Layout{}, false
	}
	return Layout{GitDir: gitDir, CommonDir: commonDir}, true
}

// within reports whether path is dir or lies beneath it. Both are resolved.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// NamesItself reports whether gitDir/commondir is a plain file that says "." and
// nothing else: git then takes gitDir as its own common directory, which is what
// it does without the file. Any other content is not resolved here: git follows
// symbolic links before it applies "..", a path join does not.
func NamesItself(gitDir string) bool {
	file := filepath.Join(gitDir, "commondir")
	st, err := os.Lstat(file)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 4 {
		return false
	}
	raw, err := os.ReadFile(file)
	return err == nil && strings.TrimRight(string(raw), "\r\n") == "."
}

// readPointer returns the resolved path a git pointer file names after
// prefix, or "". Git's pointer files are short regular text files: a FIFO
// never blocks the read, and nothing longer than 8 KiB is read.
func readPointer(file, prefix string) string {
	f, err := os.OpenFile(file, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return ""
	}
	const limit = 8192
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || len(raw) > limit {
		return ""
	}
	path, ok := strings.CutPrefix(strings.TrimRight(string(raw), "\r\n"), prefix)
	if !ok || path == "" || strings.ContainsAny(path, "\r\n\x00") {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(file), path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	return real
}
