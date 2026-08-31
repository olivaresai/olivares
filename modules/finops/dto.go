// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package finops

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// listCap bounds an internal List page; it matches the store's own maximum.
const listCap = 1000

// costIngestRequest is the HTTP body for POST /cost — a snake_case projection of the
// sealed sdkmodel.CostSample contract, so the HTTP surface stays snake_case like
// every other module DTO while toCostSample maps it back to the canonical sealed
// type. The handler routes it through the SAME onCost path the bus uses (one ledger,
// one natural-key dedup, one attribution) — NOT a divergent ingest. Monetary amounts
// are integer micro-USD (no floats in money). Empty/zero means "not reported".
type costIngestRequest struct {
	ProviderRef           string    `json:"provider_ref"`
	ModelRef              string    `json:"model_ref"`
	SessionRef            string    `json:"session_ref,omitempty"`
	InputTokens           int64     `json:"input_tokens,omitempty"`
	OutputTokens          int64     `json:"output_tokens,omitempty"`
	CostMicroUSD          int64     `json:"cost_micro_usd,omitempty"`
	OccurredAt            time.Time `json:"occurred_at"`
	CacheReadTokens       int64     `json:"cache_read_tokens,omitempty"`
	CacheCreation1hTokens int64     `json:"cache_creation_1h_tokens,omitempty"`
	CacheCreation5mTokens int64     `json:"cache_creation_5m_tokens,omitempty"`
	WorkspaceRef          string    `json:"workspace_ref,omitempty"`
	APIKeyRef             string    `json:"api_key_ref,omitempty"`
	Actor                 string    `json:"actor,omitempty"`
	ServiceTier           string    `json:"service_tier,omitempty"`
	ContextWindow         string    `json:"context_window,omitempty"`
	InferenceGeo          string    `json:"inference_geo,omitempty"`
	Gateway               string    `json:"gateway,omitempty"`
	Provenance            string    `json:"provenance,omitempty"`
	CostType              string    `json:"cost_type,omitempty"`
	// Labels are the operator-supplied attribution tags — same contract as
	// sdk CostSample.Labels: team/project are promoted to dimensions, the rest
	// ride the ledger metadata; never part of the dedup natural key.
	Labels map[string]string `json:"labels,omitempty"`
}

// toCostSample maps the HTTP request to the sealed cost contract. Gateway/Provenance
// are carried as provider vocabulary strings; onCost normalizes them (gatewayOf/
// provenanceOf) exactly as it does for bus-sourced samples.
func (c costIngestRequest) toCostSample() sdkmodel.CostSample {
	return sdkmodel.CostSample{
		ProviderRef:           c.ProviderRef,
		ModelRef:              c.ModelRef,
		SessionRef:            c.SessionRef,
		InputTokens:           c.InputTokens,
		OutputTokens:          c.OutputTokens,
		CostMicroUSD:          c.CostMicroUSD,
		OccurredAt:            c.OccurredAt,
		CacheReadTokens:       c.CacheReadTokens,
		CacheCreation1hTokens: c.CacheCreation1hTokens,
		CacheCreation5mTokens: c.CacheCreation5mTokens,
		WorkspaceRef:          c.WorkspaceRef,
		APIKeyRef:             c.APIKeyRef,
		Actor:                 c.Actor,
		ServiceTier:           c.ServiceTier,
		ContextWindow:         c.ContextWindow,
		InferenceGeo:          c.InferenceGeo,
		Gateway:               sdkmodel.Gateway(c.Gateway),
		Provenance:            sdkmodel.CostProvenance(c.Provenance),
		CostType:              c.CostType,
		Labels:                c.Labels,
	}
}

// maxScanPages bounds a full-table aggregation scan (maxScanPages × listCap rows)
// so a pathological estate cannot loop unbounded; beyond it the result is flagged
// truncated rather than silently wrong (docs: honest gradation, never faked).
const maxScanPages = 1000

// listResponse is the paginated envelope every list endpoint returns: the ONE
// engine-wide shape (items + opaque cursor + has_more), aliased rather than
// re-declared so an empty page can never serialize as `{"items":null}` here
// while it serializes as `{"items":[]}` next door (core/api/listresponse.go).
type listResponse[T any] = api.ListResponse[T]

// budgetDTO is a budget policy: a named, enabled governance policy with a spend
// limit on a dimension over a period and alert thresholds.
type budgetDTO struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	budgetSpec
}

