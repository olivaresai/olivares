// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { useInfiniteQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Field } from '@/components/ui/field'
import { useAuth } from '@/lib/auth/context'
import { identityApi } from '@/features/identity/api'
import { useAuthBoundary } from './auth-boundary'

export const AGENT_SELECTION = '__agent__'
const selectClass =
  'h-9 w-full min-w-0 rounded-md border border-border bg-background px-3 text-body focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring'
export function useLaunchIdentities(enabled: boolean) {
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const allowed = can('governance:nhi:read')
  const query = useInfiniteQuery({
    queryKey: [...agentIdentityKeys(activeTenant, boundary.epoch), 'list'],
    queryFn: ({ pageParam, signal }) =>
      identityApi.nhiLifecycle(
        { limit: 100, ...(pageParam ? { cursor: pageParam } : {}) },
        { signal },
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (page) => (page.has_more ? page.cursor : undefined),
    enabled: enabled && allowed,
  })
  const agents =
    allowed && !query.isError && !query.isPlaceholderData
      ? (query.data?.pages.flatMap((p) => p.items) ?? []).filter(
          (a) =>
            a.kind === 'agent' &&
            !a.orphaned &&
            !a.registry_orphaned &&
            a.enforcement !== 'blocked' &&
            a.offboard_state === 'none' &&
            !!a.sponsor_ref,
        )
      : []
  return { query, agents, allowed }
}
export const agentIdentityKeys = (tenant: string | null, epoch: number) =>
  ['agentops', tenant, 'b', epoch, 'launch-identities'] as const
export function LaunchIdentityFields({
  value,
  onChange,
  identities,
  requiresAgent,
  pending,
}: {
  value: string
  onChange: (value: string) => void
  identities: ReturnType<typeof useLaunchIdentities>
  requiresAgent: boolean
  pending: boolean
}) {
  const { t } = useTranslation('agentops')
  const agentMode = value !== ''
  return (
    <>
      <Field
        label={t('create.identity.title')}
        description={t('create.identity.hint')}
      >
        <select
          className={selectClass}
          value={agentMode ? AGENT_SELECTION : ''}
          disabled={pending}
          onChange={(e) => onChange(e.target.value)}
        >
          <option value="">{t('create.identity.human')}</option>
          <option value={AGENT_SELECTION}>{t('create.identity.agent')}</option>
        </select>
      </Field>
      {requiresAgent && !agentMode && (
        <p role="status" className="text-caption text-warning">
          {t('create.identity.required')}
        </p>
      )}
      {agentMode && (
        <>
          <Field
            label={t('create.identity.choose')}
            description={t('create.identity.sponsorHint')}
          >
            <select
              className={selectClass}
              disabled={
                pending || !identities.allowed || identities.query.isError
              }
              value={
                identities.agents.some((a) => a.identity_ref === value)
                  ? value
                  : AGENT_SELECTION
              }
              onChange={(e) => onChange(e.target.value)}
            >
              <option value={AGENT_SELECTION}>
                {t('create.identity.placeholder')}
              </option>
              {identities.agents.map((a) => (
                <option key={a.identity_ref} value={a.identity_ref}>
                  {a.identity_ref}
                </option>
              ))}
            </select>
          </Field>
          {!identities.allowed && (
            <p role="status" className="text-caption text-warning">
              {t('create.identity.readRequired')}
            </p>
          )}
          {identities.query.isLoading && (
            <p role="status">{t('create.identity.loading')}</p>
          )}
          {identities.query.isError && (
            <div role="alert" className="text-caption text-warning">
              {t('create.identity.loadFailed')}{' '}
              <Button
                type="button"
                size="sm"
                variant="ghost"
                onClick={() => void identities.query.refetch()}
              >
                {t('profiles.authentication.retry')}
              </Button>
            </div>
          )}
          {identities.query.hasNextPage && (
            <Button
              type="button"
              size="sm"
              variant="ghost"
              disabled={identities.query.isFetchingNextPage}
              onClick={() => void identities.query.fetchNextPage()}
            >
              {t('create.identity.loadMore')}
            </Button>
          )}
        </>
      )}
    </>
  )
}
