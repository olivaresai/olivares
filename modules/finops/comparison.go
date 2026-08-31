// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ---------------------------------------------------------------------------
// DTOs
// ---------------------------------------------------------------------------

// comparisonRequest captures the parsed query parameters for a model cost
// comparison. SourceModel and TargetModels are required; Dimension/DimKey
// optionally narrow the cost_sample scan to a single attribution slice.
type comparisonRequest struct {
	SourceModel  string
	TargetModels []string
	Dimension    string
	DimKey       string
}

// modelCostDTO is one model's retrospective cost line: the actual spend from
// cost_samples and the hypothetical spend from the rate catalog, with the delta
// and savings percentage.
type modelCostDTO struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	InputTokens     int64  `json:"input_tokens"`
	OutputTokens    int64  `json:"output_tokens"`
	CacheReadTokens int64  `json:"cache_read_tokens"`
	ActualMicroUSD  int64  `json:"actual_micro_usd"`
	RateMicroUSD    int64  `json:"rate_micro_usd"`
	DeltaMicroUSD   int64  `json:"delta_micro_usd"`
	SavingsPct      int    `json:"savings_pct"`
}

// comparisonResponse is the retrospective model-vs-model cost comparison: the
// source model's actual spend and the hypothetical spend for each target model
// using the same token volumes valued at each target's rate catalog prices.
type comparisonResponse struct {
	Source       modelCostDTO   `json:"source"`
	Targets      []modelCostDTO `json:"targets"`
	Since        string         `json:"since,omitempty"`
	Until        string         `json:"until,omitempty"`
	TotalSamples int            `json:"total_samples"`
}

// projectionDTO is one target model's prospective cost projection: the EWA-
// forecasted spend scaled by the rate differential vs the source model, with a
// 95% confidence band.
type projectionDTO struct {
	Model                  string `json:"model"`
	Provider               string `json:"provider"`
	ProjectedMicroUSD      int64  `json:"projected_micro_usd"`
	ConfidenceLowMicroUSD  int64  `json:"confidence_low_micro_usd"`
	ConfidenceHighMicroUSD int64  `json:"confidence_high_micro_usd"`
	DeltaVsSourceMicroUSD  int64  `json:"delta_vs_source_micro_usd"`
	SavingsPct             int    `json:"savings_pct"`
}

// comparisonWithProjectionResponse combines the backward-looking retrospective
// comparison with forward-looking per-target projections. When forecast_period
// is absent the Projections slice is nil and ForecastPeriod is empty.
type comparisonWithProjectionResponse struct {
	Retrospective  comparisonResponse `json:"retrospective"`
	Projections    []projectionDTO    `json:"projections,omitempty"`
	ForecastPeriod string             `json:"forecast_period,omitempty"`
}

// ---------------------------------------------------------------------------
// Core logic — retrospective
// ---------------------------------------------------------------------------

