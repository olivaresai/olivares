// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package toolinstall

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// reapWindow bounds the wait for the child to be reaped after SIGKILL. A child
// that survives SIGKILL is stuck in the kernel; the probe reports it instead of
// hanging the install.
const reapWindow = 5 * time.Second

// runProbe executes spec in its own process group. The group, not just the
// child, is signalled: a tool that spawns helpers and ignores SIGTERM is still
// collected, and a grandchild holding the output pipe cannot wedge the wait.
func runProbe(ctx context.Context, spec probeSpec) (ProbeReport, error) {
	out := &cappedBuffer{}
	// The context passed to exec is never cancelled by us: escalation is done by
	// hand below so the GROUP is signalled. Only the caller's ctx propagates.
	cmd := newProbeCmd(context.WithoutCancel(ctx), spec, out)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// After the child exits, do not wait forever on pipes a grandchild kept open.
	cmd.WaitDelay = spec.Grace
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return ProbeReport{}, refuse(KindProbeFailed, "start %s: %v", spec.Exe, err)
	}
	pgid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	signalGroup := func(sig syscall.Signal) { _ = syscall.Kill(-pgid, sig) }
	report := func(err error) ProbeReport {
		return ProbeReport{Argv: append([]string{spec.Exe}, spec.Args...), Output: out.String(), DurationMS: time.Since(start).Milliseconds(), EnvNames: envNames(spec.Env)}
	}
	var waitErr error
	timedOut := false
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		timedOut = true
	case <-time.After(spec.Budget):
		timedOut = true
	}
	if timedOut {
		signalGroup(syscall.SIGTERM)
		select {
		case waitErr = <-done:
		case <-time.After(spec.Grace):
			signalGroup(syscall.SIGKILL)
			select {
			case waitErr = <-done:
			case <-time.After(reapWindow):
				return report(nil), refuse(KindProbeFailed, "%s did not exit within %s after SIGTERM and SIGKILL; its process group %d could not be reaped", spec.Exe, spec.Budget+spec.Grace+reapWindow, pgid)
			}
		}
	}
	// Whatever exited, sweep the group so helpers the tool left behind do not
	// outlive the probe. ESRCH means the group is already empty.
	signalGroup(syscall.SIGKILL)
	rep := report(waitErr)
	if timedOut {
		if ctx.Err() != nil {
			return rep, refuse(KindProbeFailed, "probe of %s cancelled: %v", spec.Exe, ctx.Err())
		}
		return rep, refuse(KindProbeFailed, "%s did not exit within its %s budget and was terminated (output: %q)", spec.Exe, spec.Budget, firstLine(out.String()))
	}
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return rep, refuse(KindProbeFailed, "%s exited with %v (output: %q)", spec.Exe, waitErr, firstLine(out.String()))
		}
		return rep, refuse(KindProbeFailed, "%s: %v (output: %q)", spec.Exe, waitErr, firstLine(out.String()))
	}
	return rep, nil
}
