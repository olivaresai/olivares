// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT THE THREAD'S HEADER NO LONGER SAYS, TOLD IN PLAIN WORDS — the first block of the
// Context pane.
//
// The header is one line (`thread-header.tsx`). The facts it dropped are here: who manages
// the session, the mode the tool reported, whether its stream is live, which peers it can
// message, the folder it works in, what it did (the sentence and the figures) and its
// evidence. A session Olivares did not start has no conversation to show, so there this
// block IS the thread's body.
//
// ⛔ THE SENTENCE AND THE FIGURES ARE THE FRONT DOOR's, IMPORTED. `workLine` and `workFacts`
//    are the same two functions the front door calls. Re-deriving them here would let one
//    screen say a session did something the other screen does not.
//
// ⛔ AND THE SENTENCE STATES ITS OWN PROVENANCE. `workLine` reports which field it came
//    from, and the bottom rung — the session's own reference — is not a summary of
//    anything. When that is the rung, no sentence is printed at all.
//
// ⛔ THE THREE ANSWERS STAY THREE ANSWERS. Nothing here, a read that failed,
//    and a permission boundary are three different screens.
import { useTranslation } from 'react-i18next'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { ForbiddenState } from '@/components/ui/error-state'
import { workLine } from '@/features/home/work-line'
import { LiveDot } from '@/features/shared'
import { formatDuration, formatMicroUsd } from '@/lib/format'
import { AttributionChip } from './attribution-chip'
import { CanMessage } from './can-message'
import { isScopedRow, primaryRun, type UnifiedSession } from './provenance'
import type { EvidenceBlock } from './session-address'
import { SessionEvidence } from './session-evidence'
import { SessionMeter, SessionMode } from './session-meter'
import { sessionUsage } from './session-usage'
import type { SessionResolution } from './use-session-resolution'
import { WorkClause } from './work-clause'
import { workFacts } from './work-facts'
import './i18n'

export function SessionOverview({
  resolution,
  evidence,
  onExpandEvidence,
  peerSessions,
  frameCwd,
}: {
  resolution: SessionResolution
  evidence: EvidenceBlock
  onExpandEvidence: (block: EvidenceBlock) => void
  /** The sessions on the surface, from which "Can message" offers this one's peers. */
  peerSessions?: readonly UnifiedSession[]
  /** The folder the tool's own first frame names, when the run recorded none. */
  frameCwd?: string | null
}) {
  const { t, i18n } = useTranslation('sessions')
  const lang = i18n.language
  const { session, live, observeUnknown, observeError, grants } = resolution
  const run = primaryRun(session.runs)
  const line = live ? workLine(live) : null
  const usage = sessionUsage(run, live)
  // With the meter shown, the cost is its to say (or to say it is unknown): the facts
  // line does not repeat it, nor print a second figure beside "cost not reported".
  const facts = live
    ? workFacts(live, t, {
        duration: (ms) => formatDuration(ms),
        cost: usage
          ? undefined
          : (micro) => formatMicroUsd(micro, { locale: lang }),
      })
    : []
  const cwd = run?.workspace_path || frameCwd
  return (
    <div className="flex flex-col gap-4" data-testid="session-overview">
      <div className="flex flex-wrap items-center gap-2 empty:hidden">
        {live && isScopedRow(live) ? (
          <AttributionChip attribution={live.attribution} />
        ) : null}
        <SessionMode run={run} />
        {live && (!run || run.state === 'running' || run.state === 'idle') ? (
          <LiveDot status={resolution.streamStatus} />
        ) : null}
        {session && peerSessions ? (
          <CanMessage session={session} sessions={peerSessions} />
        ) : null}
      </div>

      {/* WHAT IT HAS SPENT. The header paints it only where it has room (from 1100 px);
          here it is always one look away. */}
      {usage ? (
        <p className="text-caption text-muted-foreground">
          <SessionMeter run={run} usage={usage} where="context" />
        </p>
      ) : null}

      {/* THE FOLDER, with its whole path: a path is never in a header. */}
      {cwd ? (
        <p
          data-testid="conversation-folder"
          title={cwd}
          className="text-caption text-muted-foreground [overflow-wrap:anywhere]"
        >
          {!run?.workspace_ref && run?.workspace_path
            ? t('conversation.temporaryFolder')
            : t('conversation.workingFolder', { cwd })}
        </p>
      ) : null}

      {/* WHAT IT DID. Three answers, never merged: a read that failed is not an absence
          of work, and an absence of telemetry is not a failure. */}
      {live ? (
        <div className="flex flex-col gap-1.5">
          {/* ⛔ AND THE CLAUSE IS NOT THE REFERENCE EITHER. This line printed `line.id` —
              the session reference — whenever the ladder reported `untitled`, which is
              the one rung `workLine` says is not a description of anything. */}
          {line && line.from !== 'untitled' ? (
            <p className="text-body text-foreground">
              <WorkClause text={line.text} live={live} />
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
        // Not read: without sessions:live:read that is a boundary, not a failure; a failed
        // read goes through the one error mapping.
        grants.liveRead ? (
          <QueryErrorState
            error={observeError}
            title={t('narrative.notReadTitle')}
            description={t('card.observedNotRead')}
          />
        ) : (
          <ForbiddenState
            title={t('narrative.notReadTitle')}
            description={t('card.observedNotRead')}
          />
        )
      ) : (
        <p className="text-body text-muted-foreground">
          {t('card.noObservationYet')}
        </p>
      )}

      {live ? (
        <SessionEvidence
          liveRef={live.live_ref || undefined}
          sessionRef={live.session_ref}
          echoRefs={session?.echoes?.map((e) => e.live_ref)}
          expanded={evidence}
          onExpand={onExpandEvidence}
        />
      ) : null}
    </div>
  )
}
