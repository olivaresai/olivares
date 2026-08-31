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
import { useMemo, useState } from 'react'
import { useQuery, type UseQueryResult } from '@tanstack/react-query'
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
import { useAuth } from '@/lib/auth/context'
import { useWorkspaceFilter } from '@/lib/hooks/use-workspace-filter'
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
  const { workspaceId, queryKey: wsKey } = useWorkspaceFilter()
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

  // --- rollups (aggregate only; modules own the math) ------------------------
  const cost = canFinops
    ? deriveCost(
        costSummaryQ.data,
        costTrendQ.data,
        forecastQ.data,
        modelsQ.data,
      )
    : null
  const usage = canUsage ? deriveUsage(inventoryQ.data, sessionsQ.data) : null
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
                cost={
                  deriveCost(
                    summary,
                    costTrendQ.data,
                    forecastQ.data,
                    modelsQ.data,
                  )!
                }
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
