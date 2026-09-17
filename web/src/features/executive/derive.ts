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
// The ONE rule for "this list is a page with rows beyond it" (`has_more === true`),
// deep-imported the way the features that show the badge do. Writing the predicate
// here again would be the thirteenth hand copy that module exists to end.
import { listaRecortada } from '@/features/_intel/notices'

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

/**
 * TWO HALVES, EACH ANSWERING FOR ITSELF. The usage pillar is the only rollup that
 * joins two sources a role may hold separately and that fail separately. Until
 * 2026-09-08 a missing half was folded into the other: an absent live page became an
 * empty list and "0 live", an absent inventory became "0 agents" — a denied read, a
 * pending read and a failed read all printed the same figure as an estate with
 * nothing in it. Root confirmed the defect after the tenant-wide correction landed.
 *
 * `null` on a half means THAT HALF WAS NOT ESTABLISHED: the caller had no current,
 * permitted, successful answer to hand in. A present half with nothing in it — a
 * summary with no `agent` kind, a live page with no rows — is a real zero and stays a
 * number. The rollup does not know WHY a half is missing (that is the query's state,
 * owned by the view that reads it); it only refuses to invent the number.
 */
export interface UsageKpi {
  /** Inventory half (summary). `null` when the summary was not established.*/
  activeAgents: number | null
  totalAgents: number | null
  totalEntities: number | null
  /** Live half (II), by control-channel state. `null` when the live page was not
   *  established. */
  liveActive: number | null
  liveIdle: number | null
  /** Gone silent inside its cadence — a possible-evasion signal, surfaced not hidden. */
  silentEvasion: number | null
  liveTotal: number | null
  /** The inventory aggregate hit the scan ceiling — its figures are a floor. False
   *  when that half is unknown: there is no figure to qualify. It describes Inventory
   *  alone: it says nothing about the live half. */
  truncated: boolean
  /** THE LIVE PAGE HAD ROWS BEYOND IT — `GET /v1/m/sessions/live` answers with ONE
   *  most-recent page (the store reads limit+1 rows, default 100 and ceiling 1000, and
   *  sets `has_more` when the extra row existed: core/internal/store/sqlstore/generic.go,
   *  modules/sessions/api.go handleListLive), and every live figure above is counted
   *  over that page. On such a page each of them is a FLOOR of the population, never
   *  the population. Decided by the badge rule (`has_more === true`), so a transport
   *  flag that is not the boolean does not read as a truncated page. False when the
   *  live half is unknown — nothing to qualify — and false is NOT a completeness
   *  claim: a page without `has_more` is what the contract returned, and this layer
   *  adds no guarantee to it. It describes Sessions alone: Inventory's `truncated`
   *  above is another source's marker, and neither borrows the other's. */
  livePartial: boolean
}

/**
 * THE ANSWER A ROLLUP MAY READ RIGHT NOW: the query's CURRENT successful data, and
 * only while the role may read the source. Anything else is `undefined` — not an
 * empty page — so the rollup marks the half unknown instead of counting nothing:
 *
 *  · not permitted: a query that was read before the permission went is disabled,
 *    but TanStack keeps its last data on the object; showing it would expose rows the
 *    role may no longer read, so the permission is checked here and not only in
 *    `enabled`;
 *  · pending: no answer yet;
 *  · failed after a success: the query keeps the retired data beside `isError`; a
 *    refusal or an outage must not keep painting a figure that is no longer current.
 *
 * It is a predicate over the query object the view already holds, not an
 * availability layer: the view keeps deciding skeletons, hints and exclusions from
 * the same object.
 */
export function currentAnswer<T>(
  permitted: boolean,
  query: { isSuccess: boolean; data: T | undefined },
): T | undefined {
  return permitted && query.isSuccess ? query.data : undefined
}

export function deriveUsage(
  inventory?: InventorySummary,
  live?: ListResponse<LiveDTO>,
): UsageKpi {
  // A present summary with no `agent` kind is an estate with no agents: a real zero.
  const agent = inventory?.by_kind?.agent
  const count = (state: string) =>
    live ? live.items.filter((s) => s.cc_state === state).length : null
  return {
    activeAgents: inventory ? (agent?.active ?? 0) : null,
    totalAgents: inventory ? (agent?.total ?? 0) : null,
    totalEntities: inventory ? inventory.total : null,
    liveActive: count('active'),
    liveIdle: count('idle'),
    silentEvasion: count('silent_evasion'),
    liveTotal: live ? live.items.length : null,
    truncated: !!inventory?.truncated,
    // `live` is already the CURRENT successful answer or nothing (`currentAnswer`),
    // so the rule's `!error` half is idle here; the `has_more === true` half is the one
    // that decides, and it is the same one the list badges apply.
    livePartial: listaRecortada({ data: live }),
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
