// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostops_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
)

func TestOperation_RequestIDBindsOneOperationAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	e, err := hostops.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := testCommand(strings.Repeat("a7", 16))
	b := testCommand(strings.Repeat("b7", 16))
	b.Target = "network"
	for _, cmd := range []hostops.Command{a, b} {
		if _, _, err := e.Submit(cmd, nil); err != nil {
			t.Fatal(err)
		}
	}
	request := strings.Repeat("c7", 16)
	j := &operationJournal{entry: hostops.JournalEntry{OperationID: a.OperationID, PlanDigest: a.PlanDigest, RequestID: request, Observation: hostops.Observation{P1: "unknown"}}}
	if _, err := e.Reconcile(a.OperationID, j); err != nil {
		t.Fatal(err)
	}
	e, err = hostops.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	j.entry.Observation = hostops.Observation{P1: "performed", Postconditions: []string{"measured"}}
	got, err := e.Reconcile(a.OperationID, j)
	if err != nil || got.State != "succeeded" {
		t.Fatalf("same operation: %#v %v", got, err)
	}
	data, err := json.Marshal(got)
	if err != nil || strings.Contains(string(data), request) || strings.Contains(string(data), "request_id") {
		t.Fatalf("private binding exposed: %s %v", data, err)
	}
	j.entry.OperationID = b.OperationID
	if _, err := e.Reconcile(b.OperationID, j); err == nil {
		t.Fatal("one consumer receipt finished two operations")
	}
	got, _, err = e.Get(b.OperationID)
	if err != nil || got.State != "running" {
		t.Fatalf("refused receipt changed the record: %#v %v", got, err)
	}
	j.entry.OperationID = a.OperationID
	j.entry.RequestID = strings.Repeat("d7", 16)
	if _, err := e.Reconcile(a.OperationID, j); err == nil {
		t.Fatal("operation changed its bound consumer request")
	}
	j.entry.OperationID = b.OperationID
	if _, err := e.Reconcile(b.OperationID, j); err != nil {
		t.Fatalf("independent receipt: %v", err)
	}
}

type readJournal func(string) (hostops.JournalEntry, error)

func (f readJournal) ReadOperation(id string) (hostops.JournalEntry, error) { return f(id) }

func TestOperation_RequestIDBindingIsAtomicAcrossEngines(t *testing.T) {
	dir := t.TempDir()
	a, err := hostops.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := hostops.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	commands := []hostops.Command{testCommand(strings.Repeat("e7", 16)), testCommand(strings.Repeat("f7", 16))}
	commands[1].Target = "network"
	for _, cmd := range commands {
		if _, _, err := a.Submit(cmd, nil); err != nil {
			t.Fatal(err)
		}
	}
	ready := make(chan struct{}, 2)
	proceed := make(chan struct{})
	results := make(chan error, 2)
	for i, engine := range []*hostops.Engine{a, b} {
		cmd := commands[i]
		go func() {
			_, err := engine.Reconcile(cmd.OperationID, readJournal(func(id string) (hostops.JournalEntry, error) {
				ready <- struct{}{}
				<-proceed
				return hostops.JournalEntry{OperationID: id, PlanDigest: cmd.PlanDigest, RequestID: strings.Repeat("17", 16), Observation: hostops.Observation{P1: "performed", Postconditions: []string{"measured"}}}, nil
			}))
			results <- err
		}()
	}
	<-ready
	<-ready
	close(proceed)
	accepted := 0
	for range 2 {
		if <-results == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("same request admitted %d operations", accepted)
	}
}

func TestOperation_TerminalProjectionRetainsItsFirstJournalBinding(t *testing.T) {
	dir := t.TempDir()
	e, err := hostops.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := testCommand(strings.Repeat("a8", 16))
	if _, _, err := e.Submit(a, nil); err != nil {
		t.Fatal(err)
	}
	obs := hostops.Observation{P1: "performed", Postconditions: []string{"measured"}}
	if _, err := e.Recover(a.OperationID, obs); err != nil {
		t.Fatal(err)
	}
	j := &operationJournal{entry: hostops.JournalEntry{OperationID: a.OperationID, PlanDigest: a.PlanDigest, RequestID: strings.Repeat("c8", 16), Observation: obs}}
	if _, err := e.Reconcile(a.OperationID, j); err != nil {
		t.Fatal(err)
	}
	e, err = hostops.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	b := testCommand(strings.Repeat("b8", 16))
	if _, _, err := e.Submit(b, nil); err != nil {
		t.Fatal(err)
	}
	j.entry.OperationID = b.OperationID
	if _, err := e.Reconcile(b.OperationID, j); err == nil {
		t.Fatal("terminal operation's binding was not durable")
	}
}
