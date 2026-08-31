// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Executive dashboards (module XXI) — the rollup layer. These PURE functions take the
// SAME data the technical views already fetch and SUMMARIZE it into the
// leadership KPIs the dashboard leads with: a headline number, a trend, a status mix.
//
// They AGGREGATE, they never RECOMPUTE (ARCHITECTURE.md): the modules own the math (cost in
// integer micro-USD, finding severity, control status, run score, health state); here
// we only count, sum and roll up what is already decided upstream. Every honesty seam
// the sources carry is preserved, not smoothed over: a `truncated`
// aggregate stays flagged; a `degraded` red-team run is NEVER counted as a pass;
// `approximate`/`opaque` access coverage is surfaced as a limit; compliance keeps its
// disclaimer and never reads as "compliant". Pure + input-tolerant so the view can gate
// each pillar by RBAC and pass only what the role may read.
import type {
  ForecastResponse,
  SpendBucket,
  SummaryResponse,
  TrendResponse,
} from '@/features/finops/types'
import type { GovernedModel } from '@/features/models/types'
import type { Finding } from '@/features/security/types'
import type { Run, RunStatus } from '@/features/redteam/types'
import type { DiffResponse } from '@/features/access-map/types'
import type {
  ComplianceSummaryResponse,
  FrameworkRollup,
  RiskClassification,
  RiskTier,
} from '@/features/compliance/types'
import type { IncidentDTO, StatusDTO } from '@/features/health/types'
import type { InventorySummary } from '@/features/inventory/types'
import type { LiveDTO } from '@/features/sessions/types'
import type { ListResponse } from '@/lib/api/types'

// --- cost (FinOps + Models X) --------------------------------------------

export interface CostKpi {
  totalMicroUsd: number
  inputTokens: number
  outputTokens: number
  samples: number
  /** Per-day cost series for a sparkline / area (ascending). */
  trend: { key: string; cost: number }[]
  /** Period-over-period change (recent half vs prior half of the trend), or null
   *  when there is not enough history to be honest about a delta. */
  deltaPct: number | null
  projectedMicroUsd: number | null
  /** The run-rate projection exceeds spend-to-date (heads toward more this period). */
  projectedOver: boolean
  activeModels: number | null
  /** Any contributing aggregate hit the scan ceiling — the figure is a floor. */
  truncated: boolean
}

/** Split a trend in two equal halves and compare the sums. A run-rate-free,
 *  honest period delta; null unless both halves carry spend.
 *
 *  The halves must hold the SAME NUMBER OF DAYS or the comparison measures the
 *  window instead of the spend: with an odd-length series a floor-split gave the
 *  recent half one extra day, so flat spend read as a rise (+50% over five flat
 *  days). When the count is odd the OLDEST day is dropped rather than shared,
 *  which keeps the window ending at today — the end a reader cares about. For an
 *  even count this is the previous behaviour exactly. */
export function trendDeltaPct(
  days: { cost_micro_usd: number }[],
): number | null {
  if (days.length < 4) return null
  const half = Math.floor(days.length / 2)
  const paired = days.slice(days.length - half * 2)
  const prior = paired.slice(0, half).reduce((s, d) => s + d.cost_micro_usd, 0)
  const recent = paired.slice(half).reduce((s, d) => s + d.cost_micro_usd, 0)
  if (prior <= 0) return null
  return ((recent - prior) / prior) * 100
}

export function deriveCost(
  summary?: SummaryResponse,
  trend?: TrendResponse,
  forecast?: ForecastResponse,
  models?: ListResponse<GovernedModel>,
): CostKpi | null {
  if (!summary) return null
  // Home and FinOps must project the SAME way: both read
  // `trend_projected_micro_usd` (the trailing-window run-rate). Reading
  // `projected_micro_usd` here — the legacy naive elapsed-fraction projection —
  // put two different numbers for the same spend on two screens ($5,151 vs
  // $5,039 in P's repro), AND made this card's own caption false: it says "at
  // current run-rate" while the naive field is not the run-rate method.
  const projectedMicroUsd = forecast?.trend_projected_micro_usd ?? null
  return {
    totalMicroUsd: summary.total_micro_usd,
    inputTokens: summary.input_tokens,
    outputTokens: summary.output_tokens,
    samples: summary.samples,
    trend: (trend?.days ?? []).map((d) => ({
      key: d.key,
      cost: d.cost_micro_usd,
    })),
    deltaPct: trend ? trendDeltaPct(trend.days) : null,
    projectedMicroUsd,
    // The badge compares the SAME field the card displays. Leaving this on the
    // naive projection would have shown a trend number under a naive
    // over-budget warning — a partial fix is worse than none here.
    projectedOver:
      forecast !== undefined &&
      forecast.trend_projected_micro_usd > forecast.spend_micro_usd,
    activeModels: models
      ? models.items.filter((m) => m.status === 'active').length
      : null,
    truncated:
      !!summary.truncated || !!trend?.truncated || !!forecast?.truncated,
  }
}

