// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"errors"
	"slices"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// BudgetEvidenceCapTarget binds one admitted finding to its persisted alert and
// the captured BLOCK target. The composition root checks producer provenance;
// this read-only seam checks financial evidence, not the caller's authority.
// A present but unusable extension never invokes the legacy BudgetCapTarget.
// The two reads do not promise serializability or authority after this return.
func (m *Module) BudgetEvidenceCapTarget(ctx context.Context, tenant model.TenantID, f sdkmodel.FindingReport) (dimension, key string, ok bool, err error) {
	if m.data == nil || tenant.IsZero() || tenant.IsSystem() ||
		f.Kind != "finops_budget_cap" || f.Severity != sdkmodel.SeverityCritical ||
		f.BudgetEvidenceValidity() != "structurally_valid" {
		return "", "", false, nil
	}
	err = m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(budgetAlertKind)
		if err != nil {
			return err
		}
		row, err := repo.Get(ctx, model.ID(f.BudgetEvidence.AlertID))
		if err != nil {
			return err
		}
		evidence := interpretAlertEvidence(row, tenant)
		if evidence.State != evidenceValid || evidence.Envelope == nil || evidence.Digest != f.DetailHash {
			return nil
		}
		env := evidence.Envelope
		want, got := summaryForAlert(*env), f.BudgetEvidence
		if env.BudgetID != f.SubjectRef || env.Policy.Action != "block" ||
			env.Decision.LegacyPct < 100 || env.Decision.Result != string(crossingProven) ||
			want.SchemaVersion != got.SchemaVersion || want.DigestVersion != got.DigestVersion ||
			want.AlertID != got.AlertID || want.AmountClass != got.AmountClass ||
			want.Crossing != got.Crossing || !slices.Equal(want.Causes, got.Causes) {
			return nil
		}
		p, err := sc.Policies().Get(ctx, model.ID(env.BudgetID))
		if err != nil {
			return err
		}
		spec := parseBudgetSpec(p.Spec)
		spec.fillDefaults()
		_, limitState := readSpecInt64(p.Spec, "limit_micro_usd")
		if p.Kind != policyKindBudget || !p.Enabled || spec.validate() != "" ||
			limitState != specIntValue || staticFromSpec(p.Spec).Fault != configFaultNone ||
			spec.Dimension != env.Policy.Dimension || spec.Key != env.Policy.Key || spec.Action != env.Policy.Action {
			return nil
		}
		// Carry the captured target forward; no second mutable lookup may redirect it.
		dimension, key, ok = env.Policy.Dimension, env.Policy.Key, true
		return nil
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	return dimension, key, ok, nil
}
