// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// startTerminalShell starts cmd (already wrapped by the folder's confinement) on a new
// pty and returns the master. The shell leads its own session with the pty as its
// controlling terminal, and the session guard stands in front of it as it does for every
// session: when the shell ends, or the engine dies, whatever the person left running in
// the background ends with it. Cancelling cmd's context ends the whole tree.
func startTerminalShell(cmd *exec.Cmd) (master *os.File, err error) {
	master, slave, err := openPTYPair()
	if err != nil {
		return nil, fmt.Errorf("sessions: terminal pty: %w", err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	cmd.SysProcAttr.Ctty = 0 // stdin is the slave, so the child's fd 0 is the tty
	cmd.Cancel = func() error { return procGroupKill(cmd) }
	program := specProgram(cmd) // the guard replaces cmd.Path
	engine, err := watchEngine(cmd, true)
	if err != nil {
		_ = master.Close()
		_ = slave.Close()
		return nil, fmt.Errorf("sessions: start %q: %w", program, err)
	}
	if engine != nil {
		cmd.Cancel = func() error { engine.endLifetime(); return nil }
	}
	if err := cmd.Start(); err != nil {
		engine.close()
		_ = master.Close()
		_ = slave.Close()
		return nil, fmt.Errorf("sessions: start %q: %w", program, err)
	}
	engine.started()
	cmd.Env = nil
	_ = slave.Close()
	return master, nil
}

// setTerminalSize tells the pty, and so the shell and what it runs, the window's size.
func setTerminalSize(master *os.File, rows, cols uint16) error {
	return unix.IoctlSetWinsize(int(master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: rows, Col: cols})
}

// terminalInputHidden reports an entry the kernel is not echoing while it edits the line
// itself: what a password prompt (sudo, ssh, read -s) sets up. A shell's own prompt turns
// echo off too, but it also takes over line editing (no ICANON), so it is not matched.
func terminalInputHidden(master *os.File) bool {
	tio, err := unix.IoctlGetTermios(int(master.Fd()), unix.TCGETS)
	if err != nil {
		return true // unreadable: withhold the line rather than record a secret
	}
	return tio.Lflag&unix.ECHO == 0 && tio.Lflag&unix.ICANON != 0
}
