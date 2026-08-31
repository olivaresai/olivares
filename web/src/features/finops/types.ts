// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// DTOs for module XI (FinOps), hand-authored 1:1 with the Go response structs
// (modules/finops/{dto.go,analytics.go,allocation.go,schema.go,focus.go}). Money is
// ALWAYS integer `*_micro_usd` (millionths of USD); the view formats to USD
// (lib/format) — it presents, never recomputes (ARCHITECTURE.md). No response carries a
// prompt, completion or credential — aggregates and metadata only.
//
// NOTE: the models/finops UI contract is SUPERSEDED for the Fase-F dimensions;
// these types track the CODE (analytics.go validDimensions + the response structs),
// which is the live contract.

// SpendDimension is the dimension /spend?dimension= slices by — the full set of 15
// queryable read-model columns (modules/finops/schema.go:validDimensions). Open
// (`| string`) because the backend is the source of truth and may add columns.
export type SpendDimension =
  | 'global'
  | 'model'
  | 'provider'
  | 'agent'
  | 'session'
  | 'team'
  | 'project'
  | 'workspace'
  | 'api_key'
  | 'actor'
  | 'service_tier'
  | 'context_window'
  | 'inference_geo'
  | 'gateway'
  | 'cost_type'
  | string

// SERVICE_TIER_HINTS are the 6 service tiers Anthropic documents (ANT2-02:
// standard | batch | priority | priority_on_demand | flex | flex_discount). They are
// DISPLAY HINTS for labelling whatever bucket keys the API returns — service_tier is
// FREE-FORM at the column level, so the picker/table render the real keys, never a
// closed list. New tiers appear as their raw key without a faked label.
export const SERVICE_TIER_HINTS = [
  'standard',
  'batch',
  'priority',
  'priority_on_demand',
  'flex',
  'flex_discount',
] as const

// SPEND_DIMENSIONS is the full set of 15 queryable /spend dimensions, in display
// order (schema.go:validDimensions). The chargeback dimension picker iterates this.
export const SPEND_DIMENSIONS: SpendDimension[] = [
  'global',
  'model',
  'provider',
  'agent',
  'session',
  'team',
  'project',
  'workspace',
  'api_key',
  'actor',
  'service_tier',
  'context_window',
  'inference_geo',
  'gateway',
  'cost_type',
  'cost_center',
]

// BUDGET_DIMENSIONS is SPEND_DIMENSIONS MINUS cost_type — cost_type never accrues on
// the estimated stream budgets aggregate, so the backend rejects it as a budget
// dimension (schema.go:budgetDimensions). The create-budget form iterates this.
export const BUDGET_DIMENSIONS: SpendDimension[] = SPEND_DIMENSIONS.filter(
  (d) => d !== 'cost_type',
)

export type BudgetPeriod = 'daily' | 'weekly' | 'monthly' | 'total'

// Budget enforcement action (modules/finops/schema.go:validActions, FIN-08). `alert`
// is showback-only (the safe default, never enforces); `throttle`/`block` emit a
// hard-cap signal an actuation seam consumes — FinOps emits the signal, it does not
// itself deny. Open for forward-compat.
export type BudgetAction = 'alert' | 'throttle' | 'block' | string

/** One spend bucket (a model, provider, agent…), ordered by cost desc. */
export interface SpendBucket {
  key: string
  cost_micro_usd: number
  input_tokens: number
  output_tokens: number
  samples: number
}

/** GET /spend?dimension= */
export interface SpendResponse {
  dimension: SpendDimension
  since: string
  until: string
  total_micro_usd: number
  buckets: SpendBucket[]
  /** true = aggregate hit the scan ceiling (partial, not an exact total). */
  truncated: boolean
}

/** Prompt-cache efficiency breakdown (analytics.go:cacheSummaryDTO). Token
 *  fields are counts; `savings_micro_usd` is the realized saving from cache READS
 *  (each read costs ~0.1× the base input price, so ~0.9× is saved), priced per-model
 *  from the catalog. `hit_rate_pct` is cache-read tokens as a % of total input — an
 *  honest 0 when nothing is cached. */
export interface CacheSummary {
  uncached_input_tokens: number
  cache_read_tokens: number
  cache_creation_1h_tokens: number
  cache_creation_5m_tokens: number
  savings_micro_usd: number
  hit_rate_pct: number
}

/** GET /spend/summary (analytics.go:summaryResponse) */
export interface SummaryResponse {
  since: string
  until: string
  total_micro_usd: number
  input_tokens: number
  output_tokens: number
  samples: number
  by_model: SpendBucket[]
  by_provider: SpendBucket[]
  by_agent: SpendBucket[]
  cache: CacheSummary
  truncated: boolean
}

