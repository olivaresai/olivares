// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !unix

package sessions

import "os/exec"

// configureProcGroup is a no-op on non-Unix platforms; the default per-process
// kill applies (no process-group semantics).
func configureProcGroup(cmd *exec.Cmd) {
	cmd.Cancel = func() error { return procGroupKill(cmd) }
}

// procGroupTerminate falls back to a process kill (no SIGTERM/group concept).
func procGroupTerminate(cmd *exec.Cmd) error { return procGroupKill(cmd) }

// procGroupKill kills the lone process.
func procGroupKill(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
