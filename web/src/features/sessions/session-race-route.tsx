// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { useTranslation } from 'react-i18next'
import { useUrlState } from '@/lib/hooks/use-url-state'
import { SessionRace } from './session-race'
import { SessionsWorkspaceView } from './sessions-workspace-view'
import './i18n'

const RACE_KEYS = ['race'] as const
const RUN_REF =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

export function RaceAwareSessionsView({
  entrance,
}: {
  entrance: 'observe' | 'operate'
}) {
  const [search] = useUrlState(RACE_KEYS)
  const { t } = useTranslation('sessions')
  if (!search.race) return <SessionsWorkspaceView entrance={entrance} />
  const refs = search.race.split(',')
  if (
    refs.length < 2 ||
    new Set(refs).size !== refs.length ||
    refs.some((ref) => !RUN_REF.test(ref))
  )
    return <p role="alert">{t('race.invalid')}</p>
  return <SessionRace refs={refs} />
}
