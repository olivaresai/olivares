// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT THIS SESSION DID, TOLD AS WORK — the middle pane of the work surface.
//
// The front door already carries this sentence, and what was still missing was named
// there: the row could open the ROOM and not the card. This is the card's content as a PANE,
// so an operator reads the work and its evidence in the place they are already looking.
//
// ⛔ THE SENTENCE AND THE FIGURES ARE THE FRONT DOOR's, IMPORTED. `workLine` and `workFacts` are
//    the same two functions the front door calls. Re-deriving them here would let one
//    screen say a session did something the other screen does not.
//
// ⛔ AND THE SENTENCE STATES ITS OWN PROVENANCE. `workLine` reports which field it came
//    from, and the bottom rung — the session's own reference — is not a summary of
//    anything. When that is the rung, the pane says the engine reported no objective,
//    rather than letting a reference look like a description of the work.
//
// ⛔ THE THREE ANSWERS STAY THREE ANSWERS. Nothing here, a read that
//    failed, and a permission boundary are three different screens, and the cheapest
//    place to collapse them into one is a new pane.
import {
  ArrowUpRight,
  MoreHorizontal,
  Pin,
  PinOff,
  SlidersHorizontal,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { EmptyState } from '@/components/ui/empty-state'
import { Kbd } from '@/components/ui/kbd'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { Skeleton } from '@/components/ui/skeleton'
import { RunStateBadge } from '@/features/agentops/run-state-badge'
import { workLine } from '@/features/home/work-line'
import { LiveDot } from '@/features/shared'
import { formatDuration, formatMicroUsd } from '@/lib/format'
import { AttributionChip } from './attribution-chip'
import { CcStateBadge } from './cc-state-badge'
import { isScopedRow, primaryRun, sessionLabel } from './provenance'
import { addressOf, type EvidenceBlock } from './session-address'
import { SessionEvidence } from './session-evidence'
import type { SessionResolution } from './use-session-resolution'
import { workFacts } from './work-facts'
import './i18n'

export function SessionNarrative({
  resolution,
  pinned,
  onTogglePin,
  onOpenDetail,
  evidence,
  onExpandEvidence,
  emptyAction,
}: {
  resolution: SessionResolution
  pinned: boolean
  onTogglePin: ((address: string) => void) | null
  /** The full session controls — attach, drive, stop, governance — live in the card. */
  onOpenDetail: () => void
  evidence: EvidenceBlock
  onExpandEvidence: (block: EvidenceBlock) => void
  /** Offered when no session is open at all, already gated by the caller. */
  emptyAction?: React.ReactNode
}) {
  const { t, i18n } = useTranslation('sessions')
  const lang = i18n.language
  const { target, session, live, loading, observeUnknown, grants } = resolution

  if (!target)
    return (
      <EmptyState
        icon={<SlidersHorizontal />}
        title={t('narrative.noneTitle')}
        description={t('narrative.noneDescription')}
        action={emptyAction}
      />
    )

  if (loading)
    return (
      <div className="flex flex-col gap-3 p-4" data-testid="narrative-loading">
        <Skeleton className="h-6 w-2/3" />
        <Skeleton className="h-4 w-1/2" />
        <Skeleton className="h-24 w-full" />
      </div>
    )

  if (!grants.liveRead && !grants.runRead)
    return (
      <ForbiddenState
        title={t('forbidden.title')}
        description={t('forbidden.description')}
      />
    )

  const run = primaryRun(session.runs)
  const line = live ? workLine(live) : null
  const facts = live
    ? workFacts(live, t, {
        duration: (ms) => formatDuration(ms),
        cost: (micro) => formatMicroUsd(micro, { locale: lang }),
      })
    : []
  const address = addressOf(session)

  return (
    <div className="flex flex-col gap-4" data-testid="session-narrative">
      <div className="flex flex-col gap-2">
        <div className="flex items-start gap-2">
          <h2 className="min-w-0 flex-1 truncate font-mono text-title text-foreground">
            {sessionLabel(session)}
          </h2>
          {/* THE MENU PATH, equal to the `p` key the rail listens for — and it lives
              HERE because a focusable control inside a `role="option"` row is
              children-presentational in ARIA, so it would be invisible to assistive
              technology and a finding for the gate. The item NAMES what it will do and
              shows the key that does the same thing. */}
          {onTogglePin ? (
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label={t('narrative.menu', {
                    name: sessionLabel(session),
                  })}
                  data-testid="narrative-menu"
                >
                  <MoreHorizontal />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuItem onClick={() => onTogglePin(address)}>
                  {pinned ? <PinOff /> : <Pin />}
                  {pinned ? t('rail.unpin') : t('rail.pin')}
                  <span className="ml-auto pl-3">
                    <Kbd>p</Kbd>
                  </span>
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          ) : null}
          <Button
            variant="secondary"
            size="sm"
            onClick={onOpenDetail}
            data-testid="narrative-open-detail"
          >
            {t('narrative.openDetail')}
            <ArrowUpRight className="size-3.5" />
          </Button>
        </div>

        <div className="flex flex-wrap items-center gap-2">
          {live ? <CcStateBadge state={live.cc_state} /> : null}
          {run ? <RunStateBadge state={run.state} /> : null}
          {live && isScopedRow(live) ? (
            <AttributionChip attribution={live.attribution} />
          ) : null}
          {live ? <LiveDot status={resolution.streamStatus} /> : null}
        </div>
      </div>

      {/* WHAT IT DID. Three answers, never merged: a read that failed is not an
          absence of work, and an absence of telemetry is not a failure. */}
      {live ? (
        <div className="flex flex-col gap-1.5">
          <p className="text-body text-foreground">{line?.text}</p>
          {line?.from === 'ref' ? (
            <p className="text-caption text-muted-foreground">
              {t('narrative.noObjective')}
            </p>
          ) : null}
          {facts.length > 0 ? (
            <p
              className="text-caption text-muted-foreground"
              data-testid="narrative-facts"
            >
              {facts.join(' · ')}
            </p>
          ) : null}
        </div>
      ) : observeUnknown ? (
        <ErrorState
          title={t('narrative.notReadTitle')}
          description={t('card.observedNotRead')}
        />
      ) : (
        <p className="text-body text-muted-foreground">
          {t('card.noObservationYet')}
        </p>
      )}

      {live ? (
        <SessionEvidence
          liveRef={live.live_ref || undefined}
          sessionRef={live.session_ref}
          expanded={evidence}
          onExpand={onExpandEvidence}
        />
      ) : null}
    </div>
  )
}
