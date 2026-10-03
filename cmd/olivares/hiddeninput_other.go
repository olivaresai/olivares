// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux && !darwin

package main

import (
	"bufio"
	"errors"
	"os"
)

// readHiddenLine cannot turn echo off on this platform, so it refuses rather than
// show the password on screen.
func readHiddenLine(*os.File, *bufio.Reader) (string, error) {
	return "", errors.New("this platform cannot hide typed input")
}
