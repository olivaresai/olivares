// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux && !darwin

package main

import (
	"bufio"
	"errors"
	"os"
	"time"
)

// discardTypedAhead cannot drop unread keys on this platform, so a question that
// needs it is not asked at the terminal.
func discardTypedAhead(*os.File) error {
	return errors.New("this platform cannot clear typed input")
}

// waitReadable cannot poll on this platform: the read that follows waits.
func waitReadable(*os.File, time.Duration) (bool, error) { return true, nil }

// readHiddenLine cannot turn echo off on this platform, so it refuses rather than
// show the password on screen.
func readHiddenLine(*os.File, *bufio.Reader) (string, error) {
	return "", errors.New("this platform cannot hide typed input")
}

// hideEcho cannot turn echo off on this platform; a caller refuses to read a
// secret visibly.
func hideEcho(*os.File) (func(), error) {
	return nil, errors.New("this platform cannot hide typed input")
}
