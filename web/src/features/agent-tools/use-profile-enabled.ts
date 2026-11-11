// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The on/off switch of a profile on the AI tools page: the engine's own reversible
// lifecycle (PATCH state active or disabled). Off refuses new sessions on the profile and
// keeps its home, sign-in and name; a running session keeps the authority it started with.
import { useTranslation } from 'react-i18next'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import type { ProviderAccountDTO } from '@/features/agentops/types'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { accountLabel } from './account-marks'
import './i18n'

export interface ProfileEnabled {
  on: boolean
  pending: boolean
  /** Undefined when this role cannot change a profile: the row then shows no switch. */
  set?: (on: boolean) => void
}

export function useProfileEnabled(account: ProviderAccountDTO): ProfileEnabled {
  const { t } = useTranslation('agentTools')
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const patch = usePrivilegedMutation<boolean, unknown>({
    mutationFn: (on, authority) =>
      agentOpsApi.patchProfile(
        account.account_ref,
        { state: on ? 'active' : 'disabled' },
        authority,
      ),
    successMessage: (_done, on) =>
      t(on ? 'profiles.switchedOn' : 'profiles.switchedOff', {
        name: accountLabel(account),
      }),
    invalidateKeys: [
      agentOpsKeys.accounts(activeTenant, boundary.epoch),
      agentOpsKeys.profiles(activeTenant, boundary.epoch),
    ],
  })
  return {
    on: account.state === 'active',
    pending: patch.isPending,
    set: can('sessions:profile:write') ? (on) => patch.mutate(on) : undefined,
  }
}
