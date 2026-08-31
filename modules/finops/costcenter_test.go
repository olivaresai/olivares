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

// TestCostCenterValidateDefaultsStatusForTheCaller pins the default the UPDATE path depends on.
//
// ⛔ THE DEFECT THIS WOULD HAVE CAUGHT. `validate` used a VALUE receiver, so its
// `d.Status = "active"` mutated a copy: validation passed and the CALLER kept the empty string,
// which is what reached the record. `PUT /cost-centers/{id}` is a full replace, so omitting
// `status` stored `""` — and attribution requires EXACTLY "active" (costcenter.go:460), so that
// cost center silently stopped attributing any spend.
//
// It is asserted on the CALLER's struct, not on validate's verdict: the verdict was already
// correct (the copy had a status by then). Only the caller can see the defect.
//
// And it stayed invisible because every other fixture in this file writes `colCCStatus: "active"`
// by hand — a fixture that supplies what the code needs cannot disagree with it.
func TestCostCenterValidateDefaultsStatusForTheCaller(t *testing.T) {
	in := costCenterDTO{Code: "ENG-01", Name: "Engineering"}
	if msg := in.validate(); msg != "" {
		t.Fatalf("validate rejected a valid DTO: %s", msg)
	}
	if in.Status != "active" {
		t.Fatalf("status is %q, want \"active\": validate defaulted a copy and the caller kept the empty value", in.Status)
	}
}

// And the direction that must NOT fire: an explicit status is never overwritten.
func TestCostCenterValidateKeepsAnExplicitStatus(t *testing.T) {
	in := costCenterDTO{Code: "ENG-01", Name: "Engineering", Status: "archived"}
	if msg := in.validate(); msg != "" {
		t.Fatalf("validate rejected an archived cost center: %s", msg)
	}
	if in.Status != "archived" {
		t.Fatalf("status is %q, want \"archived\": the default overwrote an explicit value", in.Status)
	}
}

func TestCostCenterCRUD(t *testing.T) {
	_, st, tenant, _ := newFin(t)
	ctx := context.Background()

	var ccID model.ID
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(costCenterKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(ctx, model.Record{
			colCCCode: "ENG-001", colCCName: "Engineering", colCCStatus: "active",
			colCCOwner: "cto@acme.com", colCCDescription: "Engineering dept",
		})
		if err != nil {
			return err
		}
		ccID = model.ID(rec.String(model.ColID))
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if ccID.IsZero() {
		t.Fatal("ccID is zero after create")
	}

	// Read back.
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(costCenterKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, ccID)
		if err != nil {
			return err
		}
		dto := toCostCenterDTO(rec)
		if dto.Code != "ENG-001" || dto.Name != "Engineering" || dto.Status != "active" {
			t.Errorf("read back = %+v", dto)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCostCenterMappingResolution(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()

	// Create a cost center and a mapping rule.
	var ccID string
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		ccRepo, err := sc.Ext(costCenterKind)
		if err != nil {
			return err
		}
		rec, err := ccRepo.Create(ctx, model.Record{
			colCCCode: "PLAT-01", colCCName: "Platform", colCCStatus: "active",
		})
		if err != nil {
			return err
		}
		ccID = rec.String(model.ColID)

		mapRepo, err := sc.Ext(costCenterMappingKind)
		if err != nil {
			return err
		}
		_, err = mapRepo.Create(ctx, model.Record{
			colCCMappingCostCenterID: ccID,
			colCCMappingDimension:    "team",
			colCCMappingKey:          "platform",
			colCCMappingPriority:     int64(10),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Ingest a sample with team=platform and verify CC is resolved.
	c := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 100, baseTime)
	c.Labels = map[string]string{"team": "platform"}
	m.ingest(t, tenant, c)

	rows := costSampleRows(t, st, tenant)
	if len(rows) == 0 {
		t.Fatal("no cost sample rows")
	}
	ccRef := rows[0].String(colCostCenterRef)
	if ccRef != "PLAT-01" {
		t.Errorf("cost_center_ref = %q, want PLAT-01", ccRef)
	}
}

func TestCostCenterUnmappedTraffic(t *testing.T) {
	m, st, tenant, _ := newFin(t)

	// No mappings exist — cost_center_ref should be empty.
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 100, baseTime))

	rows := costSampleRows(t, st, tenant)
	if len(rows) == 0 {
		t.Fatal("no cost sample rows")
	}
	if cc := rows[0].String(colCostCenterRef); cc != "" {
		t.Errorf("cost_center_ref = %q, want empty for unmapped traffic", cc)
	}
}

func TestCostCenterDimensionSpend(t *testing.T) {
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

	// Ingest samples for each team.
	eng := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 300, baseTime)
	eng.Labels = map[string]string{"team": "eng"}
	m.ingest(t, tenant, eng)

	sales := mkCost("anthropic", "claude-opus-4-8", "", 10, 5, 200, baseTime.Add(time.Minute))
	sales.Labels = map[string]string{"team": "sales"}
	m.ingest(t, tenant, sales)

	// Query spend by cost_center dimension.
	var out spendResponse
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var e error
		out, e = spendByDimension(ctx, sc, "cost_center", time.Time{}, false, time.Time{}, false)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if out.TotalMicroUSD != 500 {
		t.Errorf("total = %d, want 500", out.TotalMicroUSD)
	}
	if len(out.Buckets) != 2 {
		t.Fatalf("buckets = %d, want 2", len(out.Buckets))
	}
	if out.Buckets[0].Key != "ENG-01" || out.Buckets[0].CostMicroUSD != 300 {
		t.Errorf("first bucket = %+v, want ENG-01(300)", out.Buckets[0])
	}
}
