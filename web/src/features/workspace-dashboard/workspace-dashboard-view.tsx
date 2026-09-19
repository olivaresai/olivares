// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ArrowRight, Bot, Boxes, FolderOpen, Layers, Users } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { StaticTable } from '@/components/data/static-table'
import { XScroll } from '@/components/data/scroll-edges'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import {
  IntelPage,
  ListTruncationBadge,
  MetricStat,
  StatGrid,
} from '@/features/_intel'
import { useAuth } from '@/lib/auth/context'
import { formatInt } from '@/lib/format'
import { useWorkspaceStore } from '@/stores/workspace'
import { cuentaConSuelo } from './count-floor'
import { workspaceDashboardApi, workspaceDashboardKeys } from './api'
import './i18n'

export function WorkspaceDashboardView() {
  const { t } = useTranslation(['workspaceDashboard', 'common'])
  const { activeTenant } = useAuth()
  const { activeWorkspace, activeWorkspaceName } = useWorkspaceStore()

  if (!activeWorkspace) {
    return (
      <IntelPage icon={Layers} title={t('workspaceDashboard:title')}>
        <EmptyState
          title={t('workspaceDashboard:selectTitle')}
          description={t('workspaceDashboard:selectPrompt')}
          action={
            <Button asChild size="sm">
              <Link to={'/inventory' as never}>
                {t('workspaceDashboard:viewInventory')}
              </Link>
            </Button>
          }
        />
      </IntelPage>
    )
  }

  return (
    <WorkspaceDashboard
      workspaceId={activeWorkspace}
      workspaceName={activeWorkspaceName}
      tenant={activeTenant}
      t={t}
    />
  )
}

