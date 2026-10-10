//go:build linux

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// Every entry is first pinned with O_PATH|NOFOLLOW and type-checked. Only the
// proven inode is reopened through /proc/self/fd; a pathname swap cannot cause a
// link, FIFO or device to be opened for reading. No directory root escapes this
// implementation.
func readWorkspaceSnapshot(ctx context.Context, ws *resolvedWorkspace, dir string) (snapshotTree, error) {
	root, err := openSnapshotDirectory(ctx, filepath.Join(ws.rootReal, filepath.FromSlash(dir)))
	if err != nil {
		return snapshotTree{}, err
	}
	defer func() { _ = root.Close() }()
	limit := min(ws.maxReadBytes, snapshotMaxFile)
	tree := snapshotTree{}
	nodes := map[string]string{}
	entries, total := 0, int64(0)
	if err := walkSnapshot(ctx, root, "", limit, &entries, &total, &tree, nodes); err != nil {
		return snapshotTree{}, err
	}
	names := make([]string, 0, len(nodes))
	for name := range nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	hash := sha256.New()
	for _, name := range names {
		_, _ = io.WriteString(hash, name+"\x00"+nodes[name]+"\x00")
	}
	tree.stamp = fmt.Sprintf("%x", hash.Sum(nil))
	sort.Slice(tree.files, func(i, j int) bool { return tree.files[i].path < tree.files[j].path })
	return tree, nil
}

func openSnapshotDirectory(ctx context.Context, absolute string) (*os.File, error) {
	if !filepath.IsAbs(absolute) || filepath.Clean(absolute) != absolute {
		return nil, forbiddenErr("workspace snapshot path is not confined")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, snapshotUnavailable()
	}
	for _, part := range strings.Split(strings.TrimPrefix(absolute, "/"), "/") {
		if err := ctx.Err(); err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
		if !safeSnapshotPath(part) {
			_ = unix.Close(fd)
			return nil, forbiddenErr("workspace snapshot cannot read a protected source tree")
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			return nil, forbiddenErr("workspace snapshot directory is unavailable or contains a link")
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), "workspace snapshot directory"), nil
}

func walkSnapshot(ctx context.Context, dir *os.File, prefix string, limit int64, count *int, total *int64, tree *snapshotTree, nodes map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var before unix.Stat_t
	if unix.Fstat(int(dir.Fd()), &before) != nil || before.Mode&unix.S_IFMT != unix.S_IFDIR {
		return snapshotChanged()
	}
	nodes[prefix] = snapshotStat(before)
	remaining := snapshotMaxEntries - *count
	entries, err := dir.ReadDir(remaining + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return snapshotUnavailable()
	}
	if len(entries) > remaining {
		return badRequest("workspace snapshot exceeds the entry limit")
	}
	*count += len(entries)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		relative := path.Join(prefix, entry.Name())
		if !safeSnapshotPath(relative) {
			return forbiddenErr("workspace snapshot contains a protected or unsafe path")
		}
		handle, err := unix.Openat(int(dir.Fd()), entry.Name(), unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return snapshotChanged()
		}
		var pinned unix.Stat_t
		if unix.Fstat(handle, &pinned) != nil ||
			(pinned.Mode&unix.S_IFMT != unix.S_IFREG && pinned.Mode&unix.S_IFMT != unix.S_IFDIR) ||
			(pinned.Mode&unix.S_IFMT == unix.S_IFREG && pinned.Nlink != 1) {
			_ = unix.Close(handle)
			return forbiddenErr("workspace snapshot refuses links and special files")
		}
		// Opening the verified inode rather than its directory entry prevents a
		// concurrent swap from introducing a device, credential link or FIFO.
		fd, err := unix.Open(fmt.Sprintf("/proc/self/fd/%d", handle), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		_ = unix.Close(handle)
		if err != nil {
			return snapshotUnavailable()
		}
		file := os.NewFile(uintptr(fd), "workspace snapshot entry")
		var opened unix.Stat_t
		if unix.Fstat(fd, &opened) != nil || snapshotStat(opened) != snapshotStat(pinned) {
			_ = file.Close()
			return snapshotChanged()
		}
		nodes[relative] = snapshotStat(pinned)
		if pinned.Mode&unix.S_IFMT == unix.S_IFDIR {
			err = walkSnapshot(ctx, file, relative, limit, count, total, tree, nodes)
		} else {
			err = readSnapshotFile(ctx, file, relative, pinned, limit, total, tree)
		}
		_ = file.Close()
		if err != nil {
			return err
		}
		var current unix.Stat_t
		if unix.Fstatat(int(dir.Fd()), entry.Name(), &current, unix.AT_SYMLINK_NOFOLLOW) != nil ||
			snapshotStat(current) != snapshotStat(pinned) {
			return snapshotChanged()
		}
	}
	var after unix.Stat_t
	if unix.Fstat(int(dir.Fd()), &after) != nil || snapshotStat(before) != snapshotStat(after) {
		return snapshotChanged()
	}
	return nil
}

func readSnapshotFile(ctx context.Context, file *os.File, relative string, before unix.Stat_t, limit int64, total *int64, tree *snapshotTree) error {
	if before.Size < 0 || before.Size > limit || before.Size > snapshotMaxTotal-*total {
		return badRequest("workspace snapshot exceeds its file or total byte limit")
	}
	data := make([]byte, 0, before.Size)
	buf := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := file.Read(buf)
		if int64(len(data)+n) > limit || int64(len(data)+n) > snapshotMaxTotal-*total {
			return badRequest("workspace snapshot exceeds its file or total byte limit")
		}
		data = append(data, buf[:n]...)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return snapshotUnavailable()
		}
	}
	var after unix.Stat_t
	if unix.Fstat(int(file.Fd()), &after) != nil || snapshotStat(before) != snapshotStat(after) || int64(len(data)) != before.Size {
		return snapshotChanged()
	}
	mode := os.FileMode(0o644)
	if before.Mode&0o111 != 0 {
		mode = 0o755
	}
	*total += int64(len(data))
	tree.files = append(tree.files, snapshotFile{path: relative, mode: mode, data: data})
	return nil
}

func snapshotStat(st unix.Stat_t) string {
	return fmt.Sprintf("%d/%d/%d/%d/%d/%d/%d/%d/%d/%d/%d", st.Dev, st.Ino, st.Mode, st.Nlink,
		st.Uid, st.Gid, st.Size, st.Mtim.Sec, st.Mtim.Nsec, st.Ctim.Sec, st.Ctim.Nsec)
}

func snapshotChanged() error {
	return conflictErr("workspace snapshot source changed during the read")
}

func snapshotUnavailable() error {
	return &runErr{http.StatusFailedDependency, "workspace snapshot source is unavailable"}
}
