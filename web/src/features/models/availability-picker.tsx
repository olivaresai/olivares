// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery } from '@tanstack/react-query'
import { useId } from 'react'
import { useTranslation } from 'react-i18next'
import { Input } from '@/components/ui/input'
import { agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import type { ProviderProfileDTO } from '@/features/agentops/types'
import { useAuth } from '@/lib/auth/context'
import { useServerInfo } from '@/lib/hooks/use-server-info'
import { useModuleOn } from '@/stores/modules'
import { modelsApi } from './api'
import type { ModelAvailabilityFilter } from './types'
import './i18n'

export interface ModelAvailabilityPickerProps {
  profile?: Pick<
    ProviderProfileDTO,
    'profile_ref' | 'driver' | 'auth_source' | 'provider_record_ref'
  >
  value: string
  onChange: (value: string) => void
  enabled: boolean
}

export function ModelAvailabilityPicker({
  profile,
  value,
  onChange,
  enabled,
}: ModelAvailabilityPickerProps) {
  const { t } = useTranslation('models')
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const info = useServerInfo()
  const moduleOn = useModuleOn('models')
  const id = useId()
  const filter: ModelAvailabilityFilter | undefined =
    profile?.auth_source === 'provider_account_home'
      ? { account_ref: profile.profile_ref }
      : profile?.provider_record_ref
        ? { provider_ref: profile.provider_record_ref }
        : undefined
  const allowed =
    enabled &&
    !!activeTenant &&
    !!filter &&
    can('models:catalog:read') &&
    moduleOn &&
    info.isSuccess &&
    !info.data?.modules_not_enabled?.some(
      (name) => name.trim().toLowerCase() === 'models',
    )
  const availability = useQuery({
    queryKey: [
      ...agentOpsKeys.boundaryScope(activeTenant, boundary.epoch),
      'model-availability',
      filter,
    ],
    queryFn: ({ signal }) =>
      modelsApi.availability({ tenant: activeTenant, signal, query: filter! }),
    enabled: allowed,
    staleTime: 60_000,
    refetchInterval: allowed
      ? (availabilityInterval) =>
          (availabilityInterval.state.data?.refresh_interval_seconds ?? 3600) *
          1000
      : false,
    retry: false,
  })
  // An account/provider observation belongs only to the selection that requested it.
  // Cached data is also hidden immediately if that permission or module leaves.
  const source = allowed
    ? availability.data?.items.find((item) =>
        filter?.account_ref
          ? item.account_ref === filter.account_ref
          : item.provider_ref === filter?.provider_ref,
      )
    : undefined
  const choices =
    !availability.isError && source?.state === 'fresh' ? source.models : []
  const status =
    availability.isError || source?.state === 'failed'
      ? 'failed'
      : source?.state === 'unsupported'
        ? 'unsupported'
        : source?.state === 'stale' && source.seen_at
          ? 'stale'
          : source?.state === 'fresh'
            ? choices.length
              ? undefined
              : 'empty'
            : 'discovering'
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <Input
        aria-label={t('availability.label')}
        aria-describedby={allowed ? `${id}-status` : undefined}
        list={`${id}-models`}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder={t('availability.placeholder')}
        mono
      />
      <datalist id={`${id}-models`}>
        {choices.map((model) => (
          <option key={model.id} value={model.id} />
        ))}
      </datalist>
      <button
        type="button"
        className="self-start text-caption text-accent-text underline underline-offset-2"
        onClick={() => onChange('')}
      >
        {t('availability.default')}
      </button>
      {allowed ? (
        <div
          id={`${id}-status`}
          className="flex min-w-0 flex-col gap-1 text-caption text-text-2"
          role="status"
        >
          {status ? <p>{t(`availability.${status}`)}</p> : null}
          {source ? (
            <p>
              {t('availability.provider', { provider: source.provider_kind })}
            </p>
          ) : null}
          {source?.seen_at ? (
            <p className="break-words">
              {t('availability.observed', { time: source.seen_at })}
            </p>
          ) : null}
          {source?.checked_at ? (
            <p className="break-words">
              {t('availability.checked', { time: source.checked_at })}
            </p>
          ) : null}
          {status === 'failed' ? (
            <a
              className="self-start text-accent-text underline underline-offset-2"
              href={filter?.provider_ref ? '/providers' : '/provider-profiles'}
            >
              {t('availability.connection')}
            </a>
          ) : null}
        </div>
      ) : null}
    </div>
  )
}
