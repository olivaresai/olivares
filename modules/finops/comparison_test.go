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

func TestRetrospectiveComparison(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()

	// Set up rate catalog for two models.
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(modelRateKind)
		if err != nil {
			return err
		}
		ef := model.NewTimestamp(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)).String()
		// Opus: 15 USD/MTok in, 75 USD/MTok out → 15_000_000 µUSD/MTok in, 75_000_000 µUSD/MTok out
		_, err = repo.Create(ctx, model.Record{
			colRateProvider: "anthropic", colRateModel: "claude-opus-4-8",
			colRateInputMicroUSD: int64(15_000_000), colRateOutputMicroUSD: int64(75_000_000),
			colRateCacheReadMicroUSD: int64(1_500_000), colRateCacheCreationMicroUSD: int64(18_750_000),
			colRateEffectiveFrom: ef,
		})
		if err != nil {
			return err
		}
		// Sonnet: 3 USD/MTok in, 15 USD/MTok out
		_, err = repo.Create(ctx, model.Record{
			colRateProvider: "anthropic", colRateModel: "claude-sonnet-4-6",
			colRateInputMicroUSD: int64(3_000_000), colRateOutputMicroUSD: int64(15_000_000),
			colRateCacheReadMicroUSD: int64(300_000), colRateCacheCreationMicroUSD: int64(3_750_000),
			colRateEffectiveFrom: ef,
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// Ingest samples for opus.
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 1000, 500, 90_000, baseTime))
	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 2000, 1000, 180_000, baseTime.Add(time.Minute)))

	// Retrospective comparison: opus vs sonnet.
	var out comparisonResponse
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var e error
		out, e = retrospectiveComparison(ctx, sc, "claude-opus-4-8", []string{"claude-sonnet-4-6"}, time.Time{}, time.Time{}, false, false, nil)
		return e
	}); err != nil {
		t.Fatal(err)
	}

	if out.Source.Model != "claude-opus-4-8" {
		t.Errorf("source model = %q", out.Source.Model)
	}
	if out.Source.InputTokens != 3000 || out.Source.OutputTokens != 1500 {
		t.Errorf("source tokens = %d/%d", out.Source.InputTokens, out.Source.OutputTokens)
	}
	if out.TotalSamples != 2 {
		t.Errorf("total samples = %d, want 2", out.TotalSamples)
	}
	if len(out.Targets) != 1 {
		t.Fatalf("targets = %d, want 1", len(out.Targets))
	}
	// Sonnet should be cheaper: 3000*3 + 1500*15 = 9000+22500 = 31500 µUSD vs opus actual 270000
	if out.Targets[0].RateMicroUSD >= out.Source.ActualMicroUSD {
		t.Errorf("sonnet (%d) should be cheaper than opus (%d)", out.Targets[0].RateMicroUSD, out.Source.ActualMicroUSD)
	}
}

func TestRetrospectiveComparisonNoRate(t *testing.T) {
	m, st, tenant, _ := newFin(t)
	ctx := context.Background()

	m.ingest(t, tenant, mkCost("anthropic", "claude-opus-4-8", "", 1000, 500, 90_000, baseTime))

	// No rate catalog entries — comparison should still work with zero rates.
	var out comparisonResponse
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		var e error
		out, e = retrospectiveComparison(ctx, sc, "claude-opus-4-8", []string{"claude-sonnet-4-6"}, time.Time{}, time.Time{}, false, false, nil)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if out.Source.ActualMicroUSD != 90_000 {
		t.Errorf("source actual = %d, want 90000", out.Source.ActualMicroUSD)
	}
}
