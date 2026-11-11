// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT A SESSION IS DOING, IN A WORD WITH ITS TIME (the regression: "Working 4s", "Done").
// The list's state renderer: it reads the one classifier (sessionDisplayState) and
// never the engine's raw state, as StateDot does for the dot.
import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'
import { humanDurationSeconds } from '@/features/shared'
import { formatDateTime, formatShortDateTime } from '@/lib/format'
import { cn } from '@/lib/utils'
import { primaryRun, type UnifiedSession } from './provenance'
import { sessionDisplayState } from './session-display-state'
import { SessionStatusMark, useStatusWord } from './session-status'
import { useNow } from './use-now'
import './i18n'

/** When this page saw each session start working, by its key; null once it saw it not
 * working. A session already working when the page first saw it has no entry. */
const turnStarts = new Map<string, number | null>()

/**
 * Where a working row's clock runs from: the moment this page saw the session start its
 * turn, else the session's own start, so EVERY working row has a clock. The
 * engine keeps no turn's start. The moment is kept by key, not by row, since a row that
 * starts working moves to another group and mounts again; the row's own clock, which
 * ticks at once when it starts working, reads it.
 */
function useTurnStart(
  key: string,
  working: boolean,
  started: number,
): number | null {
  if (!working) return null
  return turnStarts.get(key) ?? (Number.isFinite(started) ? started : null)
}

/** What the person sees of a session, from the one classifier. */
function displayOf(session: UnifiedSession) {
  return sessionDisplayState(session)
}

/** Whether a turn is running now: Pending and Failed are not, nor is a session at rest. */
function isWorking(display: ReturnType<typeof displayOf>): boolean {
  return (
    display.runState !== 'pending' &&
    display.tone !== 'failed' &&
    display.group === 'active'
  )
}

/**
 * Notes, for every session of the list, when this page saw it start working, whether its
 * row is drawn or not: the Working section is folded, and a row that is not drawn would
 * miss the moment its session began a turn. The list calls it once, with its sessions.
 */
export function useSeenTurns(sessions: readonly UnifiedSession[]) {
  useEffect(() => {
    for (const session of sessions) {
      if (!isWorking(displayOf(session))) turnStarts.set(session.key, null)
      else if (turnStarts.get(session.key) === null)
        turnStarts.set(session.key, Date.now())
    }
  }, [sessions])
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
 * The status's mark and word (Failed; Needs you; Working, with how long it has worked;
 * Done; Paused; Idle), Pending before a run starts. Any session that is not working also says how
 * long ago it last did something, the date on the hover; one paused until a scheduled
 * resume still ahead says when it resumes instead. The word is the state for a screen reader too, and its colour only repeats it.
 */
export function StateWord({ session }: { session: UnifiedSession }) {
  const { t, i18n } = useTranslation(['sessions', 'agentops'])
  const statusWord = useStatusWord()
  const display = displayOf(session)
  const run = primaryRun(session.runs)
  const pending = display.runState === 'pending'
  const working = isWorking(display)
  // A second for a working clock; an age in minutes needs no more than half a minute.
  const now = useNow(true, working ? 1000 : 30_000)
  const since = useTurnStart(
    session.key,
    working,
    Date.parse(run?.started_at ?? session.live?.first_event_at ?? ''),
  )
  // ONE WORD PER STATUS: a session that finished says Done whatever ended it
  // (stopped, ended, cleaned up), so the list never shows two words for one state. The
  // run's own word is in the thread. Only a run not yet started says Pending.
  const word = pending
    ? t('agentops:state.pending')
    : statusWord(display.status)
  const worked =
    since === null
      ? null
      : humanDurationSeconds(Math.max(0, (now - since) / 1000))
  // A resume the engine refused leaves the run stopped with the time it missed.
  const resumes =
    display.status === 'paused' && Date.parse(run?.resume_at ?? '') > now
      ? run?.resume_at
      : undefined
  return (
    <>
      <span
        data-testid="rail-row-state"
        data-status={display.status}
        className={cn(
          'inline-flex shrink-0 items-center gap-1 whitespace-nowrap text-overline font-normal tabular-nums',
          display.tone === 'failed'
            ? 'text-danger'
            : display.group === 'attention'
              ? 'text-warning'
              : working
                ? 'text-text'
                : 'text-text-3',
        )}
      >
        <SessionStatusMark status={display.status} decorative />
        {worked ? `${word} ${worked}` : word}
      </span>
      {resumes ? (
        <time
          dateTime={resumes}
          title={formatDateTime(resumes, i18n.language)}
          className="shrink-0 whitespace-nowrap text-overline font-normal tabular-nums text-text-3"
        >
          {t('limit.resumes', {
            time: formatShortDateTime(resumes, i18n.language),
          })}
        </time>
      ) : !working && session.lastActivityMs > 0 ? (
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
