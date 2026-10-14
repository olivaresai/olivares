// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Which profile a new session of a tool runs under. The
// default login is the first profile and is picked by the engine's own rule (no profile
// named); a named profile is sent by its reference, even when it is the only choice.
import { useQueries, useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  needsDefaultRow,
  useProfileList,
} from '@/features/agent-tools/use-profiles'
import '@/features/agent-tools/i18n'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import type { RunDTO } from '@/features/agentops/types'
import { useAuth } from '@/lib/auth/context'
import { signInApi } from './api'
import type { SignInTool } from './api'

/**
 * The profile the engine's own rule would reuse for a new session of `tool` (read only,
 * nothing is created). The New session form and the advanced launch both read it, so
 * the default sign-in is the same in both; one read, one cache entry.
 */
export function useReuseProfile(tool: string | undefined, enabled: boolean) {
  const boundary = useAuthBoundary()
  const eligible = enabled && !!boundary.tenant && !!tool
  const query = useQuery({
    queryKey: [
      ...agentOpsKeys.boundaryScope(boundary.tenant, boundary.epoch),
      'reuse-profile',
      tool,
    ],
    queryFn: ({ signal }) =>
      agentOpsApi.previewProfile(tool!, { tenant: boundary.tenant, signal }),
    enabled: eligible,
    retry: false,
  })
  // Disabled queries still expose cached data. An ineligible tool must not take a
  // former sign-in as its default, even while that profile remains in the cache.
  return { ...query, data: eligible ? query.data : undefined }
}

export interface SessionProfileChoice {
  /** '' for the default login, else the profile's reference. */
  value: string
  label: string
  ready: boolean
  /** Why it cannot start: its login is missing, or the key it runs on is. */
  missing?: 'signIn' | 'key'
}

export interface SessionProfiles {
  choices: SessionProfileChoice[]
  /** The chosen profile's value. */
  selected: string
  select: (value: string) => void
}

/**
 * The profile choices of `driver`, or null when only the default login is available. `defaultReady`
 * is the engine's answer for the default login (its readiness for the tool). `runs` are the
 * newest sessions first: the profile used most recently that is ready is the default pick,
 * else the first ready one.
 */
export function useSessionProfiles(
  driver: string | undefined,
  runs: readonly Pick<RunDTO, 'provider_profile_ref' | 'provider_driver'>[],
  defaultReady: boolean,
): SessionProfiles | null {
  const { activeTenant, isSuperadmin } = useAuth()
  const boundary = useAuthBoundary()
  const list = useProfileList()
  const { t } = useTranslation('agentTools')
  const accounts = (list.data?.accounts ?? []).filter(
    (a) => a.driver === driver,
  )
  // Only a system administrator may ask a tool for its sign-in; for anyone else an active
  // profile is offered and the engine refuses one that cannot start, in words.
  const statuses = useQueries({
    queries: accounts.map((a) => ({
      queryKey: [
        ...agentOpsKeys.boundaryScope(activeTenant, boundary.epoch),
        'account-sign-in',
        a.account_ref,
      ],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        signInApi.status(
          a.driver as SignInTool,
          activeTenant,
          signal,
          a.account_ref,
        ),
      enabled:
        isSuperadmin && !!activeTenant && a.auth_source !== 'managed_injection',
      retry: false,
    })),
  })
  const scope = `${driver}:${activeTenant}:${boundary.epoch}`
  const [pick, setPick] = useState<{ scope: string; value: string } | null>(
    null,
  )
  // The default login is the profile without an account name; an engine without names
  // adopted it as a named profile, which is then the one choice, by its reference.
  const hasDefault = needsDefaultRow(accounts, list.data?.named ?? false)
  const choices: SessionProfileChoice[] = [
    ...(hasDefault
      ? [
          {
            value: '',
            label: t('profiles.default'),
            ready: defaultReady,
            ...(defaultReady ? {} : { missing: 'signIn' as const }),
          },
        ]
      : []),
    ...accounts.map((a, i): SessionProfileChoice => {
      const status = statuses[i]
      const ready =
        a.auth_source === 'managed_injection'
          ? !!a.provider_record_ref
          : isSuperadmin
            ? status?.data?.installed === true && status.data.signed_in
            : true
      return {
        value: a.account_ref,
        label: a.name,
        ready,
        ...(ready
          ? {}
          : {
              missing: a.auth_source === 'managed_injection' ? 'key' : 'signIn',
            }),
      } as SessionProfileChoice
    }),
  ]
  if (!driver || (choices.length === 1 && choices[0].value === '')) return null
  const readyValues = new Set(
    choices.filter((c) => c.ready).map((c) => c.value),
  )
  const knownRefs = new Set(accounts.map((a) => a.account_ref))
  const inactive = list.data?.inactive ?? new Set<string>()
  const recent = runs
    .filter(
      (r) =>
        r.provider_driver === driver &&
        // A session on a profile that was removed or switched off says nothing about
        // which profile to start on now.
        !(r.provider_profile_ref && inactive.has(r.provider_profile_ref)),
    )
    .map((r) =>
      r.provider_profile_ref && knownRefs.has(r.provider_profile_ref)
        ? r.provider_profile_ref
        : hasDefault
          ? ''
          : undefined,
    )
    .find((value) => value !== undefined && readyValues.has(value))
  const fallback = choices.find((c) => c.ready)?.value ?? choices[0].value
  const selected =
    pick?.scope === scope && choices.some((c) => c.value === pick.value)
      ? pick.value
      : (recent ?? fallback)
  return {
    choices,
    selected,
    select: (value) => setPick({ scope, value }),
  }
}
