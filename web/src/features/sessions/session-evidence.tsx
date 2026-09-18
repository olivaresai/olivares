// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE EVIDENCE, INLINE — beside the sentence, not one route away.
//
// A review recorded the gap in one row: work told as plain sentences with its evidence
// beside it, against *"numbers with a subtitle; the evidence is another route away"*.
// This is the half that closes it.
//
// ⛔ WHAT OUR EVIDENCE ACTUALLY IS, SAID PLAINLY RATHER THAN IMITATED. There are no
//    changed files here and there is no diff, because this plane does not carry one:
//    minimal-data (docs/SECURITY-HARDENING.md) means only references, classifications and counters
//    cross the wire — never SQL, payloads, secrets or PII. What a session DOES leave is
//    its reconstructible ingest order, and it has three readings an operator asks for:
//
//      · CHECKS     the findings the session produced — our validation list.
//      · ACTIVITY   its last turns: the tool and MCP calls it made.
//      · RESOURCES  what it touched, once per distinct resource.
//
//    Rendering an empty "files changed: 0" would have been the cargo-cult version of
//    the same row: the shape of the reference's evidence over a product that does not
//    produce it.
//
// ⛔ ONE BOUNDED READ, AND IT IS NOT THE CARD'S TIMELINE. The detail card walks the
//    timeline with an infinite, facet-filtered keyset query; this asks once for a
//    bounded page and derives three counts from it. React Query cannot share an entry
//    between an infinite query and a plain one, and a second facet-filtered WALK here
//    would be the worse duplicate of the two. The page is bounded and SAYS SO when it
//    fills: a count taken from a truncated page is a floor, and it is labelled as one.
import { AlertTriangle, ChevronDown, Coins, Plug, Wrench } from 'lucide-react'
import { useQuery } from '@tanstack/react-query'
import { useMemo, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ErrorState, ForbiddenState } from '@/components/ui/error-state'
import { Skeleton } from '@/components/ui/skeleton'
import { RelTimeLabel } from '@/features/shared'
import { useAuth } from '@/lib/auth/context'
import { cn } from '@/lib/utils'
import { sessionsApi, sessionsKeys } from './api'
import { EVIDENCE_PAGE, splitEvidence } from './evidence-split'
import { RefChip } from './ref-chip'
import { EVIDENCE_BLOCKS, type EvidenceBlock } from './session-address'
import type { TimelineDTO } from './types'
import './i18n'

const KIND_ICON = {
  tool: Wrench,
  mcp: Plug,
  cost: Coins,
  finding: AlertTriangle,
} as const

function EntryRow({ entry }: { entry: TimelineDTO }) {
  const { t } = useTranslation('sessions')
  const Icon = KIND_ICON[entry.kind as keyof typeof KIND_ICON] ?? Wrench
  const label = entry.title || entry.tool_ref || t(`kind.${entry.kind}`)
  return (
    <li className="flex items-start gap-2 py-1">
      <Icon
        className={cn(
          'mt-0.5 size-3.5 shrink-0',
          entry.kind === 'finding' ? 'text-warning' : 'text-muted-foreground',
        )}
        aria-hidden
      />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-caption text-foreground">
          {label}
        </span>
        {entry.resource_ref ? (
          <span className="block truncate font-mono text-caption text-muted-foreground">
            {entry.resource_ref}
          </span>
        ) : null}
      </span>
      <RelTimeLabel
        ts={entry.at}
        className="shrink-0 text-caption text-muted-foreground"
      />
    </li>
  )
}

/**
 * ⛔ A COLLAPSED BLOCK SHOWS ITS COUNT AND NOTHING ELSE, and that is an accessibility
 *    decision before it is a layout one. A first draft previewed two rows under a
 *    header whose `aria-expanded` said `false` — i.e. it told a screen reader the
 *    content was hidden while it was on screen. The count IS the inline evidence; the
 *    list is what expanding is for.
 */