// retrospectiveComparison aggregates the source model's cost_sample rows over
// [since, until], then hypothetically re-prices the same token volumes at each
// target model's rate catalog entries. The source's actual_micro_usd comes from
// the ledger; its rate_micro_usd comes from the catalog (so delta shows catalog
// drift). Each target uses the source's token volumes valued at that target's
// catalog rates.
func retrospectiveComparison(
	ctx context.Context,
	sc store.Scope,
	sourceModel string,
	targetModels []string,
	since, until time.Time,
	hasSince, hasUntil bool,
	extraFilters []model.Filter,
) (comparisonResponse, error) {
	// Scope the scan to the source model.
	filters := append([]model.Filter{eq(colModelRef, sourceModel)}, extraFilters...)
	wf := windowFilters(filters, since, hasSince, until, hasUntil)

	var (
		totalCost      int64
		totalInput     int64
		totalOutput    int64
		totalCacheRead int64
		totalCacheC1h  int64
		totalCacheC5m  int64
		sourceProvider string
		sampleCount    int
	)
	_, err := scanSamples(ctx, sc, wf, func(r model.Record) {
		totalCost += r.Int(colCostMicroUSD)
		totalInput += r.Int(colInputTokens)
		totalOutput += r.Int(colOutputTokens)
		totalCacheRead += r.Int(colCacheReadTokens)
		totalCacheC1h += r.Int(colCacheCreation1hTokens)
		totalCacheC5m += r.Int(colCacheCreation5mTokens)
		if sourceProvider == "" {
			sourceProvider = r.String(colProviderRef)
		}
		sampleCount++
	})
	if err != nil {
		return comparisonResponse{}, err
	}

	// Rate catalog lookup for the source model. Use the midpoint of the window
	// (or now if the window is open) as the "at" instant.
	rateMid := rateMidpoint(since, hasSince, until, hasUntil)
	srcRate, srcRateOK, _ := resolveRate(ctx, sc, sourceProvider, sourceModel, rateMid)

	srcRateCost := int64(0)
	if srcRateOK {
		srcRateCost = calculateRateCost(totalInput, totalOutput, totalCacheRead,
			totalCacheC1h, totalCacheC5m, srcRate)
	}

	source := modelCostDTO{
		Provider:        sourceProvider,
		Model:           sourceModel,
		InputTokens:     totalInput,
		OutputTokens:    totalOutput,
		CacheReadTokens: totalCacheRead,
		ActualMicroUSD:  totalCost,
		RateMicroUSD:    srcRateCost,
		DeltaMicroUSD:   totalCost - srcRateCost,
		SavingsPct:      savingsPct(totalCost, srcRateCost),
	}

	// Build target hypotheticals.
	targets := make([]modelCostDTO, 0, len(targetModels))
	for _, tgt := range targetModels {
		tgtRate, tgtOK, _ := resolveRate(ctx, sc, "", tgt, rateMid)
		rateCost := int64(0)
		provider := ""
		if tgtOK {
			rateCost = calculateRateCost(totalInput, totalOutput, totalCacheRead,
				totalCacheC1h, totalCacheC5m, tgtRate)
			provider = tgtRate.Provider
		}
		targets = append(targets, modelCostDTO{
			Provider:        provider,
			Model:           tgt,
			InputTokens:     totalInput,
			OutputTokens:    totalOutput,
			CacheReadTokens: totalCacheRead,
			ActualMicroUSD:  rateCost,
			RateMicroUSD:    rateCost,
			DeltaMicroUSD:   totalCost - rateCost,
			SavingsPct:      savingsPct(totalCost, rateCost),
		})
	}

	// Sort targets by savings descending (biggest saving first).
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].DeltaMicroUSD > targets[j].DeltaMicroUSD
	})

	out := comparisonResponse{
		Source:       source,
		Targets:      targets,
		TotalSamples: sampleCount,
	}
	if hasSince {
		out.Since = since.UTC().Format(time.RFC3339)
	}
	if hasUntil {
		out.Until = until.UTC().Format(time.RFC3339)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Core logic — prospective projection
// ---------------------------------------------------------------------------

// prospectiveProjection forecasts the source model's remaining-period spend
// using an EWA run rate, then scales each target model's projection by the rate
// differential (target rate / source rate). Confidence bands propagate the EWA
// variance scaled by the rate ratio and remaining days.
func prospectiveProjection(
	ctx context.Context,
	sc store.Scope,
	sourceModel string,
	targetModels []string,
	period string,
	now time.Time,
	windowDays int,
	extraFilters []model.Filter,
) ([]projectionDTO, error) {
	if windowDays <= 0 {
		windowDays = defaultForecastWindowDays
	}

	// Build the daily spend series for the source model over the trailing window.
	winStart := now.UTC().AddDate(0, 0, -windowDays)
	srcFilters := append([]model.Filter{eq(colModelRef, sourceModel)}, extraFilters...)
	srcSeries, _, err := dailySeriesFiltered(ctx, sc, winStart, now, srcFilters)
	if err != nil {
		return nil, err
	}

	// EWA forecast for the source model.
	const alpha = 0.3
	srcRunRate, srcVariance := ewaForecast(srcSeries, alpha)

	// Remaining days in the period.
	pStart, hasLower := periodStart(period, now)
	remaining := 0.0
	if hasLower && period != "total" {
		pEnd := periodEnd(period, pStart)
		remaining = pEnd.Sub(now).Hours() / 24
		if remaining < 0 {
			remaining = 0
		}
	}

	// Source projected spend for the remaining period.
	srcProjected := srcRunRate * remaining

	// Resolve the source model's rate for the ratio calculation.
	srcCatalog, srcCatalogOK, _ := resolveRate(ctx, sc, "", sourceModel, now)

	projections := make([]projectionDTO, 0, len(targetModels))
	for _, tgt := range targetModels {
		tgtCatalog, tgtOK, _ := resolveRate(ctx, sc, "", tgt, now)

		// Rate ratio: how much cheaper/dearer the target is vs the source. When
		// either rate is unresolvable the ratio defaults to 1.0 (no scaling — the
		// projection mirrors the source, honestly flagged by provider="").
		ratio := 1.0
		if srcCatalogOK && tgtOK {
			srcBlended := float64(srcCatalog.InputRateMicroUSD + srcCatalog.OutputRateMicroUSD)
			tgtBlended := float64(tgtCatalog.InputRateMicroUSD + tgtCatalog.OutputRateMicroUSD)
			if srcBlended > 0 {
				ratio = tgtBlended / srcBlended
			}
		}

		projected := srcProjected * ratio
		// Variance scales by ratio^2 (variance of c*X = c^2*Var(X)).
		scaledVariance := srcVariance * ratio * ratio
		band := 1.96 * math.Sqrt(scaledVariance*remaining)

		tgtProjected := clampNonNeg(projected)
		srcProjectedClamped := clampNonNeg(srcProjected)

		provider := ""
		if tgtOK {
			provider = tgtCatalog.Provider
		}
		projections = append(projections, projectionDTO{
			Model:                  tgt,
			Provider:               provider,
			ProjectedMicroUSD:      tgtProjected,
			ConfidenceLowMicroUSD:  clampNonNeg(projected - band),
			ConfidenceHighMicroUSD: clampNonNeg(projected + band),
			DeltaVsSourceMicroUSD:  srcProjectedClamped - tgtProjected,
			SavingsPct:             savingsPct(srcProjectedClamped, tgtProjected),
		})
	}

	// Sort by savings descending.
	sort.Slice(projections, func(i, j int) bool {
		return projections[i].DeltaVsSourceMicroUSD > projections[j].DeltaVsSourceMicroUSD
	})
	return projections, nil
}

// ---------------------------------------------------------------------------
// HTTP handler
// ---------------------------------------------------------------------------

// handleComparison serves GET /comparison — the model cost comparison endpoint.
// Query params:
//
//	source_model   (required) the model whose actual spend is the baseline
//	target_models  (required) comma-separated list of models to compare against
//	since, until   optional RFC3339 time window bounds
//	dimension      optional attribution dimension filter (e.g. "team")
//	dim_key        optional dimension value (e.g. "platform-eng")
//	forecast_period optional: "monthly", "weekly", "daily" — adds projections
//	window_days    optional trailing window for the EWA forecast (default 7)
func (m *Module) handleComparison(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	srcModel := r.URL.Query().Get("source_model")
	if srcModel == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("source_model is required"))
		return
	}
	tgtRaw := r.URL.Query().Get("target_models")
	if tgtRaw == "" {
		writeJSON(w, http.StatusBadRequest, errorBody("target_models is required"))
		return
	}
	targetModels := splitModels(tgtRaw)
	if len(targetModels) == 0 {
		writeJSON(w, http.StatusBadRequest, errorBody("target_models must contain at least one model"))
		return
	}

	since, hasSince, until, hasUntil, bad := timeWindow(r)
	if bad {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid since/until: expected RFC3339"))
		return
	}

	// Optional dimension filter.
	var extraFilters []model.Filter
	if dim := r.URL.Query().Get("dimension"); dim != "" {
		if !validDimensions[dim] {
			writeJSON(w, http.StatusBadRequest, errorBody("invalid dimension"))
			return
		}
		col := dimensionColumn(dim)
		if col != "" {
			if key := r.URL.Query().Get("dim_key"); key != "" {
				extraFilters = append(extraFilters, eq(col, key))
			}
		}
	}

	forecastPeriod := r.URL.Query().Get("forecast_period")
	windowDays := 0
	if wd := r.URL.Query().Get("window_days"); wd != "" {
		if n, err := strconv.Atoi(wd); err == nil && n > 0 {
			windowDays = n
		}
	}

	// Validate forecast_period if present.
	if forecastPeriod != "" && !validForecastPeriods[forecastPeriod] {
		writeJSON(w, http.StatusBadRequest, errorBody("forecast_period must be daily, weekly, or monthly"))
		return
	}

	now := m.clock.Now().Time()

	if forecastPeriod != "" {
		// Full comparison: retrospective + projections.
		var out comparisonWithProjectionResponse
		err := mc.Data.View(r.Context(), func(sc store.Scope) error {
			retro, e := retrospectiveComparison(r.Context(), sc, srcModel, targetModels,
				since, until, hasSince, hasUntil, extraFilters)
			if e != nil {
				return e
			}
			out.Retrospective = retro
			proj, e := prospectiveProjection(r.Context(), sc, srcModel, targetModels,
				forecastPeriod, now, windowDays, extraFilters)
			if e != nil {
				return e
			}
			out.Projections = proj
			out.ForecastPeriod = forecastPeriod
			return nil
		})
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}

	// Retrospective only.
	var out comparisonResponse
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		var e error
		out, e = retrospectiveComparison(r.Context(), sc, srcModel, targetModels,
			since, until, hasSince, hasUntil, extraFilters)
		return e
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// validForecastPeriods are the periods the comparison forecast supports. "total"
// is excluded because it has no bounded horizon to project over.
var validForecastPeriods = map[string]bool{
	"daily": true, "weekly": true, "monthly": true,
}

