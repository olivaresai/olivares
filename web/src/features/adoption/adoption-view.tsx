// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Adoption (gap #12) — the container. It wires the queries, the range, the tabs and the
// per-developer gate (deny-closed), and composes the pure presentational pieces. It
// computes nothing — modules/claudeadoption does; this presents. The two lenses
// (analytics per-developer / telemetry per-session) are shown side by side, never summed.
import './i18n'
import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Gauge, Lock } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useAuth } from '@/lib/auth/context'
import { AsyncSection, IntelPage, SectionCard } from '@/features/_intel'
import { adoptionApi, adoptionKeys } from './api'
import {
  AdoptionTrend,
  BoundaryBanner,
  DeveloperTable,
  LensSection,
  OfficialObservedComparison,
  TeamTable,
} from './components'
import type { LensId } from './types'

const RANGE_DAYS: Record<string, number> = { '7d': 7, '30d': 30, '90d': 90 }

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

export function AdoptionView() {
  const { t } = useTranslation(['adoption', 'common'])
  const { activeTenant, can } = useAuth()
  const [rangeId, setRangeId] = useState('30d')
  const since = useMemo(() => sinceFor(rangeId), [rangeId])
  const params = useMemo(() => ({ since }), [since])

  const summaryQ = useQuery({
    queryKey: adoptionKeys.summary(activeTenant, params),
    queryFn: () => adoptionApi.summary(params),
  })
  const discrepancyQ = useQuery({
    queryKey: adoptionKeys.discrepancy(activeTenant, params),
    queryFn: () => adoptionApi.discrepancy(params),
  })

  const canReadDevelopers = can('adoption:developer:read')

  return (
    <IntelPage
      icon={Gauge}
      title={t('title')}
      description={t('description')}
      actions={
        <Select value={rangeId} onValueChange={setRangeId}>
          <SelectTrigger className="w-40" aria-label={t('range.label')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="7d">{t('range.7d')}</SelectItem>
            <SelectItem value="30d">{t('range.30d')}</SelectItem>
            <SelectItem value="90d">{t('range.90d')}</SelectItem>
            <SelectItem value="mtd">{t('range.mtd')}</SelectItem>
          </SelectContent>
        </Select>
      }
    >
      <AsyncSection query={summaryQ} skeletonHeight={64}>
        {(summary) => <BoundaryBanner excludes={summary.boundary.excludes} />}
      </AsyncSection>

      <Tabs defaultValue="overview">
        <TabsList>
          <TabsTrigger value="overview">{t('tabs.overview')}</TabsTrigger>
          <TabsTrigger value="teams">{t('tabs.teams')}</TabsTrigger>
          <TabsTrigger value="developers">{t('tabs.developers')}</TabsTrigger>
          <TabsTrigger value="trend">{t('tabs.trend')}</TabsTrigger>
        </TabsList>

        <TabsContent value="overview" className="flex flex-col gap-4">
          <AsyncSection query={summaryQ} skeletonHeight={320}>
            {(summary) => (
              <>
                <SectionCard
                  title={t('lenses.analytics.title')}
                  description={t('lenses.analytics.description', {
                    developers: summary.developers,
                  })}
                >
                  <LensSection lens={summary.analytics} />
                </SectionCard>
                <SectionCard
                  title={t('lenses.telemetry.title')}
                  description={t('lenses.telemetry.description', {
                    teams: summary.teams,
                  })}
                >
                  <LensSection lens={summary.telemetry} />
                </SectionCard>
                <SectionCard
                  title={t('comparison.title')}
                  description={t('comparison.description')}
                >
                  <AsyncSection query={discrepancyQ} skeletonHeight={180}>
                    {(discrepancy) => (
                      <OfficialObservedComparison discrepancy={discrepancy} />
                    )}
                  </AsyncSection>
                </SectionCard>
              </>
            )}
          </AsyncSection>
        </TabsContent>

        <TabsContent value="teams" className="flex flex-col gap-4">
          <TeamsTab range={params} />
        </TabsContent>

        <TabsContent value="developers" className="flex flex-col gap-4">
          {canReadDevelopers ? (
            <DevelopersTab range={params} />
          ) : (
            <SectionCard
              title={t('developers.title')}
              description={t('developers.description')}
            >
              <EmptyState
                icon={<Lock />}
                title={t('developers.locked')}
                description={t('developers.lockedHint')}
              />
            </SectionCard>
          )}
        </TabsContent>

        <TabsContent value="trend" className="flex flex-col gap-4">
          <TrendTab range={params} />
        </TabsContent>
      </Tabs>
    </IntelPage>
  )
}

// --- teams tab ---------------------------------------------------------------

function TeamsTab({ range }: { range: { since: string } }) {
  const { t } = useTranslation('adoption')
  const { activeTenant } = useAuth()
  const params = useMemo(() => ({ since: range.since }), [range.since])
  const teamsQ = useQuery({
    queryKey: adoptionKeys.teams(activeTenant, params),
    queryFn: () => adoptionApi.teams(params),
  })
  return (
    <SectionCard
      title={t('teams.title')}
      description={t('teams.description')}
      noPadding
    >
      <div className="p-4">
        <AsyncSection query={teamsQ} skeletonHeight={240}>
          {(res) =>
            res.teams.length === 0 ? (
              <EmptyState
                title={t('teams.empty')}
                description={t('teams.emptyHint')}
              />
            ) : (
              <TeamTable teams={res.teams} />
            )
          }
        </AsyncSection>
      </div>
    </SectionCard>
  )
}

// --- developers tab (gated) --------------------------------------------------

function DevelopersTab({ range }: { range: { since: string } }) {
  const { t } = useTranslation('adoption')
  const { activeTenant } = useAuth()
  const params = useMemo(() => ({ since: range.since }), [range.since])
  const devQ = useQuery({
    queryKey: adoptionKeys.developers(activeTenant, params),
    queryFn: () => adoptionApi.developers(params),
  })
  return (
    <SectionCard
      title={t('developers.title')}
      description={t('developers.description')}
      noPadding
    >
      <div className="p-4">
        <AsyncSection query={devQ} skeletonHeight={240}>
          {(res) =>
            res.developers.length === 0 ? (
              <EmptyState title={t('developers.empty')} />
            ) : (
              <DeveloperTable developers={res.developers} />
            )
          }
        </AsyncSection>
      </div>
    </SectionCard>
  )
}

// --- trend tab ---------------------------------------------------------------

function TrendTab({ range }: { range: { since: string } }) {
  const { t } = useTranslation('adoption')
  const { activeTenant } = useAuth()
  const [lens, setLens] = useState<LensId>('analytics')
  const params = useMemo(() => ({ since: range.since }), [range.since])
  const trendQ = useQuery({
    queryKey: adoptionKeys.trend(activeTenant, lens, params),
    queryFn: () => adoptionApi.trend(lens, params),
  })
  return (
    <SectionCard
      title={t('trend.title')}
      description={t('trend.description')}
      actions={
        <Select value={lens} onValueChange={(v) => setLens(v as LensId)}>
          <SelectTrigger className="w-44" aria-label={t('trend.lensLabel')}>
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="analytics">
              {t('lenses.analytics.title')}
            </SelectItem>
            <SelectItem value="telemetry">
              {t('lenses.telemetry.title')}
            </SelectItem>
          </SelectContent>
        </Select>
      }
    >
      <AsyncSection query={trendQ} skeletonHeight={300}>
        {(trend) =>
          trend.days.length === 0 ? (
            <EmptyState title={t('trend.empty')} />
          ) : (
            <AdoptionTrend trend={trend} />
          )
        }
      </AsyncSection>
    </SectionCard>
  )
}
