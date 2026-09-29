// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localsession

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"github.com/olivaresai/olivares/appliance/layer/portal/localframe"
)

type recordReader struct {
	reads  int
	record hostops.Record
}

func (r *recordReader) Get(string) (hostops.Record, int, error) { r.reads++; return r.record, 202, nil }

func TestLocalSession_ReadsAndPlansButNeverGrantsAnAct(t *testing.T) {
	catalog, err := hostops.NewCatalog([]hostops.Descriptor{hostops.StatusDescriptor()})
	if err != nil {
		t.Fatal(err)
	}
	reader := &recordReader{record: hostops.Record{OperationID: strings.Repeat("12", 16), State: hostops.StateRunning, Outcome: hostops.OutcomeUnknown}}
	server := NewServer(reader, catalog, nil)
	result := server.handle(localframe.Record{Type: "request", Op: "operation.get", Body: json.RawMessage(`{"operation_id":"` + reader.record.OperationID + `","surface":"cli"}`)})
	var view hostops.View
	if result.Type != "response" || result.Status != 202 || json.Unmarshal(result.Body, &view) != nil || view.Record.OperationID != reader.record.OperationID || view.Permissions.Apply || reader.reads != 1 {
		t.Fatalf("read: %#v %#v", result, view)
	}
	for _, body := range []string{`{"module":"host","verb":"status","inputs":{},"surface":"cli"}`, `{"module":"host","verb":"status","inputs":{},"surface":"tui"}`} {
		result = server.handle(localframe.Record{Type: "request", Op: "task.plan", Body: json.RawMessage(body)})
		var plan hostops.Plan
		if result.Type != "response" || json.Unmarshal(result.Body, &plan) != nil || plan.Permissions.Apply || plan.Descriptor.EquivalentCommand == "" {
			t.Fatalf("plan: %#v", result)
		}
	}
	for _, op := range []string{"task.apply", "handoff.register"} {
		result = server.handle(localframe.Record{Type: "request", Op: op, Body: json.RawMessage(`{}`)})
		if result.Type != "error" || result.Status != 503 {
			t.Fatalf("unbuilt act accepted: %#v", result)
		}
	}
	if reader.reads != 1 {
		t.Fatal("an act reached the operation reader")
	}
	result = server.handle(localframe.Record{Type: "request", Op: "operation.get", Body: json.RawMessage(`{"operation_id":"` + reader.record.OperationID + `","surface":"cli","actor":"root"}`)})
	if result.Type != "error" || result.Code != "input_refused" || reader.reads != 1 {
		t.Fatal("caller-selected authority accepted")
	}
}

func TestLocalSession_HM21GateClosedRegisterErrorMintsNothing(t *testing.T) {
	server := NewServer(nil, nil, nil)
	result := server.handle(localframe.Record{Type: "request", Op: "handoff.register", Body: json.RawMessage(`{}`)})
	if result.Type != "error" || result.Op != "handoff.register" || result.Status != 503 || result.Code != "pidfd_unproven" || len(result.Body) != 0 {
		t.Fatalf("register: %#v", result)
	}
}
