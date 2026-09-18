// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery, type UseQueryResult } from '@tanstack/react-query'
import { AlertTriangle, Info } from 'lucide-react'
import { useId, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { agentOpsApi, agentOpsKeys } from './api'
import { useAuthBoundary } from './auth-boundary'
import {
  HostToolsIdentityMismatchError,
  hostToolIdentityOf,
  hostToolObservationApplies,
  hostToolObservationMatches,
  type HostToolObservation,
} from './host-tools'
import {
  isProfileChangedError,
  type SessionLaunchReadiness,
} from './launch-readiness'

export type ProfileHostToolsQuery = UseQueryResult<
  HostToolObservation,
  Error
> & {
  observationEnabled: boolean
  /** The only observation that may be painted: settled, this snapshot, no error. */
  current: HostToolObservation | undefined
}

/**
 * The host-tool observation of the readiness snapshot on screen. It requests only with
 * sessions:profile:read and only while that snapshot's program check is unresolved.
 * The key carries tenant, authority epoch and the snapshot identity; every subscription
 * refetches, nothing retries or polls, and an answer for another identity is refused
 * before it reaches the cache.
 */
export function useProfileHostTools(
  readiness: SessionLaunchReadiness,
): ProfileHostToolsQuery {
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const id = hostToolIdentityOf(readiness)
  const enabled =
    can('sessions:profile:read') &&
    hostToolObservationApplies(readiness) &&
    id.profileRef !== ''

  const query = useQuery({
    queryKey: agentOpsKeys.hostTools(
      activeTenant,
      boundary.epoch,
      id.profileRef,
      {
        profile_version: id.profileVersion,
        driver: id.driver,
        environment_ref: id.environmentRef,
        evaluated_environment_ref: id.evaluatedEnvironmentRef,
      },
    ),
    queryFn: async ({ signal }) => {
      const obs = await agentOpsApi.profileHostTools(id.profileRef, { signal })
      if (!hostToolObservationMatches(obs, id)) {
        throw new HostToolsIdentityMismatchError()
      }
      return obs
    },
    enabled,
    staleTime: 0,
    gcTime: 0,
    retry: false,
    // A remount (profile A→B→A, a reread of readiness) never paints what an earlier
    // subscription read: it asks again and hides the entry until the answer settles.
    refetchOnMount: 'always',
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  })

  const current =
    enabled &&
    query.status === 'success' &&
    !query.isFetching &&
    hostToolObservationMatches(query.data, id)
      ? query.data
      : undefined
  return { ...query, observationEnabled: enabled, current }
}

export function HostToolsPanel({
  readiness,
}: {
  readiness: SessionLaunchReadiness
}) {
  const { t } = useTranslation('agentops')
  const headingId = useId()
  const query = useProfileHostTools(readiness)
  if (!query.observationEnabled) return null

  const current = query.current
  const err = query.error
  const notice =
    query.isError && !query.isFetching
      ? err instanceof HostToolsIdentityMismatchError
        ? t('hostTools.mismatch')
        : isProfileChangedError(err)
          ? t('hostTools.conflict')
          : err instanceof ApiError && err.status === 401
            ? t('hostTools.unauthenticated')
            : err instanceof ApiError && err.status === 403
              ? t('hostTools.forbidden')
              : err instanceof ApiError && err.status === 404
                ? t('hostTools.notFound')
                : t('hostTools.unavailable')
      : null
  const answering = current
    ? current.evaluated_environment_ref
    : (readiness.evaluated_environment_ref ?? '')

  return (
    <section
      aria-labelledby={headingId}
      className="flex flex-col gap-2 rounded-md border border-border px-2.5 py-2"
      data-testid="host-tools"
    >
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h4 id={headingId} className="text-caption font-medium text-foreground">
          {t('hostTools.title')}
        </h4>
        <Button
          type="button"
          variant="secondary"
          size="sm"
          onClick={() => void query.refetch()}
          disabled={query.isFetching}
        >
          {query.isFetching && <Spinner className="size-3.5" />}
          {query.isFetching
            ? t('hostTools.refreshing')
            : t('hostTools.refresh')}
        </Button>
      </div>
      <p className="font-mono text-[11px] text-muted-foreground">
        {t('hostTools.environment', {
          environment: readiness.environment_ref,
          answering: answering || t('hostTools.noIdentity'),
        })}
      </p>

      {query.isFetching && !current && (
        <p
          className="flex items-center gap-2 text-caption text-muted-foreground"
          role="status"
        >
          <Spinner className="size-3.5" />
          {t('hostTools.loading')}
        </p>
      )}
      {notice && <HostToolsNotice tone="warning">{notice}</HostToolsNotice>}
      {current && <HostToolsBody obs={current} />}

      <p className="text-[11px] text-muted-foreground">
        {t('hostTools.advisory')}
      </p>
    </section>
  )
}

function HostToolsBody({ obs }: { obs: HostToolObservation }) {
  const { t } = useTranslation('agentops')
  const observedAt = (
    <p className="text-[11px] text-muted-foreground">
      {t('hostTools.observedAt', { time: obs.observed_at })}
    </p>
  )
  if (obs.state !== 'observed') {
    return (
      <div className="flex flex-col gap-1" data-state={obs.state}>
        <HostToolsNotice tone={obs.state === 'unknown' ? 'warning' : 'neutral'}>
          {t(`hostTools.state.${obs.state}`)}
        </HostToolsNotice>
        {observedAt}
      </div>
    )
  }
  const total = obs.groups.reduce((sum, g) => sum + g.count, 0)
  return (
    <div className="flex flex-col gap-1" data-state={obs.state}>
      <p className="text-caption text-foreground">
        {t('hostTools.summary', { total })}
      </p>
      <ul
        className="flex flex-col gap-1"
        aria-label={t('hostTools.groupsLabel')}
      >
        {obs.groups.map((g) => (
          <li
            key={`${g.origin}|${g.match}|${g.executable}|${g.configured}`}
            className="rounded-sm bg-muted px-2 py-1 text-caption text-muted-foreground"
            data-origin={g.origin}
            data-match={g.match}
            data-configured={g.configured}
          >
            <span className="font-medium text-foreground">
              {t(`hostTools.origin.${g.origin}`)}
            </span>
            {' · '}
            {t(`hostTools.match.${g.match}`)}
            {' · '}
            {t(`hostTools.executable.${g.executable ? 'true' : 'false'}`)}
            {' · '}
            {t(`hostTools.configured.${g.configured}`)}
            {' · '}
            {t('hostTools.groupCount', { total: g.count })}
          </li>
        ))}
      </ul>
      {observedAt}
    </div>
  )
}

function HostToolsNotice({
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
          ? 'flex items-start gap-2 rounded-md border border-warning-line bg-warning-soft px-2.5 py-2 text-caption text-warning'
          : 'flex items-start gap-2 rounded-md border border-border bg-muted px-2.5 py-2 text-caption text-muted-foreground'
      }
    >
      <Icon className="mt-0.5 size-3.5 shrink-0" aria-hidden="true" />
      <div className="min-w-0">{children}</div>
    </div>
  )
}
