// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package deltasharing

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestClassifyAction(t *testing.T) {
	cases := map[string]model.AccessMode{
		// Every recipient read RPC is egress => read.
		"queryTable":      model.ModeRead,
		"getTableData":    model.ModeRead,
		"getTableVersion": model.ModeRead,
		"getMetadata":     model.ModeRead,
		"listShares":      model.ModeRead,
		"listSchemas":     model.ModeRead,
		"listTables":      model.ModeRead,
		"listAllTables":   model.ModeRead,
		"getShare":        model.ModeRead,
		// An action the audit records that is not a recognized recipient read is
		// unknown — never guessed (ARCHITECTURE.md).
		"adminRotateToken": model.ModeUnknown,
		"":                 model.ModeUnknown,
		"QUERYTABLE":       model.ModeUnknown, // case-sensitive: the protocol vocabulary is exact
	}
	for action, want := range cases {
		if got := classifyAction(action); got != want {
			t.Errorf("classifyAction(%q) = %q, want %q", action, got, want)
		}
	}
}

func TestParseEntry(t *testing.T) {
	t.Run("table-scoped query", func(t *testing.T) {
		e, ok := parseEntry([]byte(`{"timestamp":"2026-06-04T09:15:01Z","recipient":"acme-corp","share":"sales_share","schema":"public","table":"q3","action":"queryTable"}`))
		if !ok {
			t.Fatal("expected ok")
		}
		if e.Recipient != "acme-corp" || e.Share != "sales_share" || e.Schema != "public" || e.Table != "q3" || e.Action != "queryTable" {
			t.Errorf("got %+v", e)
		}
	})

	t.Run("trims whitespace", func(t *testing.T) {
		e, ok := parseEntry([]byte(`{"recipient":" acme-corp ","share":" sales_share ","action":" listShares "}`))
		if !ok {
			t.Fatal("expected ok")
		}
		if e.Recipient != "acme-corp" || e.Share != "sales_share" || e.Action != "listShares" {
			t.Errorf("got %+v", e)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		if _, ok := parseEntry([]byte(`{not json`)); ok {
			t.Error("invalid JSON must not parse")
		}
	})

	t.Run("no action", func(t *testing.T) {
		if _, ok := parseEntry([]byte(`{"recipient":"acme-corp","share":"sales_share"}`)); ok {
			t.Error("an entry with no action must not parse")
		}
	})
}

func TestResolveResource(t *testing.T) {
	t.Run("table-scoped", func(t *testing.T) {
		kind, ref, ok := resolveResource(entry{Share: "sales_share", Schema: "public", Table: "q3"})
		if !ok || kind != resourceKindTable || ref != "sales_share.public.q3" {
			t.Errorf("got (%q,%q,%v)", kind, ref, ok)
		}
	})
	t.Run("share-level", func(t *testing.T) {
		kind, ref, ok := resolveResource(entry{Share: "sales_share"})
		if !ok || kind != resourceKindShare || ref != "sales_share" {
			t.Errorf("got (%q,%q,%v)", kind, ref, ok)
		}
	})
	t.Run("no share", func(t *testing.T) {
		if _, _, ok := resolveResource(entry{Action: "listShares"}); ok {
			t.Error("an entry with no share has no resource to anchor")
		}
	})
}

func TestParseTime(t *testing.T) {
	valid := []string{
		"2026-06-04T09:15:01Z",
		"2026-06-04T09:15:01.500Z",
		"2026-06-04T09:15:01.123456789Z",
		"2026-06-04T09:15:01+00:00",
	}
	for _, s := range valid {
		if _, ok := parseTime(s); !ok {
			t.Errorf("parseTime(%q) failed", s)
		}
	}
	if tm, ok := parseTime("2026-06-04T11:15:01+02:00"); !ok || tm.Location() != time.UTC {
		t.Errorf("expected UTC-normalized time, got %v ok=%v", tm, ok)
	} else if tm.Hour() != 9 {
		t.Errorf("expected normalization to 09:15 UTC, got %v", tm)
	}
	if _, ok := parseTime("not a timestamp"); ok {
		t.Error("garbage timestamp must not parse")
	}
}
