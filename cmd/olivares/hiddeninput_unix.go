// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux || darwin

package main

import (
	"bufio"
	"os"
	"os/signal"
	"strings"

	"golang.org/x/sys/unix"
)

// readHiddenLine reads one line from the terminal f with echo turned off, and
// turns echo back on afterwards, including when Ctrl-C ends the read.
func readHiddenLine(f *os.File, r *bufio.Reader) (string, error) {
	fd := int(f.Fd())
	saved, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return "", err
	}
	hidden := *saved
	hidden.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, ioctlSetTermios, &hidden); err != nil {
		return "", err
	}
	restore := func() { _ = unix.IoctlSetTermios(fd, ioctlSetTermios, saved) }
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
