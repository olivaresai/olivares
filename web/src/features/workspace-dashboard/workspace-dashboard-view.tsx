// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { ArrowRight, Bot, Boxes, FolderOpen, Layers, Users } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { EmptyState } from '@/components/ui/empty-state'
import {
  IntelPage,
  ListTruncationBadge,
  MetricStat,
  StatGrid,
} from '@/features/_intel'
import { useAuth } from '@/lib/auth/context'
import { cuentaConSuelo } from './count-floor'
import { formatInt } from '@/lib/format'
import { useWorkspaceStore } from '@/stores/workspace'
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
          icon={<Layers />}
          title={t('workspaceDashboard:title')}
          description={t('workspaceDashboard:selectPrompt')}
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
  const title = s?.name ?? workspaceName ?? workspaceId

  return (
    <IntelPage
      icon={Layers}
      title={title}
      description={
        s && (
          <span className="flex items-center gap-2 font-mono text-sm text-muted-foreground">
            {s.slug}
            {s.is_default && (
              <Badge variant="neutral">
                {t('workspaceDashboard:defaultBadge')}
              </Badge>
            )}
          </span>
        )
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

      <div className="grid gap-5 lg:grid-cols-2">
        {/* Agents table */}
        <Card>
          <CardHeader className="flex flex-row items-center justify-between pb-3">
            <div>
              <CardTitle className="text-base">
                {t('workspaceDashboard:recentAgents')}
              </CardTitle>
              <CardDescription>
                {s
                  ? `${cuentaConSuelo(s.agent_count, s.agent_count_capped, formatInt)} ${t('workspaceDashboard:agents').toLowerCase()}`
                  : ''}
              </CardDescription>
            </div>
            <Button variant="ghost" size="sm" asChild>
              <Link to={'/inventory' as never}>
                {t('workspaceDashboard:viewAll')}{' '}
                <ArrowRight className="ml-1 size-3.5" />
              </Link>
            </Button>
          </CardHeader>
          <CardContent>
            <ListTruncationBadge
              query={agentsQ}
              label={t('workspaceDashboard:truncation.label', {
                n: agentsQ.data?.items?.length,
              })}
              hint={t('workspaceDashboard:truncation.hint')}
              className="px-0 pt-0 pb-3"
            />
            {agentsQ.data?.items.length === 0 ? (
              <p className="py-4 text-center text-sm text-muted-foreground">
                {t('workspaceDashboard:noAgents')}
              </p>
            ) : (
              <div className="overflow-hidden rounded-lg border border-border">
                <table className="w-full text-sm">
                  <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
                    <tr>
                      <th className="px-3 py-2 font-medium">
                        {t('workspaceDashboard:name')}
                      </th>
                      <th className="px-3 py-2 font-medium">
                        {t('workspaceDashboard:kind')}
                      </th>
                      <th className="px-3 py-2 font-medium">
                        {t('workspaceDashboard:status')}
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {agentsQ.data?.items.map((a) => (
                      <tr
                        key={a.id}
                        className="border-t border-border align-top"
                      >
                        <td className="px-3 py-2 font-medium">{a.name}</td>
                        <td className="px-3 py-2">
                          <Badge variant="outline">{a.kind}</Badge>
                        </td>
                        <td className="px-3 py-2">
                          <Badge
                            variant={
                              a.status === 'active' ? 'success' : 'neutral'
                            }
                          >
                            {a.status}
                          </Badge>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </CardContent>
        </Card>

        {/* Agent groups table */}
        <Card>
          <CardHeader className="flex flex-row items-center justify-between pb-3">
            <div>
              <CardTitle className="text-base">
                {t('workspaceDashboard:recentGroups')}
              </CardTitle>
              <CardDescription>
                {s
                  ? `${cuentaConSuelo(s.group_count, s.group_count_capped, formatInt)} ${t('workspaceDashboard:groups').toLowerCase()}`
                  : ''}
              </CardDescription>
            </div>
            <Button variant="ghost" size="sm" asChild>
              <Link to={'/console' as never}>
                {t('workspaceDashboard:viewAll')}{' '}
                <ArrowRight className="ml-1 size-3.5" />
              </Link>
            </Button>
          </CardHeader>
          <CardContent>
            <ListTruncationBadge
              query={groupsQ}
              label={t('workspaceDashboard:truncation.label', {
                n: groupsQ.data?.items?.length,
              })}
              hint={t('workspaceDashboard:truncation.hint')}
              className="px-0 pt-0 pb-3"
            />
            {groupsQ.data?.items.length === 0 ? (
              <p className="py-4 text-center text-sm text-muted-foreground">
                {t('workspaceDashboard:noGroups')}
              </p>
            ) : (
              <div className="overflow-hidden rounded-lg border border-border">
                <table className="w-full text-sm">
                  <thead className="bg-muted/40 text-left text-xs text-muted-foreground">
                    <tr>
                      <th className="px-3 py-2 font-medium">
                        {t('workspaceDashboard:name')}
                      </th>
                      <th className="px-3 py-2 font-medium">
                        {t('workspaceDashboard:slug')}
                      </th>
                      <th className="px-3 py-2 font-medium">
                        {t('workspaceDashboard:status')}
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {groupsQ.data?.items.map((g) => (
                      <tr
                        key={g.id}
                        className="border-t border-border align-top"
                      >
                        <td className="px-3 py-2 font-medium">{g.name}</td>
                        <td className="px-3 py-2 font-mono text-xs">
                          {g.slug}
                        </td>
                        <td className="px-3 py-2">
                          <Badge
                            variant={
                              g.status === 'active' ? 'success' : 'neutral'
                            }
                          >
                            {g.status}
                          </Badge>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </CardContent>
        </Card>
      </div>

      {/* Workspace metadata */}
      {summaryQ.data && (
        <Card>
          <CardHeader className="pb-3">
            <CardTitle className="text-base">
              {t('workspaceDashboard:workspaceInfo')}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <dl className="grid grid-cols-2 gap-x-6 gap-y-2 text-sm sm:grid-cols-4">
              <div>
                <dt className="text-muted-foreground">
                  {t('workspaceDashboard:slug')}
                </dt>
                <dd className="font-mono">{s?.slug}</dd>
              </div>
              <div>
                <dt className="text-muted-foreground">
                  {t('workspaceDashboard:status')}
                </dt>
                <dd className="capitalize">
                  {s?.slug === 'default' ? 'active (default)' : 'active'}
                </dd>
              </div>
            </dl>
          </CardContent>
        </Card>
      )}
    </IntelPage>
  )
}