function Block({
  id,
  count,
  bounded,
  expanded,
  onToggle,
  children,
  emptySentence,
}: {
  id: EvidenceBlock
  count: number
  bounded: boolean
  expanded: boolean
  onToggle: () => void
  children: ReactNode
  emptySentence: string
}) {
  const { t } = useTranslation('sessions')
  return (
    <section className="border-t border-border first:border-t-0">
      <h4>
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={expanded}
          aria-controls={`evidence-${id}`}
          data-testid={`evidence-toggle-${id}`}
          className="flex w-full items-center gap-2 px-1 py-2 text-left outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset"
        >
          <ChevronDown
            className={cn(
              'size-3.5 shrink-0 text-muted-foreground transition-transform',
              expanded ? '' : '-rotate-90',
            )}
            aria-hidden
          />
          <span className="flex-1 text-caption font-medium text-foreground">
            {t(`evidence.${id}.title`)}
          </span>
          <span className="text-caption text-muted-foreground tabular-nums">
            {/* A FLOOR IS NOT A TOTAL. With the page full, the honest figure is "at
                least N" — and the count that lies is the one that looks exact. */}
            {bounded ? t('evidence.atLeast', { count }) : count}
          </span>
        </button>
      </h4>
      {/* RENDERED ALWAYS, HIDDEN WHEN COLLAPSED. `aria-controls` above names this
          element, and an `aria-controls` pointing at an id that is not in the document
          is an invalid attribute value — a gate finding, and a screen reader following
          a reference to nothing. `hidden` is the state; the reference stays real. */}
      <div id={`evidence-${id}`} hidden={!expanded} className="px-1 pb-2">
        {count === 0 ? (
          <p className="text-caption text-muted-foreground">{emptySentence}</p>
        ) : (
          children
        )}
      </div>
    </section>
  )
}

export function SessionEvidence({
  liveRef,
  sessionRef,
  expanded,
  onExpand,
}: {
  liveRef?: string
  sessionRef?: string
  /** Which block is open — it lives in the URL, so a shared link opens the same one. */
  expanded: EvidenceBlock
  onExpand: (block: EvidenceBlock) => void
}) {
  const { t } = useTranslation('sessions')
  const { activeTenant, can } = useAuth()
  const canRead = can('sessions:live:read')
  const ref = liveRef || sessionRef || ''

  const query = useQuery({
    // Its own key: this is ONE bounded read, and the card's timeline is an infinite
    // walk over the same endpoint. Two shapes cannot share a cache entry.
    queryKey: [
      ...(liveRef
        ? sessionsKeys.timelineById(activeTenant, liveRef, {
            limit: EVIDENCE_PAGE,
          })
        : sessionsKeys.timeline(activeTenant, ref, { limit: EVIDENCE_PAGE })),
      'evidence',
    ],
    queryFn: () =>
      liveRef
        ? sessionsApi.timelineById(liveRef, { limit: EVIDENCE_PAGE })
        : sessionsApi.timeline(ref, { limit: EVIDENCE_PAGE }),
    enabled: !!ref && canRead,
    retry: false,
  })

  const split = useMemo(
    () => splitEvidence(query.data?.items ?? [], query.data?.has_more ?? false),
    [query.data],
  )

  if (!canRead)
    return (
      <ForbiddenState
        title={t('evidence.forbiddenTitle')}
        description={t('evidence.forbiddenDescription')}
      />
    )

  if (query.isLoading)
    return (
      <div className="flex flex-col gap-2 py-2" data-testid="evidence-loading">
        {Array.from({ length: 3 }, (_, i) => (
          <Skeleton key={i} className="h-8 w-full" />
        ))}
      </div>
    )

  if (query.isError)
    return (
      // NEVER an empty list in place of a read that failed: "nothing happened" and
      // "nobody could look" are different answers and must not share a screen.
      <ErrorState
        title={t('evidence.errorTitle')}
        description={t('evidence.errorDescription')}
        retry={() => void query.refetch()}
      />
    )

  const rows: Record<EvidenceBlock, ReactNode> = {
    checks: (
      <ul>
        {split.checks.map((e, i) => (
          <EntryRow key={`${e.at}:${i}`} entry={e} />
        ))}
      </ul>
    ),
    activity: (
      <ul>
        {split.activity.map((e, i) => (
          <EntryRow key={`${e.at}:${i}`} entry={e} />
        ))}
      </ul>
    ),
    resources: (
      <ul className="flex flex-col gap-1">
        {split.resources.map((r) => (
          <li key={r.ref} className="flex items-center gap-2">
            <RefChip value={r.ref} absent={t('context.none')} />
            <span className="text-caption text-muted-foreground tabular-nums">
              {t('evidence.touches', { count: r.count })}
            </span>
          </li>
        ))}
      </ul>
    ),
  }

  const counts: Record<EvidenceBlock, number> = {
    checks: split.checks.length,
    activity: split.activity.length,
    resources: split.resources.length,
  }

  return (
    <div data-testid="session-evidence">
      {EVIDENCE_BLOCKS.map((id) => (
        <Block
          key={id}
          id={id}
          count={counts[id]}
          bounded={split.bounded}
          expanded={expanded === id}
          onToggle={() => onExpand(id)}
          emptySentence={t(`evidence.${id}.empty`)}
        >
          {rows[id]}
        </Block>
      ))}
    </div>
  )
}
