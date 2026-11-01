// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package main

import (
	"bytes"
	"os"
	"runtime"
	"strings"
)

// selinuxContext reads the calling thread's own files (/proc/thread-self). /proc/self is
// the thread group's leader: a Go program reads it from whichever OS thread runs the
// goroutine, and from any thread but the leader SELinux checks process getattr, which the
// hook domain does not hold (candidate rc18: the hook refused a loopback PEP that way).
// A mounts table that cannot be read cannot rule SELinux out, so it counts as enabled
// with an error: the caller denies closed. The goroutine stays on one OS thread for both
// reads, so thread-self is this thread from the open to the read (as confine's
// exec_domain_linux.go does).
func selinuxContext() (enabled bool, context string, err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	mounts, err := os.ReadFile("/proc/thread-self/mounts")
	if err != nil {
		return true, "", err
	}
	if !bytes.Contains(mounts, []byte(" selinuxfs ")) {
		return false, "", nil
	}
	current, err := os.ReadFile("/proc/thread-self/attr/current")
	if err != nil {
		return true, "", err
	}
	return true, strings.TrimRight(string(current), "\x00\n"), nil
}
