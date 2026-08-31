// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package oracleaudit

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

// TestClassifyAction asserts the verbatim ACTION_NAME -> Mode mapping, including
// the explicit-unknown cases (EXECUTE, LOCK, and any unrecognized action). The
// read/write nature is never guessed for an unclassified action (ARCHITECTURE.md).
func TestClassifyAction(t *testing.T) {
	cases := []struct {
		action string
		want   model.AccessMode
	}{
		{"SELECT", model.ModeRead},
		{"select", model.ModeRead}, // case-insensitive
		{"INSERT", model.ModeWrite},
		{"UPDATE", model.ModeWrite},
		{"DELETE", model.ModeWrite},
		{"MERGE", model.ModeUnknown}, // not a UNIFIED_AUDIT_TRAIL action (audited as its INSERT/UPDATE rows) -> honest unknown, never guessed
		{"TRUNCATE TABLE", model.ModeWrite},
		{"CREATE TABLE", model.ModeWrite},
		{"ALTER TABLE", model.ModeWrite},
		{"ALTER INDEX", model.ModeWrite},
		{"DROP TABLE", model.ModeWrite},
		{"EXECUTE", model.ModeUnknown},
		{"LOCK", model.ModeUnknown},
		{"LOGON", model.ModeUnknown},
		{"GRANT OBJECT PRIVILEGE", model.ModeUnknown}, // not in the mapped DDL verbs -> honest unknown
		{"COMMENT", model.ModeUnknown},
		{"", model.ModeUnknown},
	}
	for _, c := range cases {
		if got := classifyAction(c.action); got != c.want {
			t.Errorf("classifyAction(%q) = %q, want %q", c.action, got, c.want)
		}
	}
}

// TestResourceRef checks the OBJECT_SCHEMA.OBJECT_NAME qualification and the
// skip-when-no-object behavior.
func TestResourceRef(t *testing.T) {
	if ref, ok := (row{ObjectSchema: "SALES", ObjectName: "CUSTOMERS"}).resourceRef(); !ok || ref != "SALES.CUSTOMERS" {
		t.Errorf("qualified ref = %q (%v), want SALES.CUSTOMERS true", ref, ok)
	}
	if ref, ok := (row{ObjectName: "V_REVENUE"}).resourceRef(); !ok || ref != "V_REVENUE" {
		t.Errorf("schema-less ref = %q (%v), want V_REVENUE true", ref, ok)
	}
	if _, ok := (row{ObjectSchema: "SALES"}).resourceRef(); ok {
		t.Error("a row with no object name must be skipped (ok=false)")
	}
}

// TestEventTimestampPrefersUTC verifies EVENT_TIMESTAMP_UTC wins over
// EVENT_TIMESTAMP, and that EVENT_TIMESTAMP is the fallback only when the UTC
// column is absent.
func TestEventTimestampPrefersUTC(t *testing.T) {
	r := row{EventTSUTC: "2026-06-03 10:23:45.000000", EventTS: "2026-06-03 12:23:45.000000"}
	if got := r.eventTimestamp(); got != "2026-06-03 10:23:45.000000" {
		t.Errorf("eventTimestamp() = %q, want the UTC value", got)
	}
	r2 := row{EventTS: "2026-06-03 12:23:45.000000"}
	if got := r2.eventTimestamp(); got != "2026-06-03 12:23:45.000000" {
		t.Errorf("eventTimestamp() fallback = %q, want the local value", got)
	}
}

// TestParseTime checks the accepted layouts all normalize to the same UTC instant
// and that an unparseable string is rejected (so a bad timestamp drops the row
// rather than corrupting the dedup key).
func TestParseTime(t *testing.T) {
	want := time.Date(2026, 6, 3, 10, 23, 45, 123456000, time.UTC)
	for _, s := range []string{
		"2026-06-03 10:23:45.123456",
		"2026-06-03T10:23:45.123456",
		"2026-06-03T10:23:45.123456Z",
		"2026-06-03T11:23:45.123456+01:00", // offset normalized to UTC
	} {
		got, ok := parseTime(s)
		if !ok {
			t.Errorf("parseTime(%q) failed", s)
			continue
		}
		if !got.Equal(want) {
			t.Errorf("parseTime(%q) = %v, want %v", s, got, want)
		}
	}
	if _, ok := parseTime("not-a-timestamp"); ok {
		t.Error("parseTime should reject an unparseable string")
	}
}

// TestParseRowSkipsGarbage confirms a non-JSON line is skipped, not fatal.
func TestParseRowSkipsGarbage(t *testing.T) {
	if _, ok := parseRow([]byte("{not json")); ok {
		t.Error("malformed JSON line should return ok=false")
	}
	if _, ok := parseRow([]byte(`{"DBUSERNAME":"X","ACTION_NAME":"SELECT","OBJECT_SCHEMA":"S","OBJECT_NAME":"T"}`)); !ok {
		t.Error("valid JSON line should parse")
	}
}
