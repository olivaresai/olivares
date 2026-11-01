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
// ⛔ ONE SESSION, ONE ROW. The rows are the sessions `home-view` merges from the two reads
//    it holds: the runs Olivares operates and the live sessions it observes
//    (`mergeSessions`, the same join the Sessions page and the rail use). A launched
//    session used to be missing here, or shown as "Untitled session <uuid>", because this
//    list read the observed half only.
//
// ⛔ WHAT IT WILL NOT DO IS INVENT THE SENTENCE. A row is named by what its operator
//    typed, else by the degradation ladder in `work-line.ts`; nothing here composes prose
//    about work the engine did not report.
//
// ⛔ AND THE CLICK REACHES THE SESSION. Every row links to the one session it tells, by the
//    same address the Sessions page uses (`features/sessions/session-address.ts`).
import { Link } from '@tanstack/react-router'
import { ArrowRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState } from '@/components/ui/error-state'
import { Skeleton } from '@/components/ui/skeleton'
import { CcStateBadge } from '@/features/sessions/cc-state-badge'
import {
  primaryRun,
  sessionNaming,
  sharedNames,
  type UnifiedSession,
} from '@/features/sessions/provenance'
import { runFolder } from '@/features/sessions/folder'
import { RunStateBadge } from '@/features/agentops/run-state-badge'
import { useRunLedger } from '@/features/agentops/run-state-word'
import { addressOf, SESSION_PARAM } from '@/features/sessions/session-address'
import { opensInSheet } from '@/features/sessions/opens-in-sheet'
import { sessionModelRef } from '@/features/sessions/session-usage'
import { workFacts } from '@/features/sessions/work-facts'
import { RelTime } from '@/features/shared/rel-time'
import { formatDuration, formatMicroUsd } from '@/lib/format'
import type { TileState } from './view-state'
import { RECENT_WORK_ROWS, workLine } from './work-line'
import './i18n'

function WorkRow({
  session,
  shared,
  onOpen,
}: {
  session: UnifiedSession
  /** Names another row of this list also carries: those rows show their tail. */
  shared: ReadonlySet<string>
  onOpen?: (address: string) => void
}) {
  const { t } = useTranslation(['home', 'sessions'])
  const { t: ts } = useTranslation('sessions')
  // One session, one row: a launched run and what was observed of it are the same row
  // (mergeSessions), named by what its operator typed, else by what it did.
  const live = session.live
  const run = primaryRun(session.runs)
  // Finished or stopped: the run's own ledger says which.
  const ledger = useRunLedger(run)
  const naming = sessionNaming(session, t('recent.untitled'), shared)
  const name = naming.name
  const shortId = naming.shortId
  const folder = runFolder(run)
  // The "what changed" line. The RULE lives in the sessions plane, because the work
  // surface tells the same line and two copies would have drifted the first time a
  // field was added. Every part is still a figure the engine sent, and a part it did
  // not send is still left out rather than printed as a zero.
  const facts = live
    ? workFacts(
        live,
        ts,
        {
          duration: (ms) => formatDuration(ms),
          cost: (micro) => formatMicroUsd(micro, { compact: true }),
        },
        sessionModelRef(run, live),
      )
    : []
  // The action the engine reported is not the sentence (the name is the list's, the
  // same as the Sessions list says it, #2266) and it is not dropped: it is on the hover.
  const doing = live ? workLine(live) : null
  const fullTitle = [
    name,
    shortId,
    naming.reference,
    folder,
    doing?.from === 'action' ? doing.text : null,
    ...facts,
  ]
    .filter((part, i, all) => part && all.indexOf(part) === i)
    .join(' · ')
  const at =
    session.lastActivityMs > 0
      ? new Date(session.lastActivityMs).toISOString()
      : live?.last_event_at

  return (
    <li className="border-b border-border last:border-b-0">
      {/* THE WHOLE ROW IS THE LINK, and it is one link and not three: a row holding a
          badge link, a text link and a time link is three tab stops saying the same
          thing. The accessible name is the row's own text, state, sentence and age, so
          a voice user can say what they see (WCAG 2.5.3). */}
      {/* ONE LINE, 40 px, AND NOTHING WRAPS (reference row 8). It was two
          stacked lines inside `flex-wrap` at `py-3` — about 72 px, and more when the
          sentence wrapped. The reference's list rows are one line with a clear
          hierarchy; five rows here now cost 200 px instead of 360, which is the whole
          difference between "what is in progress" being on the front door and being
          below the fold.

          `min-w-0` on the row AND on the sentence: the state badge and the age are
          fixed-size, so the sentence is the only element that may give way, and
          without `min-w-0` its own text sets a floor and pushes the age off the edge —
          the failure measured on four tables.

          THE FACTS ARE NOT DROPPED. They follow the sentence in the muted register;
          nothing the engine reported is removed, and a fact it did not report is still
          left out rather than zeroed.

          ⛔ AND THE SENTENCE WRAPS; IT DOES NOT CUT. The list-row height is the row's
          least height, not its only one: at 1280 px in German the sentence was 817 px in
          an 810 px box and cut itself, with the rest only on a hover title. The state
          badge and the age stay whole beside it. */}
      <Link
        to={'/sessions' as never}
        search={{ [SESSION_PARAM]: addressOf(session) } as never}
        onClick={(e) => {
          if (!onOpen || !opensInSheet(e)) return
          e.preventDefault()
          onOpen(addressOf(session))
        }}
        data-testid="home-recent-row"
        title={fullTitle}
        /* Below `sm` the row wraps: the state and the time share the first line and the
           sentence takes the full width below them — at 320 px it was one word a line
           between the badge and "4 months ago". */
        className="flex min-h-[var(--console-list-row-height)] min-w-0 items-center gap-2 px-3 py-1.5 outline-none transition-colors hover:bg-muted focus-visible:bg-muted focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset max-sm:flex-wrap max-sm:gap-y-1"
      >
        {run ? (
          <RunStateBadge
            run={run}
            ledger={ledger}
            className="shrink-0 whitespace-nowrap"
          />
        ) : live ? (
          <CcStateBadge
            state={live.cc_state}
            className="shrink-0 whitespace-nowrap"
          />
        ) : null}
        <span
          data-slot="recent-row-sentence"
          className="min-w-0 flex-1 text-body text-foreground [overflow-wrap:anywhere] max-sm:order-last max-sm:basis-full"
        >
          <span>{name}</span>
          {shortId ? (
            <span className="text-muted-foreground"> {shortId}</span>
          ) : null}
          {folder && !name.includes(folder) ? (
            <span className="text-caption text-muted-foreground">
              {' · '}
              {folder}
            </span>
          ) : null}
          {facts.length > 0 ? (
            <span className="text-muted-foreground">
              {' '}
              · {facts.join(' · ')}
            </span>
          ) : null}
        </span>
        {at ? (
          <RelTime
            ts={at}
            className="shrink-0 whitespace-nowrap text-caption text-muted-foreground max-sm:ml-auto"
          />
        ) : null}
      </Link>
    </li>
  )
}

