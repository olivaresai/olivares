// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package redshiftaudit

import (
	"testing"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestParseLinePrefix(t *testing.T) {
	line := `'2026-06-03T10:23:45Z UTC [ db=salesdb user=analyst pid=123 userid=100 xid=456 ]' LOG: SELECT c.id FROM customers c`
	rec, ok := parseLine(line)
	if !ok {
		t.Fatal("parseLine returned ok=false for a valid line")
	}
	if rec.timestamp != "2026-06-03T10:23:45Z" {
		t.Errorf("timestamp = %q, want 2026-06-03T10:23:45Z (zone abbreviation must be stripped)", rec.timestamp)
	}
	if rec.db != "salesdb" {
		t.Errorf("db = %q, want salesdb", rec.db)
	}
	if rec.user != "analyst" {
		t.Errorf("user = %q, want analyst", rec.user)
	}
	if rec.verb != "SELECT" {
		t.Errorf("verb = %q, want SELECT", rec.verb)
	}
}

// TestParseLineDropsBody confirms the record retains only the leading verb, not
// any of the statement body — the body must never reach a struct field.
func TestParseLineDropsBody(t *testing.T) {
	line := `'2026-06-03T10:23:45Z UTC [ db=d user=u pid=1 userid=1 xid=1 ]' LOG: UPDATE secrets SET token = 'hunter2' WHERE id = 1`
	rec, ok := parseLine(line)
	if !ok {
		t.Fatal("ok=false")
	}
	if rec.verb != "UPDATE" {
		t.Errorf("verb = %q, want UPDATE", rec.verb)
	}
	// The only fields are timestamp/db/user/verb; none should hold the body.
	for name, v := range map[string]string{"timestamp": rec.timestamp, "db": rec.db, "user": rec.user, "verb": rec.verb} {
		for _, frag := range []string{"secrets", "token", "hunter2", "WHERE"} {
			if contains(v, frag) {
				t.Errorf("field %s = %q leaked body fragment %q", name, v, frag)
			}
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestParseLineContinuation rejects a continuation line of a multi-line statement
// (no bracketed prefix), which Redshift logs as a separate event.
func TestParseLineContinuation(t *testing.T) {
	if _, ok := parseLine("    AND c.active = true"); ok {
		t.Error("a continuation line (no bracket prefix) must be rejected")
	}
	if _, ok := parseLine(""); ok {
		t.Error("an empty line must be rejected")
	}
}

func TestClassifyVerb(t *testing.T) {
	cases := []struct {
		verb     string
		wantMode model.AccessMode
		wantTool string
	}{
		{"SELECT", model.ModeRead, "SELECT"},
		{"show", model.ModeRead, "SHOW"},
		{"UNLOAD", model.ModeRead, "UNLOAD"},
		{"INSERT", model.ModeWrite, "INSERT"},
		{"UPDATE", model.ModeWrite, "UPDATE"},
		{"DELETE", model.ModeWrite, "DELETE"},
		{"COPY", model.ModeWrite, "COPY"},
		{"TRUNCATE", model.ModeWrite, "TRUNCATE"},
		{"CREATE", model.ModeWrite, "CREATE"},
		{"ALTER", model.ModeWrite, "ALTER"},
		{"DROP", model.ModeWrite, "DROP"},
		{"GRANT", model.ModeWrite, "GRANT"},
		{"REVOKE", model.ModeWrite, "REVOKE"},
		{"VACUUM", model.ModeUnknown, "VACUUM"}, // maintenance — not classified
		{"BEGIN", model.ModeUnknown, "BEGIN"},   // transaction control — not classified
		{"", model.ModeUnknown, ""},             // no verb -> unknown, empty tool
	}
	for _, c := range cases {
		mode, tool := classifyVerb(c.verb)
		if mode != c.wantMode || tool != c.wantTool {
			t.Errorf("classifyVerb(%q) = (%q,%q), want (%q,%q)", c.verb, mode, tool, c.wantMode, c.wantTool)
		}
	}
}

func TestParseTime(t *testing.T) {
	for _, s := range []string{"2026-06-03T10:23:45Z", "2026-06-03T10:23:45.500Z", "2026-06-03T10:23:45+00:00"} {
		if _, ok := parseTime(s); !ok {
			t.Errorf("parseTime(%q) failed", s)
		}
	}
	if _, ok := parseTime("not-a-time"); ok {
		t.Error("parseTime accepted garbage")
	}
	// Normalization to UTC: a +02:00 offset must shift to UTC wall clock.
	tt, ok := parseTime("2026-06-03T12:23:45+02:00")
	if !ok {
		t.Fatal("offset timestamp rejected")
	}
	if tt.Hour() != 10 || tt.Location().String() != "UTC" {
		t.Errorf("parseTime offset normalization = %v, want 10:23:45 UTC", tt)
	}
}
