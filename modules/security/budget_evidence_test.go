// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package security

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func TestBudgetEvidenceSecurityPersistence(t *testing.T) {
	h := newHarness(t, nil)
	tenant := h.createOrg(h.adminLogin(), "financial-evidence")
	m := New()
	m.UseData(api.NewModuleData(h.st))
	ctx := context.Background()
	for _, tc := range []string{"exact", "lower_bound", "unknown", "legacy", "invalid", "foreign", "registration"} {
		t.Run(tc, func(t *testing.T) {
			f := sdkmodel.FindingReport{Kind: "finops_budget_cap", Severity: sdkmodel.SeverityCritical, SubjectKind: "budget", SubjectRef: model.NewID().String(), DetailHash: strings.Repeat("b", 64), BudgetEvidence: &sdkmodel.BudgetAlertEvidenceSummary{SchemaVersion: 1, DigestVersion: 1, AlertID: model.NewID().String(), AmountClass: "exact", Crossing: "proven"}}
			if tc == "lower_bound" {
				f.BudgetEvidence.AmountClass = tc
				f.BudgetEvidence.Causes = []string{"dynamic_reservation_scan_incomplete"}
			}
			if tc == "unknown" {
				f.Kind = "finops_budget_evaluation_incomplete"
				f.Severity = sdkmodel.SeverityMedium
				f.BudgetEvidence.AlertID = ""
				f.BudgetEvidence.AmountClass = "unknown"
				f.BudgetEvidence.Crossing = "unproven"
				f.BudgetEvidence.Causes = []string{"cost_read_failed"}
			}
			if tc == "legacy" {
				f.BudgetEvidence = nil
			}
			if tc == "invalid" {
				f.BudgetEvidence.SchemaVersion = 42
			}
			e := event.FromObservation(tenant.String(), sdkmodel.BudgetEvidenceProducer, f)
			if tc == "foreign" {
				e.Source = "foreign"
			}
			if tc == "registration" {
				e.SourceRegistration = &event.SourceRegistration{}
			}
			if err := m.onEvent(ctx, e); err != nil {
				t.Fatal(err)
			}
			if tc == "unknown" {
				if err := m.onEvent(ctx, e); err != nil {
					t.Fatal(err)
				}
			} // bounded diagnostic dedup
			var rows []model.Finding
			if err := h.st.View(ctx, tenant, func(sc store.Scope) error {
				var err error
				rows, _, err = sc.Findings().List(ctx, model.Query{Filters: []model.Filter{{Column: "subject_id", Op: model.OpEq, Value: f.SubjectRef}}})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 {
				t.Fatalf("rows=%d", len(rows))
			}
			row := rows[0]
			invalid := tc == "invalid" || tc == "foreign" || tc == "registration"
			if invalid {
				if row.Metadata["budget_evidence_validation"] != "invalid" || row.Metadata["budget_evidence_crossing"] != "unproven" || row.Metadata["budget_evidence_amount_class"] != "unknown" || row.Metadata["detail_hash"] != nil || row.Metadata["source_detail_hash"] != nil {
					t.Fatalf("invalid metadata: %+v", row.Metadata)
				}
				return
			}
			for k, v := range f.BudgetEvidenceFields() {
				if row.Metadata[k] != v {
					t.Errorf("%s=%v want %s", k, row.Metadata[k], v)
				}
			}
			if row.Metadata["source_detail_hash"] != f.DetailHash {
				t.Fatal("source digest lost")
			}
			if tc != "legacy" && row.Metadata["detail_hash"] != f.DetailHash {
				t.Fatal("evidence digest lost")
			}
			if tc == "legacy" && row.Metadata["budget_evidence_validation"] != nil {
				t.Fatal("legacy certified")
			}
			if tc == "unknown" && (row.Severity != model.SeverityMedium || row.Kind != f.Kind) {
				t.Fatalf("diagnostic changed: %+v", row)
			}
		})
	}
	// Ordinary Medium findings still do not enter the general HIGH+ anomaly arm.
	f := sdkmodel.FindingReport{Kind: "ordinary", Severity: sdkmodel.SeverityMedium, SubjectRef: model.NewID().String()}
	if err := m.onEvent(ctx, event.FromObservation(tenant.String(), "foreign", f)); err != nil {
		t.Fatal(err)
	}
	if err := h.st.View(ctx, tenant, func(sc store.Scope) error {
		rows, _, err := sc.Findings().List(ctx, model.Query{})
		if len(rows) != 7 {
			t.Errorf("unexpected admission count=%d", len(rows))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
