// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Alter only the result of the one real alert Get. The persisted row remains
// unchanged; this stages a damaged/unknown read without weakening append-only.
type evidenceTargetData struct {
	api.ModuleData
	change func(model.Record) model.Record
	err    error
	gets   int
}

func (d *evidenceTargetData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.ModuleData.View(ctx, tenant, func(sc store.Scope) error { return fn(evidenceTargetScope{Scope: sc, data: d}) })
}

type evidenceTargetScope struct {
	store.Scope
	data *evidenceTargetData
}

func (s evidenceTargetScope) Ext(kind model.Kind) (store.GenericRepo, error) {
	r, err := s.Scope.Ext(kind)
	if err != nil || kind != budgetAlertKind {
		return r, err
	}
	return evidenceTargetRepo{GenericRepo: r, data: s.data}, nil
}

type evidenceTargetRepo struct {
	store.GenericRepo
	data *evidenceTargetData
}

func (r evidenceTargetRepo) Get(ctx context.Context, id model.ID) (model.Record, error) {
	r.data.gets++
	if r.data.err != nil {
		return nil, r.data.err
	}
	row, err := r.GenericRepo.Get(ctx, id)
	if err == nil && r.data.change != nil {
		row = r.data.change(row)
	}
	return row, err
}

func TestBudgetEvidenceTargetDurableBinding(t *testing.T) {
	ctx := context.Background()
	for _, class := range []string{"exact", "lower_bound"} {
		t.Run(class, func(t *testing.T) {
			m, st, tenant, host := newFin(t)
			m.clock = &fakeClock{t: baseTime}
			budget := createBudget(t, st, tenant, "target", budgetSpec{Dimension: "api_key", Key: "key_original", Period: "monthly", LimitMicroUSD: 10 * oneUSD, ReservedMicroUSD: 12 * oneUSD, Action: "block", Thresholds: []float64{1}})
			if class == "lower_bound" {
				forcePager(m, func(int, model.Query) ([]model.Record, model.Page) { return nil, model.Page{HasMore: true} })
			}
			cost := mkCost("anthropic", "model", "cap", 1, 1, 0, baseTime)
			cost.APIKeyRef = "key_original"
			m.ingest(t, tenant, cost)
			fs := host.findings()
			if len(fs) != 1 || fs[0].BudgetEvidence.AmountClass != class {
				t.Fatalf("producer: %+v", fs)
			}
			original := fs[0]
			data := &evidenceTargetData{ModuleData: m.data}
			m.UseData(data)
			dim, key, ok, err := m.BudgetEvidenceCapTarget(ctx, tenant, original)
			if err != nil || !ok || dim != "api_key" || key != "key_original" || data.gets != 1 {
				t.Fatalf("target=%s/%s/%v/%v gets=%d", dim, key, ok, err, data.gets)
			}
			// Real policy update: a historical alert must never redirect to this key.
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				p, err := sc.Policies().Get(ctx, budget)
				if err != nil {
					return err
				}
				p.Spec["key"] = "key_redirected"
				_, err = sc.Policies().Update(ctx, p)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if _, _, ok, err := m.BudgetEvidenceCapTarget(ctx, tenant, original); ok || err != nil {
				t.Fatalf("retargeted historical cap: %v %v", ok, err)
			}
		})
	}
}

