// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery, type UseQueryResult } from '@tanstack/react-query'
import { AlertTriangle, Info } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { agentOpsApi, agentOpsKeys } from './api'
import { useAuthBoundary } from './auth-boundary'
import { HostToolsPanel } from './host-tools-panel'
import { hostToolObservationApplies } from './host-tools'
import {
  checkHasCause,
  currentLaunchReadinessObservation,
  hasManagedInjectionLimitation,
  isProfileChangedError,
  readinessObservationIsUnusable,
  type LaunchReadinessAggregate,
  type LaunchReadinessIsolation,
  type LaunchReadinessTransport,
  type SessionLaunchReadiness,
} from './launch-readiness'

export interface LaunchReadinessQueryInput {
  enabled: boolean
  profileRef: string | null
  transport: LaunchReadinessTransport
  isolation: LaunchReadinessIsolation
  /** Profile snapshot identity: a change drops the previous observation. */
  profileState?: string
  authSource?: string
  updatedAt?: string
}

export type ProfileLaunchReadinessQuery = UseQueryResult<
  SessionLaunchReadiness,
  Error
> & { observationEnabled: boolean }

export function useProfileLaunchReadiness(
  input: LaunchReadinessQueryInput,
): ProfileLaunchReadinessQuery {
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const canRead = can('sessions:profile:read')
  const ref = input.profileRef
  const enabled = input.enabled && canRead && !!ref

  const query = useQuery({
    queryKey: agentOpsKeys.launchReadiness(
      activeTenant,
      boundary.epoch,
      ref ?? '',
      {
        transport: input.transport,
        isolation: input.isolation,
        state: input.profileState,
        auth_source: input.authSource,
        updated_at: input.updatedAt,
      },
    ),
    queryFn: ({ signal }) =>
      agentOpsApi.profileLaunchReadiness(
        ref as string,
        { transport: input.transport, isolation: input.isolation },
        { signal },
      ),
    enabled,
    staleTime: 0,
    gcTime: 0,
    retry: false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  })
  return { ...query, observationEnabled: enabled }
}

const aggregateVariant: Record<LaunchReadinessAggregate, BadgeVariant> = {
  ready: 'success',
  not_configured: 'warning',
  unsupported: 'danger',
  unknown: 'info',
}

export function LaunchReadinessPanel({
  query,
  profileRef,
  transport,
  isolation,
  compact = false,
}: {
  query: ProfileLaunchReadinessQuery
  profileRef: string
  transport: LaunchReadinessTransport
  isolation: LaunchReadinessIsolation
  compact?: boolean
}) {
  const { t } = useTranslation('agentops')
  const selection = { profileRef, transport, isolation }
  const current = currentLaunchReadinessObservation(query, selection)
  const err = query.error
  const conflict = isProfileChangedError(err)
  const unusable = readinessObservationIsUnusable(err)
  const showLoading = query.observationEnabled && query.isFetching && !current

  return (
    <section
      className={
        compact
          ? 'flex flex-col gap-2 rounded-md border border-border bg-muted/40 px-2.5 py-2'
          : 'flex flex-col gap-3'
      }
      aria-live="polite"
      data-testid="launch-readiness"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-medium text-foreground">
          {t('readiness.title')}
        </h3>
        <p className="font-mono text-[11px] text-muted-foreground">
          {t('readiness.transport')}: {transport} · {t('readiness.isolation')}:{' '}
          {isolation}
        </p>
      </div>

      {showLoading && (
        <p
          className="flex items-center gap-2 text-xs text-muted-foreground"
          role="status"
        >
          <Spinner className="size-3.5" />
          {t('readiness.loading')}
        </p>
      )}

      {conflict && (
        <ReadinessNotice tone="warning">
          {t('readiness.conflict')}
          <Button
            type="button"
            variant="secondary"
            size="sm"
            className="mt-2"
            onClick={() => void query.refetch()}
            disabled={query.isFetching}
          >
            {query.isFetching && <Spinner className="size-3.5" />}
            {query.isFetching
              ? t('readiness.rereading')
              : t('readiness.reread')}
          </Button>
        </ReadinessNotice>
      )}

      {unusable && (
        <ReadinessNotice tone="warning">
          {err instanceof ApiError && err.status === 401
            ? t('readiness.unauthenticated')
            : err instanceof ApiError && err.status === 403
              ? t('readiness.forbidden')
              : t('readiness.notFound')}
        </ReadinessNotice>
      )}

      {query.isError && !conflict && !unusable && (
        <ReadinessNotice tone="warning">
          {t('readiness.unavailable')}
        </ReadinessNotice>
      )}

      {current && <ReadinessBody data={current} />}
    </section>
  )
}

