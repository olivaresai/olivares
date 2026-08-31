// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package pgaudit

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestClassToMode(t *testing.T) {
	cases := []struct {
		class    string
		wantMode model.AccessMode
		wantOK   bool
	}{
		{"READ", model.ModeRead, true},
		{"read", model.ModeRead, true},
		{"WRITE", model.ModeWrite, true},
		{"DDL", model.ModeWrite, true},
		{"FUNCTION", model.ModeUnknown, true},
		{"ROLE", "", false},
		{"MISC", "", false},
		{"MISC_SET", "", false},
		{"garbage", "", false},
	}
	for _, c := range cases {
		mode, ok := classToMode(c.class)
		if mode != c.wantMode || ok != c.wantOK {
			t.Errorf("classToMode(%q) = (%q,%v), want (%q,%v)", c.class, mode, ok, c.wantMode, c.wantOK)
		}
	}
}

func TestResourceKindFor(t *testing.T) {
	cases := map[string]string{
		"TABLE":             "postgres.table",
		"VIEW":              "postgres.view",
		"MATERIALIZED VIEW": "postgres.materialized_view",
		"":                  "postgres.object",
		"  ":                "postgres.object",
	}
	for in, want := range cases {
		if got := resourceKindFor(in); got != want {
			t.Errorf("resourceKindFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseAuditMessage(t *testing.T) {
	t.Run("read select", func(t *testing.T) {
		ar, ok := parseAuditMessage("AUDIT: SESSION,1,1,READ,SELECT,TABLE,public.customers,SELECT 1,1")
		if !ok {
			t.Fatal("expected ok")
		}
		if ar.class != "READ" || ar.command != "SELECT" || ar.objType != "TABLE" || ar.objName != "public.customers" {
			t.Errorf("got %+v", ar)
		}
	})

	t.Run("write insert with quoted comma statement", func(t *testing.T) {
		// The STATEMENT field is pgAudit-quoted because it contains commas; the
		// object name must still be extracted correctly and the statement ignored.
		msg := `AUDIT: SESSION,2,1,WRITE,INSERT,TABLE,public.orders,"INSERT INTO orders (id, total) VALUES (1, 2)",3 args`
		ar, ok := parseAuditMessage(msg)
		if !ok {
			t.Fatal("expected ok")
		}
		if ar.class != "WRITE" || ar.objName != "public.orders" {
			t.Errorf("got %+v", ar)
		}
	})

	t.Run("non-audit message", func(t *testing.T) {
		if _, ok := parseAuditMessage("connection authorized: user=postgres"); ok {
			t.Error("a non-AUDIT message must not parse")
		}
	})

	t.Run("too few fields", func(t *testing.T) {
		if _, ok := parseAuditMessage("AUDIT: SESSION,1,1"); ok {
			t.Error("a truncated AUDIT message must not parse")
		}
	})

	t.Run("misc with empty object", func(t *testing.T) {
		ar, ok := parseAuditMessage("AUDIT: SESSION,2,1,MISC,SET,,,SET search_path = public,<none>")
		if !ok {
			t.Fatal("expected ok (parses) — the class filter drops it later")
		}
		if ar.class != "MISC" || ar.objName != "" {
			t.Errorf("got %+v", ar)
		}
	})
}

func TestParseTimestamp(t *testing.T) {
	valid := []string{
		"2026-06-03 10:23:45.123 UTC",
		"2026-06-03 10:23:45 UTC",
		"2026-06-03T10:23:45.123456Z",
		"2026-06-03 10:23:45.123-00",
	}
	for _, s := range valid {
		if _, ok := parseTimestamp(s); !ok {
			t.Errorf("parseTimestamp(%q) failed", s)
		}
	}
	if tm, ok := parseTimestamp("2026-06-03 10:23:45.123 UTC"); !ok || tm.Location() != time.UTC {
		t.Errorf("expected UTC-normalized time, got %v ok=%v", tm, ok)
	}
	if _, ok := parseTimestamp("not a timestamp"); ok {
		t.Error("garbage timestamp must not parse")
	}
	// A non-UTC zone abbreviation cannot be resolved to an offset; it must be
	// rejected rather than silently shifted (the server must log in UTC).
	if _, ok := parseTimestamp("2026-06-03 10:23:45.123 EDT"); ok {
		t.Error("non-UTC abbreviation must be rejected, not given a wrong UTC time")
	}
	// GMT has a real zero offset and is accepted.
	if _, ok := parseTimestamp("2026-06-03 10:23:45 GMT"); !ok {
		t.Error("GMT should be accepted (offset 0)")
	}
}