/** A single day bucket on the cost trend (key = `YYYY-MM-DD` UTC, asc). */
export interface TrendDay {
  key: string
  cost_micro_usd: number
  input_tokens: number
  output_tokens: number
  samples: number
}

/** GET /spend/trend */
export interface TrendResponse {
  since: string
  until: string
  days: TrendDay[]
  truncated: boolean
}

/** A spend anomaly: a day whose spend deviates materially from the rolling baseline
 *  (analytics.go:anomalyDTO). `deviation_sigma` is how many standard deviations above
 *  the trailing-window mean — a measured outlier, never a prediction. */
export interface ForecastAnomaly {
  day: string
  spend_micro_usd: number
  baseline_micro_usd: number
  deviation_sigma: number
}

/** GET /forecast?period= (analytics.go:forecastResponse). The projection is a
 *  trailing-window daily run-rate (`method` = "trailing_window") with a ±1.96σ
 *  confidence band — a projection AT THE CURRENT RUN-RATE, NOT a forecasting model.
 *  `projected_micro_usd` is the legacy naive elapsed-fraction projection (kept for
 *  continuity); `trend_projected_micro_usd` is the windowed-mean projection. */
export interface ForecastResponse {
  period: BudgetPeriod
  period_start: string
  now: string
  spend_micro_usd: number
  projected_micro_usd: number
  samples: number
  // Trend forecast (trailing-window daily run rate).
  method: string
  window_days: number
  daily_run_rate_micro_usd: number
  trend_projected_micro_usd: number
  confidence_low_micro_usd: number
  confidence_high_micro_usd: number
  anomalies?: ForecastAnomaly[]
  truncated: boolean
}

export type RecommendationKind = 'cheaper_model' | 'budget_burn' | 'info'

/** GET /recommendations */
export interface Recommendation {
  kind: RecommendationKind | string
  title: string
  detail: string
  severity: 'info' | 'low' | 'medium' | 'high' | string
  subject: string
  /** Only on cheaper_model; the caveat lives in `detail` (no cache savings faked). */
  estimated_savings_micro_usd?: number
}

/** A budget definition (core Policy kind="budget", dto.go:budgetDTO + schema.go:
 *  budgetSpec). `action` and `reserved_micro_usd` are the FIN-08 enforcement fields. */
export interface Budget {
  id: string
  name: string
  enabled: boolean
  dimension: SpendDimension
  key: string
  limit_micro_usd: number
  period: BudgetPeriod
  thresholds: number[]
  currency: string
  /** alert (default, showback) | throttle | block (emit hard-cap signal). */
  action: BudgetAction
  /** Committed/reserved capacity counted TOWARD the limit (an accounting line, not a
   *  charge) — omitted/0 when none reserved. */
  reserved_micro_usd?: number
}

/** GET /budgets/{id}/status — consumption vs limit + run-rate projection
 *  (dto.go:budgetStatusDTO). Reserved capacity counts toward the limit: all
 *  consumption signals (consumed_pct, remaining, projection, over) are computed on
 *  effective spend (actual + reserved); `spend_micro_usd` is the raw actual spend. */
export interface BudgetStatus {
  id: string
  name: string
  enabled: boolean
  dimension: SpendDimension
  key: string
  period: BudgetPeriod
  period_start: string
  currency: string
  action: BudgetAction
  limit_micro_usd: number
  /** Reserved capacity counted toward the limit (omitted/0 when none). */
  reserved_micro_usd?: number
  spend_micro_usd: number
  remaining_micro_usd: number
  consumed_pct: number
  projected_micro_usd: number
  projected_pct: number
  /** true = already over the (reservation-adjusted) limit. */
  over: boolean
  samples: number
  truncated: boolean
}

/** A historical budget-threshold crossing (GET /alerts). */
export interface Alert {
  budget_id: string
  dimension: SpendDimension
  key: string
  period: BudgetPeriod
  period_start: string
  threshold_pct: number
  spend_micro_usd: number
  limit_micro_usd: number
  severity: 'low' | 'medium' | 'high' | string
  triggered_at: string
}

/** POST /budgets body (dto.go:budgetDTO). `limit_micro_usd` / `reserved_micro_usd`
 *  are built from USD inputs in the form. `action` defaults server-side to "alert"
 *  but the form always sends it explicitly. NOTE: cost_type is NOT a budget dimension
 *  (schema.go:budgetDimensions excludes it — it never accrues on the estimated
 *  stream), so the form omits it from the dimension select. */
export interface BudgetInput {
  name: string
  dimension: SpendDimension
  key?: string
  limit_micro_usd: number
  period: BudgetPeriod
  thresholds?: number[]
  enabled?: boolean
  action: BudgetAction
  reserved_micro_usd?: number
}

// --- reconciliation (GET /spend/reconciliation) ------------------------------