// --- usage (Inventory + Sessions II) -------------------------------------

export interface UsageKpi {
  activeAgents: number
  totalAgents: number
  totalEntities: number
  /** Live sessions by control-channel state (II). */
  liveActive: number
  liveIdle: number
  /** Gone silent inside its cadence — a possible-evasion signal, surfaced not hidden. */
  silentEvasion: number
  liveTotal: number
  truncated: boolean
}

export function deriveUsage(
  inventory?: InventorySummary,
  live?: ListResponse<LiveDTO>,
): UsageKpi | null {
  if (!inventory && !live) return null
  const agent = inventory?.by_kind?.agent
  const items = live?.items ?? []
  return {
    activeAgents: agent?.active ?? 0,
    totalAgents: agent?.total ?? 0,
    totalEntities: inventory?.total ?? 0,
    liveActive: items.filter((s) => s.cc_state === 'active').length,
    liveIdle: items.filter((s) => s.cc_state === 'idle').length,
    silentEvasion: items.filter((s) => s.cc_state === 'silent_evasion').length,
    liveTotal: items.length,
    truncated: !!inventory?.truncated,
  }
}

// --- risk (Security IX + Red-team XVIII + Access-map III) ---------------------

export type Severity = 'critical' | 'high' | 'medium' | 'low' | 'info'
export const SEVERITY_ORDER: Severity[] = [
  'critical',
  'high',
  'medium',
  'low',
  'info',
]

export interface RiskKpi {
  bySeverity: Record<Severity, number>
  openFindings: number
  /** critical + high — the figure a decision-maker acts on first. */
  criticalHigh: number
  robustness: {
    /** 0..100 — only meaningful when `status === 'completed'`. */
    score: number | null
    status: RunStatus | null
    /** The latest run could not run its probes (sandbox pending) — NOT a pass. */
    degraded: boolean
  }
  drift: {
    /** Reconciled, firm unexpected accesses (act on these). */
    unexpectedFirm: number
    /** Unexpected accesses awaiting an identity link — honest amber, not red. */
    unexpectedPending: number
    /** Over-provisioned grants never observed (least-privilege opportunity). */
    unused: number
  }
  /** Coverage limits behind the risk figures: some access is observed
   *  approximately or with lossy/opaque capture — the drift count is a lower bound. */
  coverageLimited: number
}

function emptySeverity(): Record<Severity, number> {
  return { critical: 0, high: 0, medium: 0, low: 0, info: 0 }
}

/** The most recent run by finish time (ties broken by array order). */
export function latestRun(runs: Run[]): Run | null {
  let best: Run | null = null
  for (const r of runs) {
    if (!best) {
      best = r
      continue
    }
    const a = Date.parse(r.finished_at ?? '')
    const b = Date.parse(best.finished_at ?? '')
    if (!Number.isNaN(a) && (Number.isNaN(b) || a > b)) best = r
  }
  return best
}

export function deriveRisk(
  findings?: ListResponse<Finding>,
  runs?: ListResponse<Run>,
  drift?: DiffResponse,
): RiskKpi | null {
  if (!findings && !runs && !drift) return null

  const bySeverity = emptySeverity()
  let openFindings = 0
  for (const f of findings?.items ?? []) {
    // Leadership view counts OPEN risk — resolved/dismissed findings are not a posture.
    if (f.status === 'resolved' || f.status === 'dismissed') continue
    openFindings++
    const key = (f.severity ?? '').toLowerCase()
    if (key in bySeverity) bySeverity[key as Severity]++
  }

  const run = runs ? latestRun(runs.items) : null
  const completed = run?.status === 'completed'

  const unexpected = drift?.unexpected_accesses ?? []
  const unexpectedPending = unexpected.filter(
    (d) => d.reconciliation_pending,
  ).length
  const coverageLimited = [
    ...unexpected,
    ...(drift?.unused_grants ?? []),
  ].filter(
    (d) =>
      d.edge?.confidence === 'approximate' ||
      d.edge?.coverage_tier === 'lossy' ||
      d.edge?.coverage_tier === 'opaque' ||
      d.edge?.coverage_tier === 'mixed',
  ).length

  return {
    bySeverity,
    openFindings,
    criticalHigh: bySeverity.critical + bySeverity.high,
    robustness: {
      score: completed ? run!.score : null,
      status: run?.status ?? null,
      degraded: run != null && run.status !== 'completed',
    },
    drift: {
      unexpectedFirm: (drift?.unexpected_count ?? 0) - unexpectedPending,
      unexpectedPending,
      unused: drift?.unused_count ?? 0,
    },
    coverageLimited,
  }
}

