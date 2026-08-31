// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Deterministic FinOps fixtures shaped exactly like the contract — used by the
// component tests and the visual e2e route mocks. All money is integer micro-USD
// (millionths of a dollar); the magnitudes are realistic enterprise spend so the
// charts and compact formatting (e.g. "$48.3K") read like the real product.
import type {
  Alert,
  AllocationResponse,
  Budget,
  BudgetStatus,
  ForecastResponse,
  Recommendation,
  ReconciliationResponse,
  SpendResponse,
  SummaryResponse,
  TrendResponse,
} from './types'

export const summaryFixture: SummaryResponse = {
  since: '2026-05-05T00:00:00Z',
  until: '',
  total_micro_usd: 48_280_000_000,
  input_tokens: 214_000_000,
  output_tokens: 91_000_000,
  samples: 12_840,
  by_model: [
    {
      key: 'claude-opus-4-8',
      cost_micro_usd: 32_050_000_000,
      input_tokens: 120_000_000,
      output_tokens: 60_000_000,
      samples: 4120,
    },
    {
      key: 'gemini-1.5-pro',
      cost_micro_usd: 8_200_000_000,
      input_tokens: 54_000_000,
      output_tokens: 19_000_000,
      samples: 3800,
    },
    {
      key: 'claude-haiku-4-5',
      cost_micro_usd: 4_100_000_000,
      input_tokens: 30_000_000,
      output_tokens: 9_000_000,
      samples: 4100,
    },
    {
      key: 'gemini-1.5-flash',
      cost_micro_usd: 2_070_000_000,
      input_tokens: 10_000_000,
      output_tokens: 3_000_000,
      samples: 820,
    },
  ],
  by_provider: [
    {
      key: 'anthropic',
      cost_micro_usd: 36_150_000_000,
      input_tokens: 150_000_000,
      output_tokens: 69_000_000,
      samples: 8220,
    },
    {
      key: 'google',
      cost_micro_usd: 10_270_000_000,
      input_tokens: 64_000_000,
      output_tokens: 22_000_000,
      samples: 4620,
    },
  ],
  by_agent: [
    {
      key: 'support-triage',
      cost_micro_usd: 20_600_000_000,
      input_tokens: 98_000_000,
      output_tokens: 41_000_000,
      samples: 5200,
    },
    {
      key: 'code-reviewer',
      cost_micro_usd: 15_050_000_000,
      input_tokens: 72_000_000,
      output_tokens: 30_000_000,
      samples: 4100,
    },
    {
      key: 'nightly-batch',
      cost_micro_usd: 10_770_000_000,
      input_tokens: 44_000_000,
      output_tokens: 20_000_000,
      samples: 3540,
    },
  ],
  cache: {
    // 214M total input folds these tiers; the uncached remainder is the difference.
    uncached_input_tokens: 120_000_000,
    cache_read_tokens: 78_000_000,
    cache_creation_1h_tokens: 10_000_000,
    cache_creation_5m_tokens: 6_000_000,
    // $12,400 → compact "$12.4K" (compact kicks in at ≥ $10K).
    savings_micro_usd: 12_400_000_000,
    hit_rate_pct: 36,
  },
  truncated: false,
}

export const trendFixture: TrendResponse = {
  since: '2026-05-05T00:00:00Z',
  until: '',
  days: [
    {
      key: '2026-05-28',
      cost_micro_usd: 1_580_000_000,
      input_tokens: 7_200_000,
      output_tokens: 3_100_000,
      samples: 1420,
    },
    {
      key: '2026-05-29',
      cost_micro_usd: 1_940_000_000,
      input_tokens: 9_000_000,
      output_tokens: 3_900_000,
      samples: 1680,
    },
    {
      key: '2026-05-30',
      cost_micro_usd: 1_720_000_000,
      input_tokens: 8_100_000,
      output_tokens: 3_400_000,
      samples: 1550,
    },
    {
      key: '2026-05-31',
      cost_micro_usd: 2_260_000_000,
      input_tokens: 10_300_000,
      output_tokens: 4_400_000,
      samples: 1900,
    },
    {
      key: '2026-06-01',
      cost_micro_usd: 2_010_000_000,
      input_tokens: 9_400_000,
      output_tokens: 4_000_000,
      samples: 1760,
    },
    {
      key: '2026-06-02',
      cost_micro_usd: 2_420_000_000,
      input_tokens: 11_300_000,
      output_tokens: 4_900_000,
      samples: 2110,
    },
    {
      key: '2026-06-03',
      cost_micro_usd: 2_290_000_000,
      input_tokens: 11_100_000,
      output_tokens: 4_700_000,
      samples: 2420,
    },
  ],
  truncated: false,
}

