// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func evidenceFinding() FindingReport {
	return FindingReport{Kind: "finops_budget_cap", SubjectKind: "budget",
		SubjectRef: "b14c91e9-b593-48d6-94aa-fc8de67c1f51", DetailHash: strings.Repeat("a", 64),
		BudgetEvidence: &BudgetAlertEvidenceSummary{SchemaVersion: 1, DigestVersion: 1,
			AlertID: "bd283469-7c26-4256-9798-f3b175a8df2a", AmountClass: "exact", Crossing: "proven"}}
}

func TestBudgetEvidenceShapeAndPresence(t *testing.T) {
	cases := []struct {
		name, want string
		edit       func(*FindingReport)
	}{
		{"exact", "structurally_valid", func(*FindingReport) {}},
		{"bound", "structurally_valid", func(f *FindingReport) {
			f.BudgetEvidence.AmountClass = "lower_bound"
			f.BudgetEvidence.Causes = []string{"dynamic_reservation_scan_incomplete"}
		}},
		{"diagnostic", "structurally_valid", func(f *FindingReport) {
			f.Kind = "finops_budget_evaluation_incomplete"
			f.BudgetEvidence.AlertID = ""
			f.BudgetEvidence.AmountClass = "unknown"
			f.BudgetEvidence.Crossing = "unproven"
			f.BudgetEvidence.Causes = []string{"cost_cursor_missing"}
		}},
		{"catalogue", "structurally_valid", func(f *FindingReport) {
			f.Kind = "finops_budget_evaluation_incomplete"
			f.SubjectRef = ""
			f.BudgetEvidence.AlertID = ""
			f.BudgetEvidence.AmountClass = "unknown"
			f.BudgetEvidence.Crossing = "unproven"
			f.BudgetEvidence.Causes = []string{"budget_census_truncated"}
		}},
		{"legacy", "absent", func(f *FindingReport) { f.BudgetEvidence = nil }},
		{"empty", "invalid", func(f *FindingReport) { f.BudgetEvidence = &BudgetAlertEvidenceSummary{} }},
		{"future", "invalid", func(f *FindingReport) { f.BudgetEvidence.SchemaVersion = 2 }},
		{"future_digest", "invalid", func(f *FindingReport) { f.BudgetEvidence.DigestVersion = 2 }},
		{"bad_id", "invalid", func(f *FindingReport) { f.BudgetEvidence.AlertID = "not-an-id" }},
		{"zero_subject", "invalid", func(f *FindingReport) { f.SubjectRef = "00000000-0000-0000-0000-000000000000" }},
		{"bad_hash", "invalid", func(f *FindingReport) { f.DetailHash = "unbounded raw error" }},
		{"wrong_kind", "invalid", func(f *FindingReport) { f.Kind = "arbitrary" }},
		{"wrong_subject", "invalid", func(f *FindingReport) { f.SubjectKind = "agent" }},
		{"unknown_alert", "invalid", func(f *FindingReport) { f.BudgetEvidence.AmountClass = "unknown" }},
		{"unproven_alert", "invalid", func(f *FindingReport) { f.BudgetEvidence.Crossing = "unproven" }},
		{"bound_without_cause", "invalid", func(f *FindingReport) { f.BudgetEvidence.AmountClass = "lower_bound" }},
		{"raw_cause", "invalid", func(f *FindingReport) { f.BudgetEvidence.Causes = []string{"raw store error"} }},
		{"duplicate", "invalid", func(f *FindingReport) { f.BudgetEvidence.Causes = []string{"cost_read_failed", "cost_read_failed"} }},
		{"unordered", "invalid", func(f *FindingReport) { f.BudgetEvidence.Causes = []string{"cost_scan_truncated", "cost_read_failed"} }},
		{"oversize", "invalid", func(f *FindingReport) { f.BudgetEvidence.Causes = make([]string, 37) }},
		{"diagnostic_with_alert", "invalid", func(f *FindingReport) { f.Kind = "finops_budget_evaluation_incomplete" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := evidenceFinding()
			tc.edit(&f)
			if got := f.BudgetEvidenceValidity(); got != tc.want {
				t.Fatalf("validity=%s want %s", got, tc.want)
			}
			fields := f.BudgetEvidenceFields()
			if tc.want == "absent" {
				if fields != nil {
					t.Fatal("legacy acquired evidence")
				}
			} else if fields["budget_evidence_validation"] != tc.want {
				t.Fatalf("projection: %v", fields)
			}
			if tc.want == "invalid" && (len(fields) != 3 || fields["budget_evidence_amount_class"] != "unknown" || fields["budget_evidence_crossing"] != "unproven") {
				t.Fatalf("invalid projection leaked claims: %v", fields)
			}
			b, err := json.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			var out FindingReport
			if err = json.Unmarshal(b, &out); err != nil {
				t.Fatal(err)
			}
			if (out.BudgetEvidence == nil) != (f.BudgetEvidence == nil) {
				t.Fatal("JSON lost presence")
			}
			if tc.want == "absent" && strings.Contains(string(b), "BudgetEvidence") {
				t.Fatal("legacy JSON shape changed")
			}
		})
	}
}

func TestBudgetEvidenceCloneOwnsCauses(t *testing.T) {
	f := evidenceFinding()
	f.BudgetEvidence.Causes = []string{"cost_read_failed"}
	c := f.BudgetEvidence.Clone()
	f.BudgetEvidence.Causes[0] = "mutated"
	if c.Causes[0] != "cost_read_failed" {
		t.Fatal("clone aliases producer causes")
	}
	var absent *BudgetAlertEvidenceSummary
	if absent.Clone() != nil {
		t.Fatal("nil clone acquired presence")
	}
}
