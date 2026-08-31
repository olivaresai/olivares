// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package icebergcatalog

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestPrivilegeToMode(t *testing.T) {
	cases := []struct {
		priv     string
		wantMode model.AccessMode
		wantEmit bool
	}{
		{"TABLE_READ_DATA", model.ModeRead, true},
		{" TABLE_WRITE_DATA ", model.ModeWrite, true}, // trimmed
		{"TABLE_CREATE", "", false},
		{"TABLE_DROP", "", false},
		{"TABLE_READ_PROPERTIES", "", false},
		{"TABLE_WRITE_PROPERTIES", "", false},
		{"TABLE_FULL_METADATA", "", false},
		{"", "", false},
		{"table_read_data", "", false}, // case-sensitive: verbatim token only
	}
	for _, c := range cases {
		mode, emit := privilegeToMode(c.priv)
		if mode != c.wantMode || emit != c.wantEmit {
			t.Errorf("privilegeToMode(%q) = (%q,%v), want (%q,%v)", c.priv, mode, emit, c.wantMode, c.wantEmit)
		}
	}
}

func TestParseTime(t *testing.T) {
	want := time.Date(2026, 6, 3, 14, 5, 0, 0, time.UTC)
	for _, s := range []string{
		"2026-06-03T14:05:00Z",
		"2026-06-03T14:05:00.000Z",
		"2026-06-03T16:05:00+02:00", // normalized to UTC
	} {
		got, ok := parseTime(s)
		if !ok {
			t.Errorf("parseTime(%q) ok=false", s)
			continue
		}
		if !got.Equal(want) {
			t.Errorf("parseTime(%q) = %v, want %v", s, got, want)
		}
		if got.Location() != time.UTC {
			t.Errorf("parseTime(%q) location = %v, want UTC", s, got.Location())
		}
	}
	for _, s := range []string{"", "not-a-timestamp", "2026-06-03 14:05:00"} {
		if _, ok := parseTime(s); ok {
			t.Errorf("parseTime(%q) ok=true, want false", s)
		}
	}
}
