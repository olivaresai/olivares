// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/sdk/event"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// Observes the real transaction boundary; rollback is injected AFTER all normal
// writes/evaluation succeeded, when pending publications already exist.
type transportTxData struct {
	api.ModuleData
	inside, committed bool
	rollback          error
}

func (d *transportTxData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	d.committed = false
	err := d.ModuleData.Mutate(ctx, tenant, func(sc store.Scope) error {
		d.inside = true
		defer func() { d.inside = false }()
		if err := fn(sc); err != nil {
			return err
		}
		return d.rollback
	})
	d.committed = err == nil
	return err
}

type transportCommitHost struct {
	*fakeHost
	check func(event.Event)
}

func (h *transportCommitHost) Publish(ctx context.Context, e event.Event) error {
	h.check(e)
	return h.fakeHost.Publish(ctx, e)
}

func TestBudgetEvidenceProducerCommitAndRollback(t *testing.T) {
	for _, tc := range []string{"exact", "bound", "rollback", "diagnostic"} {
		t.Run(tc, func(t *testing.T) {
			m, st, tenant, host := newFin(t)
			m.clock = &fakeClock{t: baseTime}
			spec := budgetSpec{Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD, Action: "block", Thresholds: []float64{1}}
			cost := int64(12 * oneUSD)
			if tc == "bound" {
				spec.ReservedMicroUSD = 12 * oneUSD
				cost = 0
			}
			budget := createBudget(t, st, tenant, "transport", spec)
			if tc == "bound" {
				forcePager(m, func(int, model.Query) ([]model.Record, model.Page) {
					return nil, model.Page{HasMore: true}
				})
			}
			if tc == "diagnostic" {
				forceCostPages(m, func(int, model.Query) ([]model.Record, model.Page) {
					return []model.Record{costSampleRow(tenant, baseTime, 12*oneUSD)}, model.Page{HasMore: true}
				})
			}
			d := &transportTxData{ModuleData: m.data}
			m.UseData(d)
			boom := errors.New("injected rollback after successful evaluation")
			if tc == "rollback" {
				d.rollback = boom
			}
			checked := 0
			m.host = &transportCommitHost{fakeHost: host, check: func(e event.Event) {
				checked++
				if d.inside || !d.committed {
					t.Fatal("published before Mutate committed")
				}
				f, ok := event.FindingOf(e)
				if !ok {
					t.Fatalf("not a finding: %T", e.Payload)
				}
				if e.Source != Name || e.Tenant != tenant.String() || f.SubjectRef != budget.String() || f.BudgetEvidenceValidity() != "structurally_valid" {
					t.Fatalf("invalid producer binding: %+v", e)
				}
				rows := alertRows(t, st, tenant) // A separate real read succeeds DURING publication.
				if tc == "diagnostic" {
					if len(rows) != 0 || f.Kind != "finops_budget_evaluation_incomplete" || f.Severity != sdkmodel.SeverityMedium || f.BudgetEvidence.AlertID != "" || f.BudgetEvidence.AmountClass != "unknown" || f.BudgetEvidence.Crossing != "unproven" {
						t.Fatalf("diagnostic acquired a crossing/row: %+v", f)
					}
					if !reflect.DeepEqual(f.BudgetEvidence.Causes, []string{"cost_cursor_missing"}) {
						t.Fatalf("diagnostic causes: %v", f.BudgetEvidence.Causes)
					}
					return
				}
				if len(rows) != 1 {
					t.Fatalf("committed alerts=%d", len(rows))
				}
				ev := interpretAlertEvidence(rows[0], tenant)
				if ev.State != evidenceValid {
					t.Fatalf("durable evidence invalid: %+v", ev)
				}
				b := f.BudgetEvidence
				if b.AlertID != rows[0].String(model.ColID) || b.AlertID != ev.Envelope.AlertID || f.DetailHash != ev.Digest || b.AmountClass != ev.Envelope.Amount.Class || b.Crossing != ev.Envelope.Decision.Result || b.SchemaVersion != uint32(ev.Envelope.SchemaVersion) || b.DigestVersion != uint32(ev.Envelope.DigestVersion) {
					t.Fatalf("summary disagrees with committed envelope: %+v", b)
				}
				wantClass := "exact"
				var wantCauses []string
				if tc == "bound" {
					wantClass = "lower_bound"
					wantCauses = []string{"dynamic_reservation_scan_incomplete"}
				}
				if b.AmountClass != wantClass || b.Crossing != "proven" || !reflect.DeepEqual(b.Causes, wantCauses) || f.Kind != "finops_budget_cap" {
					t.Fatalf("crossing changed: %+v", f)
				}
			}}
			err := m.onCost(context.Background(), tenant, mkCost("openai", "gpt-x", "transport", 1, 1, cost, baseTime), nil)
			if tc == "rollback" {
				if !errors.Is(err, boom) || checked != 0 || len(host.findings()) != 0 || len(alertRows(t, st, tenant)) != 0 || countCosts(t, st, tenant) != 0 {
					t.Fatalf("rollback leaked publication or rows: err=%v checked=%d", err, checked)
				}
			} else if err != nil || checked != 1 || len(host.findings()) != 1 {
				t.Fatalf("publication count=%d err=%v", checked, err)
			}
		})
	}
}

