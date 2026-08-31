// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mssqlaudit

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestClassifyAction(t *testing.T) {
	cases := []struct {
		actionID, actionName string
		wantMode             model.AccessMode
		wantTool             string
		wantOK               bool
	}{
		// action_id codes (the primary path).
		{"SL", "", model.ModeRead, "SELECT", true},
		{"IN", "", model.ModeWrite, "INSERT", true},
		{"UP", "", model.ModeWrite, "UPDATE", true},
		{"DL", "", model.ModeWrite, "DELETE", true},
		{"EX", "", model.ModeUnknown, "EXECUTE", true}, // never faked
		{"sl", "", model.ModeRead, "SELECT", true},     // case-insensitive
		// action_name fallback (when the export omits action_id).
		{"", "SELECT", model.ModeRead, "SELECT", true},
		{"", "INSERT", model.ModeWrite, "INSERT", true},
		{"", "UPDATE", model.ModeWrite, "UPDATE", true},
		{"", "DELETE", model.ModeWrite, "DELETE", true},
		{"", "EXECUTE", model.ModeUnknown, "EXECUTE", true},
		// non-DML/EXECUTE actions are not emitted.
		{"RC", "RECEIVE", "", "", false},    // RECEIVE
		{"RF", "REFERENCES", "", "", false}, // REFERENCES
		{"", "", "", "", false},             // empty
		{"ZZ", "WHATEVER", "", "", false},   // unknown
	}
	for _, c := range cases {
		mode, tool, ok := classifyAction(c.actionID, c.actionName)
		if mode != c.wantMode || tool != c.wantTool || ok != c.wantOK {
			t.Errorf("classifyAction(%q,%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.actionID, c.actionName, mode, tool, ok, c.wantMode, c.wantTool, c.wantOK)
		}
	}
}

func TestResourceKindFor(t *testing.T) {
	cases := []struct{ classType, want string }{
		{"U", "mssql.table"},
		{"u", "mssql.table"},
		{"V", "mssql.object"},  // view
		{"P", "mssql.object"},  // procedure
		{"FN", "mssql.object"}, // scalar function
		{"", "mssql.object"},   // absent
	}
	for _, c := range cases {
		if got := resourceKindFor(c.classType); got != c.want {
			t.Errorf("resourceKindFor(%q) = %q, want %q", c.classType, got, c.want)
		}
	}
}

func TestResourceRef(t *testing.T) {
	cases := []struct {
		db, schema, object, want string
	}{
		{"salesdb", "dbo", "customers", "salesdb.dbo.customers"},
		{"salesdb", "", "customers", "salesdb.customers"},
		{"", "dbo", "customers", "dbo.customers"},
		{"salesdb", "dbo", "", ""}, // no object anchor -> empty
		{"", "", "", ""},
	}
	for _, c := range cases {
		if got := resourceRef(c.db, c.schema, c.object); got != c.want {
			t.Errorf("resourceRef(%q,%q,%q) = %q, want %q", c.db, c.schema, c.object, got, c.want)
		}
	}
}

func TestParseTime(t *testing.T) {
	want := time.Date(2026, 6, 3, 10, 23, 45, 123000000, time.UTC)
	cases := []string{
		"2026-06-03T10:23:45.1230000",   // datetime2 JSON, no zone -> UTC
		"2026-06-03 10:23:45.123",       // space separator
		"2026-06-03T10:23:45.123Z",      // RFC3339 with Z
		"2026-06-03T10:23:45.123+00:00", // RFC3339 with explicit UTC offset
	}
	for _, s := range cases {
		got, ok := parseTime(s)
		if !ok {
			t.Errorf("parseTime(%q) failed", s)
			continue
		}
		if !got.Equal(want) {
			t.Errorf("parseTime(%q) = %v, want %v", s, got, want)
		}
		if _, off := got.Zone(); off != 0 {
			t.Errorf("parseTime(%q) zone offset = %d, want 0 (UTC)", s, off)
		}
	}
	if _, ok := parseTime("not a timestamp"); ok {
		t.Error("parseTime should fail on garbage")
	}
}

func TestRecordFromLine(t *testing.T) {
	line := []byte(`{"event_time":"2026-06-03T10:23:45.1230000","action_id":"SL","server_principal_name":"x","database_name":"d","schema_name":"s","object_name":"o","class_type":"U"}`)
	r, ok := recordFromLine(line)
	if !ok {
		t.Fatal("recordFromLine failed on valid JSON")
	}
	if r.ActionID != "SL" || r.ObjectName != "o" || r.ClassType != "U" {
		t.Errorf("recordFromLine parsed wrong: %+v", r)
	}
	if _, ok := recordFromLine([]byte("not json")); ok {
		t.Error("recordFromLine should fail on non-JSON")
	}
}
