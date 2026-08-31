// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package a2a

import "testing"

// TestPhaseOf classifies every v1.0 enum member (and the zero value) into the right
// coarse phase, the load-bearing guarantee that UNSPECIFIED is never success.
func TestPhaseOf(t *testing.T) {
	cases := []struct {
		state TaskState
		want  taskPhase
	}{
		{"", phaseUnspecified},
		{TaskStateUnspecified, phaseUnspecified},
		{TaskStateSubmitted, phaseActive},
		{TaskStateWorking, phaseActive},
		{TaskStateInputReq, phaseInterrupt},
		{TaskStateAuthRequired, phaseInterrupt},
		{TaskStateCompleted, phaseSuccess},
		{TaskStateFailed, phaseFailure},
		{TaskStateCanceled, phaseFailure},
		{TaskStateRejected, phaseFailure},
		{TaskState("TASK_STATE_WAT"), phaseUnknown},
	}
	for _, c := range cases {
		if got := phaseOf(c.state); got != c.want {
			t.Errorf("phaseOf(%q) = %q, want %q", c.state, got, c.want)
		}
	}
}

// TestUnspecifiedNeverSuccess is the anti-evasion invariant: the proto3 zero value is
// NOT terminal, NOT progressable, and NEVER success.
func TestUnspecifiedNeverSuccess(t *testing.T) {
	for _, s := range []TaskState{"", TaskStateUnspecified} {
		if taskSucceeded(s) {
			t.Errorf("taskSucceeded(%q) = true; UNSPECIFIED must never be success", s)
		}
		if taskStateTerminal(s) {
			t.Errorf("taskStateTerminal(%q) = true; UNSPECIFIED is not terminal", s)
		}
		if taskProgressable(s) {
			t.Errorf("taskProgressable(%q) = true; UNSPECIFIED is not progressable", s)
		}
	}
}

// TestTaskSucceededOnlyCompleted asserts COMPLETED is the ONLY success state.
func TestTaskSucceededOnlyCompleted(t *testing.T) {
	if !taskSucceeded(TaskStateCompleted) {
		t.Fatal("COMPLETED must be success")
	}
	for _, s := range []TaskState{
		TaskStateSubmitted, TaskStateWorking, TaskStateInputReq, TaskStateAuthRequired,
		TaskStateFailed, TaskStateCanceled, TaskStateRejected, TaskStateUnspecified,
	} {
		if taskSucceeded(s) {
			t.Errorf("taskSucceeded(%q) must be false", s)
		}
	}
}

// TestCanTransition guards the two evasion-relevant invariants: a terminal state is
// final, and an unmodeled current state has no legal successor.
func TestCanTransition(t *testing.T) {
	// Legal forward motion.
	if !canTransition(TaskStateSubmitted, TaskStateWorking) {
		t.Error("submitted->working must be legal")
	}
	if !canTransition(TaskStateWorking, TaskStateCompleted) {
		t.Error("working->completed must be legal")
	}
	if !canTransition(TaskStateUnspecified, TaskStateWorking) {
		t.Error("unspecified->working (first observation) must be legal")
	}
	if !canTransition(TaskStateWorking, TaskStateWorking) {
		t.Error("idempotent re-observation must be legal")
	}
	// A terminal Task never moves again (re-opening a closed Task is illegal).
	if canTransition(TaskStateCompleted, TaskStateWorking) {
		t.Error("completed->working must be ILLEGAL (terminal is final)")
	}
	if canTransition(TaskStateRejected, TaskStateSubmitted) {
		t.Error("rejected->submitted must be ILLEGAL")
	}
	// An unmodeled current state has no known-legal successor.
	if canTransition(TaskState("TASK_STATE_WAT"), TaskStateWorking) {
		t.Error("unrecognized->working must be flagged ILLEGAL")
	}
}

// TestReconcile keeps the prior state on an illegal transition (never adopting a
// re-opened terminal Task) and adopts the observed state on a legal one.
func TestReconcile(t *testing.T) {
	resolved, legal := reconcile(TaskStateWorking, TaskStateCompleted)
	if !legal || resolved != TaskStateCompleted {
		t.Errorf("legal transition: got (%q,%v), want (COMPLETED,true)", resolved, legal)
	}
	resolved, legal = reconcile(TaskStateCompleted, TaskStateWorking)
	if legal || resolved != TaskStateCompleted {
		t.Errorf("illegal transition must keep prior: got (%q,%v), want (COMPLETED,false)", resolved, legal)
	}
}