func toBudgetDTO(p model.Policy) budgetDTO {
	spec := parseBudgetSpec(p.Spec)
	spec.fillDefaults()
	return budgetDTO{ID: p.ID.String(), Name: p.Name, Enabled: p.Enabled, budgetSpec: spec}
}

// budgetStatusDTO is a budget's live consumption against its limit, with a
// run-rate projection of the period.
type budgetStatusDTO struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Enabled           bool   `json:"enabled"`
	Dimension         string `json:"dimension"`
	Key               string `json:"key,omitempty"`
	Period            string `json:"period"`
	PeriodStart       string `json:"period_start,omitempty"`
	Currency          string `json:"currency"`
	Action            string `json:"action"`
	LimitMicroUSD     int64  `json:"limit_micro_usd"`
	ReservedMicroUSD  int64  `json:"reserved_micro_usd,omitempty"`
	SpendMicroUSD     int64  `json:"spend_micro_usd"`
	RemainingMicroUSD int64  `json:"remaining_micro_usd"`
	ConsumedPct       int    `json:"consumed_pct"`
	ProjectedMicroUSD int64  `json:"projected_micro_usd"`
	ProjectedPct      int    `json:"projected_pct"`
	Over              bool   `json:"over"`
	Samples           int    `json:"samples"`
	Truncated         bool   `json:"truncated,omitempty"`
	// budget exhaustion prediction from EWA daily rate.
	ExhaustionDaysRemaining int    `json:"exhaustion_days_remaining"`
	ExhaustionConfidence    string `json:"exhaustion_confidence,omitempty"` // high/medium/low
}

// alertDTO is one recorded budget-threshold crossing.
type alertDTO struct {
	BudgetID      string `json:"budget_id"`
	Dimension     string `json:"dimension"`
	Key           string `json:"key,omitempty"`
	Period        string `json:"period"`
	PeriodStart   string `json:"period_start"`
	ThresholdPct  int64  `json:"threshold_pct"`
	SpendMicroUSD int64  `json:"spend_micro_usd"`
	LimitMicroUSD int64  `json:"limit_micro_usd"`
	Severity      string `json:"severity"`
	TriggeredAt   string `json:"triggered_at"`
}

func toAlertDTO(rec model.Record) alertDTO {
	return alertDTO{
		BudgetID:      rec.String(colBudgetID),
		Dimension:     rec.String(colDimension),
		Key:           rec.String(colDimKey),
		Period:        rec.String(colPeriod),
		PeriodStart:   rec.String(colPeriodStart),
		ThresholdPct:  rec.Int(colThresholdPct),
		SpendMicroUSD: rec.Int(colAlertSpend),
		LimitMicroUSD: rec.Int(colAlertLimit),
		Severity:      rec.String(colSeverity),
		TriggeredAt:   rec.String(colTriggeredAt),
	}
}