function WorkspaceDashboard({
  workspaceId,
  workspaceName,
  tenant,
  t,
}: {
  workspaceId: string
  workspaceName: string | null
  tenant: string | null
  t: ReturnType<typeof useTranslation>['t']
}) {
  const summaryQ = useQuery({
    queryKey: workspaceDashboardKeys.summary(tenant, workspaceId),
    queryFn: () => workspaceDashboardApi.summary(workspaceId),
    staleTime: 30_000,
  })

  const agentsQ = useQuery({
    queryKey: workspaceDashboardKeys.agents(tenant, workspaceId),
    queryFn: () => workspaceDashboardApi.agents(workspaceId),
    staleTime: 30_000,
  })

  const groupsQ = useQuery({
    queryKey: workspaceDashboardKeys.groups(tenant, workspaceId),
    queryFn: () => workspaceDashboardApi.groups(workspaceId),
    staleTime: 30_000,
  })

  const s = summaryQ.data
  const title = s?.name || workspaceName || t('workspaceDashboard:title')

  return (
    <IntelPage
      icon={Layers}
      title={title}
      description={
        s?.slug ? (
          <span className="flex min-w-0 items-center gap-2 text-caption text-muted-foreground">
            <span className="truncate" title={s.slug}>
              {s.slug}
            </span>
            {s.is_default && (
              <Badge variant="neutral">
                {t('workspaceDashboard:defaultBadge')}
              </Badge>
            )}
          </span>
        ) : undefined
      }
    >
      <StatGrid>
        <MetricStat
          icon={<Bot className="size-4 text-muted-foreground" />}
          label={t('workspaceDashboard:agents')}
          value={
            s
              ? cuentaConSuelo(s.agent_count, s.agent_count_capped, formatInt)
              : '—'
          }
        />
        <MetricStat
          icon={<Boxes className="size-4 text-muted-foreground" />}
          label={t('workspaceDashboard:sessions')}
          value={
            s
              ? cuentaConSuelo(
                  s.session_count,
                  s.session_count_capped,
                  formatInt,
                )
              : '—'
          }
        />
        <MetricStat
          icon={<FolderOpen className="size-4 text-muted-foreground" />}
          label={t('workspaceDashboard:resources')}
          value={
            s
              ? cuentaConSuelo(
                  s.resource_count,
                  s.resource_count_capped,
                  formatInt,
                )
              : '—'
          }
        />
        <MetricStat
          icon={<Users className="size-4 text-muted-foreground" />}
          label={t('workspaceDashboard:groups')}
          value={
            s
              ? cuentaConSuelo(s.group_count, s.group_count_capped, formatInt)
              : '—'
          }
        />
      </StatGrid>

      <section className="flex flex-col gap-2">
        <div className="flex min-h-8 items-center justify-between gap-2">
          <h2 className="text-overline text-muted-foreground uppercase">
            {t('workspaceDashboard:recentAgents')}
          </h2>
          <Button variant="ghost" size="sm" asChild>
            <Link to={'/inventory' as never}>
              {t('workspaceDashboard:viewAll')}{' '}
              <ArrowRight className="ml-1 size-3.5" />
            </Link>
          </Button>
        </div>
        <ListTruncationBadge
          query={agentsQ}
          label={t('workspaceDashboard:truncation.label', {
            n: agentsQ.data?.items?.length,
          })}
          hint={t('workspaceDashboard:truncation.hint')}
          className="px-0 pt-0 pb-1"
        />
        {agentsQ.data?.items.length === 0 ? (
          <EmptyState
            title={t('workspaceDashboard:noAgents')}
            description={t('workspaceDashboard:noAgentsHint')}
            action={
              <Button asChild size="sm" variant="secondary">
                <Link to={'/inventory' as never}>
                  {t('workspaceDashboard:viewInventory')}
                </Link>
              </Button>
            }
          />
        ) : (
          <XScroll contentKey={agentsQ.data?.items.length}>
            <StaticTable oneLine>
              <thead>
                <tr>
                  <th>{t('workspaceDashboard:name')}</th>
                  <th>{t('workspaceDashboard:kind')}</th>
                  <th>{t('workspaceDashboard:status')}</th>
                </tr>
              </thead>
              <tbody>
                {agentsQ.data?.items.map((a) => (
                  <tr key={a.id}>
                    <td className="font-medium">{a.name}</td>
                    <td>
                      <Badge variant="outline">{a.kind}</Badge>
                    </td>
                    <td>
                      <Badge
                        variant={a.status === 'active' ? 'success' : 'neutral'}
                      >
                        {a.status}
                      </Badge>
                    </td>
                  </tr>
                ))}
              </tbody>
            </StaticTable>
          </XScroll>
        )}
      </section>

      <section className="flex flex-col gap-2">
        <div className="flex min-h-8 items-center justify-between gap-2">
          <h2 className="text-overline text-muted-foreground uppercase">
            {t('workspaceDashboard:recentGroups')}
          </h2>
          <Button variant="ghost" size="sm" asChild>
            <Link to={'/console' as never}>
              {t('workspaceDashboard:viewAll')}{' '}
              <ArrowRight className="ml-1 size-3.5" />
            </Link>
          </Button>
        </div>
        <ListTruncationBadge
          query={groupsQ}
          label={t('workspaceDashboard:truncation.label', {
            n: groupsQ.data?.items?.length,
          })}
          hint={t('workspaceDashboard:truncation.hint')}
          className="px-0 pt-0 pb-1"
        />
        {groupsQ.data?.items.length === 0 ? (
          <EmptyState
            title={t('workspaceDashboard:noGroups')}
            description={t('workspaceDashboard:noGroupsHint')}
            action={
              <Button asChild size="sm" variant="secondary">
                <Link to={'/console' as never}>
                  {t('workspaceDashboard:viewAll')}
                </Link>
              </Button>
            }
          />
        ) : (
          <XScroll contentKey={groupsQ.data?.items.length}>
            <StaticTable oneLine>
              <thead>
                <tr>
                  <th>{t('workspaceDashboard:name')}</th>
                  <th>{t('workspaceDashboard:slug')}</th>
                  <th>{t('workspaceDashboard:status')}</th>
                </tr>
              </thead>
              <tbody>
                {groupsQ.data?.items.map((g) => (
                  <tr key={g.id}>
                    <td className="font-medium">{g.name}</td>
                    <td className="font-mono text-caption">{g.slug}</td>
                    <td>
                      <Badge
                        variant={g.status === 'active' ? 'success' : 'neutral'}
                      >
                        {g.status}
                      </Badge>
                    </td>
                  </tr>
                ))}
              </tbody>
            </StaticTable>
          </XScroll>
        )}
      </section>
    </IntelPage>
  )
}
