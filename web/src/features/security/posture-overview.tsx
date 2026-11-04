// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Current exposure, not detective findings. Every row answers independently from
// the same reads as its destination page. A denied, pending or failed read never
// becomes a reassuring zero, and only successful current answers reach a count.
import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { SectionCard } from '@/features/_intel'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { consoleApi, consoleKeys } from '@/features/console/api'
import { governanceApi, governanceKeys } from '@/features/governance/api'
import { pendingCount } from '@/features/governance/use-pending-approvals'
import { killswitchApi, killswitchKeys } from '@/features/killswitch/api'
import { EVIDENCE_PAGE, modelsApi, modelsKeys } from '@/features/models/api'
import { providerKeys, providersApi } from '@/features/providers/api'
import { ApiError } from '@/lib/api/errors'
import { useAuth } from '@/lib/auth/context'
import { formatInt } from '@/lib/format'

const POLL_MS = 30_000
const PENDING = { status: 'pending' } as const

type ReadState = 'loading' | 'unavailable' | 'error' | 'ready'

interface ReadQuery {
  isSuccess: boolean
  isError: boolean
  error: unknown
}

interface PostureRow {
  id: string
  label: string
  description: string
  href: string
  state: ReadState
  value?: string
  partial?: boolean
  error?: unknown
  retry?: () => void
}

function readState(permitted: boolean, query: ReadQuery): ReadState {
  if (!permitted) return 'unavailable'
  if (query.isError) {
    return query.error instanceof ApiError &&
      query.error.isForbidden &&
      !query.error.isStepUpRequired
      ? 'unavailable'
      : 'error'
  }
  return query.isSuccess ? 'ready' : 'loading'
}

function count(value: number, partial = false): string {
  return `${formatInt(value)}${partial ? '+' : ''}`
}

function PostureRowView({ row }: { row: PostureRow }) {
  const { t } = useTranslation(['security', 'common'])
  const labelID = `security-posture-${row.id}-label`
  const value =
    row.state === 'loading'
      ? t('common:states.loading')
      : row.state === 'unavailable'
        ? t('security:posture.notAvailable')
        : row.value

  return (
    <li
      aria-labelledby={labelID}
      className="flex flex-col gap-3 py-4 sm:grid sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center sm:gap-6"
    >
      <div className="min-w-0">
        <h3 id={labelID} className="text-body font-medium text-foreground">
          {row.label}
        </h3>
        <p className="mt-1 max-w-2xl text-caption text-muted-foreground">
          {row.description}
        </p>
      </div>
      <div className="flex min-w-0 shrink-0 items-center justify-between gap-4 sm:justify-end">
        {row.state === 'error' ? (
          <QueryErrorState error={row.error} retry={row.retry} />
        ) : (
          <span
            role={row.state === 'loading' ? 'status' : undefined}
            className="text-body font-semibold whitespace-nowrap tabular-nums text-foreground"
          >
            {value}
          </span>
        )}
        <Link
          to={row.href as never}
          aria-label={`${t('security:posture.review')}: ${row.label}`}
          className="rounded-sm text-caption font-medium text-accent-text outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
        >
          {t('security:posture.review')}
        </Link>
      </div>
    </li>
  )
}

