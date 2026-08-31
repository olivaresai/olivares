// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// cmdRunner runs an external command in a working directory with an EXPLICIT
// environment (no inherited ambient secrets) and returns its stdout, exit code, and
// a non-nil error ONLY when the command could not be executed at all (binary
// missing, context canceled). A non-zero exit (including OpenTofu's -detailed-exitcode
// value 2 for "diff present") is reported via exitCode with a nil error, so the
// caller can branch on the code. It is an interface so tests inject a deterministic
// fake without a real tofu/git binary.
type cmdRunner interface {
	run(ctx context.Context, dir string, env []string, name string, args ...string) (stdout []byte, exitCode int, err error)
}

// execRunner is the production cmdRunner over os/exec.
type execRunner struct {
	// timeout bounds a single invocation (0 => no extra timeout beyond ctx).
	timeout time.Duration
}

// run executes name+args in dir with env, capturing stdout. stderr is folded into a
// scrubbed error only when the command could not run; a non-zero exit returns the
// code with a nil error.
func (r *execRunner) run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, int, error) {
	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- name/args are internal executor inputs, not request data
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// The command ran and exited non-zero (e.g. tofu -detailed-exitcode == 2).
			return stdout.Bytes(), ee.ExitCode(), nil
		}
		// The command could not be executed at all.
		if cerr := ctx.Err(); cerr != nil {
			return stdout.Bytes(), -1, fmt.Errorf("executor: %s did not complete: %w", name, scrubTransportErr(cerr))
		}
		return stdout.Bytes(), -1, fmt.Errorf("executor: cannot execute %s (not found or not permitted)", name)
	}
	return stdout.Bytes(), 0, nil
}
