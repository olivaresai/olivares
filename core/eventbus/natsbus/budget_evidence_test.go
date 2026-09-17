// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package natsbus

import (
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/olivaresai/olivares/sdk/model"
	"reflect"
	"strings"
	"testing"
)

func TestBudgetEvidenceNATSCodec(t *testing.T) {
	for _, tc := range []struct {
		name string
		b    *model.BudgetAlertEvidenceSummary
	}{
		{"valid", &model.BudgetAlertEvidenceSummary{SchemaVersion: 1, DigestVersion: 1, AlertID: "bd283469-7c26-4256-9798-f3b175a8df2a", AmountClass: "lower_bound", Crossing: "proven", Causes: []string{"dynamic_reservation_scan_incomplete"}}},
		{"legacy", nil}, {"present_empty", &model.BudgetAlertEvidenceSummary{}},
		{"future_invalid", &model.BudgetAlertEvidenceSummary{SchemaVersion: 2, DigestVersion: 3, AlertID: "bad", Causes: []string{"raw"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := model.FindingReport{Kind: "finops_budget_cap", SubjectKind: "budget", SubjectRef: "b14c91e9-b593-48d6-94aa-fc8de67c1f51", DetailHash: strings.Repeat("a", 64), BudgetEvidence: tc.b}
			for _, obs := range []model.Observation{f, &f} {
				in := event.FromObservation("tenant-a", model.BudgetEvidenceProducer, obs)
				wire, err := EncodeEvent(in)
				if err != nil {
					t.Fatal(err)
				}
				out, err := DecodeEvent(wire, DefaultDecoders())
				if err != nil {
					t.Fatal(err)
				}
				got, ok := event.FindingOf(out)
				if !ok || !reflect.DeepEqual(got.BudgetEvidence, f.BudgetEvidence) || got.DetailHash != f.DetailHash || out.Source != in.Source || out.Tenant != in.Tenant {
					t.Fatalf("NATS lost identity/presence: %+v", out)
				}
			}
		})
	}
}