export const forecastFixture: ForecastResponse = {
  period: 'monthly',
  period_start: '2026-06-01T00:00:00Z',
  now: '2026-06-04T12:00:00Z',
  spend_micro_usd: 12_400_000_000,
  projected_micro_usd: 93_000_000_000,
  samples: 3200,
  method: 'trailing_window',
  window_days: 7,
  daily_run_rate_micro_usd: 3_100_000_000,
  trend_projected_micro_usd: 90_900_000_000,
  confidence_low_micro_usd: 81_200_000_000,
  confidence_high_micro_usd: 100_600_000_000,
  anomalies: [
    {
      day: '2026-06-03',
      spend_micro_usd: 5_900_000_000,
      baseline_micro_usd: 3_100_000_000,
      deviation_sigma: 2.4,
    },
  ],
  truncated: false,
}

export const spendFixture: SpendResponse = {
  dimension: 'service_tier',
  since: '2026-05-05T00:00:00Z',
  until: '',
  total_micro_usd: 48_280_000_000,
  buckets: [
    {
      key: 'standard',
      cost_micro_usd: 30_100_000_000,
      input_tokens: 140_000_000,
      output_tokens: 58_000_000,
      samples: 8200,
    },
    {
      key: 'priority',
      cost_micro_usd: 12_180_000_000,
      input_tokens: 54_000_000,
      output_tokens: 24_000_000,
      samples: 3100,
    },
    {
      key: 'batch',
      cost_micro_usd: 6_000_000_000,
      input_tokens: 20_000_000,
      output_tokens: 9_000_000,
      samples: 1540,
    },
  ],
  truncated: false,
}

export const reconciliationFixture: ReconciliationResponse = {
  since: '2026-06-01T00:00:00Z',
  until: '',
  billed_total_micro_usd: 41_000_000_000,
  estimated_total_micro_usd: 48_280_000_000,
  drift_micro_usd: -7_280_000_000,
  has_billed: true,
  estimated_only_tiers: ['priority'],
  note: 'Some service tiers (e.g. Priority) are not billed via cost_report and remain estimated; their spend is in the estimate total but not the billed total.',
  days: [
    {
      day: '2026-06-01',
      billed_micro_usd: 13_500_000_000,
      estimated_micro_usd: 16_010_000_000,
      drift_micro_usd: -2_510_000_000,
    },
    {
      day: '2026-06-02',
      billed_micro_usd: 14_000_000_000,
      estimated_micro_usd: 16_270_000_000,
      drift_micro_usd: -2_270_000_000,
    },
    {
      day: '2026-06-03',
      billed_micro_usd: 13_500_000_000,
      estimated_micro_usd: 16_000_000_000,
      drift_micro_usd: -2_500_000_000,
    },
  ],
  truncated: false,
}

export const allocationFixture: AllocationResponse = {
  since: '2026-06-01T00:00:00Z',
  until: '',
  allocated_method_id: 'occurrence_weighted_shared_resource',
  allocated_method_details:
    "an agent's cost is split across the resources it accessed, weighted by observed occurrence count; resources accessed by more than one agent are flagged shared for downstream split-cost. Multi-agent allocation is an open FinOps problem — this is a heuristic with explicit assumptions, not a settled cost.",
  agents: [
    {
      agent_ref: 'support-triage',
      resolved: true,
      confidence: 'attributed',
      cost_micro_usd: 20_600_000_000,
      resources: [
        {
          resource_id: 'kb-faq',
          occurrence_count: 1200,
          allocated_micro_usd: 12_400_000_000,
          co_consumer_agents: 2,
          shared: true,
        },
        {
          resource_id: 'crm-tickets',
          occurrence_count: 800,
          allocated_micro_usd: 8_200_000_000,
          co_consumer_agents: 1,
          shared: false,
        },
      ],
    },
    {
      agent_ref: 'pooled-runner',
      resolved: false,
      confidence: 'approximate',
      cost_micro_usd: 4_300_000_000,
      resources: [],
    },
  ],
  note: '',
  truncated: false,
}

