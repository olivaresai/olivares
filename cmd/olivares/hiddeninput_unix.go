// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux || darwin

package main

import (
	"bufio"
	"errors"
	"os"
	"os/signal"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// discardTypedAhead drops the keys typed on the terminal f before a question was shown,
// so an Enter or a y pressed earlier is never taken as the answer to it.
func discardTypedAhead(f *os.File) error { return flushInput(int(f.Fd())) }

// waitReadable reports whether f has input to read within timeout.
func waitReadable(f *os.File, timeout time.Duration) (bool, error) {
	fds := []unix.PollFd{{Fd: int32(f.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, int(timeout/time.Millisecond))
	if errors.Is(err, unix.EINTR) {
		return false, nil
	}
	return n > 0, err
}

// hideEcho turns echo off on the terminal f and returns what turns it back on.
func hideEcho(f *os.File) (restore func(), err error) {
	fd := int(f.Fd())
	saved, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return nil, err
	}
	hidden := *saved
	hidden.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &hidden); err != nil {
		return nil, err
	}
	return func() { _ = unix.IoctlSetTermios(fd, ioctlSetTermios, saved) }, nil
}

// readHiddenLine reads one line from the terminal f with echo turned off, and
// turns echo back on afterwards, including when Ctrl-C ends the read.
func readHiddenLine(f *os.File, r *bufio.Reader) (string, error) {
	restore, err := hideEcho(f)
	if err != nil {
		return "", err
	}
	defer restore()
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	defer signal.Stop(interrupt)
	go func() {
		if _, ok := <-interrupt; ok {
			restore()
			os.Exit(130)
		}
	}()
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
