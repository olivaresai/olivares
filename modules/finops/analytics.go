// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// aggResult is a spend aggregate over a set of read-model rows.
type aggResult struct {
	Cost      int64
	Input     int64
	Output    int64
	Count     int
	Truncated bool
}

// dimensionColumn maps a budget/analytics dimension to its read-model column
// (empty for "global", which has no column — it is the whole set).
func dimensionColumn(dim string) string {
	switch dim {
	case "provider":
		return colProviderRef
	case "model":
		return colModelRef
	case "agent":
		return colAgentRef
	case "session":
		return colSessionRef
	case "team":
		return colTeam
	case "project":
		return colProject
	case "workspace":
		return colWorkspaceRef
	case "api_key":
		return colAPIKeyRef
	case "actor":
		return colActor
	case "service_tier":
		return colServiceTier
	case "context_window":
		return colContextWindow
	case "inference_geo":
		return colInferenceGeo
	case "gateway":
		return colGateway
	case "cost_type":
		return colCostType
	case "identity":
		return colIdentityRef
	case "cost_center":
		return colCostCenterRef
	}
	return ""
}

// estimatedFilter excludes billed rows (provenance="billed") from a default
// aggregation, so the estimated (granular, per-model) stream and the billed
// (cost_report, per-workspace/day) stream never double-count. Every ingested row
// carries a provenance (onCost defaults it to "estimated" via provenanceOf — it is
// never empty/NULL), so the SQL "<>" comparison keeps all estimated rows; only
// billed rows are dropped. Reconciliation reads billed rows explicitly instead.
func estimatedFilter() model.Filter {
	return model.Filter{Column: colProvenance, Op: model.OpNe, Value: provenanceBilled}
}

// windowFilters appends the occurred_at window bounds to extra filters, and
// excludes billed rows so default spend analytics aggregate the estimated stream
// only (no double-count with the billed reconciliation stream).
func windowFilters(extra []model.Filter, since time.Time, hasSince bool, until time.Time, hasUntil bool) []model.Filter {
	f := append([]model.Filter{estimatedFilter()}, extra...)
	if hasSince {
		f = append(f, model.Filter{Column: colOccurredAt, Op: model.OpGte, Value: model.NewTimestamp(since).String()})
	}
	if hasUntil {
		f = append(f, model.Filter{Column: colOccurredAt, Op: model.OpLte, Value: model.NewTimestamp(until).String()})
	}
	return f
}

// scanSamples iterates the read-model rows matching filters, paging by the default
// id keyset cursor, calling fn for each. It returns truncated=true if it hit the
// page cap (an honest signal the aggregate is partial, never a silent under-count).
func scanSamples(ctx context.Context, sc store.Scope, filters []model.Filter, fn func(model.Record)) (bool, error) {
	repo, err := sc.Ext(costSampleKind)
	if err != nil {
		return false, err
	}
	q := model.Query{Filters: filters, Limit: listCap}
	for pages := 0; ; pages++ {
		recs, page, err := repo.List(ctx, q)
		if err != nil {
			return false, err
		}
		for _, r := range recs {
			fn(r)
		}
		if !page.HasMore {
			return false, nil
		}
		if pages+1 >= maxScanPages {
			return true, nil
		}
		q.Cursor = page.Cursor
	}
}

// aggregatePeriod sums spend over the HALF-OPEN period window [pStart, pEnd) when
// bounded, or [pStart, +inf) for an unbounded ("total") period. The upper bound is
// EXCLUSIVE (OpLt) because periodEnd returns the next period's start instant: a
// sample landing exactly there belongs to the next period, not this one, so it
// must not be counted into both. This is what keeps budget evaluation and status
// scoped to a single period even when late/out-of-order or future-dated samples
// from other periods exist in the ledger.
func aggregatePeriod(ctx context.Context, sc store.Scope, extra []model.Filter, pStart time.Time, hasLower bool, pEnd time.Time, bounded bool) (aggResult, error) {
	// Exclude billed rows: budgets and forecasts run on the estimated (granular,
	// real-time) stream, not the billed cost_report stream (which would double-count).
	filters := append([]model.Filter{estimatedFilter()}, extra...)
	if hasLower {
		filters = append(filters, model.Filter{Column: colOccurredAt, Op: model.OpGte, Value: model.NewTimestamp(pStart).String()})
	}
	if bounded {
		filters = append(filters, model.Filter{Column: colOccurredAt, Op: model.OpLt, Value: model.NewTimestamp(pEnd).String()})
	}
	var res aggResult
	trunc, err := scanSamples(ctx, sc, filters, func(r model.Record) {
		res.Cost += r.Int(colCostMicroUSD)
		res.Input += r.Int(colInputTokens)
		res.Output += r.Int(colOutputTokens)
		res.Count++
	})
	res.Truncated = trunc
	return res, err
}

func aggregateGroupPeriod(ctx context.Context, sc store.Scope, userGroups func(string) ([]string, error), dimension, key string, pStart time.Time, hasLower bool, pEnd time.Time, bounded bool) (aggResult, error) {
	var (
		col  string
		refs []string
		err  error
	)
	switch dimension {
	case "agent_group":
		col = colAgentRef
		refs, err = agentGroupMemberRefs(ctx, sc, key)
	case "user_group":
		col = colActor
		if userGroups == nil {
			return aggResult{}, fmt.Errorf("finops: user_group %q members were not resolved", key)
		}
		refs, err = userGroups(key)
	default:
		return aggResult{}, fmt.Errorf("finops: unsupported group budget dimension %q", dimension)
	}
	if err != nil {
		return aggResult{}, err
	}
	var total aggResult
	for _, ref := range refs {
		part, err := aggregatePeriod(ctx, sc, []model.Filter{eq(col, ref)}, pStart, hasLower, pEnd, bounded)
		if err != nil {
			return aggResult{}, err
		}
		total.Cost += part.Cost
		total.Input += part.Input
		total.Output += part.Output
		total.Count += part.Count
		total.Truncated = total.Truncated || part.Truncated
	}
	return total, nil
}

