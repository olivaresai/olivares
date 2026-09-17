// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Executive dashboards (module XXI) — the container. It wires the SAME read hooks the
// technical views use, gates each leadership pillar by the corresponding
// RBAC read permission (a reader who can't see /finops never sees the cost KPI — and
// the exported PDF therefore can't leak it either, docs/SECURITY-HARDENING.md), rolls the data up with
// derive.ts, and composes the pure pieces. It computes NO metric (ARCHITECTURE.md): the
// modules own the math; this aggregates and presents. Multi-tenant: every query is
// scoped to the active tenant, so the org switcher re-scopes the whole dashboard.
import { useEffect, useMemo, useState } from 'react'
import {
  useQuery,
  useQueryClient,
  type UseQueryResult,
} from '@tanstack/react-query'
import { BarChart3, FileDown } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import {
  AsyncSection,
  CaveatNotice,
  IntelPage,
  SectionCard,
  StatGrid,
} from '@/features/_intel'
import { finopsApi, finopsKeys } from '@/features/finops/api'
import type { SpendDimension } from '@/features/finops/types'
import { modelsApi, modelsKeys } from '@/features/models/api'
import { securityApi, securityKeys } from '@/features/security/api'
import { redteamApi, redteamKeys } from '@/features/redteam/api'
import { accessMapApi, accessMapKeys } from '@/features/access-map/api'
import { complianceApi, complianceKeys } from '@/features/compliance/api'
import { healthApi, healthKeys } from '@/features/health/api'
import { inventoryApi, inventoryKeys } from '@/features/inventory/api'
import { sessionsApi, sessionsKeys } from '@/features/sessions/api'
import {
  currentAnswer,
  deriveCompliance,
  deriveCost,
  deriveHealth,
  deriveRisk,
  deriveUsage,
} from './derive'
import {
  ComplianceSection,
  KpiTiles,
  ReliabilitySection,
  RiskSection,
  SpendBreakdownChart,
  SpendSection,
} from './components'
import { ReportFooter, ReportHeader } from './report'
import './i18n'

const RANGE_DAYS: Record<string, number> = { '7d': 7, '30d': 30, '90d': 90 }
const RANGE_IDS = ['7d', '30d', '90d', 'mtd'] as const

/**
 * WHY A USAGE HALF HAS NO FIGURE, read from the query the view already holds — or null
 * while the half is current. The first two answers are, on purpose, the same two the
 * design system draws at section level (AsyncSection): a permission boundary is calm
 * and named as such, whether the role lacks the read or the engine refused it; a
 * failure is "could not load". A read that is still FETCHING is not here: the headline
 * block is skeletons while a permitted usage read is loading, unchanged.
 *
 * Two more answers since 2026-09-08 (the residual cut of
 * dashboard-coverage-and-pending-states), for a permitted half that is `pending` but
 * NOT fetching — until then it fell through this function as null and the pillar
 * printed "—" with no line and no skeleton:
 *  · `pendingPaused` — the read was dispatched and is paused (TanStack pauses a fetch
 *                      in `networkMode: 'online'` while its `onlineManager` reports
 *                      offline, and resumes it when that changes);
 *  · `pendingIdle`   — the read has not started (a dispatch cancelled and reverted, a
 *                      query that never ran).
 * Both are read from the flags the observer exposes and from nothing else. A pause is
 * NOT evidence of an outage, a refusal or a lost connection — the flag says the client
 * is waiting, not why — so neither borrows the "couldn't load" or "restricted" copy.
 * Order matters: an error keeps its answer even while its retry is paused (a retired
 * answer is not repainted as current), and a SUCCESS whose refetch is paused is not
 * pending at all — it keeps its figure and its markers, so it is null here.
 */
type UsageGap =
  'restricted' | 'unavailable' | 'pendingPaused' | 'pendingIdle' | null

function usageGap(
  permitted: boolean,
  query: {
    isPending: boolean
    isError: boolean
    error: unknown
    fetchStatus: 'fetching' | 'paused' | 'idle'
  },
): UsageGap {
  if (!permitted) return 'restricted'
  if (query.isError) {
    return query.error instanceof ApiError && query.error.isForbidden
      ? 'restricted'
      : 'unavailable'
  }
  if (!query.isPending) return null
  if (query.fetchStatus === 'paused') return 'pendingPaused'
  if (query.fetchStatus === 'idle') return 'pendingIdle'
  return null
}

