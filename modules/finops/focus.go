// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// FOCUS export (FIN-06). FOCUS is the FinOps Foundation's open cost-and-usage
// schema (v1.3, ratified Dec 2025). It defines NO GenAI/token-specific columns, so
// this export REUSES the existing standard columns — tokens map to
// ConsumedQuantity/ConsumedUnit, cost to BilledCost/EffectiveCost/ListCost, the
// estimated-vs-billed provenance to which cost column is populated. Provider-specific
// detail (service tier, residency, cache split) rides in x_-prefixed columns, which
// FOCUS explicitly permits — we do NOT fabricate standard GenAI columns that the
// spec does not define.
//
// focusColumns is the fixed header, a curated subset of FOCUS v1.3 standard columns
// plus x_ extensions. ChargePeriod bounds the day; the four cost columns let a
// consumer pick billed vs list; ServiceProviderName/HostProviderName are the v1.3
// participating-entity columns (which replaced the deprecated ProviderName).
var focusColumns = []string{
	"ChargePeriodStart", "ChargePeriodEnd",
	"BillingCurrency", "BilledCost", "EffectiveCost", "ListCost", "ContractedCost",
	"ServiceName", "ServiceCategory", "ChargeCategory", "ChargeDescription",
	"ServiceProviderName", "HostProviderName",
	"ResourceId", "ResourceName", "SkuId",
	"ConsumedQuantity", "ConsumedUnit", "PricingQuantity", "PricingUnit",
	"x_Provenance", "x_Actor", "x_ServiceTier", "x_InferenceGeo", "x_ContextWindow", "x_Gateway",
	"x_CostType", "x_CacheReadTokens", "x_CacheCreation1hTokens", "x_CacheCreation5mTokens",
}

// handleExport streams the cost read-model as a FOCUS-conformant CSV. The default is
// the estimated (granular, full-coverage incl. Priority) stream so a naive sum never
// double-counts; ?provenance=billed exports the cost_report stream, ?provenance=all
// exports both (each row tagged x_Provenance — the consumer filters). The window is
// the standard since/until.
func (m *Module) handleExport(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if f := r.URL.Query().Get("format"); f != "" && f != "focus" {
		writeJSON(w, http.StatusBadRequest, errorBody("unsupported format: only focus"))
		return
	}
	since, hasSince, until, hasUntil, bad := timeWindow(r)
	if bad {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid since/until: expected RFC3339"))
		return
	}
	var filters []model.Filter
	allMode := false
	switch r.URL.Query().Get("provenance") {
	case "", provenanceEstimated:
		filters = windowFilters(nil, since, hasSince, until, hasUntil) // excludes billed
	case provenanceBilled:
		filters = windowBilled(since, hasSince, until, hasUntil)
	case "all":
		filters = onlyWindow(since, hasSince, until, hasUntil)
		allMode = true
	default:
		writeJSON(w, http.StatusBadRequest, errorBody("provenance must be estimated, billed or all"))
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"olivares-focus.csv\"")
	cw := csv.NewWriter(w)
	_ = cw.Write(focusColumns)
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		_, e := scanSamples(r.Context(), sc, filters, func(rec model.Record) {
			_ = cw.Write(focusRow(rec, allMode))
		})
		return e
	})
	cw.Flush()
	if err != nil {
		// Headers/rows may already be partly written; a trailing comment is the only
		// honest in-band signal left (never silently truncate to look complete).
		_ = cw.Write([]string{"# export error: " + err.Error()})
		cw.Flush()
	}
}

// onlyWindow is the occurred_at window with NO provenance filter (the "all" export).
func onlyWindow(since time.Time, hasSince bool, until time.Time, hasUntil bool) []model.Filter {
	var f []model.Filter
	if hasSince {
		f = append(f, model.Filter{Column: colOccurredAt, Op: model.OpGte, Value: model.NewTimestamp(since).String()})
	}
	if hasUntil {
		f = append(f, model.Filter{Column: colOccurredAt, Op: model.OpLte, Value: model.NewTimestamp(until).String()})
	}
	return f
}

// focusRow maps one cost_sample read-model row to a FOCUS CSV record. Cost is stored
// as integer micro-USD; FOCUS amounts are decimal currency units, so each is divided
// by 1e6 and rendered with 6 decimals (lossless for µUSD). Billed rows populate
// BilledCost; estimated rows populate ListCost (list-derived).
//
// EffectiveCost is FOCUS's canonical SUM column, so it must not double-count. In a
// single-provenance export (the default estimated, or billed-only) every row's cost
// is its EffectiveCost. In the mixed "all" export, billed and estimated are two views
// of the SAME spend, so only the BILLED row contributes EffectiveCost (its truth);
// the estimated row leaves EffectiveCost empty (its figure is still in ListCost),
// keeping SUM(EffectiveCost) equal to the billed total, not double.
func focusRow(rec model.Record, allMode bool) []string {
	prov := rec.String(colProvenance)
	if prov == "" {
		prov = provenanceEstimated
	}
	cost := microUSDToDecimal(rec.Int(colCostMicroUSD))
	billed, list, effective := "", "", cost
	if prov == provenanceBilled {
		billed = cost
	} else {
		list = cost
		if allMode {
			effective = "" // estimated does not contribute EffectiveCost in the mixed export
		}
	}
	day := rec.String(colOccurredAt)
	if len(day) > 10 {
		day = day[:10]
	}
	tokens := rec.Int(colInputTokens) + rec.Int(colOutputTokens)
	tokStr := strconv.FormatInt(tokens, 10)
	desc := rec.String(colModelRef)
	if ct := rec.String(colCostType); ct != "" {
		desc = desc + " " + ct
	}
	return []string{
		day, day, // ChargePeriodStart/End (daily granularity)
		"USD", billed, effective, list, "", // currency, BilledCost, EffectiveCost, ListCost, ContractedCost
		"AI Model Inference", "AI and Machine Learning", "Usage", desc,
		rec.String(colProviderRef), hostProvider(rec.String(colGateway), rec.String(colProviderRef)),
		rec.String(colWorkspaceRef), rec.String(colWorkspaceRef), rec.String(colModelRef),
		tokStr, "Tokens", tokStr, "Tokens",
		prov, rec.String(colActor), rec.String(colServiceTier), rec.String(colInferenceGeo), rec.String(colContextWindow), rec.String(colGateway),
		rec.String(colCostType),
		strconv.FormatInt(rec.Int(colCacheReadTokens), 10),
		strconv.FormatInt(rec.Int(colCacheCreation1hTokens), 10),
		strconv.FormatInt(rec.Int(colCacheCreation5mTokens), 10),
	}
}

// hostProvider maps the deployment gateway to the FOCUS HostProviderName (the entity
// the resource runs on), falling back to the model provider for the direct surface.
func hostProvider(gateway, providerRef string) string {
	switch gateway {
	case "bedrock-mantle", "bedrock-legacy", "claude-platform-aws":
		return "Amazon Web Services"
	case "vertex":
		return "Google Cloud"
	case "foundry":
		return "Microsoft Azure"
	default:
		return providerRef
	}
}

// microUSDToDecimal renders integer micro-USD as a decimal USD string with 6 dp
// (e.g. 1_234_500 → "1.234500"), lossless and float-free.
func microUSDToDecimal(micro int64) string {
	neg := micro < 0
	if neg {
		micro = -micro
	}
	whole := micro / 1_000_000
	frac := micro % 1_000_000
	s := fmt.Sprintf("%d.%06d", whole, frac)
	if neg {
		return "-" + s
	}
	return s
}
