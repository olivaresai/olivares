// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import { agentOpsApi, agentOpsKeys } from './api'
import { useAuthBoundary } from './auth-boundary'
import './i18n'

/** PersonalFoldersSwitch — the organization's choice to give each person "My files".
 * On by default; turning it off makes no new personal folder and removes none. */
export function PersonalFoldersSwitch() {
  const { t } = useTranslation('agentops')
  const { activeTenant } = useAuth()
  const boundary = useAuthBoundary()
  const qc = useQueryClient()
  const key = [
    ...agentOpsKeys.boundaryScope(activeTenant, boundary.epoch),
    'personal-folders-settings',
  ]
  const query = useQuery({
    queryKey: key,
    queryFn: ({ signal }) => agentOpsApi.personalFoldersSettings({ signal }),
    retry: false,
  })
  const save = usePrivilegedMutation<boolean, { enabled: boolean }>({
    mutationFn: agentOpsApi.setPersonalFoldersSettings,
    mutationKey: key,
    invalidateKeys: () => [agentOpsKeys.workspaces(activeTenant)],
    successMessage: t('workspaces.personalFoldersSaved'),
    onDone: (data) => qc.setQueryData(key, data),
  })
  if (query.isError)
    return (
      <QueryErrorState error={query.error} retry={() => void query.refetch()} />
    )
  if (!query.data) return null
  return (
    <label className="flex min-h-7 items-center gap-2 text-body">
      <input
        type="checkbox"
        checked={query.data.enabled}
        disabled={save.isPending}
        onChange={(event) => save.mutate(event.target.checked)}
      />
      {t('workspaces.personalFolders')}
    </label>
  )
}