/**
 * THE ONE STATEMENT THE LIVE REGION MAKES ABOUT A USAGE HALF — availability first,
 * then coverage. A pure function of the gap, the query and the rollup's own partial
 * flag as they are right now: no memory of the previous poll, tenant or role. The text
 * built from it carries NO figure, so a poll that returns the same state renders the
 * same string and React leaves the DOM text node untouched — the region announces a
 * change of state, never a change of number.
 */
type UsageAvailability =
  Exclude<UsageGap, null> | 'loading' | 'partial' | 'current'

function usageAvailability(
  gap: UsageGap,
  query: { isLoading: boolean },
  partial: boolean,
): UsageAvailability {
  if (gap) return gap
  if (query.isLoading) return 'loading'
  return partial ? 'partial' : 'current'
}

function sinceFor(rangeId: string): string {
  if (rangeId === 'mtd') {
    const now = new Date()
    return new Date(
      Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), 1),
    ).toISOString()
  }
  const days = RANGE_DAYS[rangeId] ?? 30
  return new Date(Date.now() - days * 86_400_000).toISOString()
}

export function ExecutiveView() {
  const { t } = useTranslation(['executive', 'nav', 'common'])
  const { activeTenant, can } = useAuth()
  const queryClient = useQueryClient()
  const [rangeId, setRangeId] = useState('30d')
  const params = useMemo(() => ({ since: sinceFor(rangeId) }), [rangeId])

  // RBAC: gate every pillar by the same read permission its nav item uses.
  const canFinops = can('finops:spend:read')
  const canModels = can('models:catalog:read')
  const canInventory = can('inventory:catalog:read')
  const canSessions = can('sessions:live:read')
  const canSecurity = can('security:finding:read')
  const canRedteam = can('redteam:run:read')
  const canAccessMap = can('accessmap:graph:read')
  const canCompliance = can('compliance:framework:read')
  const canHealth = can('health:status:read')

  const canUsage = canInventory || canSessions
  const canRisk = canSecurity || canRedteam || canAccessMap

  // --- queries (only the permitted ones run) ---------------------------------
  const costSummaryQ = useQuery({
    queryKey: finopsKeys.summary(activeTenant, params),
    queryFn: () => finopsApi.summary(params),
    enabled: canFinops,
  })
  const costTrendQ = useQuery({
    queryKey: finopsKeys.trend(activeTenant, params),
    queryFn: () => finopsApi.trend(params),
    enabled: canFinops,
  })
  const forecastQ = useQuery({
    queryKey: finopsKeys.forecast(activeTenant, 'monthly'),
    queryFn: () => finopsApi.forecast('monthly'),
    enabled: canFinops,
  })
  const modelsQ = useQuery({
    queryKey: modelsKeys.models(activeTenant),
    queryFn: () => modelsApi.models({ tenant: activeTenant }),
    enabled: canModels,
  })
  // ⛔ THE INVENTORY READ IS TENANT-WIDE, AND THE USAGE TILE SAYS SO. `GET /v1/m/inventory/summary`
  //    reads no request filter and the catalog carries no workspace lineage
  //    (modules/inventory/api.go:123, schema.go:67; ratified 2026-09-08 in
  //    assessments/product/inventory-effective-workspace-scope). Until today this query still
  //    sent `workspace_id` and keyed on the topbar selection, so a W1→W2 switch re-fetched the
  //    SAME tenant-wide summary and reported it as the new workspace's agent count and
  //    "tracked" total — beside a "live" figure whose note already said it was tenant-wide.
  //    Keyed by tenant only now (the very cache entry InventoryView reads), still gated by
  //    `inventory:catalog:read`; the note the tile carries names this source too. The topbar
  //    selector itself is untouched: other views do filter by it.
  const inventoryQ = useQuery({
    queryKey: inventoryKeys.summary(activeTenant),
    queryFn: () => inventoryApi.summary(),
    enabled: canInventory,
  })
  // ⛔ THE LIVE-SESSIONS READ IS TENANT-WIDE, AND THE USAGE TILE SAYS SO. `GET /v1/m/sessions/live`
  //    takes no core-workspace selector and neither DTO carries one (the ratified contract of
  //    2026-09-08, assessments/product/sessions-effective-context-contract). Until 2026-09-08
  //    this query still sent `workspace_id` and keyed on the selection, so a workspace switch
  //    re-fetched the SAME tenant-wide page and reported it as the new workspace's "live"
  //    figure. Only the ignored filter went: the read stays pinned to the tenant, gated by
  //    `sessions:live:read`, and the note the tile carries describes SCOPE — not a grant, not
  //    completeness.
  const sessionsQ = useQuery({
    queryKey: sessionsKeys.live(activeTenant),
    queryFn: () => sessionsApi.live(),
    enabled: canSessions,
  })
  const findingsQ = useQuery({
    queryKey: securityKeys.findings(activeTenant),
    queryFn: () => securityApi.findings(),
    enabled: canSecurity,
  })
  const runsQ = useQuery({
    queryKey: redteamKeys.runs(activeTenant),
    queryFn: () => redteamApi.runs(),
    enabled: canRedteam,
  })
  const driftQ = useQuery({
    queryKey: accessMapKeys.drift(activeTenant),
    queryFn: () => accessMapApi.drift(),
    enabled: canAccessMap,
  })
  const complianceSummaryQ = useQuery({
    queryKey: complianceKeys.summary(activeTenant),
    queryFn: () => complianceApi.summary(),
    enabled: canCompliance,
  })
  const complianceRiskQ = useQuery({
    queryKey: complianceKeys.risk(activeTenant),
    queryFn: () => complianceApi.risk(),
    enabled: canCompliance,
  })
  const healthStatusQ = useQuery({
    queryKey: healthKeys.status(activeTenant),
    queryFn: () => healthApi.status(),
    enabled: canHealth,
  })
  const incidentsQ = useQuery({
    queryKey: healthKeys.incidents(activeTenant),
    queryFn: () => healthApi.incidents(),
    enabled: canHealth,
  })

  // ⛔ A PAUSED READ OUTLIVES THE PERMISSION THAT DISPATCHED IT unless it is cancelled.
  //    `enabled: false` stops a query from STARTING; it does not stop one that is already
  //    dispatched and paused (query-core `Query.onOnline` continues the paused retryer
  //    for every query in the cache, whatever its observers' `enabled` says). So a usage
  //    read paused while the role could make it would still go out when the client came
  //    back online after the read was withdrawn. Its answer would never be shown —
  //    `currentAnswer` below checks the permission — but the request itself would be a
  //    read the role no longer holds. Cancelling with TanStack's default `revert` puts the
  //    query back to pending/idle, where a later re-grant starts it afresh through
  //    `enabled` as usual. Scoped to the exact key, so no other view's query is touched;
  //    a no-op when nothing is in flight or the key has no entry (a role that never had
  //    the read). A tenant change needs none of this: the observer moves to the new key
  //    and query-core cancels an orphaned paused read that was still PENDING itself
  //    (`removeObserver`). A paused REFETCH of an answer the old tenant had already
  //    accepted is different: query-core lets it complete under the old key once online
  //    (measured in the tests), which is a read the role still holds for that tenant and
  //    one that `currentAnswer` never shows under the new one.
  useEffect(() => {
    if (canSessions) return
    void queryClient.cancelQueries({
      queryKey: sessionsKeys.live(activeTenant),
      exact: true,
    })
  }, [canSessions, activeTenant, queryClient])
  useEffect(() => {
    if (canInventory) return
    void queryClient.cancelQueries({
      queryKey: inventoryKeys.summary(activeTenant),
      exact: true,
    })
  }, [canInventory, activeTenant, queryClient])

  // --- rollups (aggregate only; modules own the math) ------------------------
  const cost = canFinops
    ? deriveCost(
        costSummaryQ.data,
        costTrendQ.data,
        forecastQ.data,
        modelsQ.data,
      )
    : null
  // Each usage half is handed in only as its CURRENT, permitted, successful answer:
  // a denied, pending or failed half comes back `null` from the rollup — never as an
  // empty page counted to 0 — and data a role may no longer read is not reused. The
  // pillar itself still exists only for a role that may read at least one half.
  const usage = canUsage
    ? deriveUsage(
        currentAnswer(canInventory, inventoryQ),
        currentAnswer(canSessions, sessionsQ),
      )
    : null
  const liveGap = usageGap(canSessions, sessionsQ)
  const inventoryGap = usageGap(canInventory, inventoryQ)
  const gapText = (gap: Exclude<UsageGap, null>, source: string): string => {
    switch (gap) {
      case 'restricted':
        return t('pillars.sourceRestricted', { source })
      case 'unavailable':
        return t('pillars.sourceUnavailable', { source })
      case 'pendingPaused':
        return t('pillars.sourcePendingPaused', { source })
      case 'pendingIdle':
        return t('pillars.sourcePendingIdle', { source })
    }
  }

  // THE AVAILABILITY ANNOUNCEMENT — one sentence per usage half the role may read, in
  // the order the tile's hints use (live first), naming the half with the same noun its
  // hint and its partial marker use. Composed from the current gap, query and rollup
  // only: a half the role may not read is named as restricted exactly as its hint does
  // (no figure, no read), and a tenant change re-keys both queries, so the previous
  // tenant's state cannot be spoken under the new one. No number travels in it: the
  // region says that a half became available, partial, paused, not started, restricted
  // or failed — the tile carries the figures. Nothing when the pillar is not mounted.
  const availabilityText = (key: UsageAvailability, source: string): string => {
    switch (key) {
      case 'restricted':
        return t('availability.restricted', { source })
      case 'unavailable':
        return t('availability.unavailable', { source })
      case 'pendingPaused':
        return t('availability.pendingPaused', { source })
      case 'pendingIdle':
        return t('availability.pendingIdle', { source })
      case 'loading':
        return t('availability.loading', { source })
      case 'partial':
        return t('availability.partial', { source })
      case 'current':
        return t('availability.current', { source })
    }
  }
  const announcement = canUsage
    ? [
        availabilityText(
          usageAvailability(liveGap, sessionsQ, usage?.livePartial ?? false),
          t('nav:nouns.sessions'),
        ),
        availabilityText(
          usageAvailability(
            inventoryGap,
            inventoryQ,
            usage?.truncated ?? false,
          ),
          t('nav:items.inventory'),
        ),
      ].join(' ')
    : ''
  const risk = canRisk
    ? deriveRisk(findingsQ.data, runsQ.data, driftQ.data)
    : null
  const compliance = canCompliance
    ? deriveCompliance(complianceSummaryQ.data, complianceRiskQ.data)
    : null

  // The risk section composes three sources; bind its AsyncSection to whichever is
  // permitted first (its data type varies, but the section reads none of it — it
  // re-derives from all three .data), so widen the result type to unknown.
  const riskPrimary = (
    canSecurity ? findingsQ : canRedteam ? runsQ : driftQ
  ) as Pick<
    UseQueryResult<unknown>,
    'data' | 'isLoading' | 'isError' | 'error' | 'refetch'
  >

  const anyPermitted =
    canFinops || canModels || canUsage || canRisk || canCompliance || canHealth

  // Headline loads as one block so tiles don't pop in one by one.
  const headlineLoading =
    (canFinops && costSummaryQ.isLoading) ||
    (canUsage && (inventoryQ.isLoading || sessionsQ.isLoading)) ||
    (canRisk && (findingsQ.isLoading || runsQ.isLoading || driftQ.isLoading)) ||
    (canCompliance && complianceSummaryQ.isLoading)

  const tenantLabel = activeTenant ?? t('report.allOrgs')
  const rangeLabel = t(`range.${rangeId}`)

  if (!anyPermitted) {
    return (
      <IntelPage icon={BarChart3} title={t('title')}>
        <EmptyState
          title={t('empty.title')}
          description={t('empty.description')}
        />
      </IntelPage>
    )
  }

  return (
    <IntelPage
      icon={BarChart3}
      title={t('title')}
      description={t('description')}
      notices={<CaveatNotice>{t('asOfNote')}</CaveatNotice>}
      actions={
        <div className="flex flex-wrap items-center gap-2 print:hidden">
          <Select value={rangeId} onValueChange={setRangeId}>
            <SelectTrigger className="w-40" aria-label={t('range.label')}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {RANGE_IDS.map((id) => (
                <SelectItem key={id} value={id}>
                  {t(`range.${id}`)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            variant="outline"
            size="sm"
            onClick={() => window.print()}
            title={t('export.hint')}
          >
            <FileDown />
            {t('export.action')}
          </Button>
        </div>
      }
    >
      <ReportHeader tenantLabel={tenantLabel} rangeLabel={rangeLabel} />

      {/* THE LIVE REGION, once per view and OUTSIDE every tile link and disclosure:
          `role="status"` is polite and atomic, so a change is read whole and interrupts
          nothing; it moves no focus and holds no control. Screen-reader-only on screen,
          dropped from print (the printed report carries the visible hints). It is
          always mounted while the page renders — with empty text when the usage pillar
          is not — so it exists before the first change it has to announce, and it
          stays mounted while the headline is skeletons. A DOM assertion on this element
          does not certify every assistive technology; it pins that the text is right
          and changes only when the state does. */}
      <div
        role="status"
        aria-live="polite"
        className="sr-only print:hidden"
        data-testid="executive-usage-availability-live"
      >
        {announcement}
      </div>

      {/* headline pillars */}
      {headlineLoading ? (
        <StatGrid>
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-28 w-full" />
          ))}
        </StatGrid>
      ) : (
        <div className="animate-enter">
          <KpiTiles
            cost={cost}
            usage={usage}
            // One line per usage half that has no figure, naming the half and the
            // reason — restricted, could not load, paused, not started — beside the
            // "—" it explains. Nothing when both are current.
            usageHints={
              liveGap || inventoryGap ? (
                <>
                  {liveGap ? (
                    <span
                      className="block"
                      data-testid="executive-usage-live-gap"
                    >
                      {gapText(liveGap, t('nav:nouns.sessions'))}
                    </span>
                  ) : null}
                  {inventoryGap ? (
                    <span
                      className="block"
                      data-testid="executive-usage-inventory-gap"
                    >
                      {gapText(inventoryGap, t('nav:items.inventory'))}
                    </span>
                  ) : null}
                </>
              ) : undefined
            }
            // EITHER HALF CAN BE A FLOOR, AND EACH SAYS SO FOR ITSELF.
            //  · The "live" figure is counted over ONE most-recent page of
            //    `GET /v1/m/sessions/live`: the store reads limit+1 rows (default 100,
            //    ceiling 1000) and reports `has_more` when the extra row existed
            //    (modules/sessions/api.go handleListLive). Until today that page was
            //    printed exactly like the population, while the Inventory marker beside
            //    it was — correctly — not a statement about Sessions. Disclosed here
            //    NAMING Sessions, with the PAGE sentence as its hint. A page without
            //    `has_more` keeps the previous rendering exactly: no marker, and no
            //    completeness claim added.
            //  · The headline agent count and the "tracked" total come from the
            //    inventory summary; `deriveUsage` has always propagated its `truncated`
            //    (the engine aggregated one bounded page and there was more), and it is
            //    disclosed NAMING Inventory, with the scan-ceiling sentence.
            // Neither line borrows the other's flag. The counts are not hidden — a floor
            // is a real answer — and `currentAnswer` above means a pending, refused or
            // failed half is not partial but UNKNOWN, so a marker cannot survive the
            // figure it qualifies. Ordered like the caption and the hints: live first.
            // One value feeds both renderings: the compact caption lines inside the tile
            // link and the full sentences in the disclosure beneath it, outside the link.
            usagePartial={
              usage && (usage.livePartial || usage.truncated)
                ? {
                    testId: 'executive-usage-partial-details',
                    sources: [
                      ...(usage.livePartial
                        ? [
                            {
                              source: t('nav:nouns.sessions'),
                              kind: 'page' as const,
                              testId: 'executive-usage-sessions-partial',
                            },
                          ]
                        : []),
                      ...(usage.truncated
                        ? [
                            {
                              source: t('nav:items.inventory'),
                              kind: 'aggregate' as const,
                              testId: 'executive-usage-inventory-partial',
                            },
                          ]
                        : []),
                    ],
                  }
                : undefined
            }
            // Every figure on this tile is a tenant-wide read: the "live" figure is the
            // sessions page, the headline agent count and "tracked" total are the
            // inventory summary, and neither endpoint takes a workspace selector. One
            // note names the sources the role actually reads — a half the role cannot
            // read is not described as covered — so the disclosure says exactly which
            // figures it qualifies. With sessions alone it is the disclosure accepted on
            // 2026-09-08, unchanged.
            usageScope={
              canUsage ? (
                <span data-testid="executive-usage-scope-note">
                  {[
                    canSessions ? t('nav:nouns.sessions') : null,
                    canInventory ? t('nav:items.inventory') : null,
                    t('nav:workspace.tenantWide'),
                  ]
                    .filter((part) => part !== null)
                    .join(' · ')}
                </span>
              ) : undefined
            }
            risk={risk}
            compliance={compliance}
          />
        </div>
      )}

      {/* cost */}
      {canFinops ? (
        <div className="animate-enter" style={{ animationDelay: '40ms' }}>
          <AsyncSection query={costSummaryQ} skeletonHeight={320}>
            {(summary) => (
              <SpendSection
                cost={deriveCost(
                  summary,
                  costTrendQ.data,
                  forecastQ.data,
                  modelsQ.data,
                )!}
              />
            )}
          </AsyncSection>
        </div>
      ) : null}

      {canFinops ? (
        <div className="animate-enter" style={{ animationDelay: '80ms' }}>
          <SpendBreakdown tenant={activeTenant} params={params} />
        </div>
      ) : null}

      {/* risk */}
      {canRisk ? (
        <div className="animate-enter" style={{ animationDelay: '120ms' }}>
          <AsyncSection query={riskPrimary} skeletonHeight={220}>
            {() => (
              <RiskSection
                risk={deriveRisk(findingsQ.data, runsQ.data, driftQ.data)!}
              />
            )}
          </AsyncSection>
        </div>
      ) : null}

      {/* compliance */}
      {canCompliance ? (
        <div className="animate-enter" style={{ animationDelay: '160ms' }}>
          <AsyncSection query={complianceSummaryQ} skeletonHeight={240}>
            {(summary) => (
              <ComplianceSection
                compliance={deriveCompliance(summary, complianceRiskQ.data)!}
              />
            )}
          </AsyncSection>
        </div>
      ) : null}

      {/* reliability */}
      {canHealth ? (
        <div className="animate-enter" style={{ animationDelay: '200ms' }}>
          <AsyncSection query={healthStatusQ} skeletonHeight={160}>
            {(status) => (
              <ReliabilitySection
                health={deriveHealth(status, incidentsQ.data)!}
              />
            )}
          </AsyncSection>
        </div>
      ) : null}

      <ReportFooter />
    </IntelPage>
  )
}

// --- spend breakdown (org/team/project summary) — its own state + query -------

const DIMENSIONS: SpendDimension[] = [
  'team',
  'project',
  'agent',
  'model',
  'provider',
]

function SpendBreakdown({
  tenant,
  params,
}: {
  tenant: string | null
  params: { since: string }
}) {
  const { t } = useTranslation('executive')
  const [dimension, setDimension] = useState<SpendDimension>('team')
  const spendQ = useQuery({
    queryKey: finopsKeys.spend(tenant, dimension, params),
    queryFn: () => finopsApi.spend(dimension, params),
  })
  return (
    <SectionCard
      title={t('cost.breakdownTitle')}
      description={t('cost.breakdownDescription')}
      actions={
        <Select
          value={dimension}
          onValueChange={(v) => setDimension(v as SpendDimension)}
        >
          <SelectTrigger
            className="w-40 print:hidden"
            aria-label={t('cost.breakdownTitle')}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {DIMENSIONS.map((d) => (
              <SelectItem key={d} value={d}>
                {t(`dimensions.${d}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      }
    >
      <AsyncSection query={spendQ} skeletonHeight={240}>
        {(spend) => <SpendBreakdownChart spend={spend} />}
      </AsyncSection>
    </SectionCard>
  )
}
