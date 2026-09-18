// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Home overview — the estate front door (route `/`). This is the FIRST screen of
// a demo to a CTO/CISO/SOC, so it must be a real overview, never a placeholder. It is
// deliberately LIGHTER than the executive dashboard (module XXI, /dashboards): a single
// glanceable grid of six estate pillars — inventory, live sessions, security, compliance,
// spend run-rate and health/SLA — each a drill-down link to its module, plus a link to
// the full executive rollup + PDF.
//
// It reuses the executive machinery, never duplicating it (ARCHITECTURE.md): the SAME read
// hooks the technical views use, the SAME pure `derive*` rollups (the modules own the
// math — here we only present), and the SAME tile primitives. It fetches only what the
// six headline tiles need (no model catalog, no spend-by-dimension, no red-team/drift
// queries — those live in the deeper views), so the front door stays light; its
// tenant-scoped reads share the deeper views' query cache (identical query keys).
//
// RBAC (docs/SECURITY-HARDENING.md): each tile is gated by its source module's read permission — only
// permitted queries run, and a tile a role cannot open is never mounted. Honest empties
//: a source that errored shows an em-dash + retry hint, never a fabricated 0;
// the cost figure keeps its `truncated` floor caveat, a truncated inventory summary is
// disclosed on its own tile, and a live sessions page with rows beyond it is disclosed on
// ITS own tile — each naming its source, because this page carries several aggregates
// and pages, and each one is complete or partial on its own.
//
// A permitted source with NO current answer that is neither fetching nor failed says so
// (2026-09-08, the residual cut of dashboard-coverage-and-pending-states): a read that has
// not started and a read that is paused are two more honest states beside "—", and
// neither is an outage or a refusal. A discreet live region, one per view, announces
// availability and coverage changes of the two usage sources to assistive technology.
import { useEffect, useMemo } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import {
  Activity,
  ArrowRight,
  BarChart3,
  Boxes,
  Coins,
  HeartPulse,
  LayoutDashboard,
  ScrollText,
  ShieldAlert,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useAuth } from '@/lib/auth/context'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import {
  CaveatNotice,
  IntelPage,
  StatGrid,
  TruncatedNotice,
} from '@/features/_intel'
import {
  ComplianceMixBar,
  DeltaCaption,
  SeverityRow,
} from '@/features/executive/components'
import { Sparkline, useChartTheme } from '@/components/charts'
import {
  currentAnswer,
  deriveCompliance,
  deriveCost,
  deriveHealth,
  deriveRisk,
  deriveUsage,
} from '@/features/executive/derive'
import { finopsApi, finopsKeys } from '@/features/finops/api'
import { inventoryApi, inventoryKeys } from '@/features/inventory/api'
import { sessionsApi, sessionsKeys } from '@/features/sessions/api'
import { securityApi, securityKeys } from '@/features/security/api'
import { complianceApi, complianceKeys } from '@/features/compliance/api'
import { healthApi, healthKeys } from '@/features/health/api'
import { formatInt, formatMicroUsd, formatPercent } from '@/lib/format'
import { EstateTile, type TileState } from './components'
import './i18n'

/** The overview's 30-day window start as an ISO string. Module-level (not inline in
 *  render) so the impure clock read stays out of the render path — same approach as the
 *  executive view's `sinceFor`, and it shares the 30d query cache with it. */
function since30dISO(): string {
  return new Date(Date.now() - 30 * 86_400_000).toISOString()
}

/** Map a tile's contributing queries to one of its three honest states. A tile that
 *  derives from several queries (e.g. spend = summary+trend+forecast, health =
 *  status+incidents) must pass ALL of them: it is `loading` while ANY is still loading
 *  (so it never shows a half-loaded value — a missing forecast/incidents count would
 *  otherwise read as a fabricated 0), and `unavailable` if ANY errored. A single-source
 *  tile just passes its one query. Disabled queries never reach here — their tile is
 *  not mounted. */
function tileState(
  ...queries: { isLoading: boolean; isError: boolean }[]
): TileState {
  if (queries.some((q) => q.isError)) return 'unavailable'
  if (queries.some((q) => q.isLoading)) return 'loading'
  return 'ready'
}

/** The shape of a TanStack query this file reads to name a source's availability: the
 *  status pair the observer already exposes, nothing derived from elsewhere. */
type SourceQuery = {
  isPending: boolean
  isLoading: boolean
  isError: boolean
  fetchStatus: 'fetching' | 'paused' | 'idle'
}