function ReadinessBody({ data }: { data: SessionLaunchReadiness }) {
  const { t } = useTranslation('agentops')
  const caps = data.transport_capabilities
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant={aggregateVariant[data.configuration_state]}>
          {t(`readiness.state.${data.configuration_state}`)}
        </Badge>
        <span className="text-[11px] text-muted-foreground">
          {t(`readiness.protocol.${caps.protocol}`)}
        </span>
      </div>

      {caps.io === 'lifecycle_only' && (
        <ReadinessNotice tone="warning">
          {t('readiness.remoteControl')}
        </ReadinessNotice>
      )}
      {hasManagedInjectionLimitation(data) && (
        <ReadinessNotice tone="warning">
          {t('readiness.managedInjectionUnused')}
        </ReadinessNotice>
      )}
      {(caps.protocol === 'codex_app_server' ||
        caps.protocol === 'grok_acp' ||
        caps.protocol === 'opencode_acp') && (
        <ReadinessNotice tone="neutral">
          {caps.protocol === 'codex_app_server'
            ? t('readiness.codexProtocol')
            : caps.protocol === 'grok_acp'
              ? t('readiness.grokProtocol')
              : t('readiness.opencodeProtocol')}
        </ReadinessNotice>
      )}

      <ReadinessNotice tone="neutral">
        {t('readiness.providerAuth')}
      </ReadinessNotice>
      <ReadinessNotice tone="neutral">
        {t('readiness.launchAuth')}
      </ReadinessNotice>

      <ul className="flex flex-col gap-1">
        {data.checks.map((check) => (
          <li
            key={check.check}
            className="flex flex-col gap-0.5 rounded-sm px-0.5 py-0.5 text-xs"
            data-check={check.check}
            data-state={check.state}
            data-code={check.code}
          >
            <span className="flex flex-wrap items-baseline gap-x-2">
              <span className="font-medium text-foreground">
                {t(`readiness.check.${check.check}`)}
              </span>
              <span className="text-muted-foreground">
                {t(`readiness.checkState.${check.state}`)}
              </span>
            </span>
            {checkHasCause(check) && (
              <span className="text-muted-foreground">
                {t(`readiness.code.${check.code}`)}
                {check.remediation
                  ? ` ${t(`readiness.remediation.${check.remediation}`)}`
                  : ''}
              </span>
            )}
          </li>
        ))}
      </ul>

      {hostToolObservationApplies(data) && <HostToolsPanel readiness={data} />}

      <div className="text-[11px] text-muted-foreground">
        <p className="font-medium text-foreground">
          {t('readiness.remaining')}
        </p>
        <ul className="mt-0.5 list-disc pl-4">
          {data.remaining_checks.map((code) => (
            <li key={code}>{t(`readiness.remainingCheck.${code}`)}</li>
          ))}
        </ul>
      </div>
    </div>
  )
}

function ReadinessNotice({
  children,
  tone,
}: {
  children: ReactNode
  tone: 'warning' | 'neutral'
}) {
  const Icon = tone === 'warning' ? AlertTriangle : Info
  return (
    <div
      className={
        tone === 'warning'
          ? 'flex items-start gap-2 rounded-md border border-warning-line bg-warning-soft px-2.5 py-2 text-xs text-warning'
          : 'flex items-start gap-2 rounded-md border border-border bg-muted px-2.5 py-2 text-xs text-muted-foreground'
      }
    >
      <Icon className="mt-0.5 size-3.5 shrink-0" />
      <div className="min-w-0">{children}</div>
    </div>
  )
}
