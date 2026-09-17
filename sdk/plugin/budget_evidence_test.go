// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package plugin

import (
	"github.com/olivaresai/olivares/sdk/model"
	pb "github.com/olivaresai/olivares/sdk/plugin/genpb/olivaresv1"
	"google.golang.org/protobuf/proto"
	"reflect"
	"strings"
	"testing"
)

func TestBudgetEvidenceProtoRoundtrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		b    *model.BudgetAlertEvidenceSummary
	}{
		{"valid", &model.BudgetAlertEvidenceSummary{SchemaVersion: 1, DigestVersion: 1, AlertID: "bd283469-7c26-4256-9798-f3b175a8df2a", AmountClass: "lower_bound", Crossing: "proven", Causes: []string{"dynamic_reservation_scan_incomplete"}}},
		{"legacy", nil}, {"present_empty", &model.BudgetAlertEvidenceSummary{}},
		{"future_invalid", &model.BudgetAlertEvidenceSummary{SchemaVersion: 9, DigestVersion: 8, AlertID: "bad", AmountClass: "opaque", Crossing: "invalid", Causes: []string{"raw"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := model.FindingReport{Kind: "finops_budget_cap", SubjectKind: "budget", SubjectRef: "b14c91e9-b593-48d6-94aa-fc8de67c1f51", DetailHash: strings.Repeat("a", 64), BudgetEvidence: tc.b}
			for _, obs := range []model.Observation{f, &f} {
				in, err := ObservationToPB(obs)
				if err != nil {
					t.Fatal(err)
				}
				wire, err := proto.Marshal(in)
				if err != nil {
					t.Fatal(err)
				}
				var decoded pb.Observation
				if err = proto.Unmarshal(wire, &decoded); err != nil {
					t.Fatal(err)
				}
				out, err := ObservationFromPB(&decoded)
				if err != nil {
					t.Fatal(err)
				}
				got := out.(model.FindingReport)
				if !reflect.DeepEqual(got.BudgetEvidence, f.BudgetEvidence) || got.DetailHash != f.DetailHash || got.BudgetEvidenceValidity() != f.BudgetEvidenceValidity() {
					t.Fatalf("budget evidence lost on protobuf roundtrip: got=%+v want=%+v", got.BudgetEvidence, f.BudgetEvidence)
				}
			}
		})
	}
}
