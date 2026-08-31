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

func TestModelRateCRUD(t *testing.T) {
	_, st, tenant, _ := newFin(t)
	ctx := context.Background()

	var rateID model.ID
	effectiveFrom := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(modelRateKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(ctx, model.Record{
			colRateProvider:              "anthropic",
			colRateModel:                 "claude-opus-4-8",
			colRateInputMicroUSD:         int64(15_000_000),
			colRateOutputMicroUSD:        int64(75_000_000),
			colRateCacheReadMicroUSD:     int64(1_500_000),
			colRateCacheCreationMicroUSD: int64(18_750_000),
			colRateEffectiveFrom:         model.NewTimestamp(effectiveFrom).String(),
		})
		if err != nil {
			return err
		}
		rateID = model.ID(rec.String(model.ColID))
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if rateID.IsZero() {
		t.Fatal("rateID is zero after create")
	}

	// Read back.
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(modelRateKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, rateID)
		if err != nil {
			return err
		}
		dto := toModelRateDTO(rec)
		if dto.Provider != "anthropic" || dto.Model != "claude-opus-4-8" {
			t.Errorf("read back = %+v", dto)
		}
		if dto.InputRateMicroUSD != 15_000_000 || dto.OutputRateMicroUSD != 75_000_000 {
			t.Errorf("rates = in %d out %d", dto.InputRateMicroUSD, dto.OutputRateMicroUSD)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestResolveRate(t *testing.T) {
	_, st, tenant, _ := newFin(t)
	ctx := context.Background()

	ef1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ef2 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(modelRateKind)
		if err != nil {
			return err
		}
		// Rate 1: Jan 2026 → Jun 2026
		_, err = repo.Create(ctx, model.Record{
			colRateProvider:              "anthropic",
			colRateModel:                 "claude-opus-4-8",
			colRateInputMicroUSD:         int64(15_000_000),
			colRateOutputMicroUSD:        int64(75_000_000),
			colRateCacheReadMicroUSD:     int64(1_500_000),
			colRateCacheCreationMicroUSD: int64(18_750_000),
			colRateEffectiveFrom:         model.NewTimestamp(ef1).String(),
			colRateEffectiveUntil:        model.NewTimestamp(ef2).String(),
		})
		if err != nil {
			return err
		}
		// Rate 2: Jun 2026 → open
		_, err = repo.Create(ctx, model.Record{
			colRateProvider:              "anthropic",
			colRateModel:                 "claude-opus-4-8",
			colRateInputMicroUSD:         int64(12_000_000),
			colRateOutputMicroUSD:        int64(60_000_000),
			colRateCacheReadMicroUSD:     int64(1_200_000),
			colRateCacheCreationMicroUSD: int64(15_000_000),
			colRateEffectiveFrom:         model.NewTimestamp(ef2).String(),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Resolve rate at March 2026 — should get the Jan rate.
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		rate, found, err := resolveRate(ctx, sc, "anthropic", "claude-opus-4-8", time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC))
		if err != nil {
			return err
		}
		if !found {
			t.Error("rate not found for March 2026")
			return nil
		}
		if rate.InputRateMicroUSD != 15_000_000 {
			t.Errorf("rate.InputRate = %d, want 15_000_000", rate.InputRateMicroUSD)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Resolve rate at July 2026 — should get the Jun rate (12M).
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		rate, found, err := resolveRate(ctx, sc, "anthropic", "claude-opus-4-8", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
		if err != nil {
			return err
		}
		if !found {
			t.Error("rate not found for July 2026")
			return nil
		}
		if rate.InputRateMicroUSD != 12_000_000 {
			t.Errorf("rate.InputRate = %d, want 12_000_000", rate.InputRateMicroUSD)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Resolve rate for unknown model — should not find.
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		_, found, err := resolveRate(ctx, sc, "anthropic", "unknown-model", time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC))
		if err != nil {
			return err
		}
		if found {
			t.Error("should not find rate for unknown model")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
