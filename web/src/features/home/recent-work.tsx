// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT HAPPENED, TOLD AS WORK.
//
// A run reads as plain sentences — how long it worked, what it did, and what changed.
// This front door used to tell the same estate as six numbers with a subtitle, and the
// evidence was always another route away, which a review recorded as the row "How work
// is told".
//
// ⛔ IT COSTS NO REQUEST. `home-view` ALREADY reads `GET /v1/m/sessions/live` to put
//    one number on the live-sessions tile, and that answer carries everything a
//    narrative needs: duration, what the session was doing, how many tool calls,
//    tokens, cost, and when it was last seen. This component is handed that same
//    array. A front door that opened a second endpoint to say what the first one had
//    already said would be paying twice for one fact.
//
// ⛔ WHAT IT WILL NOT DO IS INVENT THE SENTENCE. The degradation ladder and the
//    measurement behind it live in `work-line.ts`; nothing here composes prose about
//    work the engine did not report.
//
// ⛔ AND THE CLICK NOW REACHES THE CARD. This comment used to record the gap:
//    `/sessions` held its selection in component state, so a row here could only open
//    the room that holds the card. The session is addressable now
//    (`features/sessions/session-address.ts`), so every row is a link to the ONE
//    session it tells — which is the row that review measured this front door against.
//
//    The address is the row's own key, built here from the live row the front door
//    already holds. Nothing is composed: `liveRowKey` is the same function the join
//    uses, so a row's link and the surface's own identity for it cannot drift.
import { Link } from '@tanstack/react-router'
import { ArrowRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState } from '@/components/ui/error-state'
import { Skeleton } from '@/components/ui/skeleton'
import { SectionCard } from '@/features/_intel'
import { CcStateBadge } from '@/features/sessions/cc-state-badge'
import { liveRowKey } from '@/features/sessions/provenance'
import { SESSION_PARAM } from '@/features/sessions/session-address'
import type { LiveDTO } from '@/features/sessions/types'
import { workFacts } from '@/features/sessions/work-facts'
import { RelTime } from '@/features/shared/rel-time'
import { formatDuration, formatMicroUsd } from '@/lib/format'
import type { TileState } from './components'
import { RECENT_WORK_ROWS, workLine } from './work-line'
import './i18n'

function WorkRow({ session }: { session: LiveDTO }) {
  const { t } = useTranslation(['home', 'sessions'])
  const { t: ts } = useTranslation('sessions')
  const line = workLine(session)
  // The "what changed" line. The RULE lives in the sessions plane, because the work
  // surface tells the same line and two copies would have drifted the first time a
  // field was added. Every part is still a figure the engine sent, and a part it did
  // not send is still left out rather than printed as a zero.
  const facts = workFacts(session, ts, {
    duration: (ms) => formatDuration(ms),
    cost: (micro) => formatMicroUsd(micro, { compact: true }),
  })

  return (
    <li className="border-b border-border last:border-b-0">
      {/* THE WHOLE ROW IS THE LINK, and it is one link and not three: a row holding a
          badge link, a text link and a time link is three tab stops saying the same
          thing. The accessible name is the sentence — what the session was doing —
          because that is what an operator is choosing between. */}
      <Link
        to={'/sessions' as never}
        search={{ [SESSION_PARAM]: liveRowKey(session) } as never}
        data-testid="home-recent-row"
        aria-label={t('recent.open', { what: line.text })}
        className="flex flex-col gap-1.5 px-4 py-3 outline-none transition-colors hover:bg-muted focus-visible:bg-muted focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset"
      >
        <span className="flex flex-wrap items-center gap-2">
          <CcStateBadge state={session.cc_state} />
          <span
            className="min-w-0 flex-1 truncate text-body text-foreground"
            title={line.text}
          >
            {line.text}
          </span>
          <RelTime
            ts={session.last_event_at}
            className="shrink-0 text-caption text-muted-foreground"
          />
        </span>
        {facts.length > 0 ? (
          <span className="text-caption text-muted-foreground">
            {facts.join(' · ')}
          </span>
        ) : null}
      </Link>
    </li>
  )
}

export function RecentWork({
  sessions,
  state,
  canStartSession,
}: {
  /** The page `home-view` already holds, most recent first. */
  sessions: LiveDTO[] | undefined
  state: TileState
  /** May this principal actually start a run? Decides whether the empty state offers. */
  canStartSession: boolean
}) {
  const { t } = useTranslation('home')
  const rows = (sessions ?? []).slice(0, RECENT_WORK_ROWS)

  return (
    <SectionCard
      title={t('recent.title')}
      description={t('recent.description')}
      actions={
        <Button asChild variant="outline" size="sm">
          {/* The feature registry IS the route table, so this path resolves at runtime
              even though the generated route types do not list it. */}
          <Link to={'/sessions' as never} data-testid="home-recent-all">
            {t('recent.openAll')}
            <ArrowRight />
          </Link>
        </Button>
      }
      noPadding
    >
      {state === 'loading' ? (
        <div
          className="flex flex-col gap-3 p-4"
          data-testid="home-recent-loading"
        >
          {Array.from({ length: 3 }, (_, i) => (
            <Skeleton key={i} className="h-10 w-full" />
          ))}
        </div>
      ) : state === 'unavailable' ? (
        // A failed read is NOT an empty estate, and the two must never look alike.
        <ErrorState
          title={t('recent.unavailableTitle')}
          description={t('recent.unavailableDescription')}
        />
      ) : rows.length === 0 ? (
        <EmptyState
          title={t('recent.emptyTitle')}
          description={t('recent.emptyDescription')}
          action={
            canStartSession ? (
              <Button asChild variant="primary" size="sm">
                <Link to={'/agentops' as never} data-testid="home-recent-start">
                  {t('next.session.title')}
                </Link>
              </Button>
            ) : undefined
          }
        />
      ) : (
        <ul data-testid="home-recent-rows">
          {rows.map((s) => (
            <WorkRow key={s.live_ref || s.session_ref} session={s} />
          ))}
        </ul>
      )}
    </SectionCard>
  )
}
