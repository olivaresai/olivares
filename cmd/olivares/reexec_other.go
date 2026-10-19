// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !unix

package main

import (
	"errors"
	"fmt"
	"os"
)

const canReexec = false

// openRegular opens a regular file of the data volume for reading; a link, pipe or device is refused.
func openRegular(path string) (*os.File, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return os.Open(path)
}

func reexecSelf([]string) error {
	return errors.New("this platform cannot re-execute a running process; restart the engine with its service manager")
}

func execApplied(string) error {
	return errors.New("this platform cannot hand over to an applied release")
}
