// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// UnifiedTimeline — two-lane chronological view of agent activity (left) and
// governance evidence frames (right). Both lanes are merged by timestamp so
// events and frames interleave naturally. Clicking an entry selects it and
// opens the EventDetailPanel below.
import {
  AlertTriangle,
  Coins,
  Disc3,
  Plug,
  Wrench,
  type LucideIcon,
} from 'lucide-react'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge, type BadgeVariant } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { CaveatNotice } from '@/features/_intel'
import { RelTimeLabel } from '@/features/shared'
import { cn } from '@/lib/utils'
import type { KeyedFrame, KeyedTimelineEntry, TimelineEntry } from './types'
import './i18n'

export interface UnifiedTimelineProps {
  /** Activity timeline entries (left lane). */
  timeline: KeyedTimelineEntry[]
  /** Whether the engine actually wired and completed the activity resolver. */
  timelineAvailable: boolean
  /** Evidence frames (right lane). */
  frames: KeyedFrame[]
  /** Currently selected stable event identifier. */
  selectedEventId: string | null
  /** Callback when a timeline entry is clicked. */
  onSelectTimeline: (key: string) => void
  /** Callback when a frame is clicked. */
  onSelectFrame: (key: string) => void
  /** Optional tool filter — only show entries matching this tool_ref. */
  toolFilter?: string | null
}

/** Icon + tone per timeline kind (mirrors an internal design note (not shipped)).*/
const KIND_META: Record<string, { icon: LucideIcon; tone: string }> = {
  tool: { icon: Wrench, tone: 'text-muted-foreground' },
  mcp: { icon: Plug, tone: 'text-info' },
  cost: { icon: Coins, tone: 'text-accent-text' },
  finding: { icon: AlertTriangle, tone: 'text-warning' },
}

/** Outcome to badge variant for recording evidence frames. */
const OUTCOME_VARIANT: Record<string, BadgeVariant> = {
  allowed: 'success',
  denied: 'warning',
  rejected: 'warning',
  error: 'danger',
}

/** A merged row — either a timeline entry or a frame, unified by timestamp. */
type MergedRow =
  | {
      lane: 'activity'
      key: string
      at: string
      entry: TimelineEntry
    }
  | {
      lane: 'evidence'
      key: string
      at: string
      frame: KeyedFrame['frame']
    }

