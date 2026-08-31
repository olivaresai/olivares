// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func TestGenerateStatements(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()

	// Set up two cost centers with team mappings.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ccRepo, err := sc.Ext(costCenterKind)
		if err != nil {
			return err
		}
		mapRepo, err := sc.Ext(costCenterMappingKind)
		if err != nil {
			return err
		}
		for _, cc := range []struct{ code, name, team string }{
			{"ENG-01", "Engineering", "eng"},
			{"SALES-01", "Sales", "sales"},
		} {
			rec, err := ccRepo.Create(ctx, model.Record{
				colCCCode: cc.code, colCCName: cc.name, colCCStatus: "active",
			})
			if err != nil {
				return err
			}
			_, err = mapRepo.Create(ctx, model.Record{
				colCCMappingCostCenterID: rec.String(model.ColID),
				colCCMappingDimension:    "team",
				colCCMappingKey:          cc.team,
				colCCMappingPriority:     int64(10),
			})
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Ingest samples.
	eng1 := mkCost("anthropic", "claude-opus-4-8", "", 100, 50, 500, baseTime)
	eng1.Labels = map[string]string{"team": "eng"}
	m.ingest(t, tenant, eng1)

	eng2 := mkCost("anthropic", "claude-sonnet-4-6", "", 200, 100, 300, baseTime.Add(time.Minute))
	eng2.Labels = map[string]string{"team": "eng"}
	m.ingest(t, tenant, eng2)

	sales1 := mkCost("google", "gemini-1.5-flash", "", 50, 25, 100, baseTime.Add(2*time.Minute))
	sales1.Labels = map[string]string{"team": "sales"}
	m.ingest(t, tenant, sales1)

	// Generate monthly statement for June 2026.
	pStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	pEnd := pStart.AddDate(0, 1, 0)
	now := baseTime

	var stmts []statementDTO
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		var e error
		stmts, e = generateStatements(ctx, sc, "monthly", pStart, pEnd, now)
		return e
	}); err != nil {
		t.Fatal(err)
	}

	if len(stmts) != 2 {
		t.Fatalf("generated %d statements, want 2", len(stmts))
	}

	// Find the engineering statement.
	var engStmt *statementDTO
	for i := range stmts {
		if stmts[i].CostCenterCode == "ENG-01" {
			engStmt = &stmts[i]
			break
		}
	}
	if engStmt == nil {
		t.Fatal("engineering statement not found")
	}
	if engStmt.TotalMicroUSD != 800 {
		t.Errorf("eng total = %d, want 800", engStmt.TotalMicroUSD)
	}
	if engStmt.LineCount != 2 {
		t.Errorf("eng lines = %d, want 2", engStmt.LineCount)
	}
	if engStmt.Status != "draft" {
		t.Errorf("status = %q, want draft", engStmt.Status)
	}
}