/**
 * WHY A PERMITTED SOURCE HAS NO CURRENT ANSWER while it is neither fetching nor failed.
 * `tileState` above only tells error from loading; a query that is `pending` with
 * `fetchStatus` `idle` or `paused` fell through to `ready` and the tile printed "—"
 * with a caption of dashes and no reason. Two reasons, read from the flags TanStack
 * exposes and from nothing else:
 *  · `pendingIdle`   — the read has not started (a dispatch that was cancelled and
 *                      reverted, a query that never ran): "query not started".
 *  · `pendingPaused` — the read was dispatched and is paused (TanStack pauses a
 *                      fetch in `networkMode: 'online'` while its `onlineManager`
 *                      reports offline, and resumes it when that changes): "paused".
 * A pause is NOT evidence of an outage, a refusal or a lost connection — the flag says
 * the client is waiting, not why the server is unreachable — so neither reason borrows
 * the "couldn't load" copy, and neither is reported for a query that IS fetching (that
 * is the skeleton, unchanged) or that already has a success or an error. A successful
 * answer whose REFETCH is paused is not pending: it keeps its figure and its markers.
 */
type PendingReason = 'pendingIdle' | 'pendingPaused' | null

function pendingReason(query: SourceQuery): PendingReason {
  if (!query.isPending || query.isError) return null
  if (query.fetchStatus === 'paused') return 'pendingPaused'
  if (query.fetchStatus === 'idle') return 'pendingIdle'
  return null
}

/**
 * THE ONE STATEMENT THE LIVE REGION MAKES ABOUT A SOURCE — availability first, then
 * coverage. It is a pure function of the query the view holds right now and of the
 * rollup's own partial flag: no memory of the previous poll, tenant or role. The text
 * built from it carries NO figure, so a poll that returns the same state renders the
 * same string and React leaves the DOM text node untouched — the region announces a
 * change of state, never a change of number.
 */
type SourceAvailability =
  | Exclude<PendingReason, null>
  | 'loading'
  | 'unavailable'
  | 'partial'
  | 'current'

function sourceAvailability(
  query: SourceQuery,
  partial: boolean,
): SourceAvailability {
  if (query.isError) return 'unavailable'
  if (query.isLoading) return 'loading'
  const pending = pendingReason(query)
  if (pending) return pending
  return partial ? 'partial' : 'current'
}