export function UnifiedTimeline({
  timeline,
  timelineAvailable,
  frames,
  selectedEventId,
  onSelectTimeline,
  onSelectFrame,
  toolFilter,
}: UnifiedTimelineProps) {
  const { t } = useTranslation('session-viewer')

  // Apply tool filter to the activity lane when active.
  const filteredTimeline = useMemo(
    () =>
      toolFilter
        ? timeline.filter(({ entry }) => entry.tool_ref === toolFilter)
        : timeline,
    [timeline, toolFilter],
  )

  // Merge both lanes by timestamp for chronological interleaving.
  const merged = useMemo<MergedRow[]>(() => {
    const rows: MergedRow[] = []
    for (const item of filteredTimeline) {
      rows.push({
        lane: 'activity',
        key: item.key,
        at: item.entry.at,
        entry: item.entry,
      })
    }
    for (const item of frames) {
      rows.push({
        lane: 'evidence',
        key: item.key,
        at: item.frame.at,
        frame: item.frame,
      })
    }
    rows.sort((a, b) => a.at.localeCompare(b.at))
    return rows
  }, [filteredTimeline, frames])

  const unavailableNotice = !timelineAvailable ? (
    <CaveatNotice tone="warning">
      {t('timeline.activityUnavailable')}
    </CaveatNotice>
  ) : null

  if (merged.length === 0) {
    return (
      <div className="flex flex-col gap-3">
        {unavailableNotice}
        <EmptyState
          icon={<Disc3 />}
          title={t('timeline.empty')}
          description=""
        />
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-0">
      {unavailableNotice}
      {/* Lane headers */}
      <div className="my-2 grid grid-cols-2 gap-4 border-b border-border pb-1">
        <span className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {t('lanes.activity')}
        </span>
        <span className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {t('lanes.evidence')}
        </span>
      </div>

      {/* Merged chronological rows */}
      <ol className="flex flex-col" data-testid="unified-timeline">
        {merged.map((row) => {
          const eventId = `${row.lane}:${row.key}`
          if (row.lane === 'activity') {
            const isSelected = selectedEventId === eventId
            return (
              <li
                key={eventId}
                data-event-key={eventId}
                className="grid grid-cols-2 gap-4"
              >
                <ActivityRow
                  entry={row.entry}
                  selected={isSelected}
                  onClick={() => onSelectTimeline(row.key)}
                />
                <div /> {/* empty right lane */}
              </li>
            )
          }
          const isSelected = selectedEventId === eventId
          return (
            <li
              key={eventId}
              data-event-key={eventId}
              className="grid grid-cols-2 gap-4"
            >
              <div /> {/* empty left lane */}
              <EvidenceRow
                frame={row.frame}
                selected={isSelected}
                onClick={() => onSelectFrame(row.key)}
              />
            </li>
          )
        })}
      </ol>
    </div>
  )
}

function ActivityRow({
  entry,
  selected,
  onClick,
}: {
  entry: TimelineEntry
  selected: boolean
  onClick: () => void
}) {
  const { t } = useTranslation('session-viewer')
  const meta = KIND_META[entry.kind] ?? KIND_META.tool
  const Icon = meta.icon
  const label = entry.title || entry.tool_ref || entry.resource_ref || '—'

  return (
    <button
      type="button"
      className={cn(
        'flex items-start gap-2 rounded-md px-2 py-1.5 text-left transition-colors',
        'hover:bg-muted/50',
        selected && 'bg-accent-soft/60 ring-1 ring-accent-strong',
      )}
      aria-pressed={selected}
      onClick={onClick}
    >
      {/* Selection carries THREE independent signals, so it never rests on colour
          alone (SC 1.4.1) nor on a sub-3:1 colour (SC 1.4.11): this rail is a shape
          that is either present or absent, aria-pressed above says the same thing to
          AT, and the ring uses --accent-strong (>=3:1, gated by at-run.ts). */}
      <span
        aria-hidden
        className={cn(
          'mt-0.5 h-5 w-1 shrink-0 rounded-full',
          selected ? 'bg-accent-strong' : 'bg-transparent',
        )}
      />
      <span
        className={cn(
          'mt-0.5 flex size-5 shrink-0 items-center justify-center rounded bg-muted [&_svg]:size-3',
          meta.tone,
        )}
        aria-hidden
      >
        <Icon />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-1">
          <Badge variant="outline" className="font-mono text-[10px]">
            {t(`timeline.${entry.kind}`, { defaultValue: entry.kind })}
          </Badge>
          <span
            className="truncate text-xs font-medium text-foreground"
            title={label}
          >
            {label}
          </span>
        </div>
        <RelTimeLabel
          ts={entry.at}
          className="text-[10px] text-muted-foreground"
        />
      </div>
    </button>
  )
}

function EvidenceRow({
  frame,
  selected,
  onClick,
}: {
  frame: KeyedFrame['frame']
  selected: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      className={cn(
        'flex items-start gap-2 rounded-md px-2 py-1.5 text-left transition-colors',
        'hover:bg-muted/50',
        selected && 'bg-accent-soft/60 ring-1 ring-accent-strong',
      )}
      aria-pressed={selected}
      onClick={onClick}
    >
      {/* Same three signals as ActivityRow: shape (rail) + aria-pressed + a >=3:1 ring. */}
      <span
        aria-hidden
        className={cn(
          'mt-0.5 h-5 w-1 shrink-0 rounded-full',
          selected ? 'bg-accent-strong' : 'bg-transparent',
        )}
      />
      <span
        className="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded bg-muted text-muted-foreground [&_svg]:size-3"
        aria-hidden
      >
        <Disc3 />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-1">
          <span className="font-mono text-[10px] text-muted-foreground">
            #{frame.idx}
          </span>
          <span className="truncate text-xs font-medium text-foreground">
            {frame.method} {frame.namespace}
            {frame.pattern}
          </span>
          <Badge
            variant={OUTCOME_VARIANT[frame.outcome] ?? 'neutral'}
            className="text-[10px]"
          >
            {frame.outcome}
          </Badge>
        </div>
        <RelTimeLabel
          ts={frame.at}
          className="text-[10px] text-muted-foreground"
        />
      </div>
    </button>
  )
}
