// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import { useInfiniteQuery } from '@tanstack/react-query'
import { useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { Field } from '@/components/ui/field'
import { KvList, KvRow } from '@/components/ui/kv'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import {
  providersApi,
  providerKeys,
  PROVIDER_PAGE,
} from '@/features/providers/api'
import { useProviderBoundary } from '@/features/providers/auth-boundary'
import { KIND_DRIVERS } from '@/features/providers/kinds'
import { agentOpsApi, agentOpsKeys } from './api'
import { AuthorityLostError } from './auth-boundary'
import type { ProviderProfileDTO } from './types'

export interface ProfileAuthenticationValue {
  source: string
  provider: string
}
const selectClass =
  'h-9 w-full min-w-0 rounded-md border border-border bg-background px-3 text-body focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring'

/** Mirrors the closed engine mapping, including compatible records for extensible drivers.
 * The server revalidates record state, tenant and compatibility at write and launch. */
export function useProfileProviders(driver: string, enabled: boolean) {
  const { activeTenant, can } = useAuth()
  const boundary = useProviderBoundary()
  const allowed = can('sessions:provider:read')
  const query = useInfiniteQuery({
    queryKey: providerKeys.list(activeTenant, boundary.epoch, {
      state: 'active',
      limit: PROVIDER_PAGE,
    }),
    queryFn: ({ pageParam, signal }) =>
      providersApi.list(
        {
          state: 'active',
          limit: PROVIDER_PAGE,
          ...(pageParam ? { cursor: pageParam } : {}),
        },
        { signal },
      ),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => (last.has_more ? last.cursor : undefined),
    enabled: enabled && allowed,
  })
  const records =
    allowed && !query.isError && !query.isPlaceholderData
      ? (query.data?.pages.flatMap((page) => page.items) ?? []).filter(
          (r) =>
            r.state === 'active' &&
            (r.kind === 'openai_compatible' ||
              KIND_DRIVERS[r.kind]?.includes(driver.trim().toLowerCase())),
        )
      : []
  return { query, records, allowed }
}

export function ProfileAuthenticationFields({
  value,
  onChange,
  providers,
  disabled = false,
}: {
  value: ProfileAuthenticationValue
  onChange: (value: ProfileAuthenticationValue) => void
  providers: ReturnType<typeof useProfileProviders>
  disabled?: boolean
}) {
  const { t } = useTranslation('agentops')
  return (
    <>
      <Field
        label={t('profiles.authentication.source')}
        description={t('profiles.authentication.hint')}
      >
        <select
          className={selectClass}
          value={value.source}
          disabled={disabled}
          onChange={(e) => onChange({ source: e.target.value, provider: '' })}
        >
          <option value="">{t('profiles.authentication.none')}</option>
          <option value="provider_account_home">
            {t('profiles.authentication.accountHome')}
          </option>
          <option value="managed_injection">
            {t('profiles.authentication.managed')}
          </option>
        </select>
      </Field>
      {value.source === 'managed_injection' && (
        <>
          <Field
            label={t('profiles.authentication.provider')}
            description={t('profiles.authentication.providerHint')}
          >
            <select
              className={selectClass}
              disabled={
                disabled || !providers.allowed || providers.query.isError
              }
              value={
                providers.records.some((r) => r.provider_ref === value.provider)
                  ? value.provider
                  : ''
              }
              onChange={(e) => onChange({ ...value, provider: e.target.value })}
            >
              <option value="">
                {t('profiles.authentication.chooseProvider')}
              </option>
              {providers.records.map((r) => (
                <option key={r.provider_ref} value={r.provider_ref}>
                  {r.display_name || r.provider_ref} · {r.kind}
                </option>
              ))}
            </select>
          </Field>
          {!providers.allowed && (
            <p role="status" className="text-caption text-warning">
              {t('profiles.authentication.readRequired')}
            </p>
          )}
          {providers.allowed && providers.query.isError && (
            <div role="alert" className="text-caption text-warning">
              {t('profiles.authentication.loadFailed')}{' '}
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => void providers.query.refetch()}
              >
                {t('profiles.authentication.retry')}
              </Button>
            </div>
          )}
          {providers.query.isLoading && (
            <p role="status">{t('profiles.authentication.loading')}</p>
          )}
          {providers.query.hasNextPage && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              disabled={providers.query.isFetchingNextPage}
              onClick={() => void providers.query.fetchNextPage()}
            >
              {t('profiles.authentication.loadMore')}
            </Button>
          )}
        </>
      )}
    </>
  )
}

