// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

func budgetProjectionFinding(class string) sdkmodel.FindingReport {
	f := sdkmodel.FindingReport{Kind: "finops_budget_cap", Severity: sdkmodel.SeverityCritical, SubjectKind: "budget", SubjectRef: model.NewID().String(), DetailHash: strings.Repeat("a", 64), BudgetEvidence: &sdkmodel.BudgetAlertEvidenceSummary{SchemaVersion: 1, DigestVersion: 1, AlertID: model.NewID().String(), AmountClass: class, Crossing: "proven"}}
	if class == "lower_bound" {
		f.BudgetEvidence.Causes = []string{"dynamic_reservation_scan_incomplete"}
	}
	if class == "unknown" {
		f.Kind = "finops_budget_evaluation_incomplete"
		f.Severity = sdkmodel.SeverityMedium
		f.BudgetEvidence.AlertID = ""
		f.BudgetEvidence.Crossing = "unproven"
		f.BudgetEvidence.Causes = []string{"cost_read_failed"}
	}
	return f
}

func TestBudgetEvidenceNotificationProjection(t *testing.T) {
	for _, tc := range []string{"exact", "lower_bound", "unknown", "legacy", "invalid", "foreign", "registration"} {
		t.Run(tc, func(t *testing.T) {
			f := budgetProjectionFinding("exact")
			if tc == "lower_bound" || tc == "unknown" {
				f = budgetProjectionFinding(tc)
			}
			if tc == "legacy" {
				f.BudgetEvidence = nil
			}
			if tc == "invalid" {
				f.BudgetEvidence.SchemaVersion = 42
				f.DetailHash = strings.Repeat("x", 4096)
			}
			e := event.FromObservation("tenant", sdkmodel.BudgetEvidenceProducer, f)
			if tc == "foreign" {
				e.Source = "foreign"
			}
			if tc == "registration" {
				e.SourceRegistration = &event.SourceRegistration{}
			}
			n := (&Module{}).buildNotification(model.TenantID("tenant"), buildSignal(e, f), pending{})
			b, err := json.Marshal(n)
			if err != nil {
				t.Fatal(err)
			}
			var got sdk.Notification
			if err = json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			invalid := tc == "invalid" || tc == "foreign" || tc == "registration"
			if invalid {
				if got.Fields["budget_evidence_validation"] != "invalid" || got.Fields["budget_evidence_amount_class"] != "unknown" || got.Fields["budget_evidence_crossing"] != "unproven" || got.Fields["detail_hash"] != "" || got.Fields["budget_evidence_alert_id"] != "" {
					t.Fatalf("invalid acquired evidence: %+v", got.Fields)
				}
				return
			}
			for k, v := range f.BudgetEvidenceFields() {
				if got.Fields[k] != v {
					t.Errorf("%s=%q want %q", k, got.Fields[k], v)
				}
			}
			if got.Fields["detail_hash"] != f.DetailHash || got.Severity != f.Severity {
				t.Fatalf("hash/severity lost: %+v", got)
			}
			if tc == "legacy" && got.Fields["budget_evidence_validation"] != "" {
				t.Fatal("legacy certified")
			}
		})
	}
}

type evidenceRetryDispatcher struct{ notifications []sdk.Notification }

func (d *evidenceRetryDispatcher) Destinations() []string                  { return []string{"d1"} }
func (d *evidenceRetryDispatcher) DestinationsFor(model.TenantID) []string { return d.Destinations() }
func (d *evidenceRetryDispatcher) ConnectorFingerprint(string) (string, bool) {
	return "evidence-d1", true
}
func (d *evidenceRetryDispatcher) Deliver(_ context.Context, _ model.TenantID, _ string, n sdk.Notification) error {
	d.notifications = append(d.notifications, n)
	if len(d.notifications) == 1 {
		return errors.New("transient delivery outage")
	}
	return nil
}
func TestBudgetEvidenceDeliveryReplay(t *testing.T) {
	d := &evidenceRetryDispatcher{}
	h := tinyRetryHarness(t, d)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "evidence")
	editor := h.roleToken(admin, tenant, "evidence@x.io", "editor")
	h.mustCreateRoute(editor, tenant, map[string]any{"name": "budget", "destination": "d1", "match_kinds": []string{"finops_*"}, "dedup_window_seconds": 300})
	f := budgetProjectionFinding("lower_bound")
	e := event.FromObservation(tenant.String(), sdkmodel.BudgetEvidenceProducer, f)
	// Synchronous entry into the real router + outbox avoids timing as evidence.
	if err := h.mod.onEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	h.pumpOutbox(tenant)
	rows := h.outboxRows(tenant)
	if len(rows) != 1 || rows[0].String(colObStatus) != obStatusQueued || len(d.notifications) != 1 {
		t.Fatalf("outbox=%v attempts=%d", rows, len(d.notifications))
	}
	h.clk.advance(100 * time.Millisecond)
	h.pumpOutbox(tenant)
	if len(d.notifications) != 2 || !reflect.DeepEqual(d.notifications[0], d.notifications[1]) {
		t.Fatalf("retry changed persisted notification: %+v", d.notifications)
	}
	for k, v := range f.BudgetEvidenceFields() {
		if d.notifications[1].Fields[k] != v {
			t.Errorf("retry lost %s", k)
		}
	}
	if d.notifications[1].Fields["detail_hash"] != f.DetailHash {
		t.Fatal("retry lost digest")
	}
	if err := h.mod.onEvent(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	h.pumpOutbox(tenant)
	if len(d.notifications) != 2 || len(h.outboxRows(tenant)) != 1 {
		t.Fatal("duplicate finding re-sent")
	}
}