// --- compliance (XIII /) --------------------------------------------------

export const CONTROL_STATUSES = [
  'satisfied',
  'by_design',
  'partial',
  'gap',
  'unmapped',
] as const
export type ControlStatus = (typeof CONTROL_STATUSES)[number]

export interface ComplianceKpi {
  total: number
  satisfied: number
  byDesign: number
  partial: number
  gap: number
  unmapped: number
  /** (satisfied + by_design) / total — control COVERAGE, never "% compliant".
   *  unmapped is kept in the denominator so the figure can't inflate. null if no
   *  controls are mapped yet. */
  coveredPct: number | null
  /** Per-framework rollups, passed through for the status bars. */
  frameworks: FrameworkRollup[]
  /** Agent EU-AI-Act risk-tier counts (effective tier). */
  riskTiers: Record<RiskTier, number>
  /** Always rendered, never hidden (docs/SECURITY-HARDENING.md).*/
  disclaimer: string
}

export function deriveCompliance(
  summary?: ComplianceSummaryResponse,
  risk?: ListResponse<RiskClassification>,
): ComplianceKpi | null {
  if (!summary) return null
  const acc = {
    total: 0,
    satisfied: 0,
    by_design: 0,
    partial: 0,
    gap: 0,
    unmapped: 0,
  }
  for (const fw of summary.frameworks) {
    acc.total += fw.summary.total
    acc.satisfied += fw.summary.satisfied
    acc.by_design += fw.summary.by_design
    acc.partial += fw.summary.partial
    acc.gap += fw.summary.gap
    acc.unmapped += fw.summary.unmapped
  }
  const riskTiers: Record<RiskTier, number> = {
    unacceptable: 0,
    high: 0,
    limited: 0,
    minimal: 0,
  }
  for (const r of risk?.items ?? []) {
    if (r.tier in riskTiers) riskTiers[r.tier]++
  }
  return {
    total: acc.total,
    satisfied: acc.satisfied,
    byDesign: acc.by_design,
    partial: acc.partial,
    gap: acc.gap,
    unmapped: acc.unmapped,
    coveredPct:
      acc.total > 0
        ? ((acc.satisfied + acc.by_design) / acc.total) * 100
        : null,
    frameworks: summary.frameworks,
    riskTiers,
    disclaimer: summary.disclaimer,
  }
}

// --- reliability (Health & SLA XXII /) -----------------------------------

export interface HealthKpi {
  healthy: number
  degraded: number
  down: number
  /** No check tracks the subject — NOT "healthy". */
  unknown: number
  total: number
  /** Subjects with an open SLA breach right now. */
  slaBreaches: number
  openIncidents: number
}

export function deriveHealth(
  status?: ListResponse<StatusDTO>,
  incidents?: ListResponse<IncidentDTO>,
): HealthKpi | null {
  if (!status && !incidents) return null
  const c = { healthy: 0, degraded: 0, down: 0, unknown: 0, slaBreaches: 0 }
  for (const s of status?.items ?? []) {
    if (s.state === 'healthy') c.healthy++
    else if (s.state === 'degraded') c.degraded++
    else if (s.state === 'down') c.down++
    else c.unknown++
    if (s.sla_breach_open) c.slaBreaches++
  }
  const openIncidents = (incidents?.items ?? []).filter(
    (i) => i.state === 'open',
  ).length
  return {
    healthy: c.healthy,
    degraded: c.degraded,
    down: c.down,
    unknown: c.unknown,
    total: status?.items?.length ?? 0,
    slaBreaches: c.slaBreaches,
    openIncidents,
  }
}

// --- shared: ranked spend buckets (org/team/project summaries) ----------------

/** Top-N spend buckets, descending, for a per-dimension leadership breakdown. */
export function topBuckets(buckets: SpendBucket[], n = 6): SpendBucket[] {
  return [...buckets]
    .sort((a, b) => b.cost_micro_usd - a.cost_micro_usd)
    .slice(0, n)
}