func TestGenerateStatementsIdempotent(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()

	// Create a cost center.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ccRepo, err := sc.Ext(costCenterKind)
		if err != nil {
			return err
		}
		mapRepo, err := sc.Ext(costCenterMappingKind)
		if err != nil {
			return err
		}
		rec, err := ccRepo.Create(ctx, model.Record{
			colCCCode: "ENG-01", colCCName: "Engineering", colCCStatus: "active",
		})
		if err != nil {
			return err
		}
		_, err = mapRepo.Create(ctx, model.Record{
			colCCMappingCostCenterID: rec.String(model.ColID),
			colCCMappingDimension:    "team",
			colCCMappingKey:          "eng",
			colCCMappingPriority:     int64(10),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	eng := mkCost("anthropic", "claude-opus-4-8", "", 100, 50, 500, baseTime)
	eng.Labels = map[string]string{"team": "eng"}
	m.ingest(t, tenant, eng)

	pStart := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	pEnd := pStart.AddDate(0, 1, 0)
	now := baseTime

	// Generate twice — second should be idempotent (skip existing).
	for i := 0; i < 2; i++ {
		if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			_, e := generateStatements(ctx, sc, "monthly", pStart, pEnd, now)
			return e
		}); err != nil {
			t.Fatalf("generate #%d: %v", i+1, err)
		}
	}

	// Should only have 1 statement.
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(chargebackStatementKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{Limit: listCap})
		if err != nil {
			return err
		}
		if len(recs) != 1 {
			t.Errorf("statements = %d, want 1 (idempotent)", len(recs))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestStatementKeyIsPerPeriod pins the two ways an internal adversarial panel broke chargeback
// statements on 2026-08-18, both of them one omission: `statement_key` was `<cc>|<start>` and
// `(tenant_id, statement_key)` is UNIQUE (schema.go:543).
//
//  1. COLISIÓN. A weekly and a monthly statement that start the same day —the 1st of a month that
//     falls on a Monday, about one month in seven— produced the same key. The second Create hit
//     ErrConflict and the loop `continue`d: no statement, no error, and the caller got a shorter
//     list than it had cost centers. Silence is the worst shape this can take.
//
//  2. DELTA CRUZADO. Even without a collision, a week starting on the 1st looked up
//     `<cc>|<1st of month>` for its prior and found the MONTHLY statement. Measured verbatim:
//     `weekly(2026-11-08): total=700 delta_pct=-7500 prior=2800` — 2800 was the month's total, so
//     the console showed a 75 % drop that never happened.
//
// The test drives both from one fixture because they share one cause; a fix that closed only the
// delta would leave the missing statement, and vice versa.
func TestStatementKeyIsPerPeriod(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()

	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ccRepo, err := sc.Ext(costCenterKind)
		if err != nil {
			return err
		}
		mapRepo, err := sc.Ext(costCenterMappingKind)
		if err != nil {
			return err
		}
		rec, err := ccRepo.Create(ctx, model.Record{
			colCCCode: "ENG-01", colCCName: "Engineering", colCCStatus: "active",
		})
		if err != nil {
			return err
		}
		_, err = mapRepo.Create(ctx, model.Record{
			colCCMappingCostCenterID: rec.String(model.ColID),
			colCCMappingDimension:    "team",
			colCCMappingKey:          "eng",
			colCCMappingPriority:     int64(10),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Gasto en dos semanas distintas de noviembre de 2026. El 1 de noviembre es domingo, así que se
	// usan arranques explícitos: `generateStatements` los recibe, no los deduce.
	gasta := func(at time.Time, coste int64) {
		c := mkCost("anthropic", "claude-opus-4-8", "", 100, 50, coste, at)
		c.Labels = map[string]string{"team": "eng"}
		m.ingest(t, tenant, c)
	}
	sem1 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	sem2 := sem1.AddDate(0, 0, 7)
	gasta(sem1.Add(24*time.Hour), 2100) // dentro de la semana 1 y del mes
	gasta(sem2.Add(24*time.Hour), 700)  // dentro de la semana 2 y del mes

	genera := func(periodo string, inicio, fin time.Time) {
		t.Helper()
		if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
			_, e := generateStatements(ctx, sc, periodo, inicio, fin, baseTime)
			return e
		}); err != nil {
			t.Fatalf("generate %s %s: %v", periodo, inicio.Format("2006-01-02"), err)
		}
	}
	// El mes ENTERO primero: es el orden que reproduce el hallazgo, porque deja el registro que la
	// semana encontraba por error.
	genera("monthly", sem1, sem1.AddDate(0, 1, 0))
	genera("weekly", sem1, sem2)
	genera("weekly", sem2, sem2.AddDate(0, 0, 7))

	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(chargebackStatementKind)
		if err != nil {
			return err
		}
		recs, _, err := repo.List(ctx, model.Query{Limit: listCap})
		if err != nil {
			return err
		}

		// (1) Los tres existen. Con la clave sin periodo, la semana que arranca el día 1 chocaba con
		//     el mes y desaparecía sin ruido.
		if len(recs) != 3 {
			for _, r := range recs {
				t.Logf("  %s %s total=%d prior=%d delta=%d",
					r.String(colStmtPeriod), r.String(colStmtPeriodStart),
					r.Int(colStmtTotalMicroUSD), r.Int(colStmtPriorTotal), r.Int(colStmtDeltaPct))
			}
			t.Fatalf("statements = %d, want 3 (monthly + 2 weekly) — "+
				"a weekly starting on a month start must not collide with the monthly", len(recs))
		}

		// (2) El delta de la SEGUNDA semana se calcula contra la PRIMERA semana (2100), no contra el
		//     mes (2800). Éste es el número que el panel midió mal.
		var vista bool
		for _, r := range recs {
			if r.String(colStmtPeriod) != "weekly" ||
				r.String(colStmtPeriodStart) != model.NewTimestamp(sem2).String() {
				continue
			}
			vista = true
			if got := r.Int(colStmtPriorTotal); got != 2100 {
				t.Errorf("prior of week 2 = %d, want 2100 (week 1) — %d would be the MONTH total", got, 2800)
			}
			// (700-2100)/2100 en centésimas de punto porcentual = -6666, no -7500.
			if got := r.Int(colStmtDeltaPct); got != -6666 {
				t.Errorf("delta of week 2 = %d, want -6666 (-66,66 %%); -7500 is the cross-period bug", got)
			}
		}
		if !vista {
			t.Fatalf("no weekly statement starting %s", sem2.Format("2006-01-02"))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEWAForecast(t *testing.T) {
	series := []daySpend{
		{Day: "2026-06-01", Cost: 100},
		{Day: "2026-06-02", Cost: 200},
		{Day: "2026-06-03", Cost: 300},
		{Day: "2026-06-04", Cost: 400},
		{Day: "2026-06-05", Cost: 500},
	}
	rate, variance := ewaForecast(series, 0.3)
	if rate <= 0 {
		t.Errorf("EWA rate = %f, want positive", rate)
	}
	if variance < 0 {
		t.Errorf("EWA variance = %f, want non-negative", variance)
	}
	// EWA should weigh recent values more: rate should be > simple mean (300).
	if rate <= 300 {
		t.Errorf("EWA rate = %f, should be > 300 (biased toward recent higher values)", rate)
	}
}

func TestBudgetExhaustion(t *testing.T) {
	ex := budgetExhaustion(100, 25, 5000, 10000, 0)
	if ex.DaysRemaining <= 0 {
		t.Errorf("days remaining = %d, want positive", ex.DaysRemaining)
	}
	if ex.DaysRemaining != 50 {
		t.Errorf("days remaining = %d, want 50 (5000 remaining / 100/day)", ex.DaysRemaining)
	}
	if ex.Confidence != "high" {
		t.Errorf("confidence = %q, want high (low CV)", ex.Confidence)
	}
}

func TestBudgetExhaustionAlreadyOver(t *testing.T) {
	ex := budgetExhaustion(100, 25, 10000, 10000, 0)
	if ex.DaysRemaining != 0 {
		t.Errorf("days remaining = %d, want 0 (already over)", ex.DaysRemaining)
	}
}
