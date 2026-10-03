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
  OctagonAlert,
  ScrollText,
  ShieldAlert,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { useAuth } from '@/lib/auth/context'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { IntelPage, StatGrid, TruncatedNotice } from '@/features/_intel'
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
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import {
  isLiveRun,
  mergeSessions,
  type UnifiedSession,
} from '@/features/sessions/provenance'
import type { LiveDTO } from '@/features/sessions/types'
import { securityApi, securityKeys } from '@/features/security/api'
import { complianceApi, complianceKeys } from '@/features/compliance/api'
import { healthApi, healthKeys } from '@/features/health/api'
import { formatInt, formatMicroUsd, formatPercent } from '@/lib/format'
import { EstateTile, type TileState } from './components'
import { NextStep } from './next-step'
import { NowQueue } from './now-queue'
import { BUDGET_READ } from './budget-verdict'
import { BudgetsTile } from './budgets-tile'
import { useKillSwitchState } from '@/components/layout/use-killswitch-state'
import { usePendingApprovals } from '@/features/governance/use-pending-approvals'
import { NowStart } from './now-start'
import { useModuleEnabled } from '@/stores/modules'
import { useComplianceOpened } from '@/features/compliance/compliance-opened'
import { RecentWork } from './recent-work'
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

/** How many runs Now reads for Recent work (it shows the newest few). */
const HOME_RUN_PARAMS = { limit: 30 } as const
/**
 * The live rows the Sessions tile counts: one per session (HU 029 counted "3" for one
 * launch). Rows folded into a launched session as echoes are not counted again, and a
 * launched session is active only while its run is (a stopped run's row keeps its
 * last "active" signal).
 */
export function liveForTiles<T extends { items: LiveDTO[] }>(
  answer: T | undefined,
  sessions: UnifiedSession[] | undefined,
): T | undefined {
  if (!answer || !sessions) return answer
  const echoes = new Set(
    sessions.flatMap((s) => (s.echoes ?? []).map((e) => e.live_ref)),
  )
  const runState = new Map<string, boolean>()
  for (const s of sessions)
    if (s.live && s.runs.length > 0)
      runState.set(s.live.live_ref, s.runs.some(isLiveRun))
  const items = answer.items
    .filter((l) => !echoes.has(l.live_ref))
    .map((l) =>
      runState.get(l.live_ref) === false && l.cc_state === 'active'
        ? { ...l, cc_state: 'ended' as const }
        : l,
    )
  return { ...answer, items }
}