export function ProfileAuthentication({
  profile,
  readReady,
}: {
  profile: ProviderProfileDTO
  readReady: boolean
}) {
  const { t } = useTranslation('agentops')
  const { can, activeTenant } = useAuth()
  const boundary = useProviderBoundary()
  const allowed =
    readReady && profile.state !== 'retired' && can('sessions:profile:write')
  const [open, setOpen] = useState(false)
  const editTrigger = useRef<HTMLButtonElement>(null)
  const returnFocus = (event: Event) => {
    if (editTrigger.current?.isConnected) {
      event.preventDefault()
      editTrigger.current.focus()
    }
  }
  const revision = `${boundary.key}:${profile.profile_ref}:${profile.updated_at}:${allowed}`
  const [seen, setSeen] = useState(revision)
  if (seen !== revision) {
    setSeen(revision)
    setOpen(false)
  }
  return (
    <section className="flex flex-col gap-3 border-t border-border pt-4">
      <h3 className="text-body font-medium">
        {t('profiles.authentication.title')}
      </h3>
      {readReady && (
        <KvList>
          <KvRow label={t('profiles.authentication.source')}>
            {t(
              profile.auth_source === 'managed_injection'
                ? 'profiles.authentication.managed'
                : profile.auth_source === 'provider_account_home'
                  ? 'profiles.authentication.accountHome'
                  : 'profiles.authentication.none',
            )}
          </KvRow>
          {profile.provider_record_ref && (
            <KvRow label={t('profiles.authentication.provider')} mono>
              {profile.provider_record_ref}
            </KvRow>
          )}
        </KvList>
      )}
      {allowed && (
        <Button
          ref={editTrigger}
          variant="secondary"
          size="sm"
          onClick={() => setOpen(true)}
        >
          {t('profiles.authentication.edit')}
        </Button>
      )}
      {open && allowed && (
        <AuthenticationEditor
          key={revision}
          profile={profile}
          allowed={allowed}
          onCloseAutoFocus={returnFocus}
          onClose={() => setOpen(false)}
          tenant={activeTenant}
          epoch={boundary.epoch}
        />
      )}
    </section>
  )
}

function AuthenticationEditor({
  profile,
  allowed,
  onClose,
  tenant,
  epoch,
  onCloseAutoFocus,
}: {
  profile: ProviderProfileDTO
  allowed: boolean
  onClose: () => void
  tenant: string | null
  epoch: number
  onCloseAutoFocus: (event: Event) => void
}) {
  const { t } = useTranslation('agentops')
  const [value, setValue] = useState({
    source: profile.auth_source ?? '',
    provider: profile.provider_record_ref ?? '',
  })
  const providers = useProfileProviders(
    profile.driver,
    value.source === 'managed_injection',
  )
  const valid =
    value.source !== 'managed_injection' ||
    providers.records.some((r) => r.provider_ref === value.provider)
  const mutation = usePrivilegedMutation<
    ProfileAuthenticationValue,
    ProviderProfileDTO
  >({
    mutationKey: agentOpsKeys.profile(tenant, epoch, profile.profile_ref),
    mutationFn: (next) => {
      if (!allowed || !valid) throw new AuthorityLostError()
      return agentOpsApi.patchProfile(profile.profile_ref, {
        auth_source: next.source,
        provider_record_ref:
          next.source === 'managed_injection' ? next.provider : '',
      })
    },
    invalidateKeys: [
      agentOpsKeys.profiles(tenant, epoch),
      agentOpsKeys.profile(tenant, epoch, profile.profile_ref),
    ],
    successMessage: t('profiles.authentication.saved'),
    onDone: onClose,
  })
  return (
    <ConfirmDialog
      open
      onOpenChange={(next) => {
        if (!next) onClose()
      }}
      onCloseAutoFocus={onCloseAutoFocus}
      title={t('profiles.authentication.edit')}
      description={t('profiles.authentication.launchHint')}
      confirmLabel={t('profiles.authentication.save')}
      confirmDisabled={!valid}
      pending={mutation.isPending}
      onConfirm={() => mutation.mutate(value)}
    >
      <div className="flex flex-col gap-3">
        <ProfileAuthenticationFields
          value={value}
          onChange={setValue}
          providers={providers}
          disabled={mutation.isPending}
        />
      </div>
    </ConfirmDialog>
  )
}
