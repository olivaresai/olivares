// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package skills

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// ImportFolder snapshots only the selected folder through no-follow directory
// descriptors. CLI clients use this locally before uploading an archive; engines
// must resolve registered workspace authority before calling it. No API accepts
// an arbitrary engine-host path.
func ImportFolder(ctx context.Context, selected string) (*ValidatedPack, error) {
	absolute, err := filepath.Abs(selected)
	if err != nil {
		return nil, refuse("unsafe_pack", "folder")
	}
	if h, err := os.UserHomeDir(); err == nil && filepath.Clean(h) == absolute {
		return nil, refuse("unsafe_pack", "home directory")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, refuse("unsafe_pack", "folder")
	}
	for _, part := range strings.Split(strings.TrimPrefix(absolute, "/"), "/") {
		if part == "" {
			continue
		}
		if !safePath(part) {
			_ = unix.Close(fd)
			return nil, refuse("unsafe_pack", "selected folder is inside a protected source tree")
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, refuse("unsafe_pack", "folder")
		}
		fd = next
	}
	root := os.NewFile(uintptr(fd), "selected folder")
	defer func() { _ = root.Close() }()
	prefix := ""
	var st unix.Stat_t
	if unix.Fstatat(fd, "SKILL.md", &st, unix.AT_SYMLINK_NOFOLLOW) == nil {
		prefix = filepath.Base(absolute)
		if !safePath(prefix) {
			return nil, refuse("unsafe_pack", "folder name")
		}
	}
	t := newTree()
	observed := map[string]unix.Stat_t{}
	if err := readFolder(ctx, root, prefix, t, observed); err != nil {
		return nil, err
	}
	// Verify every earlier inode after the whole traversal. A file may have
	// changed after its own read without changing its parent's directory times.
	for name, before := range observed {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rel := name
		if prefix != "" {
			if name == prefix {
				rel = ""
			} else {
				rel = strings.TrimPrefix(name, prefix+"/")
			}
		}
		after, err := folderEntryStat(int(root.Fd()), rel)
		if err != nil || !sameStat(before, after) {
			return nil, refuse("source_changed", name)
		}
	}
	p, err := t.validate("")
	if err != nil {
		return nil, err
	}
	p.SourceDigest = p.ManifestDigest
	return p, nil
}
func readFolder(ctx context.Context, dir *os.File, prefix string, t *tree, observed map[string]unix.Stat_t) error {
	var before unix.Stat_t
	if unix.Fstat(int(dir.Fd()), &before) != nil {
		return refuse("source_changed", "folder")
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := dir.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return refuse("source_changed", "folder")
		}
		for _, entry := range entries {
			name := entry.Name()
			p := path.Join(prefix, name)
			// Open first with NOFOLLOW and NONBLOCK: even a concurrently swapped FIFO
			// cannot hang or cause a symlink/credential read before type validation.
			handle, err := unix.Openat(int(dir.Fd()), name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return refuse("unsafe_pack", p)
			}
			var pinned unix.Stat_t
			if unix.Fstat(handle, &pinned) != nil || (pinned.Mode&unix.S_IFMT != unix.S_IFREG && pinned.Mode&unix.S_IFMT != unix.S_IFDIR) {
				_ = unix.Close(handle)
				return refuse("unsafe_pack", p)
			}
			// Reopen the proven regular/directory inode, never its source name.
			// A swapped device or link cannot be opened during this read.
			file, openErr := os.Open(fmt.Sprintf("/proc/self/fd/%d", handle))
			_ = unix.Close(handle)
			if openErr != nil {
				return refuse("source_changed", p)
			}

			// At the pack root, descend only into actual skill directories.
			// Selecting a parent of skill/config/auth trees is not a host-home import.
			if prefix == "" {
				if pinned.Mode&unix.S_IFMT == unix.S_IFDIR {
					var skill unix.Stat_t
					if unix.Fstatat(int(file.Fd()), "SKILL.md", &skill, unix.AT_SYMLINK_NOFOLLOW) != nil || skill.Mode&unix.S_IFMT != unix.S_IFREG {
						_ = file.Close()
						return refuse("unsafe_pack", p)
					}
				} else if !packMaterial(name) {
					_ = file.Close()
					return refuse("unsafe_pack", p)
				}
			}
			readErr := readFolderEntry(ctx, file, p, t, observed)
			closeErr := file.Close()
			if readErr != nil {
				return readErr
			}
			if closeErr != nil {
				return refuse("source_changed", p)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	var after unix.Stat_t
	if unix.Fstat(int(dir.Fd()), &after) != nil || !sameStat(before, after) {
		return refuse("source_changed", "folder")
	}
	observed[prefix] = before
	return nil
}
func readFolderEntry(ctx context.Context, file *os.File, p string, t *tree, observed map[string]unix.Stat_t) error {
	var before unix.Stat_t
	if unix.Fstat(int(file.Fd()), &before) != nil {
		return refuse("source_changed", p)
	}
	dir := before.Mode&unix.S_IFMT == unix.S_IFDIR
	if !dir && (before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1) || before.Mode&07000 != 0 {
		return refuse("unsafe_pack", p)
	}
	if err := t.entry(p, dir); err != nil {
		return err
	}
	if dir {
		return readFolder(ctx, file, p, t, observed)
	}
	if before.Size > int64(t.fileLimit(p)) {
		return refuse("import_limit", p)
	}
	b, err := boundedRead(ctx, file, t.fileLimit(p))
	if err != nil {
		return err
	}
	var after unix.Stat_t
	if unix.Fstat(int(file.Fd()), &after) != nil || !sameStat(before, after) {
		return refuse("source_changed", p)
	}
	observed[p] = before
	return t.file(p, b, os.FileMode(before.Mode&0777))
}

// Resolve again from the pinned selected directory, without following links in
// any component. Keep only one descriptor per level rather than every file.
func folderEntryStat(root int, relative string) (unix.Stat_t, error) {
	var out unix.Stat_t
	if relative == "" {
		err := unix.Fstat(root, &out)
		return out, err
	}
	fd, err := unix.Dup(root)
	if err != nil {
		return out, err
	}
	for _, part := range strings.Split(relative, "/") {
		next, openErr := unix.Openat(fd, part, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return out, openErr
		}
		fd = next
	}
	defer unix.Close(fd)
	err = unix.Fstat(fd, &out)
	return out, err
}
func sameStat(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Nlink == b.Nlink && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}
