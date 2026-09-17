// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemfmt

import (
	"strings"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func TestBudgetEvidenceEveryFormat(t *testing.T) {
	for _, class := range []string{"exact", "lower_bound", "unknown", "invalid"} {
		t.Run(class, func(t *testing.T) {
			f := model.FindingReport{Kind: "finops_budget_cap", SubjectKind: "budget", SubjectRef: "12345678-1234-1234-1234-123456789abc", DetailHash: strings.Repeat("a", 64), BudgetEvidence: &model.BudgetAlertEvidenceSummary{SchemaVersion: 1, DigestVersion: 1, AlertID: "23456789-2345-2345-2345-23456789abcd", AmountClass: class, Crossing: "proven"}}
			if class == "lower_bound" {
				f.BudgetEvidence.Causes = []string{"dynamic_reservation_scan_incomplete"}
			}
			if class == "unknown" {
				f.Kind = "finops_budget_evaluation_incomplete"
				f.BudgetEvidence.AlertID = ""
				f.BudgetEvidence.Crossing = "unproven"
				f.BudgetEvidence.Causes = []string{"cost_read_failed"}
			}
			n := sdk.Notification{Type: "finding.reported", Fields: f.BudgetEvidenceFields()}
			if class != "invalid" {
				n.Fields["detail_hash"] = f.DetailHash
			}
			outputs := map[string]string{"CEF": CEF(DefaultDevice(), n), "LEEF": LEEF(DefaultDevice(), n), "syslog": Syslog5424(DefaultDevice(), SyslogOptions{}, n)}
			for name, fn := range map[string]func(Device, sdk.Notification) ([]byte, error){"OTLP": OTLPLogJSON, "OCSF": OCSF, "ASIM": ASIMAgentEvent} {
				b, err := fn(DefaultDevice(), n)
				if err != nil {
					t.Fatal(err)
				}
				outputs[name] = string(b)
			}
			for format, out := range outputs {
				for k, v := range n.Fields {
					if !strings.Contains(out, k) || !strings.Contains(out, v) {
						t.Errorf("%s lost %s=%s", format, k, v)
					}
				}
			}
		})
	}
}
