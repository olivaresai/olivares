// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
import type { ComponentType } from 'react'
import { useTranslation } from 'react-i18next'
import { Field } from '@/components/ui/field'
import { LAUNCH_IDENTITY_EXTENSION } from '@/features/extensions'
import type { NhiLifecycleDTO } from '@/features/identity/types'
import './i18n'

export interface LaunchIdentityState {
  readonly agents: readonly NhiLifecycleDTO[]
  readonly allowed: boolean
  readonly query: {
    readonly isError: boolean
    readonly isLoading: boolean
    readonly hasNextPage: boolean
    readonly isFetchingNextPage: boolean
    readonly refetch: () => Promise<unknown>
    readonly fetchNextPage: () => Promise<unknown>
  }
}
export interface LaunchIdentityProps {
  value: string
  onChange: (value: string) => void
  identities: LaunchIdentityState
  pending: boolean
}
export interface LaunchIdentityExtension {
  readonly useIdentities: (enabled: boolean) => LaunchIdentityState
  readonly Preflight?: ComponentType
  readonly Component: ComponentType<LaunchIdentityProps>
}

const LOCAL_IDENTITY: LaunchIdentityState = Object.freeze({
  agents: Object.freeze([]),
  allowed: false,
  query: Object.freeze({
    isError: false,
    isLoading: false,
    hasNextPage: false,
    isFetchingNextPage: false,
    refetch: async () => undefined,
    fetchNextPage: async () => undefined,
  }),
})
function useLocalIdentity(_enabled: boolean): LaunchIdentityState {
  return LOCAL_IDENTITY
}

export function useLaunchIdentities(enabled: boolean): LaunchIdentityState {
  const useIdentities =
    LAUNCH_IDENTITY_EXTENSION?.useIdentities ?? useLocalIdentity
  return useIdentities(enabled)
}

function LocalIdentityFields({ pending }: LaunchIdentityProps) {
  const { t } = useTranslation('agentops')
  return (
    <Field
      label={t('create.identity.title')}
      description={t('create.identity.humanHint')}
    >
      <select
        className="h-9 w-full min-w-0 rounded-md border border-ctl-border bg-background px-3 text-body focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        value=""
        disabled={pending}
        onChange={() => {}}
      >
        <option value="">{t('create.identity.human')}</option>
      </select>
    </Field>
  )
}

export function LaunchIdentityFields({
  value,
  onChange,
  identities,
  pending,
}: LaunchIdentityProps) {
  const Component = LAUNCH_IDENTITY_EXTENSION?.Component ?? LocalIdentityFields
  return (
    <Component
      value={value}
      onChange={onChange}
      identities={identities}
      pending={pending}
    />
  )
}

/** Edition preflight controls are visible before Start, outside Advanced options. */
export function LaunchPreflight() {
  const Component = LAUNCH_IDENTITY_EXTENSION?.Preflight
  return Component ? <Component /> : null
}
