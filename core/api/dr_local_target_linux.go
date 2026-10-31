//go:build linux

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func pinDRBackupDirectory(path string, mounted bool) (drPinnedDirectory, error) {
	refuse := errors.New("the selected backup disk is not mounted and writable")
	if mounted {
		if !validDRLocalTarget(path) || path == "" {
			return drPinnedDirectory{}, refuse
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil || real != path {
			return drPinnedDirectory{}, refuse
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return drPinnedDirectory{}, err
	}
	fail := func() (drPinnedDirectory, error) { _ = f.Close(); return drPinnedDirectory{}, refuse }
	info, err := f.Stat()
	if err != nil || !info.IsDir() {
		return fail()
	}
	if mounted {
		mounts, err := os.ReadFile("/proc/self/mountinfo")
		if err != nil {
			return fail()
		}
		id := drLocalTargetMountID(path, mounts)
		fdinfo, err := os.ReadFile(fmt.Sprintf("/proc/self/fdinfo/%d", f.Fd()))
		if err != nil || id == "" {
			return fail()
		}
		matches := false
		for _, line := range strings.Split(string(fdinfo), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 2 && fields[0] == "mnt_id:" && fields[1] == id {
				matches = true
			}
		}
		if !matches {
			return fail()
		}
	}
	// A held mount survives lazy unmount. The parent's PID also works for a
	// pg_dump child, whose close-on-exec descriptor table does not contain f.
	return drPinnedDirectory{Path: fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), f.Fd()), Target: path, file: f}, nil
}