export function HomeView() {
  const { t } = useTranslation(['home', 'nav', 'common'])
  const { activeTenant, can } = useAuth()
  const theme = useChartTheme()
  const queryClient = useQueryClient()

  // RBAC: gate every tile by the same read permission its nav item uses (docs/SECURITY-HARDENING.md).
  // A tile reads its module only where the module runs (ARCH C1): a module that is not
  // enabled shows no tile and is never asked, so Home shows no error for it.
  const on = useModuleEnabled()
  const may = (permission: string) => can(permission) && on(permission)
  const canFinops = may('finops:spend:read')
  const canBudgets = may(BUDGET_READ)
  const canInventory = may('inventory:catalog:read')
  const canSessions = can('sessions:live:read')
  const canSecurity = may('security:finding:read')
  const canCompliance = may('compliance:framework:read')
  const canHealth = may('health:status:read')
  const complianceOpened = useComplianceOpened()

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
  // Pending approvals for "Needs you": the approval queue's own read (shared cache).
  const pendingApprovals = usePendingApprovals()
  const killSwitch = useKillSwitchState()
  const sessionsQ = useQuery({
    queryKey: sessionsKeys.live(activeTenant),
    queryFn: () => sessionsApi.live(),
    enabled: canSessions,
  })
  // The sessions Olivares operates, so a launched session is on Now by its name and state
  // (one row with what was observed of it, merged below).
  const canRuns = can('sessions:run:read')
  // Whoever can start a session reads the real next step on the start line (install, sign
  // in, or New session); the generic offers are only for those who cannot (N2 J8: a fresh
  // install showed them under "Sign Claude Code in"), and only the ones they may take.
  const showNextStep = !can('sessions:run:write')
  const runsQ = useQuery({
    queryKey: [...agentOpsKeys.runs(activeTenant, HOME_RUN_PARAMS), 'home'],
    queryFn: () => agentOpsApi.listRuns(HOME_RUN_PARAMS),
    enabled: canRuns,
  })
  const recentSessions = useMemo(
    () =>
      sessionsQ.data || runsQ.data
        ? mergeSessions(sessionsQ.data?.items ?? [], runsQ.data?.items ?? [])
        : undefined,
    [sessionsQ.data, runsQ.data],
  )
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
    liveForTiles(currentAnswer(canSessions, sessionsQ), recentSessions),
  )
  const risk = deriveRisk(findingsQ.data)
  const compliance = deriveCompliance(complianceQ.data)
  const health = deriveHealth(healthStatusQ.data, incidentsQ.data)

  // Why a usage tile that is neither loading nor failed still has no figure. Read only
  // for the two sources this residual cut covers; a tile the role cannot read is not
  // mounted and gets no reason.
  const inventoryPending = canInventory ? pendingReason(inventoryQ) : null
  const sessionsPending = canSessions ? pendingReason(sessionsQ) : null

  // A TILE ONLY WHEN IT HAS SOMETHING TO SAY (HU2-25, Root 19:53Z). An upgraded install
  // kept 26.10's modules, and Now drew a tile per module: "0", "—", "No health checks
  // yet", and a compliance score nobody asked for. A tile hides ONLY after a successful,
  // complete, empty answer from every source it reads (SR4C on ea62fad5): a read that is
  // loading, paused or not started, a page with more behind it, a truncated aggregate and
  // a failure all keep the tile, since each is true and none proves "nothing". The
  // compliance score waits until the person opened Compliance.
  const answeredEmpty = (
    queries: readonly { isSuccess: boolean }[],
    complete: boolean,
    nothing: boolean,
  ) => queries.every((q) => q.isSuccess) && complete && nothing
  const showInventory =
    canInventory &&
    !answeredEmpty(
      [inventoryQ],
      !usage.truncated,
      (usage.totalEntities ?? 0) === 0,
    )
  const showSessions =
    canSessions &&
    !answeredEmpty(
      [sessionsQ],
      !usage.livePartial,
      (usage.liveNow ?? 0) === 0 &&
        (usage.liveIdle ?? 0) === 0 &&
        !(usage.silentEvasion && usage.silentEvasion > 0),
    )
  const showSecurity =
    canSecurity &&
    !answeredEmpty(
      [findingsQ],
      !findingsQ.data?.has_more,
      !risk || risk.openFindings === 0,
    )
  const showCompliance =
    canCompliance &&
    complianceOpened &&
    !answeredEmpty([complianceQ], true, !compliance || compliance.total === 0)
  const showSpend =
    canFinops &&
    !answeredEmpty(
      [costSummaryQ, costTrendQ, forecastQ],
      !cost?.truncated,
      !cost || cost.totalMicroUsd === 0,
    )
  const showHealth =
    canHealth &&
    !answeredEmpty(
      [healthStatusQ, incidentsQ],
      !healthStatusQ.data?.has_more && !incidentsQ.data?.has_more,
      !health || (health.total === 0 && health.openIncidents === 0),
    )
  const showKillSwitch =
    killSwitch.permitted &&
    !answeredEmpty(
      [killSwitch.query],
      true,
      !killSwitch.posture ||
        (!killSwitch.posture.estate && killSwitch.posture.active === 0),
    )

  const pendingText = (reason: Exclude<PendingReason, null>) =>
    reason === 'pendingPaused'
      ? t('state.pendingPaused')
      : t('state.pendingIdle')
  // The first source of a tile that has no answer yet and is not fetching (paused or not
  // started): its tile says so, with "—", instead of words no answer backs (SR4C on
  // ea62fad5: "No open findings" or "$0" while the read was paused).
  const pendingOf = (...queries: SourceQuery[]) =>
    queries.map(pendingReason).find((r) => r !== null) ?? null
  const securityPending = canSecurity ? pendingOf(findingsQ) : null
  const compliancePending = canCompliance ? pendingOf(complianceQ) : null
  const spendPending = canFinops
    ? pendingOf(costSummaryQ, costTrendQ, forecastQ)
    : null
  const healthPending = canHealth ? pendingOf(healthStatusQ, incidentsQ) : null
  const killSwitchPending = killSwitch.permitted
    ? pendingOf(killSwitch.query)
    : null

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
    canHealth ||
    killSwitch.permitted ||
    pendingApprovals.permitted ||
    canBudgets

  if (!anyPermitted) {
    return (
      <IntelPage icon={LayoutDashboard} title={t('title')} className="gap-0">
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
      className="gap-0"
      /* ⛔ THE PURPOSE BANNER IS GONE. It was a 36 px info notice restating
         what this screen is for — *"Live aggregates over the last 30 days, across the
         modules your role can open. Each tile links to its module; open the executive
         report…"* — under an 82 px title block, under a 48 px header. Of its four
         clauses, three described affordances the affordances already offer (a tile
         links; the report opens from a button three centimetres to the right). The one
         that carried information — 30-day, role-scoped — moved into the description,
         which is now ONE line. In 23 captures of the product named as the
         standard, the number of screens with a banner like this is zero.
         The TRUNCATION notice below stays: it reports a measurement limit, which is
         the canon's own requirement and not a restatement of purpose. */
      actions={
        <Button asChild variant="outline" size="sm" className="h-6">
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
      {/* THE WORK, FIRST: how to start, what needs a person, what is running, then the
          numbers. How to start is read from the tools themselves (NowStart, HU-19). */}
      {/* NOW (console remake 26.10): the work column — start work, what needs a person,
          what is running — beside a quiet column of the estate figures. Nothing below is
          new data: the queue is the session rail's own groups, the list and the tiles are
          the reads this view already made. */}
      <div className="grid gap-x-8 gap-y-6 lg:grid-cols-[minmax(0,1fr)_320px]">
        <div className="flex min-w-0 flex-col gap-6">
          <NowStart />

          {/* What needs a person: the sessions half for a role that reads live sessions,
              the approvals for a role that reads the queue — either alone is enough. */}
          {canSessions || pendingApprovals.permitted ? (
            <NowQueue
              sessions={sessionsQ.data?.items}
              state={tileState(sessionsQ)}
              sessionsReadable={canSessions}
              approvals={pendingApprovals.query.data?.items}
              approvalsState={
                pendingApprovals.permitted
                  ? tileState(pendingApprovals.query)
                  : 'ready'
              }
            />
          ) : null}

          {/* WHAT IS IN PROGRESS. Reads NOTHING new: the rows are the very page `sessionsQ`
          already fetched for the live tile. Mounted only for a role that may read live
          sessions, like the tile below it. */}
          {canSessions || canRuns ? (
            <RecentWork
              titled
              sessions={recentSessions}
              state={tileState(canRuns ? runsQ : sessionsQ)}
              canStartSession={can('sessions:run:write')}
            />
          ) : null}
        </div>

        <aside
          aria-label={t('aside')}
          data-testid="now-aside"
          className="flex min-w-0 flex-col gap-4"
        >
          {/* THE NEXT ACTION. A side-by-side with the reference found this page offering
          "six
          read-only cards, no action anywhere"; the verbs stay above the numbers because
          an operator who cannot start anything does not need a faster way to read. */}
          {/* The start line above holds the next step for whoever can start a session
              (HU 022, N2 J8); the generic offers are for those who cannot. */}
          {showNextStep && <NextStep stacked />}

          {/* Three columns on a wide screen, not four: the front door holds SIX tiles, and
          a four-column grid leaves the second row half-empty — measured at 1600 px on
          2026-09-17. `StatGrid`'s own default stays as it is for the views that lead
          with four figures. */}
          <StatGrid className="grid-cols-1 sm:grid-cols-2 lg:grid-cols-1">
            {showInventory ? (
              <EstateTile
                compact
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

            {showSessions ? (
              <EstateTile
                compact
                to="/sessions"
                icon={<Activity />}
                label={t('tiles.sessions.label')}
                state={tileState(sessionsQ)}
                value={formatInt(usage.liveNow)}
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
                    <span className="inline-flex items-center gap-1 text-caption text-warning">
                      <Activity className="size-3.5" aria-hidden />
                      {t('tiles.sessions.silent', {
                        count: usage.silentEvasion,
                      })}
                    </span>
                  ) : undefined
                }
              />
            ) : null}

            {showSecurity ? (
              <EstateTile
                compact
                to="/security"
                icon={<ShieldAlert />}
                label={t('tiles.security.label')}
                state={tileState(findingsQ)}
                value={
                  securityPending ? '—' : formatInt(risk?.openFindings ?? 0)
                }
                tone={
                  securityPending
                    ? undefined
                    : risk && risk.criticalHigh > 0
                      ? 'danger'
                      : risk && risk.openFindings > 0
                        ? 'warning'
                        : 'success'
                }
                caption={
                  securityPending
                    ? pendingText(securityPending)
                    : risk && risk.openFindings > 0
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

            {showCompliance ? (
              <EstateTile
                compact
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
                  compliancePending
                    ? pendingText(compliancePending)
                    : compliance && compliance.total > 0
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

            {showSpend ? (
              <EstateTile
                compact
                to="/finops"
                icon={<Coins />}
                label={t('tiles.spend.label')}
                state={tileState(costSummaryQ, costTrendQ, forecastQ)}
                value={
                  spendPending
                    ? '—'
                    : formatMicroUsd(cost?.totalMicroUsd ?? 0, {
                        compact: true,
                      })
                }
                tone={
                  !spendPending && cost?.projectedOver ? 'warning' : undefined
                }
                /* ⛔ THE STATE IS IN THE WORDS, NOT ONLY IN THE TOKEN (WCAG 2.1 AA 1.4.1).
                 The tone above tints this caption and nothing else changed: the same
                 estate at 90 % and at 140 % of its run-rate printed the SAME sentence,
                 so a reader who does not see the amber — a monochrome display, a
                 colour-blind reader, a screen reader — read one tile for two states.
                 The health tile next door already differs in words (`2/3 healthy`
                 against `3/3`); this one did not. `projectedOver` is
                 `trend_projected_micro_usd > spend_micro_usd` (executive/derive.ts),
                 so the second caption says exactly that and not "over budget", which
                 is a different fact this screen does not read. */
                caption={
                  spendPending
                    ? pendingText(spendPending)
                    : cost && cost.projectedMicroUsd !== null
                      ? t(
                          cost.projectedOver
                            ? 'tiles.spend.projectedOver'
                            : 'tiles.spend.projected',
                          {
                            amount: formatMicroUsd(cost.projectedMicroUsd, {
                              compact: true,
                            }),
                          },
                        )
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

            {/* SPEND AGAINST BUDGETS (CONCEPT-IA): which budget is PROVEN over its limit,
                read from the canonical amount under the Cost page's own keys
                (budgets-tile.tsx states the financial rule). */}
            {canBudgets ? <BudgetsTile /> : null}

            {showHealth ? (
              <EstateTile
                compact
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
                  healthPending
                    ? pendingText(healthPending)
                    : health && health.total > 0
                      ? t('tiles.health.caption', {
                          breaches: formatInt(health.slaBreaches),
                          incidents: formatInt(health.openIncidents),
                        })
                      : t('tiles.health.noChecks')
                }
              />
            ) : null}
            {/* THE KILL SWITCH (CONCEPT-IA, Now's aside): how many stops are active and what
                that means, from the kill switch page's own read. Engaging stays on that
                page, behind its own confirmation and step-up. */}
            {showKillSwitch ? (
              <EstateTile
                compact
                to="/killswitch"
                icon={<OctagonAlert />}
                label={t('nav:items.killswitch')}
                state={tileState(killSwitch.query)}
                value={formatInt(killSwitch.posture?.active ?? null)}
                tone={
                  killSwitch.posture?.estate
                    ? 'danger'
                    : killSwitch.posture && killSwitch.posture.agentStops > 0
                      ? 'warning'
                      : undefined
                }
                caption={
                  killSwitchPending
                    ? pendingText(killSwitchPending)
                    : killSwitch.posture?.estate
                      ? t('nav:shell.killswitch.estateStopped')
                      : killSwitch.posture && killSwitch.posture.agentStops > 0
                        ? t('nav:shell.killswitch.agentStops', {
                            count: killSwitch.posture.agentStops,
                          })
                        : t('nav:shell.killswitch.none')
                }
              />
            ) : null}
          </StatGrid>

          {/* Keep the cost figure's honesty: a truncated aggregate is a floor, never hidden. */}
          {canFinops && cost?.truncated ? <TruncatedNotice /> : null}
        </aside>
      </div>
    </IntelPage>
  )
}
