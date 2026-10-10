// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// WHAT THIS SESSION DID, TOLD AS A THREAD — the middle pane of the work surface.
//
// One line of header (`thread-header.tsx`), then the conversation in one centered column.
// A session Olivares did not start has no conversation to tell; there the body is the
// overview (`session-overview.tsx`), the sentence and the figures the engine sent, with
// its evidence. For every other session the overview is the Context pane's first block.
//
// ⛔ THE SENTENCE AND THE FIGURES ARE THE FRONT DOOR's, IMPORTED (see the overview): the
//    pane never re-derives what the front door already says.
//
// ⛔ THE THREE ANSWERS STAY THREE ANSWERS. Nothing here, a read that failed,
//    and a permission boundary are three different screens, and the cheapest place to
//    collapse them into one is a new pane.
import { SlidersHorizontal } from 'lucide-react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { EmptyState } from '@/components/ui/empty-state'
import { ForbiddenState } from '@/components/ui/error-state'
import { Skeleton } from '@/components/ui/skeleton'
import type { ConversationItem } from './conversation-frames'
import {
  capabilities,
  operatorName,
  primaryRun,
  sessionReference,
  sessionShortId,
  type UnifiedSession,
} from './provenance'
import { addressOf, type EvidenceBlock } from './session-address'
import { hasThread } from './has-thread'
import { SessionConversation } from './session-conversation'
import { SessionOverview } from './session-overview'
import type { SessionResolution } from './use-session-resolution'
import { ThreadHeader } from './thread-header'
import './i18n'

export function SessionNarrative({
  resolution,
  pinned,
  onTogglePin,
  onOpenDetail,
  evidence,
  onExpandEvidence,
  inspectedId,
  onInspect,
  contextToggle,
  contextPaneButton,
  onOpenContext,
  onFolder,
  back,
  peerSessions,
}: {
  resolution: SessionResolution
  pinned: boolean
  onTogglePin: ((address: string) => void) | null
  /** The full session controls — attach, drive, stop, governance — live in the card. */
  onOpenDetail: () => void
  evidence: EvidenceBlock
  onExpandEvidence: (block: EvidenceBlock) => void
  inspectedId?: string | null
  onInspect?: (item: ConversationItem) => void
  /** The work surface's Context icon toggle, shown beside the session's own actions. */
  contextToggle?: ReactNode
  /** The Context icon that brings the pane forward where it is a pane of its own. */
  contextPaneButton?: ReactNode
  onOpenContext?: () => void
  /** Told the folder the tool's own first frame names. */
  onFolder?: (cwd: string | null) => void
  /** The way back to the list, below 761 px. */
  back?: ReactNode
  /** The sessions on the surface, from which "Can message" offers this one's peers. */
  peerSessions?: readonly UnifiedSession[]
}) {
  const { t } = useTranslation('sessions')
  const { target, session, live, loading, grants } = resolution

  // Recovery belongs to the pane, including before its session can be read.
  if (!target || loading || (!grants.liveRead && !grants.runRead))
    return (
      <div className="flex min-h-0 flex-1 flex-col">
        {back ? (
          <div className="flex h-12 shrink-0 items-center px-3">{back}</div>
        ) : null}
        {!target ? (
          <EmptyState
            icon={<SlidersHorizontal />}
            title={t('narrative.noneTitle')}
            description={t('narrative.noneDescription')}
          />
        ) : loading ? (
          <div
            className="flex flex-col gap-3 p-4"
            data-testid="narrative-loading"
          >
            <Skeleton className="h-6 w-2/3" />
            <Skeleton className="h-4 w-1/2" />
            <Skeleton className="h-24 w-full" />
          </div>
        ) : (
          <ForbiddenState
            title={t('forbidden.title')}
            description={t('forbidden.description')}
          />
        )}
      </div>
    )

  const run = primaryRun(session.runs)
  const caps = capabilities({ ...session, runs: run ? [run] : [] }, grants)
  // ⛔ THE HEADING ANSWERS *WHICH* SESSION. It must not be the sentence the overview tells
  //    (*what it did*), and it must not be the raw reference either, which is what
  //    `sessionLabel` painted here in the monospaced face. So: the name its operator typed,
  //    else the distinguishing tail of the reference — whose whole value is on `title` and
  //    in the identifiers block.
  const named = operatorName(session)
  const shortId = sessionShortId(session)
  const reference = sessionReference(session)
  const titleHint = [named, shortId, reference]
    .filter((part, i, all) => part && all.indexOf(part) === i)
    .join(' · ')

  return (
    <div
      className="flex min-h-0 flex-1 flex-col"
      data-testid="session-narrative"
    >
      <ThreadHeader
        session={session}
        run={run}
        live={live}
        caps={caps}
        title={named ?? shortId}
        titleHint={titleHint}
        titleMono={!named}
        address={addressOf(session)}
        reference={reference ?? null}
        pinned={pinned}
        onTogglePin={onTogglePin}
        onOpenDetail={onOpenDetail}
        onOpenContext={onOpenContext}
        back={back}
        contextToggle={contextToggle}
        contextPaneButton={contextPaneButton}
      />

      {/* MC: a Grok Build or OpenCode run can reach MCP servers named in the tool's own
          settings, outside Olivares. The engine says so; the sentence is shown as-is. */}
      {run?.mcp_governance_warning ? (
        <p
          role="note"
          className="px-4 pt-2 text-caption text-warning"
          data-slot="mcp-governance-warning"
        >
          {run.mcp_governance_warning}
        </p>
      ) : null}

      {hasThread(resolution) && run ? (
        <SessionConversation
          run={run}
          selectedId={inspectedId}
          onInspect={onInspect}
          onFolder={onFolder}
        />
      ) : (
        <div className="min-h-0 flex-1 overflow-y-auto p-4">
          <SessionOverview
            resolution={resolution}
            evidence={evidence}
            onExpandEvidence={onExpandEvidence}
            peerSessions={peerSessions}
          />
        </div>
      )}
    </div>
  )
}
