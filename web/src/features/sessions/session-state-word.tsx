// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT A SESSION IS DOING, IN A WORD WITH ITS TIME (dev#3162: "Working 4s", "Done").
// The list's state renderer: it reads the one classifier (sessionDisplayState) and
// never the engine's raw state, as StateDot does for the dot.
import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { runStateWord } from '@/features/agentops/run-state-word'
import { humanDurationSeconds } from '@/features/shared'
import { formatDateTime } from '@/lib/format'
import { cn } from '@/lib/utils'
import { primaryRun, type UnifiedSession } from './provenance'
import { sessionDisplayState } from './session-display-state'
import { useNow } from './use-now'
import './i18n'

/** A run that started this recently is on its first turn: its start is the turn's. */
const JUST_STARTED_MS = 60_000

/** When this page saw each session start working, by its key; null once it saw it not
 * working. A session already working when the page first saw it has no entry. */
const turnStarts = new Map<string, number | null>()

/**
 * When the turn now running started, as far as this page knows: the run's own start when
 * that was moments ago, else the moment this page saw the session start working, else
 * unknown (null). The engine keeps no turn's start, and a launch hours old is not one: a
 * clock never runs from it. The moment is kept by key, not by row, since a row that starts
 * working moves to another group and mounts again; the row's own clock, which ticks at
 * once when it starts working, reads it.
 */
function useTurnStart(
  key: string,
  working: boolean,
  started: number,
  now: number,
): number | null {
  useEffect(() => {
    if (!working) turnStarts.set(key, null)
    else if (turnStarts.get(key) === null) turnStarts.set(key, Date.now())
  }, [key, working])
  if (!working) return null
  if (Number.isFinite(started) && now - started < JUST_STARTED_MS)
    return started
  return turnStarts.get(key) ?? null
}

/** Forgets every turn start when the list that saw them goes away: seen again later, a
 * session may be in another turn. The list calls it once. */
export function useForgetTurnsOnLeave() {
  useEffect(() => () => turnStarts.clear(), [])
}

/** How long ago, in its largest unit: "46s", "5m", "3h", "2d". */
function ago(ms: number, now: number): string {
  const s = Math.max(0, Math.floor((now - ms) / 1000))
  if (s < 60) return `${s}s`
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) return `${Math.floor(s / 3600)}h`
  return `${Math.floor(s / 86400)}d`
}

/**
 * Failed; Needs you; Pending; Working, with how long this turn has worked when the page
 * knows when it began; Done once a turn or a seen
 * session has ended; for a run that ended, the run's own word (Stopped, Ended). Any
 * session that is not working also says how long ago it last did something, the date on
 * the hover. The word is the state for a screen reader too, and its colour only repeats it.
 */
export function StateWord({ session }: { session: UnifiedSession }) {
  const { t, i18n } = useTranslation(['sessions', 'agentops'])
  const display = sessionDisplayState(session)
  const run = primaryRun(session.runs)
  const pending = display.runState === 'pending'
  const working =
    !pending && display.tone !== 'failed' && display.group === 'active'
  // A second for a working clock; an age in minutes needs no more than half a minute.
  const now = useNow(true, working ? 1000 : 30_000)
  const since = useTurnStart(
    session.key,
    working,
    Date.parse(run?.started_at ?? session.live?.first_event_at ?? ''),
    now,
  )
  const word =
    display.tone === 'failed'
      ? t('agentops:state.failed')
      : display.group === 'attention'
        ? t('sessions:list.group.attention')
        : pending
          ? t('agentops:state.pending')
          : working
            ? t('agentops:state.running')
            : run
              ? display.runState === 'idle'
                ? t('agentops:state.done')
                : t(`agentops:state.${runStateWord(run)}`, {
                    defaultValue: display.runState,
                  })
              : display.observedState === 'ended'
                ? t('agentops:state.done')
                : t(`agentops:state.${display.observedState ?? 'idle'}`, {
                    defaultValue: display.observedState,
                  })
  const worked =
    since === null
      ? null
      : humanDurationSeconds(Math.max(0, (now - since) / 1000))
  return (
    <>
      <span
        data-testid="rail-row-state"
        className={cn(
          'shrink-0 whitespace-nowrap text-overline font-normal tabular-nums',
          display.tone === 'failed'
            ? 'text-danger'
            : display.group === 'attention'
              ? 'text-warning'
              : working
                ? 'text-text'
                : 'text-text-3',
        )}
      >
        {worked ? `${word} ${worked}` : word}
      </span>
      {!working && session.lastActivityMs > 0 ? (
        <time
          dateTime={new Date(session.lastActivityMs).toISOString()}
          title={formatDateTime(
            new Date(session.lastActivityMs).toISOString(),
            i18n.language,
          )}
          className="shrink-0 whitespace-nowrap text-overline font-normal tabular-nums text-text-3"
        >
          {ago(session.lastActivityMs, now)}
        </time>
      ) : null}
    </>
  )
}
