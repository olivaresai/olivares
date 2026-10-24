// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// What an apply says while it runs and after it fails: the engine is restarting, it has not
// come back, or the update was refused in the words `olivares upgrade` uses. Shared by
// Settings > Updates and the licensed-edition switch, which apply the same update.
import { useTranslation } from 'react-i18next'
import { Spinner } from '@/components/ui/spinner'
import type { UpdatePhase } from './use-engine-update'

export function UpdateProgress({
  phase,
  refusal,
}: {
  phase: UpdatePhase
  refusal: string | null
}) {
  const { t } = useTranslation('settings')
  return (
    <>
      {phase === 'waiting' ? (
        <p
          role="status"
          className="flex items-center gap-2 text-caption text-text-2"
          data-slot="engine-restarting"
        >
          <Spinner size="sm" aria-hidden />
          {t('updates.restarting')}
        </p>
      ) : null}
      {phase === 'stuck' ? (
        <p role="status" className="text-caption text-warning">
          {t('updates.stillRestarting')}
        </p>
      ) : null}
      {refusal ? (
        <p role="alert" className="text-caption text-danger">
          {refusal}
        </p>
      ) : null}
    </>
  )
}
