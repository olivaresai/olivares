// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"slices"
	"strings"
	"time"
)

// goBuildCacheLimit bounds the Go build cache of the home a child ran under (a provider account's home). Go drops an
// entry only after five days unused, so between its trims that cache has no byte bound.
// ponytail: one fixed bound per home; make it a setting when an operator needs a different one.
var goBuildCacheLimit int64 = 5 << 30

// goBuildCacheDir is Go's default build cache below a home (os.UserCacheDir; a session's XDG_CACHE_HOME, when set, is
// the same <home>/.cache).
const goBuildCacheDir = ".cache/go-build"

// withGoBuildCacheBound returns release followed, when env names the child's home, by the bound of that home's Go build
// cache. Both runners use it: the native child and the container (which binds the home at the same host path) write
// the same cache, and a fleet node runs its sessions through the native runner.
func withGoBuildCacheBound(release func(), env []EnvVar) func() {
	home := ""
	for _, e := range env {
		if e.Name == envUserHome {
			home = e.Value
		}
	}
	if home == "" {
		return release
	}
	return func() {
		release()
		if err := boundGoBuildCache(home); err != nil {
			slog.Warn("sessions: the Go build cache of the session's home was not brought within its bound", "home", home, "limit_bytes", goBuildCacheLimit, "err", err)
		}
	}
}

// boundGoBuildCache cuts the Go build cache below home back to goBuildCacheLimit, least recently used entries first:
// Go refreshes an entry's modification time when a build uses it. It reads the cache as Go's own trim does: the
// <xx>/<hash>-a and -d entries of its subdirectories, where a -d directory is a cached executable (counted and removed
// whole). Go's trim removes entries the same way while other builds run; a build that misses one rebuilds it. It works
// through an os.Root on home, so a link the child left there cannot lead it outside the home. What it could not read or
// remove, and a cache still over the bound, come back as the error.
func boundGoBuildCache(home string) error {
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	cache := root.FS()
	subdirs, err := fs.ReadDir(cache, goBuildCacheDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	type entry struct {
		path string
		dir  bool
		size int64
		mod  time.Time
	}
	var entries []entry
	var total int64
	var errs []error
	for _, sub := range subdirs {
		if !sub.IsDir() {
			continue
		}
		subdir := path.Join(goBuildCacheDir, sub.Name())
		names, err := fs.ReadDir(cache, subdir)
		if err != nil {
			errs = append(errs, err)
		}
		for _, d := range names {
			if !strings.HasSuffix(d.Name(), "-a") && !strings.HasSuffix(d.Name(), "-d") {
				continue
			}
			info, err := d.Info()
			if err != nil {
				errs = append(errs, err)
				continue
			}
			e := entry{path: path.Join(subdir, d.Name()), dir: info.IsDir(), mod: info.ModTime()}
			switch {
			case info.Mode().IsRegular():
				e.size = info.Size()
			case e.dir:
				e.size, err = treeSize(cache, e.path)
				errs = append(errs, err)
			default:
				continue // a link or a device is no cache entry
			}
			entries = append(entries, e)
			total += e.size
		}
	}
	if total > goBuildCacheLimit {
		slices.SortFunc(entries, func(a, b entry) int { return a.mod.Compare(b.mod) })
		for _, e := range entries {
			if total <= goBuildCacheLimit {
				break
			}
			remove := root.Remove
			if e.dir {
				remove = root.RemoveAll
			}
			if err := remove(e.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
				continue
			}
			total -= e.size
		}
	}
	if total > goBuildCacheLimit {
		errs = append(errs, fmt.Errorf("the cache still holds %d bytes", total))
	}
	return errors.Join(errs...)
}

// treeSize is the size of the regular files below dir.
func treeSize(fsys fs.FS, dir string) (int64, error) {
	var size int64
	err := fs.WalkDir(fsys, dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			size += info.Size()
		}
		return nil
	})
	return size, err
}
