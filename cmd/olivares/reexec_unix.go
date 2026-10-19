// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package main

import (
	"os"
	"strings"
	"syscall"
)

const canReexec = true

// reexecSelf replaces this process with the same binary and arguments, adding
// env. A binary upgraded in place is started from its path, not from the deleted
// file the process was running.
func reexecSelf(env []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe = strings.TrimSuffix(exe, " (deleted)")
	return syscall.Exec(exe, os.Args, append(os.Environ(), env...)) // #nosec G204 -- the engine's own binary and its own argv
}

// execApplied replaces this process with the applied binary, keeping the arguments and environment.
func execApplied(path string) error {
	return syscall.Exec(path, os.Args, os.Environ()) // #nosec G204 -- a binary the hand-over checked against OTA-signed evidence, started with this process's own argv
}

// openRegular opens a file of the data volume for reading without following a symlink and without
// blocking on a FIFO. The caller asks the descriptor whether it is a regular file.
func openRegular(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}
