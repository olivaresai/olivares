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
import { StatusDot, type StatusTone } from '@/components/ui/status-chip'
import { runStateWord } from '@/features/agentops/run-state-word'
import { cn } from '@/lib/utils'
import { primaryRun, type UnifiedSession } from './provenance'
import { sessionDisplayState, type StateTone } from './session-display-state'
import './i18n'
import '@/features/agentops/i18n'

export type { StateTone } from './session-display-state'

const TONE: Record<StateTone, StatusTone> = {
  needs: 'warn',
  working: 'ok',
  idle: 'neutral',
  failed: 'bad',
}

export function StateDot({
  session,
  className,
}: {
  session: UnifiedSession
  className?: string
}) {
  const { t } = useTranslation(['sessions', 'agentops'])
  const display = sessionDisplayState(session)
  const run = primaryRun(session.runs)
  const word = display.runState
    ? t(`agentops:state.${run ? runStateWord(run) : display.runState}`, {
        defaultValue: String(display.runState),
      })
    : display.observedState
      ? t(`sessions:state.${display.observedState}`, {
          defaultValue: String(display.observedState),
        })
      : ''
  return (
    <span
      role="img"
      aria-label={word}
      title={word}
      data-tone={display.tone}
      className={cn('inline-flex size-1.5 shrink-0 items-center', className)}
    >
      <StatusDot tone={TONE[display.tone]} />
    </span>
  )
}
