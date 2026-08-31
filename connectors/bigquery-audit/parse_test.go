// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package bigqueryaudit

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestClassifyMode(t *testing.T) {
	read := &tableDataEvent{}
	change := &tableDataEvent{}

	cases := []struct {
		name     string
		meta     bqAuditMetadata
		wantMode model.AccessMode
		wantOK   bool
	}{
		{"read", bqAuditMetadata{TableDataRead: read}, model.ModeRead, true},
		{"change", bqAuditMetadata{TableDataChange: change}, model.ModeWrite, true},
		{"neither", bqAuditMetadata{}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, ok := classifyMode(c.meta)
			if ok != c.wantOK || mode != c.wantMode {
				t.Errorf("classifyMode = (%q, %v), want (%q, %v)", mode, ok, c.wantMode, c.wantOK)
			}
		})
	}
}

func TestResourceRefFromName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"table", "projects/acme-prod/datasets/sales/tables/customers", "acme-prod.sales.customers", true},
		{"job-level", "projects/acme-prod/jobs/bquxjob_x", "", false},
		{"dataset-level", "projects/acme-prod/datasets/sales", "", false},
		{"empty", "", "", false},
		{"empty-table-id", "projects/acme-prod/datasets/sales/tables/", "", false},
		{"wrong-collection", "projects/acme-prod/datasets/sales/views/v1", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := resourceRefFromName(c.in)
			if ok != c.ok || got != c.want {
				t.Errorf("resourceRefFromName(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestParseTime(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"2026-06-03T10:23:45.123456Z", true},
		{"2026-06-03T10:23:45Z", true},
		{"2026-06-03T10:23:45.123456789Z", true},
		{"not-a-time", false},
		{"", false},
	}
	for _, c := range cases {
		ts, ok := parseTime(c.in)
		if ok != c.ok {
			t.Errorf("parseTime(%q) ok = %v, want %v", c.in, ok, c.ok)
		}
		if ok && ts.Location() != time.UTC {
			t.Errorf("parseTime(%q) location = %v, want UTC", c.in, ts.Location())
		}
	}
}