func TestBudgetEvidenceTargetRefusals(t *testing.T) {
	ctx := context.Background()
	m, st, tenant, host := newFin(t)
	m.clock = &fakeClock{t: baseTime}
	id := createBudget(t, st, tenant, "refusals", budgetSpec{Dimension: "api_key", Key: "key_original", Period: "monthly", LimitMicroUSD: 10 * oneUSD, ReservedMicroUSD: 12 * oneUSD, Action: "block", Thresholds: []float64{1}})
	cost := mkCost("anthropic", "model", "refusals", 1, 1, 0, baseTime)
	cost.APIKeyRef = "key_original"
	m.ingest(t, tenant, cost)
	fs := host.findings()
	if len(fs) != 1 {
		t.Fatalf("findings=%d", len(fs))
	}
	original := fs[0]
	data := &evidenceTargetData{ModuleData: m.data}
	m.UseData(data)
	for name, change := range map[string]func(*sdkmodel.FindingReport){
		"absent":          func(f *sdkmodel.FindingReport) { f.BudgetEvidence = nil },
		"present_invalid": func(f *sdkmodel.FindingReport) { f.BudgetEvidence = &sdkmodel.BudgetAlertEvidenceSummary{} },
		"future":          func(f *sdkmodel.FindingReport) { f.BudgetEvidence.SchemaVersion = 2 },
		"id":              func(f *sdkmodel.FindingReport) { f.BudgetEvidence.AlertID = model.NewID().String() },
		"subject":         func(f *sdkmodel.FindingReport) { f.SubjectRef = model.NewID().String() },
		"hash":            func(f *sdkmodel.FindingReport) { f.DetailHash = strings.Repeat("f", 64) },
		"summary_causes": func(f *sdkmodel.FindingReport) {
			f.BudgetEvidence.Causes = []string{"dynamic_reservation_scan_incomplete"}
		},
		"summary_class": func(f *sdkmodel.FindingReport) {
			f.BudgetEvidence.AmountClass = "lower_bound"
			f.BudgetEvidence.Causes = []string{"dynamic_reservation_scan_incomplete"}
		},
		"diagnostic": func(f *sdkmodel.FindingReport) {
			f.Kind = "finops_budget_evaluation_incomplete"
			f.Severity = sdkmodel.SeverityMedium
			f.BudgetEvidence.AlertID = ""
			f.BudgetEvidence.AmountClass = "unknown"
			f.BudgetEvidence.Crossing = "unproven"
			f.BudgetEvidence.Causes = []string{"cost_read_failed"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := original
			f.BudgetEvidence = f.BudgetEvidence.Clone()
			change(&f)
			if _, _, ok, err := m.BudgetEvidenceCapTarget(ctx, tenant, f); ok || err != nil {
				t.Fatalf("accepted: %v %v", ok, err)
			}
		})
	}
	other := model.TenantID(model.NewID())
	if err := st.System(ctx, func(sys store.SystemScope) error {
		o, err := sys.CreateOrg(ctx, model.Org{Name: "other", Slug: "other", Status: model.StatusActive})
		other = o.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Run("other_tenant", func(t *testing.T) {
		if _, _, ok, err := m.BudgetEvidenceCapTarget(ctx, other, original); ok || err != nil {
			t.Fatalf("crossed tenant: %v %v", ok, err)
		}
	})
	for name, change := range map[string]func(model.Record) model.Record{
		"legacy_record": func(r model.Record) model.Record {
			delete(r, colAlertEvidence)
			delete(r, colAlertEvidenceHash)
			return r
		},
		"row_mismatch": func(r model.Record) model.Record { r[colAlertSpend] = int64(1); return r },
		"captured_throttle": func(r model.Record) model.Record {
			return resealed(t, r, func(e *alertEvidenceEnvelope) { e.Policy.Action = "throttle" })
		},
		"captured_target": func(r model.Record) model.Record {
			r = resealed(t, r, func(e *alertEvidenceEnvelope) { e.Policy.Key = "key_elsewhere"; e.Context.ScopeValue = "key_elsewhere" })
			r[colDimKey] = "key_elsewhere"
			return r
		},
		"impossible_resealed": func(r model.Record) model.Record {
			return resealed(t, r, func(e *alertEvidenceEnvelope) { e.Amount.Class = "unknown" })
		},
	} {
		t.Run(name, func(t *testing.T) {
			data.change = change
			defer func() { data.change = nil }()
			f := original
			if name == "captured_throttle" || name == "captured_target" || name == "impossible_resealed" {
				r := change(alertRows(t, st, tenant)[0])
				f.DetailHash = r.String(colAlertEvidenceHash)
				if name != "impossible_resealed" && interpretAlertEvidence(r, tenant).State != evidenceValid {
					t.Fatal("snapshot fixture is not independently valid evidence")
				}
			}
			if _, _, ok, err := m.BudgetEvidenceCapTarget(ctx, tenant, f); ok || err != nil {
				t.Fatalf("accepted stored mismatch: %v %v", ok, err)
			}
		})
	}
	t.Run("lookup_error", func(t *testing.T) {
		boom := errors.New("alert read unavailable")
		data.err = boom
		defer func() { data.err = nil }()
		if _, _, ok, err := m.BudgetEvidenceCapTarget(ctx, tenant, original); ok || !errors.Is(err, boom) {
			t.Fatalf("error lost: %v %v", ok, err)
		}
	})
	t.Run("missing_data", func(t *testing.T) {
		if _, _, ok, err := New().BudgetEvidenceCapTarget(ctx, tenant, original); ok || err != nil {
			t.Fatalf("missing data: %v %v", ok, err)
		}
	})
	for name, change := range map[string]func(*model.Policy){
		"disabled":          func(p *model.Policy) { p.Enabled = false },
		"different_kind":    func(p *model.Policy) { p.Kind = "guardrail" },
		"action_changed":    func(p *model.Policy) { p.Spec["action"] = "alert" },
		"dimension_changed": func(p *model.Policy) { p.Spec["dimension"] = "workspace" },
		"limit_invalid":     func(p *model.Policy) { p.Spec["limit_micro_usd"] = nil },
	} {
		t.Run(name, func(t *testing.T) {
			var before model.Policy
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				p, err := sc.Policies().Get(ctx, id)
				if err != nil {
					return err
				}
				before = p
				before.Spec = parseBudgetSpec(p.Spec).toSpecMap()
				change(&p)
				_, err = sc.Policies().Update(ctx, p)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if _, _, ok, err := m.BudgetEvidenceCapTarget(ctx, tenant, original); ok || err != nil {
				t.Fatalf("ineligible: %v %v", ok, err)
			}
			if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
				p, err := sc.Policies().Get(ctx, id)
				if err != nil {
					return err
				}
				before.Version = p.Version
				_, err = sc.Policies().Update(ctx, before)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
