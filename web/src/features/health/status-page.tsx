// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'

import { currentLanguage } from '@/lib/i18n'
import {
  CheckCircle2,
  AlertTriangle,
  XCircle,
  CircleDashed,
  RefreshCw,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { AuthShell } from '@/components/layout/auth-shell'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { cn } from '@/lib/utils'
import { healthApi, healthKeys } from './api'
import type { ComponentStatus, ComponentStatusDTO } from './types'
import './i18n'

const REFETCH_INTERVAL = 30_000

export function StatusPage() {
  const { t } = useTranslation('health')

  const query = useQuery({
    queryKey: healthKeys.publicStatus(),
    queryFn: () => healthApi.publicStatus(),
    refetchInterval: REFETCH_INTERVAL,
  })

  const data = query.data
  // No answer is NOT an answer. Until /status replies, the banner claims
  // nothing: defaulting to 'operational' painted "All Systems Operational" over
  // an engine the console could not even reach.
  const overallStatus = data?.status

  return (
    // The shell every public single-card page uses: its `main` landmark and skip link (this
    // page needs no sign-in, so it never passes through the signed-in layout), the product
    // mark above, and the page's one card on the frame.
    <AuthShell>
      <Card>
        <CardContent>
          <div className="flex flex-col gap-5">
            <header className="text-center">
              <h1 className="font-display text-title text-foreground">
                {t('statusPage.title')}
              </h1>
              <p className="mt-1 text-body text-muted-foreground">
                {t('statusPage.subtitle')}
              </p>
            </header>

            {/* Overall status banner. `not_configured` is deliberately NEUTRAL, not a
                warning tone: an install that simply has optional capabilities left
                unprovisioned is incomplete, not broken, and painting it amber taught
                operators to ignore amber. An unanswered probe gets the same neutral
                tone and says so — never a green verdict with no evidence. */}
            <div
              className={cn(
                'rounded-lg border p-4 text-center',
                overallStatus === 'operational' &&
                  'border-success/30 bg-success/5',
                (overallStatus === 'not_configured' ||
                  overallStatus === undefined) &&
                  'border-border bg-muted/40',
                overallStatus === 'degraded' &&
                  'border-warning/30 bg-warning/5',
                overallStatus === 'outage' && 'border-danger/30 bg-danger/5',
              )}
            >
              <StatusIcon
                status={overallStatus}
                className="mx-auto mb-2 size-8"
              />
              <div className="font-display text-title">
                {overallStatus ? (
                  t(`statusPage.overall.${overallStatus}`)
                ) : query.isLoading ? (
                  <span className="mx-auto block h-5 w-48 rounded bg-muted" />
                ) : (
                  t('statusPage.unavailable')
                )}
              </div>
            </div>

            {/* Component list */}
            <div className="space-y-0 divide-y rounded-lg border">
              {query.isLoading ? (
                <>
                  <ComponentSkeleton />
                  <ComponentSkeleton />
                  <ComponentSkeleton />
                  <ComponentSkeleton />
                </>
              ) : data ? (
                data.components.map((c) => (
                  <ComponentRow key={c.name} component={c} />
                ))
              ) : (
                <div className="p-6 text-center text-body text-muted-foreground">
                  {t('statusPage.unavailable')}
                </div>
              )}
            </div>

            <footer className="flex items-center justify-between text-caption text-muted-foreground">
              <span>
                {data?.timestamp &&
                  t('statusPage.lastUpdated', {
                    time: new Date(data.timestamp).toLocaleTimeString(
                      currentLanguage(),
                    ),
                  })}
              </span>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => void query.refetch()}
                disabled={query.isFetching}
              >
                <RefreshCw
                  aria-hidden
                  className={query.isFetching ? 'animate-spin' : undefined}
                />
                {t('refresh')}
              </Button>
            </footer>
          </div>
        </CardContent>
      </Card>
    </AuthShell>
  )
}

function ComponentRow({ component }: { component: ComponentStatusDTO }) {
  const { t } = useTranslation('health')
  return (
    <div className="flex items-center justify-between px-4 py-3">
      <span className="text-body font-medium text-foreground">
        {t(`statusPage.component.${component.name}`, {
          defaultValue: component.name,
        })}
      </span>
      <div className="flex items-center gap-2">
        <StatusIcon status={component.status} className="size-4" />
        <span
          className={cn(
            'text-body',
            component.status === 'operational' && 'text-success',
            component.status === 'not_configured' && 'text-muted-foreground',
            component.status === 'degraded' && 'text-warning',
            component.status === 'outage' && 'text-danger',
          )}
        >
          {t(`statusPage.status.${component.status}`, {
            defaultValue: component.status,
          })}
        </span>
      </div>
    </div>
  )
}

function StatusIcon({
  status,
  className,
}: {
  /** undefined = the engine has not answered; the icon must claim nothing. */
  status: ComponentStatus | undefined
  className?: string
}) {
  if (!status) {
    return <CircleDashed className={cn('text-muted-foreground', className)} />
  }
  switch (status) {
    case 'operational':
      return <CheckCircle2 className={cn('text-success', className)} />
    case 'not_configured':
      return <CircleDashed className={cn('text-muted-foreground', className)} />
    case 'degraded':
      return <AlertTriangle className={cn('text-warning', className)} />
    case 'outage':
      return <XCircle className={cn('text-danger', className)} />
    default:
      // Unknown to this build: show it as a warning rather than a tick. A state
      // the console cannot name is not evidence that anything is fine.
      return <AlertTriangle className={cn('text-warning', className)} />
  }
}

function ComponentSkeleton() {
  return (
    <div className="flex items-center justify-between px-4 py-3">
      <div className="h-4 w-24 rounded bg-muted" />
      <div className="h-4 w-20 rounded bg-muted" />
    </div>
  )
}
