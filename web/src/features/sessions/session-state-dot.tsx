// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A SESSION'S STATE AS ONE DOT. The list row and the thread header say the state with a
// 6 px dot and carry the state's WORD as the dot's name, so colour never says it alone.
// The tone is `groupOf`'s answer (attention, active, settled) and nothing else, plus one
// refinement the group cannot make: a failed run is red where "needs you" is amber. The
// words are the existing state words (the run's own, else the observed one), so a state
// has one name wherever it is shown.
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { primaryRun, type UnifiedSession } from './provenance'
import { groupOf } from './session-groups'
import './i18n'
import '@/features/agentops/i18n'

export type StateTone = 'needs' | 'working' | 'idle' | 'failed'

function stateTone(session: UnifiedSession): StateTone {
  if (primaryRun(session.runs)?.state === 'failed') return 'failed'
  const group = groupOf(session)
  return group === 'attention'
    ? 'needs'
    : group === 'active'
      ? 'working'
      : 'idle'
}

const TONE_CLASS: Record<StateTone, string> = {
  needs: 'bg-warn',
  working: 'bg-ok animate-pulse-live',
  idle: 'bg-text-3',
  failed: 'bg-bad',
}

export function StateDot({
  session,
  className,
}: {
  session: UnifiedSession
  className?: string
}) {
  const { t } = useTranslation(['sessions', 'agentops'])
  const run = primaryRun(session.runs)
  const word = run
    ? t(`agentops:state.${run.state}`, { defaultValue: String(run.state) })
    : session.live
      ? t(`sessions:state.${session.live.cc_state}`, {
          defaultValue: String(session.live.cc_state),
        })
      : ''
  return (
    <span
      role="img"
      aria-label={word}
      title={word}
      data-tone={stateTone(session)}
      className={cn(
        'inline-block size-1.5 shrink-0 rounded-full',
        TONE_CLASS[stateTone(session)],
        className,
      )}
    />
  )
}