func agentGroupMemberRefs(ctx context.Context, sc store.Scope, key string) ([]string, error) {
	g, err := agentGroupByKey(ctx, sc, key)
	if err != nil {
		return nil, err
	}
	members, err := listAgentGroupMembersByGroup(ctx, sc, g.ID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	refs := make([]string, 0, len(members))
	for _, member := range members {
		a, err := sc.Agents().Get(ctx, member.AgentID)
		if err != nil {
			return nil, err
		}
		ref := a.ExternalID
		if ref == "" {
			ref = a.Name
		}
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs, nil
}

func agentGroupByKey(ctx context.Context, sc store.Scope, key string) (model.AgentGroup, error) {
	if g, ok, err := findOne(ctx, sc.AgentGroups(), eq("slug", key)); err != nil {
		return model.AgentGroup{}, err
	} else if ok {
		return g, nil
	}
	if id, err := model.ParseID(key); err == nil {
		return sc.AgentGroups().Get(ctx, id)
	}
	return model.AgentGroup{}, fmt.Errorf("finops: agent_group %q not found", key)
}

func listAgentGroupMembersByGroup(ctx context.Context, sc store.Scope, groupID model.ID) ([]model.AgentGroupMember, error) {
	var out []model.AgentGroupMember
	q := model.Query{Filters: []model.Filter{eq("group_id", groupID.String())}, Limit: listCap}
	for {
		recs, page, err := sc.AgentGroupMembers().List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
		if !page.HasMore || page.Cursor == "" {
			return out, nil
		}
		q.Cursor = page.Cursor
	}
}

func listAgentGroupMembersByAgent(ctx context.Context, sc store.Scope, agentID model.ID) ([]model.AgentGroupMember, error) {
	var out []model.AgentGroupMember
	q := model.Query{Filters: []model.Filter{eq("agent_id", agentID.String())}, Limit: listCap}
	for {
		recs, page, err := sc.AgentGroupMembers().List(ctx, q)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
		if !page.HasMore || page.Cursor == "" {
			return out, nil
		}
		q.Cursor = page.Cursor
	}
}

// spendByDimension breaks spend down by a dimension over a window.
func spendByDimension(ctx context.Context, sc store.Scope, dim string, since time.Time, hasSince bool, until time.Time, hasUntil bool) (spendResponse, error) {
	col := dimensionColumn(dim)
	buckets := map[string]*spendBucketDTO{}
	var total int64
	trunc, err := scanSamples(ctx, sc, windowFilters(nil, since, hasSince, until, hasUntil), func(r model.Record) {
		key := ""
		if col != "" {
			key = r.String(col)
		}
		b := buckets[key]
		if b == nil {
			b = &spendBucketDTO{Key: key}
			buckets[key] = b
		}
		c := r.Int(colCostMicroUSD)
		b.CostMicroUSD += c
		b.InputTokens += r.Int(colInputTokens)
		b.OutputTokens += r.Int(colOutputTokens)
		b.Samples++
		total += c
	})
	if err != nil {
		return spendResponse{}, err
	}
	out := spendResponse{Dimension: dim, TotalMicroUSD: total, Truncated: trunc, Buckets: sortedBuckets(buckets)}
	if hasSince {
		out.Since = since.UTC().Format(time.RFC3339)
	}
	if hasUntil {
		out.Until = until.UTC().Format(time.RFC3339)
	}
	return out, nil
}

// sortedBuckets returns the buckets ordered by cost descending, then key.
func sortedBuckets(buckets map[string]*spendBucketDTO) []spendBucketDTO {
	out := make([]spendBucketDTO, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CostMicroUSD != out[j].CostMicroUSD {
			return out[i].CostMicroUSD > out[j].CostMicroUSD
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// summaryResponse is the spend overview: totals plus the top spenders by model,
// provider and agent, plus the prompt-cache efficiency breakdown.
type summaryResponse struct {
	Since         string           `json:"since,omitempty"`
	Until         string           `json:"until,omitempty"`
	TotalMicroUSD int64            `json:"total_micro_usd"`
	InputTokens   int64            `json:"input_tokens"`
	OutputTokens  int64            `json:"output_tokens"`
	Samples       int              `json:"samples"`
	ByModel       []spendBucketDTO `json:"by_model"`
	ByProvider    []spendBucketDTO `json:"by_provider"`
	ByAgent       []spendBucketDTO `json:"by_agent"`
	Cache         cacheSummaryDTO  `json:"cache"`
	Truncated     bool             `json:"truncated,omitempty"`
}

// cacheSummaryDTO is the prompt-cache efficiency breakdown — measurable now that the
// CostSample carries the cache split (closes the "not measurable" gap). Token
// fields are counts; SavingsMicroUSD is the realized saving from cache READS (each
// read costs ~0.1× the base input price, so ~0.9× is saved vs an uncached read),
// computed per-model from the priced catalog. HitRatePct is cache-read tokens as a
// percentage of total input volume — an honest "0" when nothing is cached.
type cacheSummaryDTO struct {
	UncachedInputTokens   int64 `json:"uncached_input_tokens"`
	CacheReadTokens       int64 `json:"cache_read_tokens"`
	CacheCreation1hTokens int64 `json:"cache_creation_1h_tokens"`
	CacheCreation5mTokens int64 `json:"cache_creation_5m_tokens"`
	SavingsMicroUSD       int64 `json:"savings_micro_usd"`
	HitRatePct            int   `json:"hit_rate_pct"`
}

// cacheReadSavingsFraction is the share of the base input price saved by a cache
// hit: a cache read is priced at ~0.1× base input, so ~0.9× is saved versus paying
// the uncached input rate for the same tokens.
const cacheReadSavingsFraction = 0.9

// summarize computes the spend overview in a single scan over the estimated stream.
func summarize(ctx context.Context, sc store.Scope, since time.Time, hasSince bool, until time.Time, hasUntil bool) (summaryResponse, error) {
	byModel := map[string]*spendBucketDTO{}
	byProvider := map[string]*spendBucketDTO{}
	byAgent := map[string]*spendBucketDTO{}
	cacheReadByModel := map[string]int64{}
	var out summaryResponse
	trunc, err := scanSamples(ctx, sc, windowFilters(nil, since, hasSince, until, hasUntil), func(r model.Record) {
		c, in, o := r.Int(colCostMicroUSD), r.Int(colInputTokens), r.Int(colOutputTokens)
		out.TotalMicroUSD += c
		out.InputTokens += in
		out.OutputTokens += o
		out.Samples++
		cr := r.Int(colCacheReadTokens)
		c1h := r.Int(colCacheCreation1hTokens)
		c5m := r.Int(colCacheCreation5mTokens)
		out.Cache.CacheReadTokens += cr
		out.Cache.CacheCreation1hTokens += c1h
		out.Cache.CacheCreation5mTokens += c5m
		// InputTokens folds all input tiers; the uncached remainder is the
		// total minus the cache tiers (clamped, never negative for a malformed row).
		if uncached := in - cr - c1h - c5m; uncached > 0 {
			out.Cache.UncachedInputTokens += uncached
		}
		if cr > 0 {
			cacheReadByModel[r.String(colModelRef)] += cr
		}
		addBucket(byModel, r.String(colModelRef), c, in, o)
		addBucket(byProvider, r.String(colProviderRef), c, in, o)
		if a := r.String(colAgentRef); a != "" {
			addBucket(byAgent, a, c, in, o)
		}
	})
	if err != nil {
		return summaryResponse{}, err
	}
	out.Truncated = trunc
	out.ByModel = topN(sortedBuckets(byModel), 10)
	out.ByProvider = topN(sortedBuckets(byProvider), 10)
	out.ByAgent = topN(sortedBuckets(byAgent), 10)
	out.Cache.SavingsMicroUSD = cacheReadSavings(ctx, sc, cacheReadByModel)
	if total := out.InputTokens; total > 0 {
		out.Cache.HitRatePct = int(float64(out.Cache.CacheReadTokens) / float64(total) * 100)
	}
	if hasSince {
		out.Since = since.UTC().Format(time.RFC3339)
	}
	if hasUntil {
		out.Until = until.UTC().Format(time.RFC3339)
	}
	return out, nil
}

// cacheReadSavings computes the realized prompt-cache saving across models: for each
// model, the cache-read tokens valued at the base input price × the saved fraction
// (a cache read costs ~0.1× base, so ~0.9× is saved). Unpriced models contribute 0
// rather than a guessed rate (ARCHITECTURE.md). Rounded once to integer micro-USD.
func cacheReadSavings(ctx context.Context, sc store.Scope, cacheReadByModel map[string]int64) int64 {
	if len(cacheReadByModel) == 0 {
		return 0
	}
	rates, _, err := modelRateIndex(ctx, sc)
	if err != nil {
		return 0
	}
	var savings float64
	for m, tok := range cacheReadByModel {
		if r, ok := rates[m]; ok && r.priced {
			savings += float64(tok) * r.in * cacheReadSavingsFraction
		}
	}
	if savings <= 0 {
		return 0
	}
	return int64(math.Round(savings))
}

func addBucket(m map[string]*spendBucketDTO, key string, cost, in, out int64) {
	b := m[key]
	if b == nil {
		b = &spendBucketDTO{Key: key}
		m[key] = b
	}
	b.CostMicroUSD += cost
	b.InputTokens += in
	b.OutputTokens += out
	b.Samples++
}

func topN(b []spendBucketDTO, n int) []spendBucketDTO {
	if len(b) > n {
		b = b[:n]
	}
	if b == nil {
		return []spendBucketDTO{}
	}
	return b
}

// trendResponse is a per-day spend series.
type trendResponse struct {
	Since     string           `json:"since,omitempty"`
	Until     string           `json:"until,omitempty"`
	Days      []spendBucketDTO `json:"days"`
	Truncated bool             `json:"truncated,omitempty"`
}

// trend buckets spend by UTC day (the read-model timestamps are canonical
// fixed-width, so the date is the first 10 chars).
func trendByDay(ctx context.Context, sc store.Scope, since time.Time, hasSince bool, until time.Time, hasUntil bool) (trendResponse, error) {
	days := map[string]*spendBucketDTO{}
	trunc, err := scanSamples(ctx, sc, windowFilters(nil, since, hasSince, until, hasUntil), func(r model.Record) {
		ts := r.String(colOccurredAt)
		if len(ts) < 10 {
			return
		}
		addBucket(days, ts[:10], r.Int(colCostMicroUSD), r.Int(colInputTokens), r.Int(colOutputTokens))
	})
	if err != nil {
		return trendResponse{}, err
	}
	series := make([]spendBucketDTO, 0, len(days))
	for _, b := range days {
		series = append(series, *b)
	}
	sort.Slice(series, func(i, j int) bool { return series[i].Key < series[j].Key })
	out := trendResponse{Days: series, Truncated: trunc}
	if hasSince {
		out.Since = since.UTC().Format(time.RFC3339)
	}
	if hasUntil {
		out.Until = until.UTC().Format(time.RFC3339)
	}
	return out, nil
}

// reconciliationDayDTO is one day's billed-vs-estimated comparison.
type reconciliationDayDTO struct {
	Day               string `json:"day"`
	BilledMicroUSD    int64  `json:"billed_micro_usd"`
	EstimatedMicroUSD int64  `json:"estimated_micro_usd"`
	DriftMicroUSD     int64  `json:"drift_micro_usd"` // billed − estimated (positive = under-estimated)
}

// reconciliationResponse compares the authoritative billed cost (Anthropic
// cost_report) against the derived estimate, per day, so a CFO can see how closely
// the estimate tracks the invoice and where it cannot (Priority Tier is never billed
// via cost_report, so its spend is estimated-only — surfaced honestly, not hidden).
type reconciliationResponse struct {
	Since                  string                 `json:"since,omitempty"`
	Until                  string                 `json:"until,omitempty"`
	BilledTotalMicroUSD    int64                  `json:"billed_total_micro_usd"`
	EstimatedTotalMicroUSD int64                  `json:"estimated_total_micro_usd"`
	DriftMicroUSD          int64                  `json:"drift_micro_usd"`
	HasBilled              bool                   `json:"has_billed"`
	EstimatedOnlyTiers     []string               `json:"estimated_only_tiers,omitempty"`
	Note                   string                 `json:"note,omitempty"`
	Days                   []reconciliationDayDTO `json:"days"`
	Truncated              bool                   `json:"truncated,omitempty"`
}

// reconcile compares billed vs estimated spend by UTC day over the window. It scans
// the billed stream and the estimated stream separately (they are tagged by
// provenance) and never sums them together. It also reports which service tiers were
// seen only in the estimate (e.g. Priority), since cost_report cannot bill them.
func reconcile(ctx context.Context, sc store.Scope, since time.Time, hasSince bool, until time.Time, hasUntil bool) (reconciliationResponse, error) {
	billedByDay := map[string]int64{}
	estByDay := map[string]int64{}
	billedTiers := map[string]bool{}
	estTiers := map[string]bool{}
	var out reconciliationResponse

	dayOf := func(r model.Record) string {
		ts := r.String(colOccurredAt)
		if len(ts) < 10 {
			return ""
		}
		return ts[:10]
	}

	billedFilters := windowBilled(since, hasSince, until, hasUntil)
	tBilled, err := scanSamples(ctx, sc, billedFilters, func(r model.Record) {
		c := r.Int(colCostMicroUSD)
		out.BilledTotalMicroUSD += c
		out.HasBilled = true
		if d := dayOf(r); d != "" {
			billedByDay[d] += c
		}
		if t := r.String(colServiceTier); t != "" {
			billedTiers[t] = true
		}
	})
	if err != nil {
		return reconciliationResponse{}, err
	}
	tEst, err := scanSamples(ctx, sc, windowFilters(nil, since, hasSince, until, hasUntil), func(r model.Record) {
		c := r.Int(colCostMicroUSD)
		out.EstimatedTotalMicroUSD += c
		if d := dayOf(r); d != "" {
			estByDay[d] += c
		}
		if t := r.String(colServiceTier); t != "" {
			estTiers[t] = true
		}
	})
	if err != nil {
		return reconciliationResponse{}, err
	}
	out.DriftMicroUSD = out.BilledTotalMicroUSD - out.EstimatedTotalMicroUSD
	out.Truncated = tBilled || tEst

	// Union the day sets and emit a sorted per-day comparison.
	days := map[string]bool{}
	for d := range billedByDay {
		days[d] = true
	}
	for d := range estByDay {
		days[d] = true
	}
	out.Days = make([]reconciliationDayDTO, 0, len(days))
	for d := range days {
		out.Days = append(out.Days, reconciliationDayDTO{
			Day: d, BilledMicroUSD: billedByDay[d], EstimatedMicroUSD: estByDay[d],
			DriftMicroUSD: billedByDay[d] - estByDay[d],
		})
	}
	sort.Slice(out.Days, func(i, j int) bool { return out.Days[i].Day < out.Days[j].Day })

	// Tiers seen only in the estimate (never in cost_report) — e.g. Priority Tier.
	for t := range estTiers {
		if !billedTiers[t] {
			out.EstimatedOnlyTiers = append(out.EstimatedOnlyTiers, t)
		}
	}
	sort.Strings(out.EstimatedOnlyTiers)
	if !out.HasBilled {
		out.Note = "No billed cost_report data in this window — figures are estimated from list pricing. Enable the claude-api cost_report poll for billed truth."
	} else if len(out.EstimatedOnlyTiers) > 0 {
		out.Note = "Some service tiers (e.g. Priority) are not billed via cost_report and remain estimated; their spend is in the estimate total but not the billed total."
	}
	if hasSince {
		out.Since = since.UTC().Format(time.RFC3339)
	}
	if hasUntil {
		out.Until = until.UTC().Format(time.RFC3339)
	}
	return out, nil
}

// windowBilled is windowFilters' billed-stream counterpart: it selects ONLY billed
// rows (provenance="billed") plus the occurred_at window.
func windowBilled(since time.Time, hasSince bool, until time.Time, hasUntil bool) []model.Filter {
	f := []model.Filter{{Column: colProvenance, Op: model.OpEq, Value: provenanceBilled}}
	if hasSince {
		f = append(f, model.Filter{Column: colOccurredAt, Op: model.OpGte, Value: model.NewTimestamp(since).String()})
	}
	if hasUntil {
		f = append(f, model.Filter{Column: colOccurredAt, Op: model.OpLte, Value: model.NewTimestamp(until).String()})
	}
	return f
}

// anomalyDTO is one day whose spend deviates materially from the rolling baseline.
type anomalyDTO struct {
	Day              string  `json:"day"`
	SpendMicroUSD    int64   `json:"spend_micro_usd"`
	BaselineMicroUSD int64   `json:"baseline_micro_usd"`
	DeviationSigma   float64 `json:"deviation_sigma"`
}

// forecastResponse projects the current period's spend two ways: the naive
// elapsed-fraction run-rate (ProjectedMicroUSD, kept for continuity) and a
// trailing-window trend forecast with an explicit confidence band
// (TrendProjectedMicroUSD ± Confidence{Low,High}), plus spend anomalies — the
// shorter-window, frequently-revised approach AI spend volatility calls for
// (FinOps for AI: forecasting + anomaly management).
type forecastResponse struct {
	Period            string `json:"period"`
	PeriodStart       string `json:"period_start,omitempty"`
	Now               string `json:"now"`
	SpendMicroUSD     int64  `json:"spend_micro_usd"`
	ProjectedMicroUSD int64  `json:"projected_micro_usd"`
	Samples           int    `json:"samples"`

	// Trend forecast (trailing-window daily run rate).
	Method                 string       `json:"method"`
	WindowDays             int          `json:"window_days"`
	DailyRunRateMicroUSD   int64        `json:"daily_run_rate_micro_usd"`
	TrendProjectedMicroUSD int64        `json:"trend_projected_micro_usd"`
	ConfidenceLowMicroUSD  int64        `json:"confidence_low_micro_usd"`
	ConfidenceHighMicroUSD int64        `json:"confidence_high_micro_usd"`
	Anomalies              []anomalyDTO `json:"anomalies,omitempty"`
	Truncated              bool         `json:"truncated,omitempty"`

	// EWA (exponentially weighted average) forecast.
	EWADailyRateMicroUSD int64   `json:"ewa_daily_rate_micro_usd"`
	EWAProjectedMicroUSD int64   `json:"ewa_projected_micro_usd"`
	EWAConfidenceLow     int64   `json:"ewa_confidence_low_micro_usd"`
	EWAConfidenceHigh    int64   `json:"ewa_confidence_high_micro_usd"`
	EWAAlpha             float64 `json:"ewa_alpha"`

	// per-dimension forecasts (when ?dimension=X is supplied).
	DimensionForecasts []dimensionForecastDTO `json:"dimension_forecasts,omitempty"`
}

// defaultForecastWindowDays is the trailing window the trend forecast averages over;
// short enough to track bursty AI spend, long enough to smooth daily noise.
const defaultForecastWindowDays = 7

// forecastPeriod projects spend for the period containing now, with both the naive
// run-rate and a trailing-window trend forecast + anomaly scan. windowDays bounds
// the trailing average (<=0 uses the default).
func forecastPeriod(ctx context.Context, sc store.Scope, period string, now time.Time, windowDays int) (forecastResponse, error) {
	if windowDays <= 0 {
		windowDays = defaultForecastWindowDays
	}
	pStart, hasLower := periodStart(period, now)
	pEnd := periodEnd(period, pStart)
	// Bound to the period [pStart, pEnd) so spend from neighboring periods (late
	// or future-dated samples) cannot leak into the run-rate base.
	agg, err := aggregatePeriod(ctx, sc, nil, pStart, hasLower, pEnd, hasLower)
	if err != nil {
		return forecastResponse{}, err
	}
	out := forecastResponse{
		Period: period, Now: now.UTC().Format(time.RFC3339),
		SpendMicroUSD:     agg.Cost,
		ProjectedMicroUSD: projectSpend(agg.Cost, period, pStart, now, hasLower),
		Samples:           agg.Count, Truncated: agg.Truncated,
		Method: "trailing_window", WindowDays: windowDays,
	}
	if hasLower {
		out.PeriodStart = model.NewTimestamp(pStart).String()
	}

	// Build the per-day spend series over a trailing window ending at `now`, then
	// project the rest of the period off the windowed daily mean with a confidence
	// band from the daily standard deviation. This is distinct from the static
	// budget-threshold alerting and revises every day as new data lands.
	winStart := now.UTC().AddDate(0, 0, -windowDays)
	series, trunc, err := dailySeries(ctx, sc, winStart, now)
	if err != nil {
		return forecastResponse{}, err
	}
	out.Truncated = out.Truncated || trunc
	mean, std := meanStd(series)
	out.DailyRunRateMicroUSD = int64(math.Round(mean))
	out.fillTrend(mean, std, period, pStart, now, hasLower)
	out.Anomalies = detectAnomalies(series, mean, std)

	// EWA (exponentially weighted average) forecast — recency-biased.
	ewaRate, ewaVar := ewaForecast(series, defaultEWAAlpha)
	out.EWAAlpha = defaultEWAAlpha
	out.EWADailyRateMicroUSD = int64(math.Round(ewaRate))
	out.fillEWATrend(ewaRate, ewaVar, period, pStart, now, hasLower)
	return out, nil
}

// fillTrend projects period spend off the trailing daily mean and a ±1.96σ (≈95%)
// confidence band scaled by the remaining days. For "total"/unbounded periods there
// is no remaining horizon, so the trend equals the spend so far.
func (out *forecastResponse) fillTrend(mean, std float64, period string, pStart, now time.Time, hasLower bool) {
	if !hasLower || period == "total" {
		out.TrendProjectedMicroUSD = out.SpendMicroUSD
		out.ConfidenceLowMicroUSD = out.SpendMicroUSD
		out.ConfidenceHighMicroUSD = out.SpendMicroUSD
		return
	}
	pEnd := periodEnd(period, pStart)
	remaining := pEnd.Sub(now).Hours() / 24
	if remaining < 0 {
		remaining = 0
	}
	projected := float64(out.SpendMicroUSD) + mean*remaining
	// Variance of a sum of `remaining` iid days scales the σ by sqrt(remaining).
	band := 1.96 * std * math.Sqrt(remaining)
	out.TrendProjectedMicroUSD = clampNonNeg(projected)
	out.ConfidenceLowMicroUSD = clampNonNeg(projected - band)
	out.ConfidenceHighMicroUSD = clampNonNeg(projected + band)
}

// daySpend is one calendar day's spend in the trailing window (zero-filled).
type daySpend struct {
	Day  string // UTC date, YYYY-MM-DD
	Cost int64
}

// dailySeries returns the per-UTC-day estimated spend over the FULL calendar window
// [sinceDay, untilDay], ZERO-FILLED for days with no spend, plus the scan-truncation
// flag. Zero-filling is essential: the run-rate must divide spend by CALENDAR days,
// not by active days (bursty AI spend has many zero days), or it over-projects; and
// the baseline must include zero days or a lone spike fails to clear the σ threshold.
func dailySeries(ctx context.Context, sc store.Scope, since, until time.Time) ([]daySpend, bool, error) {
	byDay := map[string]int64{}
	trunc, err := scanSamples(ctx, sc, windowFilters(nil, since, true, until, true), func(r model.Record) {
		ts := r.String(colOccurredAt)
		if len(ts) >= 10 {
			byDay[ts[:10]] += r.Int(colCostMicroUSD)
		}
	})
	if err != nil {
		return nil, false, err
	}
	var out []daySpend
	day := time.Date(since.UTC().Year(), since.UTC().Month(), since.UTC().Day(), 0, 0, 0, 0, time.UTC)
	end := until.UTC()
	for !day.After(end) {
		key := day.Format("2006-01-02")
		out = append(out, daySpend{Day: key, Cost: byDay[key]})
		day = day.AddDate(0, 0, 1)
	}
	return out, trunc, nil
}

// dailySeriesFiltered is like dailySeries but accepts extra filters (e.g. a budget's
// dimension filter) to partition the series by a specific scope.
func dailySeriesFiltered(ctx context.Context, sc store.Scope, since, until time.Time, extra []model.Filter) ([]daySpend, bool, error) {
	byDay := map[string]int64{}
	trunc, err := scanSamples(ctx, sc, windowFilters(extra, since, true, until, true), func(r model.Record) {
		ts := r.String(colOccurredAt)
		if len(ts) >= 10 {
			byDay[ts[:10]] += r.Int(colCostMicroUSD)
		}
	})
	if err != nil {
		return nil, false, err
	}
	var out []daySpend
	day := time.Date(since.UTC().Year(), since.UTC().Month(), since.UTC().Day(), 0, 0, 0, 0, time.UTC)
	end := until.UTC()
	for !day.After(end) {
		key := day.Format("2006-01-02")
		out = append(out, daySpend{Day: key, Cost: byDay[key]})
		day = day.AddDate(0, 0, 1)
	}
	return out, trunc, nil
}

// meanStd returns the mean and population standard deviation of the daily costs (0,0
// for an empty series). The series is the zero-filled calendar window, so the mean is
// a true per-calendar-day run rate.
func meanStd(series []daySpend) (mean, std float64) {
	if len(series) == 0 {
		return 0, 0
	}
	var sum float64
	for _, d := range series {
		sum += float64(d.Cost)
	}
	mean = sum / float64(len(series))
	var ss float64
	for _, d := range series {
		x := float64(d.Cost) - mean
		ss += x * x
	}
	return mean, math.Sqrt(ss / float64(len(series)))
}

// detectAnomalies flags days more than anomalySigma standard deviations above the
// mean (a spend spike), labeled with the REAL calendar date. It needs at least 3
// days and a non-zero σ to be meaningful; below that it returns nothing rather than
// fabricate an outlier.
func detectAnomalies(series []daySpend, mean, std float64) []anomalyDTO {
	const anomalySigma = 2.0
	if len(series) < 3 || std <= 0 {
		return nil
	}
	var out []anomalyDTO
	for _, d := range series {
		dev := (float64(d.Cost) - mean) / std
		if dev >= anomalySigma {
			out = append(out, anomalyDTO{
				Day:              d.Day,
				SpendMicroUSD:    d.Cost,
				BaselineMicroUSD: int64(math.Round(mean)),
				DeviationSigma:   math.Round(dev*100) / 100,
			})
		}
	}
	return out
}

// clampNonNeg rounds a float to int64, never below 0 (spend/projection is never
// negative; a negative confidence-band floor clamps to 0).
func clampNonNeg(f float64) int64 {
	if f < 0 {
		return 0
	}
	if f > 1e18 {
		return 1e18
	}
	return int64(math.Round(f))
}

// recommendationDTO is one optimization opportunity, grounded in recorded data and
// explicit about its assumptions (the product never fakes certainty, ARCHITECTURE.md).
type recommendationDTO struct {
	Kind                     string `json:"kind"`
	Title                    string `json:"title"`
	Detail                   string `json:"detail"`
	Severity                 string `json:"severity"`
	Subject                  string `json:"subject,omitempty"`
	EstimatedSavingsMicroUSD int64  `json:"estimated_savings_micro_usd,omitempty"`
}

// recommendations derives optimization opportunities: cheaper-model savings
// estimates over the current month, budgets on track to exceed, and the honest
// note that cache-savings is not derivable from the current cost stream.
func (m *Module) recommendations(ctx context.Context, sc store.Scope, now time.Time) ([]recommendationDTO, error) {
	out := []recommendationDTO{}

	// Cheaper-model opportunities over the current month.
	pStart, hasLower := periodStart("monthly", now)
	byModel, err := spendByDimension(ctx, sc, "model", pStart, hasLower, time.Time{}, false)
	if err != nil {
		return nil, err
	}
	rates, cheapest, err := modelRateIndex(ctx, sc)
	if err != nil {
		return nil, err
	}
	if cheapest != "" {
		cr := rates[cheapest]
		for _, b := range byModel.Buckets {
			if b.Key == "" || b.Key == cheapest {
				continue
			}
			r, ok := rates[b.Key]
			if !ok || !r.priced {
				continue
			}
			hypothetical := int64(math.Round(float64(b.InputTokens)*cr.in + float64(b.OutputTokens)*cr.out))
			savings := b.CostMicroUSD - hypothetical
			if savings <= 0 {
				continue
			}
			out = append(out, recommendationDTO{
				Kind:     "cheaper_model",
				Title:    "Consider routing eligible " + b.Key + " traffic to " + cheapest,
				Detail:   "This month's spend on " + b.Key + " would be lower on the cheapest governed model (" + cheapest + "). Estimate assumes the workload is eligible for that model — verify capability and quality fit before switching; it does not account for prompt-cache savings.",
				Severity: "info", Subject: b.Key, EstimatedSavingsMicroUSD: savings,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EstimatedSavingsMicroUSD > out[j].EstimatedSavingsMicroUSD })

	// Budgets projected to exceed their limit.
	budgets, _, err := sc.Policies().List(ctx, model.Query{Filters: []model.Filter{eq("kind", policyKindBudget)}, Limit: listCap})
	if err != nil {
		return nil, err
	}
	for _, p := range budgets {
		if !p.Enabled {
			continue
		}
		st, err := budgetStatus(ctx, sc, p, now)
		if err != nil {
			return nil, err
		}
		if st.LimitMicroUSD > 0 && st.ProjectedPct >= 100 {
			out = append(out, recommendationDTO{
				Kind:     "budget_burn",
				Title:    "Budget \"" + p.Name + "\" is on track to exceed its limit",
				Detail:   "At the current run rate this period's spend is projected to surpass the budget. Reduce usage on this dimension, route to a cheaper model, or revise the limit.",
				Severity: "medium", Subject: p.ID.String(),
			})
		}
	}

	// Prompt-cache efficiency — now MEASURABLE: the CostSample carries the cache
	// split, so report the realized saving and, when caching is underused,
	// flag the opportunity. This replaces the prior "not measurable" disclaimer.
	sum, err := summarize(ctx, sc, pStart, hasLower, time.Time{}, false)
	if err != nil {
		return nil, err
	}
	cache := sum.Cache
	if cache.SavingsMicroUSD > 0 {
		out = append(out, recommendationDTO{
			Kind:                     "cache_savings",
			Title:                    "Prompt caching is saving spend this month",
			Detail:                   "Cache reads were billed at a fraction of the uncached input price. The estimate values cache-read tokens at the saved share of each model's base input rate; keep cacheable context stable to sustain it.",
			Severity:                 "info",
			EstimatedSavingsMicroUSD: cache.SavingsMicroUSD,
		})
	} else if cache.UncachedInputTokens > 0 && cache.CacheReadTokens == 0 {
		out = append(out, recommendationDTO{
			Kind:     "cache_opportunity",
			Title:    "No prompt-cache reads observed — caching may be unused",
			Detail:   "All input this month was uncached. Workloads with a stable prefix (system prompt, tools, long context) can mark it cacheable so repeat reads bill at ~10% of the input price. Verify the workload reuses a prefix before enabling.",
			Severity: "info",
		})
	}
	return out, nil
}

// modelRate is a model's blended per-token rate in micro-USD/token (the precise
// USD/MTok figure equals µUSD/token), preferring the precise figure module X
// stored in metadata, falling back to the coarse core per-token field.
type modelRate struct {
	in     float64
	out    float64
	priced bool
}

// modelRateIndex builds per-model rates and the cheapest priced model ref.
func modelRateIndex(ctx context.Context, sc store.Scope) (map[string]modelRate, string, error) {
	mods, _, err := sc.Models().List(ctx, model.Query{Limit: listCap})
	if err != nil {
		return nil, "", err
	}
	rates := make(map[string]modelRate, len(mods))
	cheapest := ""
	best := math.MaxFloat64
	for _, md := range mods {
		r := modelRateOf(md)
		rates[md.Name] = r
		if r.priced {
			if blended := r.in + r.out; blended < best {
				best = blended
				cheapest = md.Name
			}
		}
	}
	return rates, cheapest, nil
}

// modelRateOf reads a model's per-token rate, preferring metadata precision.
func modelRateOf(md model.Model) modelRate {
	in, inOK := metaFloat(md.Metadata, "input_per_mtok_usd")
	out, outOK := metaFloat(md.Metadata, "output_per_mtok_usd")
	if inOK || outOK {
		return modelRate{in: in, out: out, priced: in > 0 || out > 0}
	}
	ci, co := float64(md.InputCostMicroUSD), float64(md.OutputCostMicroUSD)
	return modelRate{in: ci, out: co, priced: ci > 0 || co > 0}
}

func metaFloat(meta map[string]any, key string) (float64, bool) {
	if meta == nil {
		return 0, false
	}
	if f, ok := meta[key].(float64); ok {
		return f, true
	}
	return 0, false
}

// --- team summary analytics --------------------------------------------------

// teamSummaryResponse is the team-level spend aggregation for a fixed period window.
type teamSummaryResponse struct {
	Period string           `json:"period"`
	Teams  []teamSummaryDTO `json:"teams"`
}

// teamSummaryDTO aggregates spend, session count, and per-project/model
// breakdowns for one team, plus a zero-filled per-calendar-day trend series.
type teamSummaryDTO struct {
	Team         string              `json:"team"`
	Sessions     int64               `json:"sessions"`
	InputTokens  int64               `json:"input_tokens"`
	OutputTokens int64               `json:"output_tokens"`
	CostMicroUSD int64               `json:"cost_micro_usd"`
	Trend        []int64             `json:"trend"`
	Projects     []projectSummaryDTO `json:"projects"`
	Models       []modelSummaryDTO   `json:"models"`
}

// projectSummaryDTO is one project's spend within a team.
type projectSummaryDTO struct {
	Project      string `json:"project"`
	Sessions     int64  `json:"sessions"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	CostMicroUSD int64  `json:"cost_micro_usd"`
}

// modelSummaryDTO is one model's spend within a team.
type modelSummaryDTO struct {
	Model        string `json:"model"`
	CostMicroUSD int64  `json:"cost_micro_usd"`
}

// teamSummary aggregates the estimated cost stream by team over [since, until],
// breaking each team down by project, model, and a zero-filled per-calendar-day
// trend series. Samples without a team label are bucketed under "(untagged)".
func teamSummary(ctx context.Context, sc store.Scope, since, until time.Time) (teamSummaryResponse, error) {
	type projAgg struct {
		dto      projectSummaryDTO
		sessions map[string]bool
	}
	type teamAgg struct {
		sessions            map[string]bool
		input, output, cost int64
		projects            map[string]*projAgg
		models              map[string]int64
		daily               map[string]int64 // UTC date string → cost
	}
	teams := map[string]*teamAgg{}

	_, err := scanSamples(ctx, sc, windowFilters(nil, since, true, until, true), func(r model.Record) {
		team := r.String(colTeam)
		if team == "" {
			team = "(untagged)"
		}
		a := teams[team]
		if a == nil {
			a = &teamAgg{
				sessions: map[string]bool{},
				projects: map[string]*projAgg{},
				models:   map[string]int64{},
				daily:    map[string]int64{},
			}
			teams[team] = a
		}
		cost := r.Int(colCostMicroUSD)
		a.input += r.Int(colInputTokens)
		a.output += r.Int(colOutputTokens)
		a.cost += cost

		if sess := r.String(colSessionRef); sess != "" {
			a.sessions[sess] = true
		}

		if proj := r.String(colProject); proj != "" {
			p := a.projects[proj]
			if p == nil {
				p = &projAgg{
					dto:      projectSummaryDTO{Project: proj},
					sessions: map[string]bool{},
				}
				a.projects[proj] = p
			}
			p.dto.InputTokens += r.Int(colInputTokens)
			p.dto.OutputTokens += r.Int(colOutputTokens)
			p.dto.CostMicroUSD += cost
			if sess := r.String(colSessionRef); sess != "" {
				p.sessions[sess] = true
			}
		}

		if mod := r.String(colModelRef); mod != "" {
			a.models[mod] += cost
		}

		if ts := r.String(colOccurredAt); len(ts) >= 10 {
			a.daily[ts[:10]] += cost
		}
	})
	if err != nil {
		return teamSummaryResponse{}, err
	}

	out := teamSummaryResponse{
		Period: since.Format(time.DateOnly) + "/" + until.Format(time.DateOnly),
		Teams:  make([]teamSummaryDTO, 0, len(teams)),
	}
	for name, a := range teams {
		dto := teamSummaryDTO{
			Team:         name,
			Sessions:     int64(len(a.sessions)),
			InputTokens:  a.input,
			OutputTokens: a.output,
			CostMicroUSD: a.cost,
		}
		for _, p := range a.projects {
			p.dto.Sessions = int64(len(p.sessions))
			dto.Projects = append(dto.Projects, p.dto)
		}
		sort.Slice(dto.Projects, func(i, j int) bool {
			return dto.Projects[i].CostMicroUSD > dto.Projects[j].CostMicroUSD
		})
		for mod, c := range a.models {
			dto.Models = append(dto.Models, modelSummaryDTO{Model: mod, CostMicroUSD: c})
		}
		sort.Slice(dto.Models, func(i, j int) bool {
			return dto.Models[i].CostMicroUSD > dto.Models[j].CostMicroUSD
		})
		// Zero-filled daily trend over the full [since, until] calendar range.
		for d := since; !d.After(until); d = d.AddDate(0, 0, 1) {
			dto.Trend = append(dto.Trend, a.daily[d.Format(time.DateOnly)])
		}
		out.Teams = append(out.Teams, dto)
	}
	sort.Slice(out.Teams, func(i, j int) bool {
		return out.Teams[i].CostMicroUSD > out.Teams[j].CostMicroUSD
	})
	return out, nil
}

// --- EWA forecasting, per-dimension forecast, budget exhaustion --------

const defaultEWAAlpha = 0.3

// dimensionForecastDTO is one value's forecast within a per-dimension forecast.
type dimensionForecastDTO struct {
	Key                  string `json:"key"`
	SpendMicroUSD        int64  `json:"spend_micro_usd"`
	EWADailyRateMicroUSD int64  `json:"ewa_daily_rate_micro_usd"`
	EWAProjectedMicroUSD int64  `json:"ewa_projected_micro_usd"`
	EWAConfidenceLow     int64  `json:"ewa_confidence_low_micro_usd"`
	EWAConfidenceHigh    int64  `json:"ewa_confidence_high_micro_usd"`
	Samples              int    `json:"samples"`
}

// ewaForecast computes the exponentially weighted average daily rate and
// exponentially weighted variance from a zero-filled daily series. Alpha
// controls recency bias: higher alpha weighs recent days more heavily.
// Returns (0, 0) for an empty series.
func ewaForecast(series []daySpend, alpha float64) (rate, variance float64) {
	if len(series) == 0 {
		return 0, 0
	}
	if alpha <= 0 || alpha > 1 {
		alpha = defaultEWAAlpha
	}
	rate = float64(series[0].Cost)
	variance = 0
	for i := 1; i < len(series); i++ {
		x := float64(series[i].Cost)
		diff := x - rate
		rate = alpha*x + (1-alpha)*rate
		variance = (1 - alpha) * (variance + alpha*diff*diff)
	}
	return rate, variance
}

// fillEWATrend projects period spend off the EWA daily rate with a confidence
// band derived from the EWA variance, analogous to fillTrend but using the
// recency-biased estimator instead of the flat mean.
func (out *forecastResponse) fillEWATrend(ewaRate, ewaVar float64, period string, pStart, now time.Time, hasLower bool) {
	if !hasLower || period == "total" {
		out.EWAProjectedMicroUSD = out.SpendMicroUSD
		out.EWAConfidenceLow = out.SpendMicroUSD
		out.EWAConfidenceHigh = out.SpendMicroUSD
		return
	}
	pEnd := periodEnd(period, pStart)
	remaining := pEnd.Sub(now).Hours() / 24
	if remaining < 0 {
		remaining = 0
	}
	projected := float64(out.SpendMicroUSD) + ewaRate*remaining
	ewaSigma := math.Sqrt(ewaVar)
	band := 1.96 * ewaSigma * math.Sqrt(remaining)
	out.EWAProjectedMicroUSD = clampNonNeg(projected)
	out.EWAConfidenceLow = clampNonNeg(projected - band)
	out.EWAConfidenceHigh = clampNonNeg(projected + band)
}

// forecastByDimension partitions the spend stream by a dimension and returns
// an independent EWA forecast for each value.
func forecastByDimension(ctx context.Context, sc store.Scope, dim, period string, now time.Time, windowDays int) ([]dimensionForecastDTO, error) {
	if windowDays <= 0 {
		windowDays = defaultForecastWindowDays
	}
	col := dimensionColumn(dim)
	if col == "" && dim != "global" {
		return nil, nil
	}

	pStart, hasLower := periodStart(period, now)
	pEnd := periodEnd(period, pStart)
	winStart := now.UTC().AddDate(0, 0, -windowDays)

	// Collect per-key daily spend.
	type keyData struct {
		daily   map[string]int64
		total   int64
		samples int
	}
	byKey := map[string]*keyData{}

	_, err := scanSamples(ctx, sc, windowFilters(nil, winStart, true, now, true), func(r model.Record) {
		key := ""
		if col != "" {
			key = r.String(col)
		}
		kd := byKey[key]
		if kd == nil {
			kd = &keyData{daily: map[string]int64{}}
			byKey[key] = kd
		}
		cost := r.Int(colCostMicroUSD)
		kd.total += cost
		kd.samples++
		if ts := r.String(colOccurredAt); len(ts) >= 10 {
			kd.daily[ts[:10]] += cost
		}
	})
	if err != nil {
		return nil, err
	}

	var out []dimensionForecastDTO
	for key, kd := range byKey {
		// Build zero-filled series for this key.
		var series []daySpend
		day := time.Date(winStart.UTC().Year(), winStart.UTC().Month(), winStart.UTC().Day(), 0, 0, 0, 0, time.UTC)
		end := now.UTC()
		for !day.After(end) {
			ds := day.Format("2006-01-02")
			series = append(series, daySpend{Day: ds, Cost: kd.daily[ds]})
			day = day.AddDate(0, 0, 1)
		}

		ewaRate, ewaVar := ewaForecast(series, defaultEWAAlpha)
		ewaSigma := math.Sqrt(ewaVar)

		// Period spend for this key.
		periodSpend := int64(0)
		if hasLower {
			pStartStr := model.NewTimestamp(pStart).String()
			pEndStr := model.NewTimestamp(pEnd).String()
			filters := []model.Filter{estimatedFilter()}
			if col != "" {
				filters = append(filters, eq(col, key))
			}
			filters = append(filters,
				model.Filter{Column: colOccurredAt, Op: model.OpGte, Value: pStartStr},
				model.Filter{Column: colOccurredAt, Op: model.OpLt, Value: pEndStr},
			)
			agg := aggResult{}
			_, scanErr := scanSamples(ctx, sc, filters, func(r model.Record) {
				agg.Cost += r.Int(colCostMicroUSD)
				agg.Count++
			})
			if scanErr != nil {
				return nil, scanErr
			}
			periodSpend = agg.Cost
		}

		remaining := float64(0)
		if hasLower && period != "total" {
			remaining = pEnd.Sub(now).Hours() / 24
			if remaining < 0 {
				remaining = 0
			}
		}
		projected := float64(periodSpend) + ewaRate*remaining
		band := 1.96 * ewaSigma * math.Sqrt(remaining)

		out = append(out, dimensionForecastDTO{
			Key:                  key,
			SpendMicroUSD:        periodSpend,
			EWADailyRateMicroUSD: int64(math.Round(ewaRate)),
			EWAProjectedMicroUSD: clampNonNeg(projected),
			EWAConfidenceLow:     clampNonNeg(projected - band),
			EWAConfidenceHigh:    clampNonNeg(projected + band),
			Samples:              kd.samples,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].EWAProjectedMicroUSD > out[j].EWAProjectedMicroUSD
	})
	return out, nil
}

// budgetExhaustionDTO is the exhaustion prediction for a budget.
type budgetExhaustionDTO struct {
	ExhaustionDate string `json:"exhaustion_date,omitempty"`
	DaysRemaining  int    `json:"days_remaining"`
	Confidence     string `json:"confidence"` // high, medium, low
}

// budgetExhaustion predicts when a budget will be exhausted at the current EWA
// daily spend rate. Returns an empty result when the budget is unbounded
// (total period) or when there's insufficient data.
func budgetExhaustion(ewaDailyRate, ewaVariance float64, spend, limit, reserved int64) budgetExhaustionDTO {
	remaining := limit - spend - reserved
	if remaining <= 0 {
		return budgetExhaustionDTO{DaysRemaining: 0, Confidence: "high"}
	}
	if ewaDailyRate <= 0 {
		return budgetExhaustionDTO{DaysRemaining: -1, Confidence: "low"}
	}
	days := float64(remaining) / ewaDailyRate
	if days > 365*10 {
		return budgetExhaustionDTO{DaysRemaining: -1, Confidence: "low"}
	}

	cv := float64(0)
	if ewaDailyRate > 0 {
		cv = math.Sqrt(ewaVariance) / ewaDailyRate
	}
	confidence := "high"
	if cv > 0.5 {
		confidence = "low"
	} else if cv > 0.2 {
		confidence = "medium"
	}

	return budgetExhaustionDTO{
		DaysRemaining: int(math.Ceil(days)),
		Confidence:    confidence,
	}
}
