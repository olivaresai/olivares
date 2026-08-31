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
// the cost figure keeps its `truncated` floor caveat.
import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
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
import { useWorkspaceFilter } from '@/lib/hooks/use-workspace-filter'
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

export function HomeView() {
  const { t } = useTranslation(['home', 'common'])
  const { activeTenant, can } = useAuth()
  const { workspaceId, queryKey: wsKey } = useWorkspaceFilter()
  const theme = useChartTheme()

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
  const inventoryQ = useQuery({
    queryKey: [...inventoryKeys.summary(activeTenant), wsKey],
    queryFn: () => inventoryApi.summary({ workspace_id: workspaceId }),
    enabled: canInventory,
  })
  const sessionsQ = useQuery({
    queryKey: [...sessionsKeys.live(activeTenant), wsKey],
    queryFn: () => sessionsApi.live({ workspace_id: workspaceId }),
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

  // --- rollups (aggregate only; the modules own the math, ARCHITECTURE.md) ---------
  const cost = deriveCost(costSummaryQ.data, costTrendQ.data, forecastQ.data)
  const usage = deriveUsage(inventoryQ.data, sessionsQ.data)
  const risk = deriveRisk(findingsQ.data)
  const compliance = deriveCompliance(complianceQ.data)
  const health = deriveHealth(healthStatusQ.data, incidentsQ.data)

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
      <StatGrid>
        {canInventory ? (
          <EstateTile
            to="/inventory"
            icon={<Boxes />}
            label={t('tiles.inventory.label')}
            state={tileState(inventoryQ)}
            value={formatInt(usage?.totalEntities ?? 0)}
            caption={t('tiles.inventory.caption', {
              agents: formatInt(usage?.totalAgents ?? 0),
              active: formatInt(usage?.activeAgents ?? 0),
            })}
          />
        ) : null}

        {canSessions ? (
          <EstateTile
            to="/sessions"
            icon={<Activity />}
            label={t('tiles.sessions.label')}
            state={tileState(sessionsQ)}
            value={formatInt(usage?.liveActive ?? 0)}
            caption={t('tiles.sessions.caption', {
              idle: formatInt(usage?.liveIdle ?? 0),
            })}
            tone={usage && usage.silentEvasion > 0 ? 'warning' : undefined}
            trend={
              usage && usage.silentEvasion > 0 ? (
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
