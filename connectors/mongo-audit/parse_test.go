// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package mongoaudit

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestCommandToMode(t *testing.T) {
	reads := []string{"find", "aggregate", "count", "distinct", "getMore", "listCollections", "listIndexes"}
	for _, c := range reads {
		if got := commandToMode(c); got != model.ModeRead {
			t.Errorf("commandToMode(%q) = %q, want read", c, got)
		}
	}
	writes := []string{"insert", "update", "delete", "findAndModify", "create", "createIndexes", "drop", "dropDatabase", "renameCollection"}
	for _, c := range writes {
		if got := commandToMode(c); got != model.ModeWrite {
			t.Errorf("commandToMode(%q) = %q, want write", c, got)
		}
	}
	// A command MongoDB does not classify R/RW yields explicit unknown, never a
	// guess (ARCHITECTURE.md). The access still happened — the edge is still emitted.
	unknowns := []string{"replSetGetStatus", "ping", "serverStatus", "", "hello"}
	for _, c := range unknowns {
		if got := commandToMode(c); got != model.ModeUnknown {
			t.Errorf("commandToMode(%q) = %q, want unknown", c, got)
		}
	}
}

func TestResourceFor(t *testing.T) {
	cases := []struct {
		ns, wantKind, wantRef string
	}{
		{"salesdb.customers", "mongo.collection", "salesdb.customers"},
		{"salesdb.events.archive", "mongo.collection", "salesdb.events.archive"}, // dotted collection name
		{"salesdb", "mongo.database", "salesdb"},
		{"admin.$cmd", "mongo.collection", "admin.$cmd"},
		{"salesdb.", "mongo.database", "salesdb"}, // trailing dot, no collection
		{"", "mongo.database", ""},
	}
	for _, c := range cases {
		kind, ref := resourceFor(c.ns)
		if kind != c.wantKind || ref != c.wantRef {
			t.Errorf("resourceFor(%q) = (%q,%q), want (%q,%q)", c.ns, kind, ref, c.wantKind, c.wantRef)
		}
	}
}

func TestOriginRef(t *testing.T) {
	cases := []struct {
		users []mongoUser
		want  string
	}{
		{[]mongoUser{{User: "u", DB: "admin"}}, "u@admin"},
		{[]mongoUser{{User: "u", DB: ""}}, "u"},
		{[]mongoUser{{User: "first", DB: "admin"}, {User: "second", DB: "x"}}, "first@admin"},
		{nil, ""},
		{[]mongoUser{}, ""},
		{[]mongoUser{{User: "  ", DB: "admin"}}, ""},
	}
	for _, c := range cases {
		if got := originRef(c.users); got != c.want {
			t.Errorf("originRef(%+v) = %q, want %q", c.users, got, c.want)
		}
	}
}

func TestParseTime(t *testing.T) {
	want := time.Date(2026, 6, 3, 10, 23, 45, 806000000, time.UTC)
	cases := []string{
		"2026-06-03T10:23:45.806Z",
		"2026-06-03T10:23:45.806+0000",  // MongoDB classic offset (no colon)
		"2026-06-03T11:23:45.806+0100",  // offset normalized to UTC
		"2026-06-03T10:23:45.806+00:00", // RFC3339 with colon
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
		if got.Location() != time.UTC {
			t.Errorf("parseTime(%q) not normalized to UTC: %v", s, got.Location())
		}
	}
	if _, ok := parseTime("not-a-date"); ok {
		t.Error("parseTime(garbage) should fail")
	}
	if _, ok := parseTime(""); ok {
		t.Error("parseTime(empty) should fail")
	}
}

// TestDeniedNotAnAccess documents the result-code rule at the constant level: only
// result==0 (resultAuthorized) is an authorized access; a non-zero code (e.g. 13
// Unauthorized) is a denial — an attempt, not an access — and is dropped. The
// fixture covers the end-to-end skip; this pins the constant.
func TestDeniedNotAnAccess(t *testing.T) {
	if resultAuthorized != 0 {
		t.Errorf("resultAuthorized = %d, want 0", resultAuthorized)
	}
	if atypeAuthCheck != "authCheck" {
		t.Errorf("atypeAuthCheck = %q, want authCheck", atypeAuthCheck)
	}
}
