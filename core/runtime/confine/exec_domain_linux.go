// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// StartInDomain starts cmd so that its exec enters domain when the engine runs under the Olivares Server policy
// (execContext); anywhere else it is cmd.Start. The exec context belongs to the calling thread and the child is
// forked from it, so the write, the start and the reset run on one locked thread, in a goroutine of their own. A
// reset that fails stops and reaps the command and ends that goroutine still locked: Go then ends the thread, so no
// later exec inherits the domain.
func StartInDomain(cmd *exec.Cmd, domain string) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		reset, err := startInDomain(cmd, domain)
		if reset {
			runtime.UnlockOSThread()
		}
		done <- err
	}()
	return <-done
}

// startInDomain runs on a locked thread. reset reports whether the thread's exec context is clean again.
func startInDomain(cmd *exec.Cmd, domain string) (reset bool, err error) {
	current, err := os.ReadFile("/proc/thread-self/attr/current")
	if err != nil {
		return true, cmd.Start() // no LSM attribute: SELinux is not running
	}
	next, ok := execContext(string(current), domain)
	if !ok {
		return true, cmd.Start()
	}
	const attr = "/proc/thread-self/attr/exec"
	if err := os.WriteFile(attr, []byte(next), 0); err != nil {
		return true, fmt.Errorf("enter the SELinux domain %s: %w", domain, err)
	}
	startErr := cmd.Start()
	if err := os.WriteFile(attr, nil, 0); err != nil {
		if startErr == nil { // the caller gets an error and no command: stop and reap the one that started
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		return false, fmt.Errorf("reset the SELinux exec context after %s: %w", domain, err)
	}
	return true, startErr
}