export function RecentWork({
  sessions,
  state,
  titled = false,
  onOpen,
}: {
  /** Show the section title (Now, where the list follows "Needs you"); otherwise it is
   *  for assistive technology only. */
  titled?: boolean
  /** The sessions `home-view` holds (runs and live rows merged), most recent first. */
  sessions: UnifiedSession[] | undefined
  state: TileState
  /** Opens a row's session in place (Estate's sheet); without it a row goes to Sessions. */
  onOpen?: (address: string) => void
}) {
  const { t } = useTranslation('home')
  const rows = (sessions ?? []).slice(0, RECENT_WORK_ROWS)
  const shared = sharedNames(rows, t('recent.untitled'))

  return (
    <section aria-labelledby="home-recent-heading" data-testid="home-recent">
      <h2
        id="home-recent-heading"
        className={
          titled ? 'mb-1.5 text-heading font-semibold text-text' : 'sr-only'
        }
      >
        {t('recent.title')}
      </h2>
      {state === 'loading' ? (
        <div className="flex flex-col gap-3" data-testid="home-recent-loading">
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
        // Now's start line above is the one next step (it knows what each tool can run on);
        // a second "Start a session" here offered it twice, and while no tool was ready it
        // contradicted the line's Install or Sign in.
        <EmptyState
          title={t('recent.emptyTitle')}
          description={t('recent.emptyDescription')}
        />
      ) : (
        <>
          <ul data-testid="home-recent-rows">
            {rows.map((s) => (
              <WorkRow
                key={s.key}
                session={s}
                shared={shared}
                onOpen={onOpen}
              />
            ))}
          </ul>
          <div className="flex h-6 items-center justify-end">
            <Button asChild variant="ghost" size="sm">
              <Link to={'/sessions' as never} data-testid="home-recent-all">
                {t('recent.openAll')}
                <ArrowRight />
              </Link>
            </Button>
          </div>
        </>
      )}
    </section>
  )
}
