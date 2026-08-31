// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

//go:build unix

package mcp

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup puts the MCP server subprocess in its own process group
// and makes context cancellation kill the whole group, so a hung server and any
// grandchildren it spawned are terminated (a grandchild holding the stdout pipe
// open would otherwise block the parent's read and Close forever).
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return terminate(cmd) }
}

// terminate kills the subprocess's process group (negative pid), falling back to
// killing the lone process if the group signal fails.
func terminate(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}
