// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// EVERY LIVE SESSION, SIDE BY SIDE: one card per session that has not ended, on Now above
// Recent work, which keeps the sessions that did. The cards read the sessions `home-view`
// already merged (no second read) and tell each one with the parts the Sessions list uses:
// its tool, its folder, its name, what it is doing and what it cost. Every part is a fact
// the engine sent; one it did not send is left out. A card opens its session in the
// Estate sheet, over this page (#2549).
import { Fragment } from 'react'
import type { ReactNode } from 'react'
import { Link } from '@tanstack/react-router'
import { ArrowRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { RunStateBadge } from '@/features/agentops/run-state-badge'
import { CcStateBadge } from '@/features/sessions/cc-state-badge'
import { runFolder } from '@/features/sessions/folder'
import {
  primaryRun,
  sessionNaming,
  sessionPlace,
  sessionTool,
  sharedNames,
  type UnifiedSession,
} from '@/features/sessions/provenance'
import { addressOf, SESSION_PARAM } from '@/features/sessions/session-address'
import { opensInSheet } from '@/features/sessions/opens-in-sheet'
import {
  sessionModelRef,
  sessionUsage,
} from '@/features/sessions/session-usage'
import { workFacts } from '@/features/sessions/work-facts'
import { RelTime } from '@/features/shared/rel-time'
import { tickLive, useSecondClock } from '@/features/shared/use-second-clock'
import { formatDuration, formatMicroUsd } from '@/lib/format'
import { LIVE_CARDS } from './now-sessions'
import { workLine } from './work-line'
import './i18n'

function LiveCard({
  session,
  shared,
  nowMs,
  onOpen,
}: {
  session: UnifiedSession
  shared: ReadonlySet<string>
  nowMs: number
  onOpen?: (address: string) => void
}) {
  const { t } = useTranslation(['home', 'sessions'])
  const { t: ts } = useTranslation('sessions')
  const live = session.live
  const run = primaryRun(session.runs)
  const naming = sessionNaming(session, t('recent.untitled'), shared)
  const tool = sessionTool(session)
  const folder = runFolder(run)
  // A session with no name of its own is named by its place (tool and folder), which the
  // card already shows on its first two lines: the name line is not repeated.
  const name = naming.name === sessionPlace(session) ? null : naming.name
  const doing = live ? workLine(live) : null
  const cost = sessionUsage(session.runs, live)?.costMicroUsd
  // C2-DESIGN §3: an active session's elapsed figure ticks each second, extrapolated
  // from its last engine event; every other session keeps the engine's figure.
  const tickedLive = live && tickLive(live, nowMs)
  const tickedSeconds = tickedLive?.duration_seconds
  const elapsed =
    tickedSeconds != null && tickedSeconds > 0
      ? formatDuration(tickedSeconds * 1000)
      : null
  // The elapsed and cost figures leave the facts line: they tick as their own tabular
  // figures (data-elapsed / data-cost), so the line keeps the rest.
  const facts = tickedLive
    ? workFacts(tickedLive, ts, {}, sessionModelRef(run, tickedLive))
    : []
  const costText = cost != null ? formatMicroUsd(cost, { compact: true }) : null
  const at =
    session.lastActivityMs > 0
      ? new Date(session.lastActivityMs).toISOString()
      : live?.last_event_at
  const lineParts: ReactNode[] = []
  if (elapsed)
    lineParts.push(
      <span key="elapsed" data-elapsed>
        {elapsed}
      </span>,
    )
  for (const f of facts) lineParts.push(<span key={`f:${f}`}>{f}</span>)
  if (costText)
    lineParts.push(
      <span key="cost" data-cost>
        {costText}
      </span>,
    )

  return (
    <li className="min-w-0">
      <Link
        to={'/sessions' as never}
        search={{ [SESSION_PARAM]: addressOf(session) } as never}
        onClick={(e) => {
          if (!onOpen || !opensInSheet(e)) return
          e.preventDefault()
          onOpen(addressOf(session))
        }}
        data-testid="now-live-card"
        // No aria-label: the card's own text (tool, state, folder, name, cost) names the
        // link, so what a screen reader says starts with what the card shows (WCAG 2.5.3).
        className="flex h-full min-w-0 flex-col gap-1 rounded-card border border-line bg-surface p-3 outline-none transition-colors hover:bg-hover focus-visible:ring-2 focus-visible:ring-focus"
      >
        <span className="flex min-w-0 items-center gap-2">
          <span className="min-w-0 flex-1 truncate font-medium text-text">
            {tool ?? naming.name}
            {!tool && naming.shortId ? (
              <span className="font-normal text-text-3"> {naming.shortId}</span>
            ) : null}
          </span>
          <span data-testid="now-live-card-state" className="shrink-0">
            {run ? (
              <RunStateBadge run={run} className="whitespace-nowrap" />
            ) : live ? (
              <CcStateBadge
                state={live.cc_state}
                className="whitespace-nowrap"
              />
            ) : null}
          </span>
        </span>
        {folder && folder !== (tool ?? naming.name) ? (
          <span className="truncate font-mono text-caption text-text-3">
            {folder}
          </span>
        ) : null}
        {name && tool ? (
          <span className="line-clamp-2 text-body text-text [overflow-wrap:anywhere]">
            {name}
            {naming.shortId ? (
              <span className="text-text-3"> {naming.shortId}</span>
            ) : null}
          </span>
        ) : null}
        {doing?.from === 'action' && doing.text !== naming.name ? (
          <span className="truncate font-mono text-caption text-text-2">
            {doing.text}
          </span>
        ) : null}
        {lineParts.length > 0 || at ? (
          <span className="mt-auto flex min-w-0 items-center gap-2 pt-1 text-caption text-text-3">
            <span className="min-w-0 flex-1 truncate tabular-nums">
              {lineParts.map((part, i) => (
                <Fragment key={i}>
                  {i > 0 ? <span aria-hidden> · </span> : null}
                  {part}
                </Fragment>
              ))}
            </span>
            {at ? (
              <RelTime ts={at} className="shrink-0 whitespace-nowrap" />
            ) : null}
          </span>
        ) : null}
      </Link>
    </li>
  )
}

export function LiveCards({
  sessions,
  showAll,
  onOpen,
}: {
  /** The live sessions, most recent first; the first `LIVE_CARDS` are cards. */
  sessions: UnifiedSession[]
  /** The link to every session, when no Recent work list below carries it. */
  showAll: boolean
  /** Opens a card's session in place (Estate's sheet); without it a card goes to Sessions. */
  onOpen?: (address: string) => void
}) {
  const { t } = useTranslation('home')
  const cards = sessions.slice(0, LIVE_CARDS)
  // One clock for the row of cards: the ticking figures all move on the same second,
  // and run only while a card shows an active session.
  const nowMs = useSecondClock(cards.some((s) => s.live?.cc_state === 'active'))
  const shared = sharedNames(cards, t('recent.untitled'))
  return (
    <section aria-labelledby="now-live-heading" data-testid="now-live">
      {/* No count beside the title: Estate's sentence counts the sessions open, and a
          second number over the cards shown here could disagree with it. */}
      <h2
        id="now-live-heading"
        className="mb-1.5 text-heading font-semibold text-text"
      >
        {t('live.title')}
      </h2>
      <ul data-testid="now-live-cards" className="grid gap-2 sm:grid-cols-2">
        {cards.map((s) => (
          <LiveCard
            key={s.key}
            session={s}
            shared={shared}
            nowMs={nowMs}
            onOpen={onOpen}
          />
        ))}
      </ul>
      {showAll ? (
        <div className="flex h-6 items-center justify-end">
          <Button asChild variant="ghost" size="sm">
            <Link to={'/sessions' as never} data-testid="now-live-all">
              {t('recent.openAll')}
              <ArrowRight />
            </Link>
          </Button>
        </div>
      ) : null}
    </section>
  )
}
