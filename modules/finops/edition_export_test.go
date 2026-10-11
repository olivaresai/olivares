// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestStoredFinOpsCostEvidenceExportsAcrossEditions(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	m.ingest(t, tenant, mkCost("stored-provider", "stored-model", "", 2, 3, 20, m.clock.Now().Time()))
	if err := st.Export(context.Background(), tenant, func(sc store.ExportScope) error {
		repo, err := sc.Ext(costSampleKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(context.Background(), model.Query{})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			t.Fatalf("exported cost samples = %d, want 1", len(rows))
		}
		r := rows[0]
		if r.String(colProviderRef) != "stored-provider" || r.String(colModelRef) != "stored-model" || r.Int(colInputTokens) != 2 || r.Int(colOutputTokens) != 3 || r.Int(colCostMicroUSD) != 20 {
			t.Fatalf("stored FinOps evidence changed in export: %+v", r)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
