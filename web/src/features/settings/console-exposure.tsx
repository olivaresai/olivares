// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useState, type ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { http, type RequestOptions } from '@/lib/api'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { BoxedList } from '@/components/ui/boxed-list'
import { Button } from '@/components/ui/button'
import { DisabledReason } from '@/components/ui/disabled-reason'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import '@/features/identity/i18n'
import { SettingsRow } from './settings-row'

export interface ExposureStatus {
  listen: string
  grpc_listen: string
  public_url: string
  saved_public_url: string
  tls: boolean
  admin_step_up: string
  external_reachability: string
  addresses: string[]
  managed_public: boolean
}
export const exposureApi = {
  get: (signal?: AbortSignal) =>
    http.get<ExposureStatus>('/v1/console/exposure', { signal }),
  save: (
    input: { public_url: string },
    authority: Pick<RequestOptions, 'signal' | 'dispatchGuard'>,
  ) =>
    http.put<{ public_url: string; restart_required: boolean }>(
      '/v1/console/exposure',
      input,
      authority,
    ),
}

export function ConsoleExposure() {
  const { t } = useTranslation(['settings', 'identity', 'common'])
  const { activeTenant, principal, can } = useAuth()
  const allowed = can('system:admin')
  const key = [
    'settings',
    'console-exposure',
    activeTenant,
    principal?.actor,
    principal?.admin_step_up,
  ] as const
  const client = useQueryClient()
  const query = useQuery({
    queryKey: key,
    queryFn: ({ signal }) => exposureApi.get(signal),
    enabled: allowed,
  })
  const [draft, setDraft] = useState<string | null>(null)
  const save = usePrivilegedMutation({
    mutationFn: exposureApi.save,
    successMessage: t('settings:exposure.saved'),
    onDone: () => {
      void client.invalidateQueries({ queryKey: key })
      setDraft(null)
    },
  })
  if (!allowed) return null
  if (query.isPending) return <Spinner />
  if (query.isError)
    return (
      <QueryErrorState error={query.error} retry={() => void query.refetch()} />
    )
  const state = query.data
  const fact = (text: ReactNode) => (
    <span className="font-mono text-caption break-all text-text-2">{text}</span>
  )
  return (
    <section
      className="flex max-w-xl flex-col gap-3"
      aria-labelledby="console-exposure-title"
    >
      <h2
        id="console-exposure-title"
        className="text-body-l font-semibold text-text"
      >
        {t('settings:exposure.title')}
      </h2>
      <BoxedList>
        <SettingsRow
          name={t('settings:exposure.listen')}
          description=""
          control={fact(state.listen || t('settings:exposure.notSet'))}
        />
        <SettingsRow
          name={t('settings:exposure.url')}
          description=""
          control={fact(state.public_url || t('settings:exposure.notSet'))}
        />
        <SettingsRow
          name={t('settings:exposure.grpc')}
          description=""
          control={fact(state.grpc_listen || t('settings:exposure.notSet'))}
        />
        <SettingsRow
          name={t('settings:exposure.tls')}
          description=""
          control={
            <span className="text-body text-text-2">
              {t(
                state.tls
                  ? 'settings:exposure.encrypted'
                  : 'settings:exposure.unencrypted',
              )}
            </span>
          }
        />
        <SettingsRow
          name={t('settings:exposure.policy')}
          description=""
          control={
            <span className="text-body text-text-2">
              {t(`identity:stepUpPolicy.options.${state.admin_step_up}`)}
            </span>
          }
        />
        <SettingsRow
          name={t('settings:exposure.addresses')}
          description=""
          control={fact(
            state.addresses?.length
              ? state.addresses.map((address) => (
                  <span key={address} className="block">
                    {address}
                  </span>
                ))
              : t('settings:exposure.notSet'),
          )}
        />
      </BoxedList>
      <p className="text-body">{t('settings:exposure.unverified')}</p>
      <p className="text-caption text-text-2">{t('settings:exposure.hint')}</p>
      <p className="text-caption text-text-2">
        {t(
          state.managed_public
            ? 'settings:exposure.managed'
            : 'settings:exposure.unmanaged',
        )}
      </p>
      {state.admin_step_up === 'none' ? (
        <p className="text-body">{t('settings:exposure.guard')}</p>
      ) : null}
      <form
        className="flex flex-col gap-2"
        onSubmit={(event) => {
          event.preventDefault()
          save.mutate({ public_url: (draft ?? state.saved_public_url).trim() })
        }}
      >
        <BoxedList>
          <SettingsRow
            name={t('settings:exposure.url')}
            description={t('settings:exposure.urlHint')}
            descriptionId="console-public-url-hint"
            control={
              <Input
                id="console-public-url"
                type="url"
                className="w-64"
                aria-label={t('settings:exposure.url')}
                value={draft ?? state.saved_public_url}
                onChange={(event) => setDraft(event.target.value)}
                aria-describedby="console-public-url-hint"
              />
            }
          />
        </BoxedList>
        <DisabledReason
          mode="native"
          disabled={save.isPending || draft === null}
          reason={t(
            save.isPending
              ? 'common:privileged.working'
              : 'settings:exposure.unchanged',
          )}
        >
          <Button type="submit">{t('common:actions.save')}</Button>
        </DisabledReason>
      </form>
      {save.isSuccess ? (
        <p role="status">{t('settings:modules.state.needsRestart')}</p>
      ) : null}
      <p className="text-caption text-text-2">
        {t('settings:exposure.tunnelHint')}
      </p>
    </section>
  )
}