/** One day's billed-vs-estimated comparison (analytics.go:reconciliationDayDTO).
 *  `drift_micro_usd` = billed − estimated (positive = under-estimated). */
export interface ReconciliationDay {
  day: string
  billed_micro_usd: number
  estimated_micro_usd: number
  drift_micro_usd: number
}

/** GET /spend/reconciliation (analytics.go:reconciliationResponse). Compares the
 *  authoritative billed cost (provider cost_report) against the derived estimate.
 *  The two streams are NEVER summed: `has_billed` tells the UI whether billed truth
 *  exists, `estimated_only_tiers[]` lists tiers (e.g. Priority) that cost_report
 *  cannot bill and which therefore live in the estimate only, and `note` carries the
 *  honest caveat. Surface note + estimated_only_tiers prominently. */
export interface ReconciliationResponse {
  since: string
  until: string
  billed_total_micro_usd: number
  estimated_total_micro_usd: number
  drift_micro_usd: number
  has_billed: boolean
  estimated_only_tiers?: string[]
  note?: string
  days: ReconciliationDay[]
  truncated: boolean
}

// --- multi-agent allocation (GET /spend/allocation) --------------------------

/** One resource an agent's cost was allocated to (allocation.go:allocationResourceDTO). */
export interface AllocationResource {
  resource_id: string
  occurrence_count: number
  allocated_micro_usd: number
  /** distinct agents touching it (incl. this one). */
  co_consumer_agents: number
  shared: boolean
}

/** One agent's cost and its allocation across resources (allocation.go:
 *  allocationAgentDTO). `confidence` is attributed (a concrete agent) or approximate
 *  (a shared/pooled-credential origin — never split to a fabricated agent). */
export interface AllocationAgent {
  agent_ref: string
  resolved: boolean
  confidence: 'attributed' | 'approximate' | string
  cost_micro_usd: number
  resources: AllocationResource[]
}

/** GET /spend/allocation (allocation.go:allocationResponse). The method + its
 *  EXPLICIT assumptions (`allocated_method_details`) and the per-agent allocation.
 *  MANDATORY: `note` / `allocated_method_details` state multi-agent allocation is an
 *  open FinOps problem — a heuristic with explicit assumptions, not a settled cost.
 *  Render that disclaimer; never present the split as authoritative. */
export interface AllocationResponse {
  since: string
  until: string
  allocated_method_id: string
  allocated_method_details: string
  agents: AllocationAgent[]
  note: string
  truncated: boolean
}

// --- FOCUS export (GET /spend/export) ----------------------------------------

/** Provenance mode for the FOCUS export. `estimated` is the granular full-coverage
 *  stream (incl. Priority); `billed` is the cost_report stream; `all` is both, each
 *  row tagged x_Provenance. In `all`, EffectiveCost (FOCUS's canonical SUM column) is
 *  populated ONLY on billed rows so a naive SUM never double-counts — estimated rows
 *  keep their figure in ListCost. Do NOT recompute totals client-side. */
export type ExportProvenance = 'estimated' | 'billed' | 'all'

// --- cost center types ------------------------------------------------

export interface CostCenter {
  id: string
  code: string
  name: string
  description?: string
  owner?: string
  status: 'active' | 'archived'
  metadata?: Record<string, string>
  created_at?: string
  updated_at?: string
}

export interface CostCenterMapping {
  id: string
  cost_center_id: string
  source_dimension: string
  source_key: string
  priority: number
  created_at?: string
  updated_at?: string
}

// --- model rate catalog types -----------------------------------------

export interface ModelRate {
  id: string
  provider: string
  model: string
  input_rate_micro_usd: number
  output_rate_micro_usd: number
  cache_read_rate_micro_usd: number
  cache_creation_rate_micro_usd: number
  effective_from: string
  effective_until?: string
  notes?: string
  created_at?: string
  updated_at?: string
}

// --- model comparison types -------------------------------------------

export interface ModelCost {
  provider: string
  model: string
  input_tokens: number
  output_tokens: number
  cache_read_tokens: number
  actual_micro_usd: number
  rate_micro_usd: number
  delta_micro_usd: number
  savings_pct: number
}

export interface ComparisonResponse {
  source: ModelCost
  targets: ModelCost[]
  since?: string
  until?: string
  total_samples: number
}

export interface Projection {
  model: string
  provider: string
  projected_micro_usd: number
  confidence_low_micro_usd: number
  confidence_high_micro_usd: number
  delta_vs_source_micro_usd: number
  savings_pct: number
}

export interface ComparisonWithProjection {
  retrospective: ComparisonResponse
  projections?: Projection[]
  forecast_period?: string
}

// --- chargeback statement types ---------------------------------------

export interface StatementLine {
  id: string
  statement_id: string
  model_ref: string
  provider_ref: string
  agent_ref?: string
  input_tokens: number
  output_tokens: number
  cost_micro_usd: number
  sample_count: number
}

