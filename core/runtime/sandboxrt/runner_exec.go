// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sandboxrt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// cmdRunner runs an external command (runsc / firecracker / jailer) in a working
// directory with an EXPLICIT environment (no inherited ambient secrets) and
// returns its stdout, exit code, and a non-nil error ONLY when the command could
// not be executed at all (binary missing, context canceled). A non-zero exit is
// reported via exitCode with a nil error so the caller can branch on the code.
//
// It is an interface so the backends are TESTABLE without the real isolation
// binaries: a test injects a deterministic fake (mirroring executor's cmdRunner
// pattern), exercising the full spec-build → run → destroy → verify orchestration
// while the real runsc/firecracker spawn stays an operator-provisioned,
// preflight-gated production path.
type cmdRunner interface {
	run(ctx context.Context, dir string, env []string, name string, args ...string) (stdout []byte, exitCode int, err error)
	// look reports whether a binary is resolvable on PATH (for Preflight). It
	// returns the resolved path or an error when the binary is absent.
	look(name string) (string, error)
}

// execRunner is the production cmdRunner over os/exec.
type execRunner struct {
	// timeout bounds a single invocation (0 ⇒ no extra timeout beyond ctx).
	timeout time.Duration
}

// run executes name+args in dir with env, capturing stdout. A non-zero exit
// returns the code with a nil error; an unexecutable command returns -1 + error.
func (r execRunner) run(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, int, error) {
	if r.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- name/args are internal executor inputs, not request data; per-plugin runtime confinement is layered on top
	cmd.Dir = dir
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return stdout.Bytes(), ee.ExitCode(), nil
		}
		if cerr := ctx.Err(); cerr != nil {
			return stdout.Bytes(), -1, fmt.Errorf("sandboxrt: %s did not complete: %w", name, cerr)
		}
		return stdout.Bytes(), -1, fmt.Errorf("sandboxrt: cannot execute %s (not found or not permitted)", name)
	}
	return stdout.Bytes(), 0, nil
}

// look resolves a binary on PATH.
func (execRunner) look(name string) (string, error) { return exec.LookPath(name) }
