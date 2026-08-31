// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"os/exec"
	"syscall"
)

// configureProcGroup puts the launched `claude` in its own process group and
// makes context cancellation hard-kill the whole group, so a hung process and
// any grandchildren it spawned are terminated (a grandchild holding the stdout
// pipe open would otherwise block the read and wedge teardown forever). On
// non-Unix this is a no-op and the default per-process kill applies.
func configureProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return procGroupKill(cmd) }
}

// procGroupTerminate sends SIGTERM to the process GROUP (negative pid), letting
// `claude` flush and persist its transcript before exit, with a lone-process
// fallback.
func procGroupTerminate(cmd *exec.Cmd) error {
	return signalGroup(cmd, syscall.SIGTERM)
}

// procGroupKill sends SIGKILL to the process group (the hard escalation).
func procGroupKill(cmd *exec.Cmd) error {
	return signalGroup(cmd, syscall.SIGKILL)
}

func signalGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err == nil {
		return nil
	}
	return cmd.Process.Signal(sig)
}