// spendBucketDTO is one row of a spend breakdown (per dimension value or per day).
type spendBucketDTO struct {
	Key          string `json:"key"`
	CostMicroUSD int64  `json:"cost_micro_usd"`
	InputTokens  int64  `json:"input_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	Samples      int    `json:"samples"`
}

// spendResponse is a spend breakdown by a dimension over a window.
type spendResponse struct {
	Dimension     string           `json:"dimension"`
	Since         string           `json:"since,omitempty"`
	Until         string           `json:"until,omitempty"`
	TotalMicroUSD int64            `json:"total_micro_usd"`
	Buckets       []spendBucketDTO `json:"buckets"`
	Truncated     bool             `json:"truncated,omitempty"`
}

// --- value attribution ------------------------------------------------

// outcomeDTO is one graded outcome row (the value-attribution substrate).
type outcomeDTO struct {
	SubjectKind   string `json:"subject_kind"`
	SubjectRef    string `json:"subject_ref"`
	OutcomeRef    string `json:"outcome_ref,omitempty"`
	Verdict       string `json:"verdict"`
	ValueMicroUSD int64  `json:"value_micro_usd,omitempty"`
	OccurredAt    string `json:"occurred_at"`
	Source        string `json:"source,omitempty"`
	AgentRef      string `json:"agent_ref,omitempty"`
	IdentityRef   string `json:"identity_ref,omitempty"`
	SessionRef    string `json:"session_ref,omitempty"`
}

func toOutcomeDTO(rec model.Record) outcomeDTO {
	return outcomeDTO{
		SubjectKind:   rec.String(colOutcomeSubjectKind),
		SubjectRef:    rec.String(colOutcomeSubjectRef),
		OutcomeRef:    rec.String(colOutcomeRef),
		Verdict:       rec.String(colOutcomeVerdict),
		ValueMicroUSD: rec.Int(colOutcomeValue),
		OccurredAt:    rec.String(colOccurredAt),
		Source:        rec.String(colOutcomeSource),
		AgentRef:      rec.String(colAgentRef),
		IdentityRef:   rec.String(colIdentityRef),
		SessionRef:    rec.String(colSessionRef),
	}
}

// valueBucketDTO is one subject's spend-vs-outcome row. cost_per_outcome and
// cost_per_satisfied are micro-USD per outcome (omitted when there are no outcomes);
// creditable is the fallback_attempt portion of cost surfaced distinctly so
// fallback credit neither inflates nor hides the burn. eval_pass_rate_pct is the
// per-agent EvalResult signal (agent dimension only), present only where evals exist.
type valueBucketDTO struct {
	Key                      string `json:"key"`
	CostMicroUSD             int64  `json:"cost_micro_usd"`
	CreditableMicroUSD       int64  `json:"creditable_micro_usd,omitempty"`
	ValueMicroUSD            int64  `json:"value_micro_usd"`
	NetValueMicroUSD         int64  `json:"net_value_micro_usd"`
	Outcomes                 int    `json:"outcomes"`
	Satisfied                int    `json:"satisfied"`
	Unsatisfied              int    `json:"unsatisfied"`
	CostPerOutcomeMicroUSD   int64  `json:"cost_per_outcome_micro_usd,omitempty"`
	CostPerSatisfiedMicroUSD int64  `json:"cost_per_satisfied_micro_usd,omitempty"`
	SatisfiedRatePct         int    `json:"satisfied_rate_pct"`
	HasOutcomes              bool   `json:"has_outcomes"`
	EvalPassRatePct          *int   `json:"eval_pass_rate_pct,omitempty"`
	CancellationRisk         bool   `json:"cancellation_risk"`
	RiskReason               string `json:"risk_reason,omitempty"`
}

// valueResponse is the cost-per-outcome breakdown by a dimension over a window.
// total_cost_micro_usd is ALL estimated spend (attributed buckets + the unattributed
// remainder); sum(buckets[].cost_micro_usd) + unattributed_cost_micro_usd == it.
type valueResponse struct {
	Dimension                string           `json:"dimension"`
	Since                    string           `json:"since,omitempty"`
	Until                    string           `json:"until,omitempty"`
	TotalCostMicroUSD        int64            `json:"total_cost_micro_usd"`
	UnattributedCostMicroUSD int64            `json:"unattributed_cost_micro_usd,omitempty"`
	TotalValueMicroUSD       int64            `json:"total_value_micro_usd"`
	NetValueMicroUSD         int64            `json:"net_value_micro_usd"`
	TotalOutcomes            int              `json:"total_outcomes"`
	Buckets                  []valueBucketDTO `json:"buckets"`
	Truncated                bool             `json:"truncated,omitempty"`
}

// cancellationRiskDTO is one subject burning spend without successful outcomes.
type cancellationRiskDTO struct {
	Dimension    string `json:"dimension"`
	Key          string `json:"key"`
	CostMicroUSD int64  `json:"cost_micro_usd"`
	Outcomes     int    `json:"outcomes"`
	Satisfied    int    `json:"satisfied"`
	Reason       string `json:"reason"`
}

// valueSummaryResponse is the CFO panel: spend-vs-outcome totals, the creditable
// (fallback) breakout, cost-per-outcome and the ranked cancellation-risk list.
type valueSummaryResponse struct {
	Dimension                string                `json:"dimension"`
	Since                    string                `json:"since,omitempty"`
	Until                    string                `json:"until,omitempty"`
	TotalCostMicroUSD        int64                 `json:"total_cost_micro_usd"`
	UnattributedCostMicroUSD int64                 `json:"unattributed_cost_micro_usd,omitempty"`
	CreditableMicroUSD       int64                 `json:"creditable_micro_usd"`
	TotalValueMicroUSD       int64                 `json:"total_value_micro_usd"`
	NetValueMicroUSD         int64                 `json:"net_value_micro_usd"`
	TotalOutcomes            int                   `json:"total_outcomes"`
	Satisfied                int                   `json:"satisfied"`
	Unsatisfied              int                   `json:"unsatisfied"`
	SatisfiedRatePct         int                   `json:"satisfied_rate_pct"`
	CostPerOutcomeMicroUSD   int64                 `json:"cost_per_outcome_micro_usd,omitempty"`
	CostPerSatisfiedMicroUSD int64                 `json:"cost_per_satisfied_micro_usd,omitempty"`
	CancellationRisk         []cancellationRiskDTO `json:"cancellation_risk"`
	Note                     string                `json:"note,omitempty"`
	Truncated                bool                  `json:"truncated,omitempty"`
}

// --- query helpers -----------------------------------------------------------

func listQuery(r *http.Request) model.Query {
	q := model.Query{}
	if c := r.URL.Query().Get("cursor"); c != "" {
		q.Cursor = c
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			q.Limit = n
		}
	}
	return q
}

func eq(col, val string) model.Filter { return model.Filter{Column: col, Op: model.OpEq, Value: val} }

// timeParam parses an RFC3339 query param. ok=false with bad=false means the
// param was absent; bad=true means it was present but unparseable — the caller
// must reject that with a 400 rather than silently widening the window to all
// time (which would return the wrong, larger spend).
func timeParam(r *http.Request, key string) (t time.Time, ok bool, bad bool) {
	v := r.URL.Query().Get(key)
	if v == "" {
		return time.Time{}, false, false
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, false, true
	}
	return t, true, false
}

// timeWindow reads the since/until window from the request, returning bad=true
// if either is present-but-invalid.
func timeWindow(r *http.Request) (since time.Time, hasSince bool, until time.Time, hasUntil bool, bad bool) {
	since, hasSince, badSince := timeParam(r, "since")
	until, hasUntil, badUntil := timeParam(r, "until")
	return since, hasSince, until, hasUntil, badSince || badUntil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid request body"))
		return false
	}
	// A BODY IS ONE JSON DOCUMENT (2026-08-06). Decode reads the FIRST value and stops,
	// so `{...}{...}` used to decode the first, silently discard the rest and perform a
	// durable mutation returning 201. Measured against a live engine on the models route,
	// with the created row read back by a separate GET; core/api/render.go has rejected
	// this since it was written, and 21 of the 22 copies of this helper had drifted from
	// it. A concatenation error becomes an apparently correct action, and two layers can
	// disagree about which document the request meant.
	if dec.More() {
		writeJSON(w, http.StatusBadRequest, errorBody("invalid request body"))
		return false
	}
	return true
}

func errorBody(msg string) map[string]any {
	return map[string]any{"error": map[string]string{"message": msg}}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// writeStoreError maps a store error to an HTTP status. Everything except this
// module's own conflict wording is api.StoreErrorStatus (core/api/moduleerrors.go),
// the ONE mapping the whole product shares — see the note there for what the
// thirty-six hand-written copies had drifted into.
func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, nil)
	case errors.Is(err, store.ErrConflict):
		// KEPT LOCAL: this module answers "version conflict" where the shared mapping
		// says "conflict". Two of the thirty-six copies word it this way; centralizing
		// the mapping is not license to change a message on the wire that nothing in
		// the tree tests and a client may be reading.
		writeJSON(w, http.StatusConflict, errorBody("version conflict"))
	default:
		status, msg, _ := api.StoreErrorStatus(err)
		writeJSON(w, status, errorBody(msg))
	}
}

// --- Policy.Spec scalar helpers (JSON-typed values) --------------------------

func specString(m map[string]any, k string) string {
	if s, ok := m[k].(string); ok {
		return s
	}
	return ""
}

func specInt64(m map[string]any, k string) int64 {
	switch v := m[k].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

func specBool(m map[string]any, k string) bool {
	if b, ok := m[k].(bool); ok {
		return b
	}
	return false
}

func specFloats(m map[string]any, k string) []float64 {
	switch v := m[k].(type) {
	case []float64:
		return v
	case []any:
		out := make([]float64, 0, len(v))
		for _, e := range v {
			if f, ok := e.(float64); ok {
				out = append(out, f)
			}
		}
		return out
	}
	return nil
}
