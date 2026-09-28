// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostops_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
)

func TestOperation_ClaimIsReadableBeforeTheEffectReturns(t *testing.T) {
	e, err := hostops.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := testCommand(strings.Repeat("a1", 16))
	// Re-entering through the public API must not deadlock behind an effect.
	_, _, err = e.Submit(c, func() error {
		got, status, err := e.Get(c.OperationID)
		if err != nil || got.State != hostops.StateRunning || status != 202 {
			t.Fatalf("claim: %#v %d %v", got, status, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c.PlanDigest = "invalid-on-a-retrieval"
	if _, status, err := e.Submit(c, func() error { t.Fatal("replayed"); return nil }); status != 409 || err == nil || err.Error() != "409 plan_changed" {
		t.Fatalf("changed plan: %d %v", status, err)
	}
}

func TestOperation_RecoveryReadsJournalsAndRetainsUnknownTarget(t *testing.T) {
	e, err := hostops.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := testCommand(strings.Repeat("a2", 16))
	c.Target = "network"
	if _, _, err := e.Submit(c, nil); err != nil {
		t.Fatal(err)
	}
	journal := &operationJournal{entry: hostops.JournalEntry{OperationID: c.OperationID, RequestID: strings.Repeat("b2", 16), PlanDigest: c.PlanDigest, Observation: hostops.Observation{P1: "unknown"}}}
	got, err := e.Reconcile(c.OperationID, journal)
	if err != nil || got.State != hostops.StateRunning || got.EffectPending || journal.reads != 1 {
		t.Fatalf("reconcile: %#v %v reads=%d", got, err, journal.reads)
	}
	next := c
	next.OperationID = strings.Repeat("c2", 16)
	_, status, err := e.Submit(next, nil)
	var locked *hostops.TargetLocked
	if status != 409 || !errors.As(err, &locked) || locked.OperationID != c.OperationID {
		t.Fatalf("pending target: %d %v", status, err)
	}
	journal.entry.Observation = hostops.Observation{P1: "performed", Postconditions: []string{"address measured"}}
	got, err = e.Reconcile(c.OperationID, journal)
	if err != nil || got.State != hostops.StateSucceeded || got.EffectPending {
		t.Fatalf("resolved: %#v %v", got, err)
	}
	got, err = e.Recover(c.OperationID, hostops.Observation{P1: "unknown"})
	if err == nil || got.State == hostops.StateRunning {
		t.Fatalf("terminal regression: %#v %v", got, err)
	}
}

func TestOperation_IDIsNotTheConsumersRequestID(t *testing.T) {
	e, err := hostops.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := testCommand(strings.Repeat("a3", 16))
	if _, _, err := e.Submit(c, nil); err != nil {
		t.Fatal(err)
	}
	j := &operationJournal{entry: hostops.JournalEntry{OperationID: c.OperationID, RequestID: c.OperationID, PlanDigest: c.PlanDigest, Observation: hostops.Observation{P1: "performed", Postconditions: []string{"measured"}}}}
	if _, err := e.Reconcile(c.OperationID, j); err == nil {
		t.Fatal("operation id was accepted as the consumer request id")
	}
	j.entry.RequestID = strings.Repeat("b3", 16)
	if _, err := e.Reconcile(c.OperationID, j); err != nil {
		t.Fatal(err)
	}
	j.entry.OperationID = strings.Repeat("c3", 16)
	if _, err := e.Reconcile(c.OperationID, j); err == nil {
		t.Fatal("unrelated receipt accepted")
	}
}

type operationJournal struct {
	entry hostops.JournalEntry
	reads int
}

func (j *operationJournal) ReadOperation(id string) (hostops.JournalEntry, error) {
	j.reads++
	return j.entry, nil
}

func TestOperation_RevertAvailabilityDoesNotChangeTheReceiptOutcome(t *testing.T) {
	cases := []struct {
		p1             string
		change         bool
		post           []string
		state, outcome string
	}{
		{"not-performed", false, nil, hostops.StateFailed, ""},
		{"failed", false, nil, hostops.StateFailed, ""},
		{"performed", true, []string{"requested state measured"}, hostops.StateSucceeded, ""},
		{"unknown", true, nil, hostops.StateRunning, hostops.OutcomeUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.p1, func(t *testing.T) {
			e, err := hostops.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			c := testCommand(strings.Repeat("f1", 16))
			if _, _, err := e.Submit(c, nil); err != nil {
				t.Fatal(err)
			}
			got, err := e.Recover(c.OperationID, hostops.Observation{P1: tc.p1, MeasuredChange: tc.change, Postconditions: tc.post, RevertDefined: true})
			if err != nil || got.State != tc.state || got.Outcome != tc.outcome {
				t.Fatalf("available but unused revert: %#v %v", got, err)
			}
		})
	}
}
