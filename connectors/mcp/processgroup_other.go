// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

//go:build !unix

package mcp

import "os/exec"

// configureProcessGroup is a no-op on non-Unix platforms; the default
// per-process cancellation kill from exec.CommandContext applies.
func configureProcessGroup(*exec.Cmd) {}

// terminate kills the lone subprocess (no process-group support).
func terminate(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
