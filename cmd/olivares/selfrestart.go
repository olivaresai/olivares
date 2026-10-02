// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"
	"log/slog"
	"sync"
)

// errSelfRestart marks a server that drained and closed because it asked to
// start again; main then executes the same binary with the same arguments.
var errSelfRestart = errors.New("engine restart requested")

// selfRestartError is errSelfRestart with its reason and the environment the
// next start adds.
type selfRestartError struct {
	reason string
	env    []string
}

func (e *selfRestartError) Error() string     { return "engine restart requested: " + e.reason }
func (e *selfRestartError) Is(err error) bool { return err == errSelfRestart }

// selfRestart is how a serving engine asks to restart itself: it drains exactly
// as on a signal, closes the engine, and starts the same binary again in the same
// process (same PID, so a service manager keeps tracking it).
type selfRestart struct {
	mu     sync.Mutex
	reason string
	cancel context.CancelFunc
	log    *slog.Logger
}

// Request asks the engine to restart once the requests in flight finish. It
// returns at once. An engine that is not serving, or a platform that cannot
// re-execute a running process, refuses.
func (r *selfRestart) Request(reason string) error {
	if r == nil || r.cancel == nil {
		return errors.New("this engine is not running as a server and cannot restart itself")
	}
	if !canReexec {
		return errors.New("restart the engine with its service manager: this platform cannot re-execute a running process")
	}
	r.mu.Lock()
	first := r.reason == ""
	if first {
		r.reason = reason
	}
	r.mu.Unlock()
	if first && r.log != nil {
		r.log.Info("engine: restarting itself after the requests in flight finish", "reason", reason)
	}
	r.cancel()
	return nil
}

// requested is the reason of the pending restart, or "".
func (r *selfRestart) requested() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reason
}

// restartRequester is r.Request, or nil when there is no serving engine to restart.
func restartRequester(r *selfRestart) func(string) error {
	if r == nil {
		return nil
	}
	return r.Request
}
