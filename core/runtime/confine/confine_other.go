// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package confine

import (
	"errors"
	"os"
	"runtime"
)

func probe() State {
	return State{Mode: ModeNone, Reason: "process confinement is implemented with Linux Landlock; on " + runtime.GOOS +
		" a child runs as the engine user and can read what that user can"}
}

func restrictAndExec(Policy, string, []string) error {
	return errors.New(probe().Reason)
}

func openDirectory(string) (*os.File, error) { return nil, errors.New(probe().Reason) }

func directoryHandleUnchanged(*os.File) error { return errors.New(probe().Reason) }
