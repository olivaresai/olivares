// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { agentOpsApi, agentOpsKeys } from '@/features/agentops/api'
import { useAuthBoundary } from '@/features/agentops/auth-boundary'
import { useAuth } from '@/lib/auth/context'
import { usePrivilegedMutation } from '@/lib/hooks/use-privileged-mutation'
import './i18n'

export function LimitResumePreference({
  compact = false,
}: {
  compact?: boolean
}) {
  const { t } = useTranslation('sessions')
  const { activeTenant, can } = useAuth()
  const boundary = useAuthBoundary()
  const qc = useQueryClient()
  const key = [
    ...agentOpsKeys.boundaryScope(activeTenant, boundary.epoch),
    'limit-resume-settings',
  ]
  const allowed = can('sessions:run:write')
  const query = useQuery({
    queryKey: key,
    queryFn: ({ signal }) => agentOpsApi.limitResumeSettings({ signal }),
    enabled: allowed,
    retry: false,
  })
  const save = usePrivilegedMutation<boolean, { automatic: boolean }>({
    mutationFn: agentOpsApi.setLimitResumeSettings,
    mutationKey: key,
    successMessage: t('limit.settingsSaved'),
    onDone: (data) => qc.setQueryData(key, data),
  })
  if (!allowed) return null
  if (query.isError)
    return (
      <QueryErrorState error={query.error} retry={() => void query.refetch()} />
    )
  if (!query.data) return null
  return (
    <label
      className={`flex min-h-7 items-center gap-2 ${compact ? 'text-caption' : 'text-body'}`}
    >
      <input
        type="checkbox"
        checked={query.data.automatic}
        disabled={save.isPending}
        onChange={(event) => save.mutate(event.target.checked)}
      />
      {t('limit.automatic')}
    </label>
  )
}