export const budgetsFixture: Budget[] = [
  {
    id: 'bdg-global',
    name: 'Monthly cap',
    enabled: true,
    dimension: 'global',
    key: '',
    limit_micro_usd: 100_000_000_000,
    period: 'monthly',
    thresholds: [0.5, 0.8, 1.0],
    currency: 'USD',
    action: 'alert',
  },
  {
    id: 'bdg-opus',
    name: 'Opus guardrail',
    enabled: true,
    dimension: 'model',
    key: 'claude-opus-4-8',
    limit_micro_usd: 30_000_000_000,
    period: 'monthly',
    thresholds: [0.8, 1.0],
    currency: 'USD',
    action: 'block',
    reserved_micro_usd: 5_000_000_000,
  },
]

export const budgetStatusFixtures: Record<string, BudgetStatus> = {
  'bdg-global': {
    id: 'bdg-global',
    name: 'Monthly cap',
    enabled: true,
    dimension: 'global',
    key: '',
    period: 'monthly',
    period_start: '2026-06-01T00:00:00Z',
    currency: 'USD',
    action: 'alert',
    limit_micro_usd: 100_000_000_000,
    spend_micro_usd: 12_400_000_000,
    remaining_micro_usd: 87_600_000_000,
    consumed_pct: 12,
    projected_micro_usd: 93_000_000_000,
    projected_pct: 93,
    over: false,
    samples: 3200,
    truncated: false,
  },
  // On track to exceed: projection > 100% — the at-risk case the flow test asserts.
  // Enforcing (block) budget with reserved capacity counting toward the limit.
  'bdg-opus': {
    id: 'bdg-opus',
    name: 'Opus guardrail',
    enabled: true,
    dimension: 'model',
    key: 'claude-opus-4-8',
    period: 'monthly',
    period_start: '2026-06-01T00:00:00Z',
    currency: 'USD',
    action: 'block',
    limit_micro_usd: 30_000_000_000,
    reserved_micro_usd: 5_000_000_000,
    spend_micro_usd: 24_000_000_000,
    remaining_micro_usd: 1_000_000_000,
    consumed_pct: 96,
    projected_micro_usd: 185_000_000_000,
    projected_pct: 616,
    over: false,
    samples: 4120,
    truncated: false,
  },
}

export const alertsFixture: Alert[] = [
  {
    budget_id: 'bdg-opus',
    dimension: 'model',
    key: 'claude-opus-4-8',
    period: 'monthly',
    period_start: '2026-06-01T00:00:00Z',
    threshold_pct: 80,
    spend_micro_usd: 24_000_000_000,
    limit_micro_usd: 30_000_000_000,
    severity: 'medium',
    triggered_at: '2026-06-03T09:12:00Z',
  },
  {
    budget_id: 'bdg-global',
    dimension: 'global',
    key: '',
    period: 'monthly',
    period_start: '2026-05-01T00:00:00Z',
    threshold_pct: 50,
    spend_micro_usd: 50_000_000_000,
    limit_micro_usd: 100_000_000_000,
    severity: 'low',
    triggered_at: '2026-05-18T18:40:00Z',
  },
]

export const recommendationsFixture: Recommendation[] = [
  {
    kind: 'cheaper_model',
    title:
      'Consider routing eligible claude-opus-4-8 traffic to gemini-1.5-flash',
    detail:
      'Estimate assumes the workload is eligible and does not account for prompt-cache savings. Verify capability and quality fit before routing.',
    severity: 'medium',
    subject: 'claude-opus-4-8',
    estimated_savings_micro_usd: 18_500_000_000,
  },
  {
    kind: 'budget_burn',
    title: 'Opus guardrail is on track to exceed its monthly limit',
    detail:
      'At the current run-rate the budget is projected at ~600% of its limit.',
    severity: 'high',
    subject: 'bdg-opus',
  },
  {
    kind: 'info',
    title:
      'Prompt-cache savings are not derivable from the current cost stream',
    detail:
      'Input tokens fold the cache tiers, so a cache-hit saving cannot be computed — no figure is invented here.',
    severity: 'info',
    subject: 'cache',
  },
]
