// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import "testing"

func TestSameServerReportRequiresMeasuredConsistentWitnesses(t *testing.T) {
	valid := func() SameServerReport {
		return SameServerReport{
			HolderDatabase: "estate",
			Witnesses: []SameServerVerdict{
				{Label: "app", Database: "estate", SameServer: true},
				{Label: "admin", Database: "estate", SameServer: true},
			},
		}
	}
	cases := []struct {
		name string
		edit func(*SameServerReport)
		want bool
	}{
		{"measured agreement", func(*SameServerReport) {}, true},
		{"zero value", func(r *SameServerReport) { *r = SameServerReport{} }, false},
		{"no challenge needed", func(r *SameServerReport) { r.Witnesses = nil }, false},
		{"holder unmeasured", func(r *SameServerReport) { r.HolderDatabase = "" }, false},
		{"holder failed", func(r *SameServerReport) { r.HolderErr = "cancelled" }, false},
		{"second witness unmeasured", func(r *SameServerReport) { r.Witnesses[1] = SameServerVerdict{} }, false},
		{"witness error contradicts success", func(r *SameServerReport) { r.Witnesses[1].Err = "cancelled" }, false},
		{"acquired lock contradicts success", func(r *SameServerReport) { r.Witnesses[1].AcquiredHoldersLock = true }, false},
		{"different database contradicts success", func(r *SameServerReport) { r.Witnesses[1].Database = "other" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := valid()
			tc.edit(&r)
			if got := r.AllSameServer(); got != tc.want {
				t.Fatalf("AllSameServer() = %v, want %v; report=%+v", got, tc.want, r)
			}
		})
	}
}
