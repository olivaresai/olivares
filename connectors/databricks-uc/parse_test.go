// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package databricksuc

import (
	"testing"
	"time"
)

func TestParseTime(t *testing.T) {
	want := time.Date(2026, 6, 3, 10, 23, 45, 123000000, time.UTC)
	cases := []string{
		"2026-06-03T10:23:45.123Z",      // RFC3339 with Z
		"2026-06-03T10:23:45.123+00:00", // RFC3339 with explicit UTC offset
		"2026-06-03 10:23:45.123Z",      // space-separated variant
	}
	for _, in := range cases {
		got, ok := parseTime(in)
		if !ok {
			t.Errorf("parseTime(%q) ok=false", in)
			continue
		}
		if !got.Equal(want) {
			t.Errorf("parseTime(%q) = %v, want %v", in, got, want)
		}
	}
	if _, ok := parseTime("not a time"); ok {
		t.Error("parseTime(garbage) should be ok=false")
	}
}

func TestSourceRefTargetRef(t *testing.T) {
	// table_lineage row: both sides are tables.
	tbl := lineageRow{
		SourceTableFullName: "c.s.src",
		TargetTableFullName: "c.s.tgt",
	}
	if k, r, ok := tbl.sourceRef(); !ok || k != kindTable || r != "c.s.src" {
		t.Errorf("table sourceRef = (%q,%q,%v)", k, r, ok)
	}
	if k, r, ok := tbl.targetRef(); !ok || k != kindTable || r != "c.s.tgt" {
		t.Errorf("table targetRef = (%q,%q,%v)", k, r, ok)
	}

	// column_lineage row: a non-empty column makes it a column resource.
	col := lineageRow{
		SourceTableFullName: "c.s.src", SourceColumnName: "a",
		TargetTableFullName: "c.s.tgt", TargetColumnName: "b",
	}
	if k, r, ok := col.sourceRef(); !ok || k != kindColumn || r != "c.s.src.a" {
		t.Errorf("column sourceRef = (%q,%q,%v)", k, r, ok)
	}
	if k, r, ok := col.targetRef(); !ok || k != kindColumn || r != "c.s.tgt.b" {
		t.Errorf("column targetRef = (%q,%q,%v)", k, r, ok)
	}

	// Empty sides yield ok=false.
	empty := lineageRow{}
	if _, _, ok := empty.sourceRef(); ok {
		t.Error("empty sourceRef should be ok=false")
	}
	if _, _, ok := empty.targetRef(); ok {
		t.Error("empty targetRef should be ok=false")
	}
}

func TestParseRow(t *testing.T) {
	r, ok := parseRow([]byte(`{"source_table_full_name":"c.s.t","created_by":"u@x","event_time":"2026-06-03T10:00:00Z"}`))
	if !ok {
		t.Fatal("parseRow ok=false on valid JSON")
	}
	if r.SourceTableFullName != "c.s.t" || r.CreatedBy != "u@x" {
		t.Errorf("parseRow = %+v", r)
	}
	if _, ok := parseRow([]byte(`{not json`)); ok {
		t.Error("parseRow should be ok=false on invalid JSON")
	}
}
