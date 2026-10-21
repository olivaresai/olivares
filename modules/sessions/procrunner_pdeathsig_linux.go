// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"os/exec"
	"syscall"
)

// endWithEngine makes the kernel kill the child when the engine dies. Nothing
// runs after a kill -9, so without it the child outlives the engine that
// supervises it. The signal covers the child itself: the kernel does not carry
// it to the processes that child starts. A session is not launched this way: its
// guard (watchEngine) ends the whole tree. This is for the short-lived children
// that have no tree worth guarding (git).
//
// It fires when the thread that forked the child exits, not the process. A Go
// thread ends only when a goroutine locked to it exits; the launching goroutines
// here are never locked, so their threads outlive the session.
func endWithEngine(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}