// splitModels parses a comma-separated list of model refs, trimming whitespace
// and dropping empty entries.
func splitModels(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// calculateRateCost computes the hypothetical cost of a token volume at the
// given rate catalog entry's prices. All arithmetic is integer micro-USD with
// per-million token scaling (the catalog stores micro-USD per million tokens).
func calculateRateCost(input, output, cacheRead, cacheC1h, cacheC5m int64, rate modelRateDTO) int64 {
	// Uncached input = total input minus cache tiers (clamped).
	uncached := input - cacheRead - cacheC1h - cacheC5m
	if uncached < 0 {
		uncached = 0
	}
	cost := (uncached * rate.InputRateMicroUSD) / 1_000_000
	cost += (output * rate.OutputRateMicroUSD) / 1_000_000
	cost += (cacheRead * rate.CacheReadRateMicroUSD) / 1_000_000
	// Cache creation tokens are billed at the creation rate (both 1h and 5m
	// tenors use the same catalog rate — the creation event itself is what
	// costs, not the TTL).
	cost += ((cacheC1h + cacheC5m) * rate.CacheCreationRateMicroUSD) / 1_000_000
	return cost
}

// savingsPct returns (actual - hypothetical) / actual * 100 when actual > 0,
// clamped to [-999, 999] to avoid absurd percentages from tiny denominators.
// A positive result means the hypothetical is cheaper (saving); negative means
// the hypothetical is more expensive.
func savingsPct(actual, hypothetical int64) int {
	if actual <= 0 {
		return 0
	}
	pct := int(float64(actual-hypothetical) / float64(actual) * 100)
	if pct > 999 {
		return 999
	}
	if pct < -999 {
		return -999
	}
	return pct
}

// rateMidpoint returns the midpoint of the [since, until] window, or the
// current time if the window is unbounded. The midpoint is the "at" instant
// passed to resolveRate so the catalog entry effective at the window's center
// is used for repricing.
func rateMidpoint(since time.Time, hasSince bool, until time.Time, hasUntil bool) time.Time {
	if hasSince && hasUntil {
		return since.Add(until.Sub(since) / 2)
	}
	if hasSince {
		return since
	}
	if hasUntil {
		return until
	}
	return time.Now().UTC()
}

// dailySeriesFiltered is defined in analytics.go — used here for model-scoped
// daily series in prospective projection.
