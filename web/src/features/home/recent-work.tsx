// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT HAPPENED, TOLD AS WORK (D21).
//
// T3 Code tells a run in plain sentences — "Worked for 3m 43s", "Implemented and
// filed PR #7723", then `2 changed files +29 −12 · Open diff`. Our front door told
// the same estate as six numbers with a subtitle, and the evidence was always another
// route away (D15 §E.2-bis, the side-by-side row "How work is told").
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
// ⛔ AND "ONE CLICK TO THE EVIDENCE" IS AN HONEST CLICK, not the one T3 has. There is
//    no per-session deep link in this console today: `/sessions` holds its selection
//    in component state, not in the URL (sessions-workspace-view.tsx), so a row here
//    can only open the room that holds the card, not the card. Adding that URL state
//    is a change to the sessions view, which lane D19 owns and is editing in parallel.
//    It is named in the lane report as the gap, not papered over with a link that
//    would land on an unselected list and look like a bug.
import { Link } from '@tanstack/react-router'
import { ArrowRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { ErrorState } from '@/components/ui/error-state'
import { Skeleton } from '@/components/ui/skeleton'
import { SectionCard } from '@/features/_intel'
import { CcStateBadge } from '@/features/sessions/cc-state-badge'
import type { LiveDTO } from '@/features/sessions/types'
import { RelTime } from '@/features/shared/rel-time'
import { formatDuration, formatMicroUsd } from '@/lib/format'
import type { TileState } from './components'
import { RECENT_WORK_ROWS, workLine } from './work-line'
import './i18n'

function WorkRow({ session }: { session: LiveDTO }) {
  const { t } = useTranslation(['home', 'sessions'])
  const line = workLine(session)
  // The "what changed" line. Every part is a figure the engine sent; a part the
  // engine did not send is left out rather than printed as a zero.
  const facts = [
    session.duration_seconds > 0
      ? t('recent.worked', {
          duration: formatDuration(session.duration_seconds * 1000),
        })
      : null,
    session.tool_call_count > 0
      ? t('recent.toolCalls', { count: session.tool_call_count })
      : null,
    session.event_count > 0
      ? t('recent.events', { count: session.event_count })
      : null,
    session.cost_micro_usd > 0
      ? formatMicroUsd(session.cost_micro_usd, { compact: true })
      : null,
    session.model_ref ?? null,
  ].filter((f): f is string => f !== null)

  return (
    <li className="flex flex-col gap-1.5 border-b border-border px-4 py-3 last:border-b-0">
      <div className="flex flex-wrap items-center gap-2">
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
      </div>
      {facts.length > 0 ? (
        <p className="text-caption text-muted-foreground">
          {facts.join(' · ')}
        </p>
      ) : null}
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
