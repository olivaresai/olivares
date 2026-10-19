// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package skills

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// Own the full Git helper group until its pipes have drained; cancellation must
// never remove staging beneath a live helper.
func boundedGitCommand(ctx context.Context, executable string, args ...string) (*exec.Cmd, error) {
	// #nosec G204 -- the only production caller (GitImporter.Import in git.go) passes the
	// prlimit path it resolved with exec.LookPath and an argv it builds itself: fixed
	// resource limits, "--", the git path it resolved, fixed -c settings, then its own
	// subcommand; the only values from outside (ref, loopback URL, subdir, commit) are
	// validated and follow "--" or are 40-hex. No shell is involved.
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return cmd, nil
}
func stopGitGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
