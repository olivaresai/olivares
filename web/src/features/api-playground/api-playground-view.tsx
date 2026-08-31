// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { History } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { PageHeader } from '@/components/ui/page-header'
import { Skeleton } from '@/components/ui/skeleton'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useAuth } from '@/lib/auth/context'
import { ApiError } from '@/lib/api/errors'
import {
  filterEndpointsForAdmin,
  groupByTag,
  parseSpecs,
} from './openapi-parser'
import { EndpointTree } from './endpoint-tree'
import { RequestPanel } from './request-panel'
import { ResponsePanel } from './response-panel'
import { RequestHistory } from './request-history'
import { usePlayground } from './use-playground'
import './i18n'

async function fetchSpecAt(
  url: string,
  loadErrorMessage: string,
): Promise<Record<string, unknown>> {
  const resp = await fetch(url)
  if (!resp.ok) {
    throw new ApiError(resp.status, 'internal', loadErrorMessage)
  }
  return resp.json()
}

export function ApiPlaygroundView() {
  const auth = useAuth()
  const { t } = useTranslation('apiPlayground')
  const [rightTab, setRightTab] = useState<'response' | 'history'>('response')
  const canUsePlayground = auth.can('tenant:admin')
  const isSystemAdmin = auth.can('system:admin')

  const {
    selectedEndpoint,
    selectEndpoint,
    response,
    isLoading,
    isStreaming,
    history,
    clearHistory,
  } = usePlayground()

  // The stable core doc (24-month promise) is required; the beta doc adds the
  // ~460 /v1/m/<ns>/ module routes and is best-effort — an older engine may 404 it,
  // in which case the playground still lists the core API.
  const specQuery = useQuery({
    queryKey: ['openapi-spec'],
    queryFn: () => fetchSpecAt('/openapi.json', t('errors.loadSpec')),
    staleTime: 5 * 60_000,
    gcTime: 30 * 60_000,
    enabled: canUsePlayground,
  })
  const betaQuery = useQuery({
    queryKey: ['openapi-spec', 'beta'],
    queryFn: () => fetchSpecAt('/openapi.beta.json', t('errors.loadSpec')),
    staleTime: 5 * 60_000,
    gcTime: 30 * 60_000,
    retry: false,
    enabled: canUsePlayground,
  })

  const endpoints = useMemo(
    () =>
      specQuery.data
        ? filterEndpointsForAdmin(
            parseSpecs(specQuery.data, betaQuery.data),
            isSystemAdmin,
          )
        : [],
    [specQuery.data, betaQuery.data, isSystemAdmin],
  )

  const groups = useMemo(
    () => (specQuery.data ? groupByTag(specQuery.data, endpoints) : []),
    [specQuery.data, endpoints],
  )

  const betaCount = useMemo(
    () => endpoints.filter((e) => e.stability === 'beta').length,
    [endpoints],
  )

  const visibleSelectedEndpoint = useMemo(
    () =>
      selectedEndpoint &&
      endpoints.some(
        (endpoint) =>
          endpoint.method === selectedEndpoint.method &&
          endpoint.path === selectedEndpoint.path,
      )
        ? selectedEndpoint
        : null,
    [endpoints, selectedEndpoint],
  )

  if (!canUsePlayground) return <ForbiddenState />

  if (specQuery.isLoading) {
    // Keep the page's <h1> present while the OpenAPI spec loads (matches the house
    // pattern of PageHeader-above-content), so the view is always announced with a
    // heading and never renders an h1-less skeleton (AT gate).
    return (
      <div className="flex h-full flex-col">
        <PageHeader title={t('title')} />
        <div className="space-y-4 p-6">
          <Skeleton className="h-8 w-64" />
          <div className="flex gap-4">
            <Skeleton className="h-[60vh] w-64" />
            <Skeleton className="h-[60vh] flex-1" />
            <Skeleton className="h-[60vh] flex-1" />
          </div>
        </div>
      </div>
    )
  }

  if (specQuery.error) {
    return (
      <div className="flex h-full flex-col">
        <PageHeader title={t('title')} />
        <ErrorState retry={() => void specQuery.refetch()} />
      </div>
    )
  }

  return (
    <div className="flex h-full flex-col">
      <PageHeader
        title={t('title')}
        description={
          betaCount > 0
            ? t('summaryWithBeta', {
                endpoints: endpoints.length,
                groups: groups.length,
                beta: betaCount,
              })
            : t('summary', {
                endpoints: endpoints.length,
                groups: groups.length,
              })
        }
      />

      <div className="flex min-h-0 flex-1 divide-x border-t">
        {/* Left panel: endpoint tree */}
        <div className="w-72 shrink-0 overflow-hidden">
          <EndpointTree
            groups={groups}
            selected={visibleSelectedEndpoint}
            onSelect={selectEndpoint}
          />
        </div>

        {/* Center panel: request editor */}
        <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
          {visibleSelectedEndpoint ? (
            <RequestPanel endpoint={visibleSelectedEndpoint} />
          ) : (
            <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
              {t('selectEndpoint')}
            </div>
          )}
        </div>

        {/* Right panel: response + history */}
        <div className="flex min-w-0 flex-1 flex-col overflow-hidden">
          <Tabs
            value={rightTab}
            onValueChange={(v) => setRightTab(v as 'response' | 'history')}
            className="flex h-full flex-col"
          >
            <TabsList className="mx-3 mt-2">
              <TabsTrigger value="response">{t('response')}</TabsTrigger>
              <TabsTrigger value="history" className="gap-1.5">
                <History className="h-3 w-3" />
                {t('history')}
                {history.length > 0 && (
                  <span className="ml-1 rounded-full bg-muted px-1.5 text-[10px] tabular-nums">
                    {history.length}
                  </span>
                )}
              </TabsTrigger>
            </TabsList>

            <TabsContent value="response" className="flex-1 overflow-hidden">
              <ResponsePanel
                response={visibleSelectedEndpoint ? response : null}
                isLoading={isLoading}
                isStreaming={isStreaming}
              />
            </TabsContent>

            <TabsContent value="history" className="flex-1 overflow-y-auto">
              <RequestHistory entries={history} onClear={clearHistory} />
            </TabsContent>
          </Tabs>
        </div>
      </div>
    </div>
  )
}

export default ApiPlaygroundView
