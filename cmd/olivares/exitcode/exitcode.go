// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package exitcode defines the olivares CLI exit-code contract. Scripts and CI
// pipelines branch on these values, so they are API: never renumber an existing
// code, only append.
package exitcode

import "errors"

// The exit-code contract (documented in the root command's help).
const (
	// OK — the command succeeded.
	OK = 0
	// Err — generic failure with no more specific classification.
	Err = 1
	// Usage — the invocation itself is wrong (unknown flag, bad arguments).
	Usage = 2
	// Auth — the control plane rejected the caller (401/403).
	Auth = 3
	// NotFound — the addressed entity does not exist (404).
	NotFound = 4
	// Conflict — the request contradicts current state (409).
	Conflict = 5
	// Server — the control plane failed or was unreachable (5xx, transport).
	Server = 6
	// Degraded — the command succeeded but reports a degraded condition
	// (`status` when the engine is not fully ok; `security check` on an
	// affected version).
	Degraded = 7
	// Indeterminate — the command could not reach a verdict because an input it
	// needs is UNKNOWN, as distinct from a verdict of "fine" (0) or "bad" (7)
	// and from a failure to run (1). `security check` returns it when the build
	// declares no usable version, so no advisory range can be evaluated against
	// it: a clean answer there would be an artifact, not a measurement.
	// A fleet sweep must treat this as "not yet answered", never as "clean".
	Indeterminate = 8
)

// coded wraps an error with the exit code the process must return.
type coded struct {
	code int
	err  error
}

func (c coded) Error() string {
	if c.err == nil {
		return ""
	}
	return c.err.Error()
}

func (c coded) Unwrap() error { return c.err }

// New wraps err so the CLI exits with code. A nil err still carries the code
// with an empty message (used for quiet degraded exits whose report is already
// on stdout).
func New(code int, err error) error { return coded{code: code, err: err} }

// From extracts the exit code carried by err, or Err when none is attached.
func From(err error) int {
	var c coded
	if errors.As(err, &c) {
		return c.code
	}
	return Err
}

// Has reports whether err already carries a code of its own.
//
// From cannot answer this: it returns Err (1) both for "classified as the
// generic failure" and for "not classified at all", and a caller supplying a
// FALLBACK has to tell those apart.
func Has(err error) bool {
	var c coded
	return errors.As(err, &c)
}

// Or classifies err as fallback ONLY if nothing already classified it, and
// returns it untouched otherwise.
//
// It is the shape a call site wants when it knows the code an UNCLASSIFIED
// failure deserves but must not overrule a helper that knew better. New is the
// shape for the opposite case — the call site itself establishing the code.
//
// The distinction is not academic (C08-03). Four CLI clients wrote
// New(Server, …) around cliTransport, whose refusals about the caller's own
// arguments are classified Usage: a mistyped --pin-sha256 came back as 6, "the
// control plane failed or was unreachable", from `mcp`, `findings`, `compliance`
// and `auth status`, while the identical mistake exits 2 from `agent session
// ls`. A script that retries a 6 retried a typo forever, and the operator was
// told to go look at a healthy server.
func Or(fallback int, err error) error {
	if err == nil || Has(err) {
		return err
	}
	return New(fallback, err)
}

// Silent reports whether err carries no message of its own — the command
// already printed its report and the wrapper exists only for the code.
func Silent(err error) bool {
	var c coded
	return errors.As(err, &c) && c.err == nil
}