func TestBudgetEvidenceSDKAcceptsProducerVocabulary(t *testing.T) {
	// Cross-boundary conformance without importing core/modules into the SDK.
	var causes []string
	for c := range alertEvidenceCauseVocabulary {
		causes = append(causes, c)
	}
	sort.Strings(causes)
	f := sdkmodel.FindingReport{Kind: "finops_budget_evaluation_incomplete", SubjectKind: "budget", SubjectRef: model.NewID().String(), DetailHash: strings.Repeat("a", 64), BudgetEvidence: &sdkmodel.BudgetAlertEvidenceSummary{SchemaVersion: 1, DigestVersion: 1, AmountClass: "unknown", Crossing: "unproven", Causes: causes}}
	if got := f.BudgetEvidenceValidity(); got != "structurally_valid" {
		t.Fatalf("SDK refuses producer's bounded v1 vocabulary: %s %v", got, causes)
	}
}

func TestBudgetEvidenceCommittedReferenceDoesNotAuthorizeCollector(t *testing.T) {
	m, st, tenant, host := newFin(t)
	m.clock = &fakeClock{t: baseTime}
	createBudget(t, st, tenant, "authentic-reference", budgetSpec{
		Dimension: "global", Period: "monthly", LimitMicroUSD: 10 * oneUSD,
		Action: "block", Thresholds: []float64{1},
	})
	m.ingest(t, tenant, mkCost("openai", "gpt-x", "reference", 1, 1, 12*oneUSD, baseTime))
	findings, rows := host.findings(), alertRows(t, st, tenant)
	if len(findings) != 1 || len(rows) != 1 {
		t.Fatal("missing real committed alert/finding")
	}
	ev := interpretAlertEvidence(rows[0], tenant)
	f := findings[0]
	if ev.State != evidenceValid || f.DetailHash != ev.Digest || f.BudgetEvidence.AlertID != ev.Envelope.AlertID {
		t.Fatal("reference is not the authentic committed evidence")
	}
	r := runtime.New(runtime.Options{Logger: host.Logger()})
	if err := r.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := r.Stop(ctx); err != nil {
			t.Error(err)
		}
	}()
	// The receiving collector boundary sees the authentic tenant, ID and digest,
	// plus a spoofed FinOps Source. Record authenticity cannot grant emit authority.
	for _, obs := range []sdkmodel.Observation{f, &f} {
		if err := r.Ingest(context.Background(), tenant.String(), Name, obs); !errors.Is(err, runtime.ErrReservedBudgetEvidence) {
			t.Fatalf("authentic reference granted collector authority: %v", err)
		}
	}
}
