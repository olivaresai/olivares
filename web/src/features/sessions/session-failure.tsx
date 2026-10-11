// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import type { RunDTO } from '@/features/agentops/types'
import { hostIsolationUnavailable } from './provenance'
import './i18n'

/** The persisted engine reason, shared by the session pane and its full controls. */
export function SessionFailure({ run }: { run?: RunDTO }) {
  const { t } = useTranslation('sessions')
  if (run?.state !== 'failed') return null
  return (
    <div
      role="alert"
      data-testid="session-failure"
      className="flex flex-col gap-2 text-body [overflow-wrap:anywhere]"
    >
      <p className="text-bad whitespace-pre-wrap">
        {run.reason?.trim() || t('card.failureReasonMissing')}
      </p>
      {hostIsolationUnavailable(run) && <p>{t('card.hostIsolationRemedy')}</p>}
    </div>
  )
}
