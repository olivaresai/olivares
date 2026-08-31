// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

// A2A v1.0 Task finite-state machine. The v1.0 enum (TaskState, types.go) has 8
// states with meaning plus TASK_STATE_UNSPECIFIED (the proto3 zero value) — the
// exact set verified against a2a.proto v1.0.1 (CANCELED one L; terminal/interrupted
// designations from the enum comments; v1.0 defines NO formal transition matrix, so
// this machine enforces only the documented constraints):
//
//	          ┌──────────────► INPUT_REQUIRED ─┐   (interrupt: needs more from caller)
//	          │                                 │
//	SUBMITTED ┼──► WORKING ──┬──► AUTH_REQUIRED ┤   (interrupt: needs auth)
//	          │              │                  │
//	          │              ├──► COMPLETED      │   (terminal, success)
//	          │              ├──► FAILED         ├──► (interrupt states resume to
//	          │              ├──► CANCELED       │     WORKING / a terminal state)
//	          │              └──► REJECTED       │
//	          └──────────────────────────────────┘
//
// This file models that machine EXPLICITLY so the delegation client (a) never treats
// a non-success state as success — most importantly TASK_STATE_UNSPECIFIED, the
// proto3 zero value, which means "unset/unknown" and is handled as a distinct,
// non-terminal, non-progressable phase, NEVER as completion (docs/SECURITY-HARDENING.md anti-evasion);
// and (b) can sanity-check an observed transition during reconciliation (GetTask /
// ListTasks / a push update) — an illegal transition (e.g. a terminal state moving
// on) is a signal worth surfacing, never silently followed.
//
// taskStateTerminal / taskStateInterrupt live in emit_task.go (the primitive);
// this file builds the richer machine on top of them without redefining them.

// taskPhase is the coarse lifecycle phase of a Task state — the classification the
// FSM reasons about (every caller asks "is this done / waiting / still going / a
// non-state").
type taskPhase string

const (
	// phaseUnspecified is TASK_STATE_UNSPECIFIED (proto3 zero value) or the empty
	// string: unset/unknown. NOT terminal, NOT progressable, NEVER success — it is
	// surfaced as an actionable "indeterminate" outcome the caller must reconcile.
	phaseUnspecified taskPhase = "unspecified"
	// phaseActive covers SUBMITTED / WORKING: the Task is progressing on its own and
	// can be polled (GetTask) until it interrupts or reaches a terminal state.
	phaseActive taskPhase = "active"
	// phaseInterrupt covers INPUT_REQUIRED / AUTH_REQUIRED: the Task paused needing
	// more from the caller. It is NOT success and NOT terminal — it is elevated to the
	// human-in-the-loop and never auto-resolved (docs/SECURITY-HARDENING.md: actuation stays governed).
	phaseInterrupt taskPhase = "interrupt"
	// phaseSuccess is the single success terminal state COMPLETED.
	phaseSuccess taskPhase = "success"
	// phaseFailure covers the non-success terminal states FAILED / CANCELED / REJECTED.
	phaseFailure taskPhase = "failure"
	// phaseUnknown is a state string OUTSIDE the v1.0 enum (a peer speaking a newer or
	// non-standard revision). Surfaced, never trusted as progress or success.
	phaseUnknown taskPhase = "unrecognized"
)

// phaseOf classifies a Task state into its coarse lifecycle phase. The empty string
// and TASK_STATE_UNSPECIFIED both map to phaseUnspecified (a missing status is not a
// finished one).
func phaseOf(s TaskState) taskPhase {
	switch s {
	case "", TaskStateUnspecified:
		return phaseUnspecified
	case TaskStateSubmitted, TaskStateWorking:
		return phaseActive
	case TaskStateInputReq, TaskStateAuthRequired:
		return phaseInterrupt
	case TaskStateCompleted:
		return phaseSuccess
	case TaskStateFailed, TaskStateCanceled, TaskStateRejected:
		return phaseFailure
	default:
		return phaseUnknown
	}
}

// taskSucceeded reports whether a state is the success terminal (COMPLETED) — the
// ONLY state the delegation client may treat as a delivered result. Everything else
// (active, interrupt, failure, unspecified, unrecognized) is explicitly NOT success.
func taskSucceeded(s TaskState) bool { return phaseOf(s) == phaseSuccess }

// taskProgressable reports whether a Task can still advance on its own (SUBMITTED /
// WORKING) and is therefore worth polling via GetTask. Interrupt states are NOT
// progressable (they wait on the caller); terminal/unspecified/unrecognized are not.
func taskProgressable(s TaskState) bool { return phaseOf(s) == phaseActive }

// terminalPhases is the set of phases from which no further transition is legal.
var terminalPhases = map[taskPhase]struct{}{phaseSuccess: {}, phaseFailure: {}}

// canTransition reports whether moving from→to is a legal A2A v1.0 lifecycle
// transition, used to sanity-check a reconciled state (GetTask / ListTasks / push
// update). It is deliberately permissive about FORWARD motion (the spec does not
// fully constrain the WORKING→* fan-out) but STRICT about two evasion-relevant
// invariants:
//
//   - a terminal state (COMPLETED / FAILED / CANCELED / REJECTED) is FINAL: nothing
//     transitions out of it. A peer reporting a terminal Task as later "working"
//     (re-opening a closed Task) is an illegal transition — a signal, not progress.
//   - an unrecognized current state has no known-legal successor (we cannot reason
//     about a state we do not model), so any transition from it is flagged.
//
// An identical from==to (idempotent re-observation) is always legal. A transition
// INTO the same state, or from unspecified into any real state (the first observation
// filling in an unset status), is legal.
func canTransition(from, to TaskState) bool {
	if from == to {
		return true
	}
	fp := phaseOf(from)
	if _, terminal := terminalPhases[fp]; terminal {
		return false // a terminal Task never moves again
	}
	if fp == phaseUnknown {
		return false // cannot reason about an unmodeled state; flag the move
	}
	// From unspecified/active/interrupt, any modeled forward state is acceptable;
	// a move INTO an unrecognized state is itself suspect (surfaced as a finding by
	// the caller), but not an FSM violation we can assert on a state we do not model.
	return phaseOf(to) != phaseUnknown
}

// reconcile folds an observed state (from GetTask / ListTasks / a push update) into a
// prior state, reporting the resolved state and whether the transition was legal. An
// illegal transition keeps the PRIOR state (never silently adopting a re-opened
// terminal Task) and returns legal=false so the caller can surface it.
func reconcile(prior, observed TaskState) (resolved TaskState, legal bool) {
	if canTransition(prior, observed) {
		return observed, true
	}
	return prior, false
}