export function HomeView() {
  const { t } = useTranslation(['home', 'nav', 'common'])
  const { activeTenant, can } = useAuth()
  const theme = useChartTheme()
  const queryClient = useQueryClient()

  // RBAC: gate every tile by the same read permission its nav item uses (docs/SECURITY-HARDENING.md).
  const canFinops = can('finops:spend:read')
  const canInventory = can('inventory:catalog:read')
  const canSessions = can('sessions:live:read')
  const canSecurity = can('security:finding:read')
  const canCompliance = can('compliance:framework:read')
  const canHealth = can('health:status:read')

  // The estate overview reads the last 30 days, computed once on mount so the query key
  // is stable across re-renders (the executive view derives its own 30d window the same way).
  const params = useMemo(() => ({ since: since30dISO() }), [])

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
  // ⛔ THE INVENTORY READ IS TENANT-WIDE, AND ITS TILE SAYS SO. `GET /v1/m/inventory/summary`
  //    reads no request filter and the catalog carries no workspace lineage
  //    (modules/inventory/api.go:123, schema.go:67; ratified 2026-09-08 in
  //    an internal design note (not shipped)). Until today this query still
  //    sent `workspace_id` and keyed on the topbar selection, so a W1→W2 switch re-fetched the
  //    SAME tenant-wide summary and presented it as the new workspace's estate — beside a
  //    Sessions tile that already said it was tenant-wide. Keyed by tenant only now (the very
  //    cache entry InventoryView reads), still gated by `inventory:catalog:read`; the label
  //    under the tile describes SCOPE — not a grant, not completeness (a truncated summary is
  //    still a floor). The topbar selector itself is untouched: other views do filter by it.
  const inventoryQ = useQuery({
    queryKey: inventoryKeys.summary(activeTenant),
    queryFn: () => inventoryApi.summary(),
    enabled: canInventory,
  })
  // ⛔ THE LIVE-SESSIONS READ IS TENANT-WIDE, AND THIS TILE SAYS SO. `GET /v1/m/sessions/live`
  //    takes no core-workspace selector and neither DTO carries one (the ratified contract of
  //    2026-09-08, an internal design note (not shipped)). Until 2026-09-08
  //    this query still sent `workspace_id` and keyed on the selection, so a workspace switch
  //    re-fetched the SAME tenant-wide page and presented it as the new workspace's figure.
  //    Only that ignored filter went: the read is still pinned to the tenant, still gated by
  //    `sessions:live:read`, and the label under the tile describes SCOPE — not a grant, not
  //    completeness. The inventory read above now follows the same rule.
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
  const complianceQ = useQuery({
    queryKey: complianceKeys.summary(activeTenant),
    queryFn: () => complianceApi.summary(),
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

  // --- rollups (aggregate only; the modules own the math, ARCHITECTURE.md) ---------
  const cost = deriveCost(costSummaryQ.data, costTrendQ.data, forecastQ.data)
  // Each usage half is handed in only as its CURRENT, permitted, successful answer:
  // a denied, pending or failed half is `undefined` and comes back `null`, never as an
  // empty page counted to 0, and data a role may no longer read is not reused. The
  // tiles print that `null` through `formatInt`, which degrades to the em-dash by
  // design — the `?? 0` this file used to put in front of it was the fabrication.
  const usage = deriveUsage(
    currentAnswer(canInventory, inventoryQ),
    currentAnswer(canSessions, sessionsQ),
  )
  const risk = deriveRisk(findingsQ.data)
  const compliance = deriveCompliance(complianceQ.data)
  const health = deriveHealth(healthStatusQ.data, incidentsQ.data)

  // Why a usage tile that is neither loading nor failed still has no figure. Read only
  // for the two sources this residual cut covers; a tile the role cannot read is not
  // mounted and gets no reason.
  const inventoryPending = canInventory ? pendingReason(inventoryQ) : null
  const sessionsPending = canSessions ? pendingReason(sessionsQ) : null
  const pendingText = (reason: Exclude<PendingReason, null>) =>
    reason === 'pendingPaused'
      ? t('state.pendingPaused')
      : t('state.pendingIdle')

  // THE AVAILABILITY ANNOUNCEMENT — one sentence per permitted usage source, in tile
  // order, naming the source with the same noun its partial marker uses. Composed from
  // the current query and rollup only: a source the role may not read has no sentence
  // (its tile is not mounted either), and a tenant change re-keys both queries, so the
  // previous tenant's state cannot be spoken under the new one. No number travels in
  // it: the region says that a source became available, partial, paused, not started
  // or failed — the tiles carry the figures.
  const availabilityText = (
    key: SourceAvailability,
    source: string,
  ): string => {
    switch (key) {
      case 'loading':
        return t('availability.loading', { source })
      case 'pendingIdle':
        return t('availability.pendingIdle', { source })
      case 'pendingPaused':
        return t('availability.pendingPaused', { source })
      case 'unavailable':
        return t('availability.unavailable', { source })
      case 'partial':
        return t('availability.partial', { source })
      case 'current':
        return t('availability.current', { source })
    }
  }
  const announcement = [
    canInventory
      ? availabilityText(
          sourceAvailability(inventoryQ, usage.truncated),
          t('nav:items.inventory'),
        )
      : null,
    canSessions
      ? availabilityText(
          sourceAvailability(sessionsQ, usage.livePartial),
          t('nav:nouns.sessions'),
        )
      : null,
  ]
    .filter((line) => line !== null)
    .join(' ')

  const anyPermitted =
    canFinops ||
    canInventory ||
    canSessions ||
    canSecurity ||
    canCompliance ||
    canHealth

  if (!anyPermitted) {
    return (
      <IntelPage icon={LayoutDashboard} title={t('title')}>
        <EmptyState
          title={t('empty.title')}
          description={t('empty.description')}
        />
      </IntelPage>
    )
  }

  return (
    <IntelPage
      icon={LayoutDashboard}
      title={t('title')}
      description={t('description')}
      notices={<CaveatNotice tone="info">{t('asOfNote')}</CaveatNotice>}
      actions={
        <Button asChild variant="outline" size="sm">
          {/* The feature registry IS the route table, so this path is valid at runtime
              even though the generated route types don't list it (as `DrillLink` does). */}
          <Link to={'/dashboards' as never}>
            <BarChart3 />
            {t('openReport')}
            <ArrowRight />
          </Link>
        </Button>
      }
    >
      {/* THE LIVE REGION, once per view and OUTSIDE every tile link and disclosure:
          `role="status"` is polite and atomic, so a change is read whole and interrupts
          nothing; it moves no focus and holds no control. Screen-reader-only on screen,
          dropped from print (the printed report carries the visible hints). It is
          always mounted while the page renders, with empty text when the role reads
          neither usage source, so it exists before the first change it has to announce.
          A DOM assertion on this element does not certify every assistive technology;
          it pins that the text is right and changes only when the state does. */}
      <div
        role="status"
        aria-live="polite"
        className="sr-only print:hidden"
        data-testid="home-usage-availability-live"
      >
        {announcement}
      </div>
      <StatGrid>
        {canInventory ? (
          <EstateTile
            to="/inventory"
            icon={<Boxes />}
            label={t('tiles.inventory.label')}
            state={tileState(inventoryQ)}
            value={formatInt(usage.totalEntities)}
            // With no current answer and no fetch in flight, the caption is the REASON
            // beside the "—" (a line of dashes explained nothing), in the same muted
            // register as the tile's own retry hint. The figure line keeps `formatInt`'s
            // em-dash: nothing here is a zero.
            caption={
              inventoryPending ? (
                <span
                  className="text-muted-foreground"
                  data-testid="home-inventory-pending-reason"
                >
                  {pendingText(inventoryPending)}
                </span>
              ) : (
                t('tiles.inventory.caption', {
                  agents: formatInt(usage.totalAgents),
                  active: formatInt(usage.activeAgents),
                })
              )
            }
            scope={
              <span data-testid="home-inventory-scope-note">
                {t('nav:workspace.tenantWide')}
              </span>
            }
            // A TRUNCATED SUMMARY IS A FLOOR, AND THE FRONT DOOR NOW SAYS SO.
            // `deriveUsage` has always propagated `inventory.truncated` — the engine
            // aggregated one bounded page and there was more — and the Inventory view
            // has always flagged it (inventory-view.tsx:189), but this tile printed the
            // same counts with nothing said, so a bounded scan read as the whole estate.
            // The source is named because this page shows several aggregates; this
            // marker describes only Inventory. The counts stay: a floor is a real answer,
            // and hiding it would be the opposite defect. `currentAnswer` above means a
            // pending, refused or failed summary is not truncated but UNKNOWN, and the
            // tile renders this only when it is `ready`, so the marker cannot outlive
            // the figure it qualifies.
            partial={
              usage.truncated
                ? {
                    testId: 'home-inventory-partial-details',
                    sources: [
                      {
                        source: t('nav:items.inventory'),
                        kind: 'aggregate',
                        testId: 'home-inventory-partial-note',
                      },
                    ],
                  }
                : undefined
            }
          />
        ) : null}

        {canSessions ? (
          <EstateTile
            to="/sessions"
            icon={<Activity />}
            label={t('tiles.sessions.label')}
            state={tileState(sessionsQ)}
            value={formatInt(usage.liveActive)}
            caption={
              sessionsPending ? (
                <span
                  className="text-muted-foreground"
                  data-testid="home-sessions-pending-reason"
                >
                  {pendingText(sessionsPending)}
                </span>
              ) : (
                t('tiles.sessions.caption', {
                  idle: formatInt(usage.liveIdle),
                })
              )
            }
            tone={
              usage.silentEvasion !== null && usage.silentEvasion > 0
                ? 'warning'
                : undefined
            }
            scope={
              <span data-testid="home-sessions-scope-note">
                {t('nav:workspace.tenantWide')}
              </span>
            }
            // A PAGE WITH ROWS BEYOND IT IS A FLOOR, AND THE FRONT DOOR NOW SAYS SO.
            // `GET /v1/m/sessions/live` answers with ONE most-recent page — the store
            // reads limit+1 rows (default 100, ceiling 1000) and reports `has_more`
            // when the extra row existed (modules/sessions/api.go handleListLive) — and
            // the active and idle figures on this tile are counted over that page. The
            // Sessions view says so of its own table (sessions-workspace-view.tsx, the
            // `partial.truncated` notice); this tile printed the same counts with
            // nothing said, so a bounded page read as the estate. The
            // source is named because the neighbouring Inventory tile carries a marker of
            // its own, for its own aggregate: neither describes the other. The hint is the
            // PAGE sentence, not the scan-ceiling one. The counts stay — a floor is a real
            // answer. `currentAnswer` above means a pending, refused or failed page is not
            // partial but UNKNOWN, and the tile renders this only when it is `ready`, so
            // the marker cannot outlive the figure it qualifies. A page without
            // `has_more` keeps the previous rendering exactly: no marker, and no claim of
            // completeness added either. The tile renders the entry twice from this one
            // value: a compact caption line inside its link and the full sentence in the
            // disclosure beneath it (components.tsx, `CoverageLinkTile`).
            partial={
              usage.livePartial
                ? {
                    testId: 'home-sessions-partial-details',
                    sources: [
                      {
                        source: t('nav:nouns.sessions'),
                        kind: 'page',
                        testId: 'home-sessions-partial-note',
                      },
                    ],
                  }
                : undefined
            }
            trend={
              usage.silentEvasion !== null && usage.silentEvasion > 0 ? (
                <span className="inline-flex items-center gap-1 text-xs text-warning">
                  <Activity className="size-3.5" aria-hidden />
                  {t('tiles.sessions.silent', { count: usage.silentEvasion })}
                </span>
              ) : undefined
            }
          />
        ) : null}

        {canSecurity ? (
          <EstateTile
            to="/security"
            icon={<ShieldAlert />}
            label={t('tiles.security.label')}
            state={tileState(findingsQ)}
            value={formatInt(risk?.openFindings ?? 0)}
            tone={
              risk && risk.criticalHigh > 0
                ? 'danger'
                : risk && risk.openFindings > 0
                  ? 'warning'
                  : 'success'
            }
            caption={
              risk && risk.openFindings > 0
                ? t('tiles.security.caption', {
                    count: formatInt(risk.criticalHigh),
                  })
                : t('tiles.security.clear')
            }
            trend={
              risk ? (
                <SeverityRow bySeverity={risk.bySeverity} compact />
              ) : undefined
            }
          />
        ) : null}

        {canCompliance ? (
          <EstateTile
            to="/compliance"
            icon={<ScrollText />}
            label={t('tiles.compliance.label')}
            state={tileState(complianceQ)}
            value={
              compliance && compliance.coveredPct !== null
                ? formatPercent(compliance.coveredPct, { digits: 0 })
                : '—'
            }
            caption={
              compliance && compliance.total > 0
                ? t('tiles.compliance.caption', {
                    gap: formatInt(compliance.gap),
                    unmapped: formatInt(compliance.unmapped),
                  })
                : t('tiles.compliance.noControls')
            }
            trend={
              compliance ? (
                <ComplianceMixBar
                  compliance={compliance}
                  height={6}
                  showLegend={false}
                />
              ) : undefined
            }
          />
        ) : null}

        {canFinops ? (
          <EstateTile
            to="/finops"
            icon={<Coins />}
            label={t('tiles.spend.label')}
            state={tileState(costSummaryQ, costTrendQ, forecastQ)}
            value={formatMicroUsd(cost?.totalMicroUsd ?? 0, { compact: true })}
            tone={cost?.projectedOver ? 'warning' : undefined}
            caption={
              cost && cost.projectedMicroUsd !== null
                ? t('tiles.spend.projected', {
                    amount: formatMicroUsd(cost.projectedMicroUsd, {
                      compact: true,
                    }),
                  })
                : t('tiles.spend.caption', { range: t('range') })
            }
            trend={
              cost && cost.trend.length > 1 ? (
                <div className="flex items-center justify-between gap-2">
                  <Sparkline
                    data={cost.trend}
                    dataKey="cost"
                    color={theme.accent}
                    className="max-w-[60%]"
                  />
                  <DeltaCaption pct={cost.deltaPct} />
                </div>
              ) : cost ? (
                <DeltaCaption pct={cost.deltaPct} />
              ) : undefined
            }
          />
        ) : null}

        {canHealth ? (
          <EstateTile
            to="/health"
            icon={<HeartPulse />}
            label={t('tiles.health.label')}
            state={tileState(healthStatusQ, incidentsQ)}
            value={
              health && health.total > 0
                ? t('tiles.health.value', {
                    healthy: formatInt(health.healthy),
                    total: formatInt(health.total),
                  })
                : '—'
            }
            tone={
              health && (health.down > 0 || health.slaBreaches > 0)
                ? 'danger'
                : health && health.degraded > 0
                  ? 'warning'
                  : health && health.total > 0
                    ? 'success'
                    : undefined
            }
            caption={
              health && health.total > 0
                ? t('tiles.health.caption', {
                    breaches: formatInt(health.slaBreaches),
                    incidents: formatInt(health.openIncidents),
                  })
                : t('tiles.health.noChecks')
            }
          />
        ) : null}
      </StatGrid>

      {/* Keep the cost figure's honesty: a truncated aggregate is a floor, never hidden. */}
      {canFinops && cost?.truncated ? <TruncatedNotice /> : null}
    </IntelPage>
  )
}
