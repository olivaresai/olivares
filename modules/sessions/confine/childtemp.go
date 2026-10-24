// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package confine

import (
	"errors"
	"os"
	"path/filepath"
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

// SweepChildTemp removes every entry a previous engine left in the child temporary root: their children are gone.
func SweepChildTemp() error {
	root := childRoot()
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		errs = append(errs, os.RemoveAll(filepath.Join(root, e.Name())))
	}
	return errors.Join(errs...)
}
