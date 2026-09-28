// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hostops_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
)

func TestEveryHostTaskHasWebCLIAndTUIParity(t *testing.T) {
	fixture := hostops.StatusDescriptor()
	catalog, err := hostops.NewCatalog([]hostops.Descriptor{fixture})
	if err != nil {
		t.Fatal(err)
	}
	for _, surface := range []string{"web", "cli", "tui"} {
		got, err := catalog.Describe("host", "status", surface)
		if err != nil || !reflect.DeepEqual(got, fixture) {
			t.Fatalf("%s: %#v %v", surface, got, err)
		}
	}
	fixture.Surfaces = []string{"web", "cli"}
	if _, err := hostops.NewCatalog([]hostops.Descriptor{fixture}); err == nil {
		t.Fatal("missing TUI accepted")
	}
	if _, err := catalog.Describe("host", "shell", "cli"); err == nil {
		t.Fatal("undeclared verb accepted")
	}
	fixture = hostops.StatusDescriptor()
	fixture.InputSchema.AdditionalProperties = true
	if _, err := hostops.NewCatalog([]hostops.Descriptor{fixture}); err == nil {
		t.Fatal("open input schema accepted")
	}
	fixture = hostops.StatusDescriptor()
	for _, input := range []string{`{"path":"/tmp/x"}`, `{"operation_id":null}`, `{"operation_id":"a","operation_id":"b"}`} {
		if err := fixture.ValidateInput(json.RawMessage(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	if err := fixture.ValidateInput(json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
}

func TestOperation_WebCLIAndTUIReadTheSameStateAndPermissions(t *testing.T) {
	e, err := hostops.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := testCommand(strings.Repeat("b4", 16))
	if _, _, err := e.Submit(c, nil); err != nil {
		t.Fatal(err)
	}
	s := hostops.NewService(e, hostops.ReadOnlyAccess{})
	var first hostops.View
	for i, surface := range []string{"web", "cli", "tui"} {
		got, status, err := s.Operation(c.OperationID, surface)
		if err != nil || status != 202 || !got.Permissions.Read || got.Permissions.Apply || got.Permissions.Code != "act_not_adopted" {
			t.Fatalf("%s: %#v %d %v", surface, got, status, err)
		}
		if i == 0 {
			first = got
		} else if !reflect.DeepEqual(got, first) {
			t.Fatalf("%s read a different view", surface)
		}
	}
	if _, err := e.Recover(c.OperationID, hostops.Observation{P1: "performed", Postconditions: []string{"measured"}}); err != nil {
		t.Fatal(err)
	}
	for _, surface := range []string{"web", "cli", "tui"} {
		got, status, err := s.Operation(c.OperationID, surface)
		if err != nil || status != 200 || got.Record.State != hostops.StateSucceeded {
			t.Fatalf("stale %s: %#v %d %v", surface, got, status, err)
		}
	}
	if _, _, err := hostops.NewService(e, nil).Operation(c.OperationID, "web"); err == nil {
		t.Fatal("missing authority grants reads")
	}
}

func TestClosedJSON_RejectsDuplicateDeepTrailingAndUnknownFields(t *testing.T) {
	for _, data := range []string{`{"a":1,"a":2}`, `{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":{}}}}}}}}}`, `{} {}`, `{"unknown":1}`, `null`} {
		var target struct {
			A int `json:"a"`
		}
		if hostops.DecodeClosed([]byte(data), &target) == nil {
			t.Fatalf("accepted %s", data)
		}
	}
	var target struct {
		A int `json:"a"`
	}
	if err := hostops.DecodeClosed([]byte(`{"a":1}`), &target); err != nil {
		t.Fatal(err)
	}
}

func TestDescriptor_ActAudienceComesFromTheClosedVerbTable(t *testing.T) {
	pairs := [][2]string{
		{"apply-update", "appliance-helper:olivares-portal-apt"},
		{"roll-back", "appliance-helper:olivares-portal-snapshot"},
		{"network", "appliance-helper:olivares-portal-firewall"},
		{"network", "appliance-netclient"},
		{"vpn", "appliance-netclient"},
		{"certificate", "appliance-helper:olivares-portal-cert"},
		{"support-bundle", "appliance-helper:olivares-portal-support-bundle"},
		{"power", "appliance-helper:olivares-portal-power"},
		{"package", "appliance-helper:olivares-portal-apt"},
		{"service", "appliance-helper:olivares-portal-units"},
		{"storage", "appliance-helper:olivares-portal-storage"},
		{"app", "appliance-helper:olivares-portal-apps"},
		{"system", "appliance-helper:olivares-portal-system"},
	}
	for _, pair := range pairs {
		t.Run(pair[0]+"/"+pair[1], func(t *testing.T) {
			d := hostops.StatusDescriptor()
			d.ActVerb = pair[0]
			d.Audience = pair[1]
			d.Confirmation = "confirm"
			if _, err := hostops.NewCatalog([]hostops.Descriptor{d}); err != nil {
				t.Fatalf("closed pair: %v", err)
			}
			d.Audience = "appliance-helper:unlisted"
			if _, err := hostops.NewCatalog([]hostops.Descriptor{d}); err == nil {
				t.Fatal("unlisted audience accepted")
			}
		})
	}
	for _, pair := range [][2]string{{"certificate", "appliance-netclient"}, {"vpn", "appliance-helper:olivares-portal-firewall"}, {"shell", "appliance-helper:olivares-portal-apt"}, {"", "appliance-netclient"}, {"network", ""}} {
		d := hostops.StatusDescriptor()
		d.ActVerb = pair[0]
		d.Audience = pair[1]
		if _, err := hostops.NewCatalog([]hostops.Descriptor{d}); err == nil {
			t.Fatalf("wrong pair accepted: %v", pair)
		}
	}
	if _, err := hostops.NewCatalog([]hostops.Descriptor{hostops.StatusDescriptor()}); err != nil {
		t.Fatalf("read-only descriptor: %v", err)
	}
}

type accessDecision func(hostops.Record) hostops.Permissions

func (f accessDecision) Permissions(r hostops.Record) hostops.Permissions { return f(r) }

type recordReader func(string) (hostops.Record, int, error)

func (f recordReader) Get(id string) (hostops.Record, int, error) { return f(id) }

func TestOperation_AccessRefusesBeforeAnyModelRead(t *testing.T) {
	for _, id := range []string{strings.Repeat("18", 16), strings.Repeat("28", 16)} {
		reads := 0
		model := recordReader(func(string) (hostops.Record, int, error) { reads++; return hostops.Record{}, 404, nil })
		access := accessDecision(func(r hostops.Record) hostops.Permissions {
			if r.OperationID != id {
				t.Fatalf("access has no requested id: %#v", r)
			}
			return hostops.Permissions{Code: "mode_refused"}
		})
		_, status, err := hostops.NewService(model, access).Operation(id, "web")
		if status != 403 || err == nil || reads != 0 {
			t.Fatalf("status=%d err=%v reads=%d", status, err, reads)
		}
	}
}

func TestOperation_RecordSpecificAccessStillAppliesAfterReadAdmission(t *testing.T) {
	id := strings.Repeat("38", 16)
	model := recordReader(func(string) (hostops.Record, int, error) {
		return hostops.Record{OperationID: id, Actor: "another-user"}, 200, nil
	})
	access := accessDecision(func(r hostops.Record) hostops.Permissions {
		return hostops.Permissions{Read: r.Actor == ""}
	})
	_, status, err := hostops.NewService(model, access).Operation(id, "web")
	if status != 403 || err == nil {
		t.Fatalf("record-specific refusal lost: %d %v", status, err)
	}
}
