// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package sessions

import (
	"errors"
	"os"
	"os/exec"
)

var errTerminalNeedsLinux = errors.New("sessions: the browser terminal requires Linux")

func startTerminalShell(*exec.Cmd) (*os.File, error) { return nil, errTerminalNeedsLinux }
func setTerminalSize(*os.File, uint16, uint16) error { return errTerminalNeedsLinux }
func terminalInputHidden(*os.File) bool              { return true }
