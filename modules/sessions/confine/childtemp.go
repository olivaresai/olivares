// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package confine

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// childTempRoot is the directory under which the engine makes each confined child's private TMPDIR. The composition root
// sets it once, to <data>/tool-tmp; unset (a test, a bare library use) it is the system's temporary directory.
var childTempRoot atomic.Pointer[string]

// UseChildTempRoot sets the root of confined children's temporary directories.
func UseChildTempRoot(root string) { childTempRoot.Store(&root) }

func childRoot() string {
	if p := childTempRoot.Load(); p != nil {
		return *p
	}
	return ""
}

// ChildTempDir makes one confined child's private temporary directory (0700, s-*) under the child temporary root. The
// caller grants it to the child and removes it when the child has exited.
func ChildTempDir() (string, error) {
	root := childRoot()
	if root != "" {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return "", err
		}
	}
	return os.MkdirTemp(root, "s-")
}

// RemoveChildTemp removes a confined child's temporary directory once the child has exited. A failure is logged: the
// folder then waits for the next start-up sweep.
func RemoveChildTemp(dir string) {
	if err := removeChildTemp(dir); err != nil {
		slog.Warn("sessions: a confined child's temporary directory was not removed; the next start-up sweep removes it", "dir", dir, "err", err)
	}
}

// removeChildTemp removes dir, also when the child left folders there without the owner's write permission (Go's module
// cache is read-only): it gives each folder back to the owner, never through a symbolic link, and removes the tree.
func removeChildTemp(dir string) error {
	if os.RemoveAll(dir) == nil {
		return nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	_ = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			if info, ierr := d.Info(); ierr == nil {
				_ = root.Chmod(p, info.Mode().Perm()|0o700)
			}
		}
		return nil // remove what can be removed; RemoveAll reports the rest
	})
	return os.RemoveAll(dir)
}

// ErrChildTempHeld is the sweep's answer when another engine holds the child temporary root: it serves on the same data
// folder, its sessions' temporary folders are live, and nothing was removed.
var ErrChildTempHeld = errors.New("another engine holds the child temporary root; nothing was swept")

// held keeps, for the life of this process, the lock of each child temporary root this engine holds. The kernel drops
// it when the process ends, however it ends. waiting marks a root whose lock another engine still holds and this one
// waits for.
var held = struct {
	sync.Mutex
	locks   map[string]*os.File
	waiting map[string]bool
}{locks: map[string]*os.File{}, waiting: map[string]bool{}}

// holdChildTemp takes the root's lock (<root>.lock, beside the root) without waiting, once per process. When another
// engine holds it (a --reuse-port hand-over: this engine serves beside the previous one), it answers ErrChildTempHeld
// and keeps waiting for the lock in the background on the same file, so this engine holds the root as soon as the
// previous one exits and a later engine's sweep removes nothing of its sessions. It never sweeps then: its own
// sessions' folders are live, and what the previous engine left waits for the next cold start.
func holdChildTemp(root string) error {
	held.Lock()
	defer held.Unlock()
	if held.locks[root] != nil {
		return nil
	}
	if held.waiting[root] {
		return ErrChildTempHeld
	}
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		return err
	}
	f, err := lockChildTemp(root + ".lock")
	if errors.Is(err, ErrChildTempHeld) && f != nil {
		held.waiting[root] = true
		go func() {
			werr := waitChildTemp(f)
			held.Lock()
			defer held.Unlock()
			delete(held.waiting, root)
			if werr != nil {
				_ = f.Close() // a later sweep tries again
				return
			}
			held.locks[root] = f
		}()
	}
	if err != nil {
		return err
	}
	held.locks[root] = f
	return nil
}

// SweepChildTemp removes the child temporary directories a previous engine left in the child temporary root: their
// children are gone. It removes only what ChildTempDir makes there, direct s-<decimal> folders that are no link; it
// leaves anything else in place and logs it. It first holds the root for this engine, so a second engine on the same
// data folder (started by hand, about to fail on the busy port, or a --reuse-port hand-over serving beside the previous
// engine) removes nothing and gets ErrChildTempHeld; a serving one then holds the root once the previous engine exits.
func SweepChildTemp() error {
	root := childRoot()
	if root == "" {
		return nil
	}
	if err := holdChildTemp(root); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	resolved, rerr := filepath.EvalSymlinks(root)
	if rerr != nil {
		resolved = root
	}
	slog.Info("sessions: sweeping the confined children's temporary directories a previous engine left", "root", resolved)
	var errs []error
	for _, e := range entries {
		if !e.IsDir() || !childTempName(e.Name()) { // ReadDir's type is the entry's own, so a link is no directory
			slog.Warn("sessions: the start-up sweep left an entry that is no child temporary directory", "root", resolved, "entry", e.Name())
			continue
		}
		errs = append(errs, removeChildTemp(filepath.Join(root, e.Name())))
	}
	return errors.Join(errs...)
}

// childTempName reports a name ChildTempDir gives: "s-" and os.MkdirTemp's decimal suffix.
func childTempName(name string) bool {
	digits, ok := strings.CutPrefix(name, "s-")
	_, err := strconv.ParseUint(digits, 10, 32)
	return ok && err == nil
}