export interface ChargebackStatement {
  id: string
  cost_center_id: string
  cost_center_code: string
  cost_center_name: string
  period: string
  period_start: string
  period_end: string
  total_micro_usd: number
  line_count: number
  prior_period_total_micro_usd: number
  delta_pct: number
  status: 'draft' | 'final'
  generated_at: string
  lines?: StatementLine[]
  created_at?: string
  updated_at?: string
}

// --- enhanced forecast types ------------------------------------------

export interface DimensionForecast {
  key: string
  spend_micro_usd: number
  ewa_daily_rate_micro_usd: number
  ewa_projected_micro_usd: number
  ewa_confidence_low_micro_usd: number
  ewa_confidence_high_micro_usd: number
  samples: number
}

export interface EnhancedForecastResponse extends ForecastResponse {
  ewa_daily_rate_micro_usd: number
  ewa_projected_micro_usd: number
  ewa_confidence_low_micro_usd: number
  ewa_confidence_high_micro_usd: number
  ewa_alpha: number
  dimension_forecasts?: DimensionForecast[]
}

export interface EnhancedBudgetStatus extends BudgetStatus {
  exhaustion_days_remaining: number
  exhaustion_confidence?: 'high' | 'medium' | 'low'
}

// ── C07-04 · las doce rutas de finops que la consola nunca llamaba ───────────────────
//
// Medido el 2026-08-17: `modules/finops/api.go:52-113` registra 42 rutas y el cliente escrito
// a mano llamaba 30. Entre las doce que faltaban está el **panel del CFO** entero: gasto frente
// a resultados, coste por resultado, y la lista de **riesgo de cancelación** — sujetos que
// queman presupuesto sin producir resultados satisfechos.

/** Un resultado calificado: el sustrato de la atribución de valor. */
export interface Outcome {
  subject_kind: string
  subject_ref: string
  outcome_ref?: string
  verdict: string
  value_micro_usd?: number
  occurred_at: string
  source?: string
  agent_ref?: string
  identity_ref?: string
  session_ref?: string
}

/**
 * Un cubo del desglose coste-por-resultado.
 *
 * ⛔ `has_outcomes` NO es `outcomes > 0`, y confundirlos es el defecto que este tipo existe para
 * no cometer. `has_outcomes: false` significa **«no tenemos datos de resultado para esto»**;
 * `outcomes: 0` con `has_outcomes: true` significa **«los medimos y salieron cero»**. Un panel
 * que los funda pinta un coste-por-resultado de algo que nunca midió.
 */
export interface ValueBucket {
  key: string
  cost_micro_usd: number
  creditable_micro_usd?: number
  value_micro_usd: number
  net_value_micro_usd: number
  outcomes: number
  satisfied: number
  unsatisfied: number
  cost_per_outcome_micro_usd?: number
  cost_per_satisfied_micro_usd?: number
  satisfied_rate_pct: number
  has_outcomes: boolean
  eval_pass_rate_pct?: number
  cancellation_risk: boolean
  risk_reason?: string
}

/**
 * El desglose por dimensión sobre una ventana.
 *
 * ⛔ `total_cost_micro_usd` ES EL GASTO ENTERO: los cubos atribuidos MÁS
 * `unattributed_cost_micro_usd`. El motor lo dice con todas las letras en `dto.go:236-238`
 * — `sum(buckets[].cost_micro_usd) + unattributed == total`. **Una pantalla que sume los cubos
 * y llame a eso «el gasto» infra-declara la factura**, y lo hace en la dirección cómoda.
 */
export interface ValueBreakdown {
  dimension: string
  since?: string
  until?: string
  total_cost_micro_usd: number
  unattributed_cost_micro_usd?: number
  total_value_micro_usd: number
  net_value_micro_usd: number
  total_outcomes: number
  buckets: ValueBucket[]
  truncated?: boolean
}

/** Un sujeto que quema gasto sin resultados satisfechos. */
export interface CancellationRisk {
  dimension: string
  key: string
  cost_micro_usd: number
  outcomes: number
  satisfied: number
  reason: string
}

/** El panel del CFO: totales, coste por resultado y la lista de riesgo ordenada. */
export interface ValueSummary {
  dimension: string
  since?: string
  until?: string
  total_cost_micro_usd: number
  unattributed_cost_micro_usd?: number
  creditable_micro_usd: number
  total_value_micro_usd: number
  net_value_micro_usd: number
  total_outcomes: number
  satisfied: number
  unsatisfied: number
  satisfied_rate_pct: number
  cost_per_outcome_micro_usd?: number
  cost_per_satisfied_micro_usd?: number
  cancellation_risk: CancellationRisk[]
  note?: string
  truncated?: boolean
}