export function SecurityPostureOverview() {
  const { t } = useTranslation('security')
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const inTenant = !!activeTenant
  const canReadMembers = can('user:read') && inTenant
  const canReadTokens = can('token:read') && inTenant
  const canReadApprovals = can('governance:approval:read') && inTenant
  const canReadKillSwitch = can('governance:killswitch:read') && inTenant
  const canReadProviders = can('sessions:provider:read') && inTenant
  const canReadPolicy = can('models:admission:read') && inTenant

  const membersQ = useQuery({
    queryKey: [...consoleKeys.members(activeTenant), 'b', boundary.epoch],
    queryFn: () => consoleApi.listMembers(),
    enabled: canReadMembers,
  })
  const tokenParams = { limit: EVIDENCE_PAGE }
  const tokensQ = useQuery({
    queryKey: [
      ...consoleKeys.tokens(activeTenant, tokenParams),
      'b',
      boundary.epoch,
    ],
    queryFn: () => consoleApi.listTokens(tokenParams),
    enabled: canReadTokens,
  })
  const approvalsQ = useQuery({
    queryKey: [
      ...governanceKeys.approvals(activeTenant, PENDING),
      'b',
      boundary.epoch,
    ],
    queryFn: () => governanceApi.listApprovals(PENDING),
    enabled: canReadApprovals,
    refetchInterval: POLL_MS,
  })
  const killSwitchQ = useQuery({
    queryKey: [...killswitchKeys.state(activeTenant), 'b', boundary.epoch],
    queryFn: () => killswitchApi.state(),
    enabled: canReadKillSwitch,
    refetchInterval: POLL_MS,
  })
  const providersQ = useQuery({
    queryKey: providerKeys.list(activeTenant, boundary.epoch),
    queryFn: ({ signal }) => providersApi.list(undefined, { signal }),
    enabled: canReadProviders,
  })
  const policyQ = useQuery({
    queryKey: [
      ...modelsKeys.admissionPolicy(activeTenant),
      'b',
      boundary.epoch,
    ],
    queryFn: () => modelsApi.admissionPolicy(),
    enabled: canReadPolicy,
  })

  const peoplePartial = !!membersQ.data?.has_more
  const administratorCount =
    membersQ.data?.items.filter(
      (member) =>
        member.status === 'active' &&
        (member.role === 'owner' || member.role === 'admin'),
    ).length ?? 0
  const tokens = tokensQ.data?.items.filter((token) => !token.revoked)
  const noExpiry = tokens?.filter((token) => !token.expires_at).length ?? 0
  const unused = tokens?.filter((token) => !token.last_used_at).length ?? 0
  const tokenPartial = !!tokensQ.data?.has_more
  const providerPartial = !!providersQ.data?.has_more
  const policy = policyQ.data
  const policyValue = policy
    ? policy.configured === false
      ? t('posture.policy.unconfigured')
      : policy.require_signed
        ? t('posture.policy.enforce')
        : t('posture.policy.observe')
    : undefined
  const agentStops =
    killSwitchQ.data?.active.filter((stop) => stop.scope_kind === 'agent')
      .length ?? 0
  const killValue = killSwitchQ.data
    ? killSwitchQ.data.estate_stopped
      ? t('posture.killSwitch.estateStopped')
      : agentStops > 0
        ? t('posture.killSwitch.agentStops', { count: agentStops })
        : t('posture.killSwitch.notEngaged')
    : undefined

  const rows: PostureRow[] = [
    {
      id: 'second-factor',
      label: t('posture.secondFactor.label'),
      description: t('posture.secondFactor.description'),
      href: '/identity?tab=people',
      state: 'unavailable',
    },
    {
      id: 'administrators',
      label: t('posture.administrators.label'),
      description: t('posture.administrators.description'),
      href: '/identity?tab=people',
      state: readState(canReadMembers, membersQ),
      value: count(administratorCount, peoplePartial),
      partial: peoplePartial,
      error: membersQ.error,
      retry: () => void membersQ.refetch(),
    },
    {
      id: 'api-keys',
      label: t('posture.apiKeys.label'),
      description: t('posture.apiKeys.description'),
      href: '/identity?tab=apiKeys',
      state: readState(canReadTokens, tokensQ),
      value: t('posture.apiKeys.value', {
        noExpiry: count(noExpiry, tokenPartial),
        unused: count(unused, tokenPartial),
      }),
      partial: tokenPartial,
      error: tokensQ.error,
      retry: () => void tokensQ.refetch(),
    },
    {
      id: 'approvals',
      label: t('posture.approvals.label'),
      description: t('posture.approvals.description'),
      href: '/permissions?tab=approvals',
      state: readState(canReadApprovals, approvalsQ),
      value: pendingCount(approvalsQ.data) ?? '0',
      partial: !!approvalsQ.data?.has_more,
      error: approvalsQ.error,
      retry: () => void approvalsQ.refetch(),
    },
    {
      id: 'kill-switch',
      label: t('posture.killSwitch.label'),
      description: t('posture.killSwitch.description'),
      href: '/killswitch',
      state: readState(canReadKillSwitch, killSwitchQ),
      value: killValue,
      error: killSwitchQ.error,
      retry: () => void killSwitchQ.refetch(),
    },
    {
      id: 'providers',
      label: t('posture.providers.label'),
      description: t('posture.providers.description'),
      href: '/providers',
      state: readState(canReadProviders, providersQ),
      value: count(
        providersQ.data?.items.filter(
          (provider) =>
            provider.state === 'active' && provider.kind !== 'ollama',
        ).length ?? 0,
        providerPartial,
      ),
      partial: providerPartial,
      error: providersQ.error,
      retry: () => void providersQ.refetch(),
    },
    {
      id: 'policy',
      label: t('posture.policy.label'),
      description: t('posture.policy.description'),
      href: '/model-operations?tab=admission',
      state: readState(canReadPolicy, policyQ),
      value: policyValue,
      error: policyQ.error,
      retry: () => void policyQ.refetch(),
    },
  ]

  return (
    <SectionCard
      title={t('posture.title')}
      description={t('posture.description')}
      noPadding
    >
      <ul className="divide-y divide-border px-4">
        {rows.map((row) => (
          <PostureRowView key={row.id} row={row} />
        ))}
      </ul>
      {rows.some((row) => row.state === 'ready' && row.partial) ? (
        <p className="border-t border-border px-4 py-3 text-caption text-muted-foreground">
          {t('posture.partialHint')}
        </p>
      ) : null}
    </SectionCard>
  )
}
