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
//    anything. When that is the rung, the pane prints no sentence at all, rather than
//    letting a reference look like a description of the work (or a line saying so).
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
import { CodeLine } from '@/components/ui/code-line'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { EmptyState } from '@/components/ui/empty-state'
import { Kbd } from '@/components/ui/kbd'
import { ForbiddenState } from '@/components/ui/error-state'
import { QueryErrorState } from '@/components/layout/query-error-state'
import { Skeleton } from '@/components/ui/skeleton'
import { RunStateBadge } from '@/features/agentops/run-state-badge'
import { workLine } from '@/features/home/work-line'
import { LiveDot } from '@/features/shared'
import { formatDuration, formatMicroUsd } from '@/lib/format'
import { cn } from '@/lib/utils'
import { AttributionChip } from './attribution-chip'
import { CcStateBadge } from './cc-state-badge'
import { runFolder } from './folder'
import { CanMessage } from './can-message'
import type { UnifiedSession } from './provenance'
import type { ConversationItem } from './conversation-frames'
import {
  capabilities,
  isScopedRow,
  operatorName,
  primaryRun,
  sessionReference,
  sessionShortId,
} from './provenance'
import { addressOf, type EvidenceBlock } from './session-address'
import { RunActions } from './run-actions'
import { SessionConversation } from './session-conversation'
import { SessionEvidence } from './session-evidence'
import type { SessionResolution } from './use-session-resolution'
import { WorkClause } from './work-clause'
import { workFacts } from './work-facts'
import './i18n'

/** The lifecycle actions the header offers; the rest stay in the full controls. */
const HEADER_ACTIONS = ['interrupt', 'stop', 'resume'] as const

export function SessionNarrative({
  resolution,
  pinned,
  onTogglePin,
  onOpenDetail,
  evidence,
  onExpandEvidence,
  emptyAction,
  inspectedId,
  onInspect,
  contextToggle,
  peerSessions,
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
  inspectedId?: string | null
  onInspect?: (item: ConversationItem) => void
  /** The work surface's Context control, shown beside the session's own actions. */
  contextToggle?: React.ReactNode
  /** The sessions on the surface, from which "Can message" offers this one's peers. */
  peerSessions?: readonly UnifiedSession[]
}) {
  const { t, i18n } = useTranslation('sessions')
  const lang = i18n.language
  const {
    target,
    session,
    live,
    loading,
    observeUnknown,
    observeError,
    grants,
  } = resolution

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
  const caps = capabilities({ ...session, runs: run ? [run] : [] }, grants)
  const folder = runFolder(run)
  const line = live ? workLine(live) : null
  const facts = live
    ? workFacts(live, t, {
        duration: (ms) => formatDuration(ms),
        cost: (micro) => formatMicroUsd(micro, { locale: lang }),
      })
    : []
  const address = addressOf(session)
  // ⛔ THE HEADING ANSWERS *WHICH* SESSION, AND THE LINE BELOW ANSWERS *WHAT IT DID*.
  //    They must not be the same sentence: the pane already tells the clause, so a
  //    heading that also degraded to it would print one fact twice. And it must not be
  //    the raw reference either, which is what `sessionLabel` painted here in the
  //    monospaced face. So: the name its operator typed, else the distinguishing tail
  //    of the reference — whose whole value is on `title` and in the identifiers block.
  const named = operatorName(session)
  const shortId = sessionShortId(session)
  const reference = sessionReference(session)

  return (
    <div className="flex flex-col gap-4" data-testid="session-narrative">
      <div className="flex flex-col gap-2">
        <div className="flex items-start gap-2">
          <h2
            className={cn(
              'min-w-0 flex-1 truncate text-title text-foreground',
              !named && 'font-mono',
            )}
            title={[named, shortId, reference]
              .filter((part, i, all) => part && all.indexOf(part) === i)
              .join(' · ')}
          >
            {named ?? shortId}
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
                  aria-label={t('narrative.menu', { name: named ?? shortId })}
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
          {contextToggle}
        </div>

        {/* ONE STATE. A launched session's state is its run's; the observed state is
            for sessions Olivares did not start. Three badges read "Active · Stopped ·
            Live" after a stop (HU-11). The live dot shows only while the run works. */}
        <div className="flex flex-wrap items-center gap-2">
          {run ? (
            <RunStateBadge state={run.state} />
          ) : live ? (
            <CcStateBadge state={live.cc_state} />
          ) : null}
          {live && isScopedRow(live) ? (
            <AttributionChip attribution={live.attribution} />
          ) : null}
          {live && (!run || run.state === 'running' || run.state === 'idle') ? (
            <LiveDot status={resolution.streamStatus} />
          ) : null}
          {folder ? (
            <span
              className="font-mono text-caption text-muted-foreground"
              title={run?.workspace_path}
              data-testid="narrative-folder"
            >
              {folder}
            </span>
          ) : null}
          {session && peerSessions ? (
            <CanMessage session={session} sessions={peerSessions} />
          ) : null}
          <span className="ml-auto">
            <RunActions
              run={run}
              caps={caps}
              only={HEADER_ACTIONS}
              onClose={() => {}}
            />
          </span>
        </div>
      </div>

      {/* MC: a Grok Build or OpenCode run can reach MCP servers named in the tool's own
          settings, outside Olivares. The engine says so; the sentence is shown as-is. */}
      {run?.mcp_governance_warning ? (
        <p
          role="note"
          className="text-caption text-warning"
          data-slot="mcp-governance-warning"
        >
          {run.mcp_governance_warning}
        </p>
      ) : null}

      {/* THE SAME IN A TERMINAL (Pomerium concept): the exact olivares commands for this
          session, each with Copy. The id is used because a name can hold spaces. */}
      {run ? (
        <details className="text-caption" data-testid="narrative-cli">
          <summary className="cursor-pointer text-muted-foreground">
            {t('narrative.terminal')}
          </summary>
          <div className="mt-2 flex flex-col gap-1.5">
            <CodeLine command={`olivares session follow ${run.run_ref}`} />
            <CodeLine command={`olivares session send ${run.run_ref} "…"`} />
            <CodeLine
              command={`olivares session ${run.state === 'running' || run.state === 'idle' || run.state === 'pending' ? 'stop' : 'resume'} ${run.run_ref}`}
            />
          </div>
        </details>
      ) : null}

      {/* WHAT IT DID. Three answers, never merged: a read that failed is not an
          absence of work, and an absence of telemetry is not a failure. */}
      {live ? (
        <div className="flex flex-col gap-1.5">
          {/* ⛔ AND THE CLAUSE IS NOT THE REFERENCE EITHER. This line printed `line.id` —
              the session reference — whenever the ladder reported `untitled`, which is
              the one rung `workLine` says is not a description of anything. The pane
              says what it knows instead; the heading above already carries the tail. */}
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

      {run && run.transport !== 'remote-control' ? (
        <div className="min-h-40 flex-1 border-t border-border pt-3">
          <SessionConversation
            run={run}
            selectedId={inspectedId}
            onInspect={onInspect}
            workspaceRef={run.workspace_ref}
          />
        </div>
      ) : null}
    </div>
  )
}
