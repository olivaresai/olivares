// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package snowflakeaudit

import (
	"testing"
	"time"
)

func TestParseRowReadsOnlyContractFields(t *testing.T) {
	line := []byte(`{"QUERY_ID":"q-1","QUERY_TEXT":"SELECT secret FROM vault","QUERY_START_TIME":"2026-06-03 10:23:45.123 +0000","USER_NAME":"U1","ROLE_NAME":"R1","DIRECT_OBJECTS_ACCESSED":[{"objectName":"DB.S.T","objectDomain":"Table","objectId":42,"columns":[{"columnName":"C1","columnId":7}]}],"BASE_OBJECTS_ACCESSED":[],"OBJECTS_MODIFIED":[]}`)
	row, ok := parseRow(line)
	if !ok {
		t.Fatal("parseRow returned ok=false for valid JSON")
	}
	if row.UserName != "U1" || row.RoleName != "R1" {
		t.Errorf("identity = %q/%q, want U1/R1", row.UserName, row.RoleName)
	}
	if len(row.DirectObjectsAccessed) != 1 || row.DirectObjectsAccessed[0].ObjectName != "DB.S.T" {
		t.Fatalf("direct objects = %+v", row.DirectObjectsAccessed)
	}
	cols := nonEmptyColumns(row.DirectObjectsAccessed[0].Columns)
	if len(cols) != 1 || cols[0] != "C1" {
		t.Errorf("columns = %v, want [C1]", cols)
	}
	// The struct has no field that could hold QUERY_TEXT/QUERY_ID — proven by the
	// fact that there is no such field to assert on; this test documents intent.
}

func TestParseRowRejectsNonJSON(t *testing.T) {
	if _, ok := parseRow([]byte(`not json`)); ok {
		t.Error("parseRow should reject a non-JSON line")
	}
}

// TestNoUnknownMode documents the honest classification: every ACCESS_HISTORY
// object is placed by Snowflake into a read bucket (DIRECT/BASE) or the write
// bucket (OBJECTS_MODIFIED), so this connector classifies every emitted edge as
// read or write verbatim and never emits Mode=unknown. The mode is determined by
// the bucket, never inferred (there is no SQL to inspect).
func TestNoUnknownMode(t *testing.T) {
	var s Source
	row := accessRow{
		QueryStartTime:        "2026-06-03 10:23:45.123 +0000",
		UserName:              "U1",
		DirectObjectsAccessed: []accessObject{{ObjectName: "DB.S.T", ObjectDomain: "Function"}},
		ObjectsModified:       []accessObject{{ObjectName: "DB.S.W"}},
	}
	for _, e := range s.buildEdges(row) {
		if e.Mode == "unknown" || e.Mode == "" {
			t.Errorf("edge %+v has non-classified mode %q; connector must classify by bucket", e, e.Mode)
		}
	}
}

func TestParseTimeUTC(t *testing.T) {
	cases := []struct{ in, want string }{
		{"2026-06-03 10:23:45.123 +0000", "2026-06-03T10:23:45.123Z"},
		{"2026-06-03 12:23:45.000 +0200", "2026-06-03T10:23:45Z"}, // normalized to UTC
		{"2026-06-03T10:23:45.123456789Z", "2026-06-03T10:23:45.123456789Z"},
		{"2026-06-03 10:23:45 +0000", "2026-06-03T10:23:45Z"},
	}
	for _, c := range cases {
		got, ok := parseTime(c.in)
		if !ok {
			t.Errorf("parseTime(%q) ok=false", c.in)
			continue
		}
		want, _ := time.Parse(time.RFC3339Nano, c.want)
		if !got.Equal(want) {
			t.Errorf("parseTime(%q) = %s, want %s", c.in, got.Format(time.RFC3339Nano), c.want)
		}
		if got.Location() != time.UTC {
			t.Errorf("parseTime(%q) location = %v, want UTC", c.in, got.Location())
		}
	}
	if _, ok := parseTime("not a time"); ok {
		t.Error("parseTime should reject an unparseable value")
	}
}
