// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// EventDetailPanel — expandable detail view for a selected timeline event or
// evidence frame. Shows the full metadata with RedactionToggle on sensitive
// fields (params, resource_ref). Renders below the timeline when an event is
// selected; collapses when cleared.
import { ChevronDown, ChevronUp, X } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { KvList, KvRow } from '@/components/ui/kv'
import { RelTimeLabel } from '@/features/shared'
import { formatLatency } from '@/lib/format'
import type { FrameDTO } from '@/features/recordings/types'
import type { TimelineEntry } from './types'
import { RedactionToggle } from './redaction-toggle'
import './i18n'

export interface EventDetailPanelProps {
  /** The selected timeline entry (activity lane). */
  timelineEntry?: TimelineEntry | null
  /** The selected evidence frame (governance lane). */
  frame?: FrameDTO | null
  /** Callback to dismiss the panel. */
  onClose: () => void
}

/** Kind label badge — maps the timeline kind to a semantic variant. */
const KIND_VARIANT: Record<string, 'neutral' | 'info' | 'accent' | 'warning'> =
  {
    tool: 'neutral',
    mcp: 'info',
    cost: 'accent',
    finding: 'warning',
  }

export function EventDetailPanel({
  timelineEntry,
  frame,
  onClose,
}: EventDetailPanelProps) {
  const { t } = useTranslation('session-viewer')
  const [expanded, setExpanded] = useState(true)

  const hasContent = !!(timelineEntry || frame)
  if (!hasContent) return null

  return (
    <section className="rounded-lg border border-border bg-background">
      {/* Header bar */}
      <div className="flex items-center justify-between border-b border-border px-3 py-2">
        <button
          type="button"
          className="flex items-center gap-1.5 text-sm font-semibold text-foreground"
          onClick={() => setExpanded((v) => !v)}
        >
          {expanded ? (
            <ChevronUp className="size-3.5" />
          ) : (
            <ChevronDown className="size-3.5" />
          )}
          {frame
            ? `#${frame.idx} ${frame.method} ${frame.namespace}${frame.pattern}`
            : (timelineEntry?.title ?? timelineEntry?.tool_ref ?? '—')}
        </button>
        <Button
          variant="ghost"
          size="sm"
          className="size-6 p-0"
          onClick={onClose}
        >
          <span className="sr-only">{t('detail.close')}</span>
          <X className="size-3.5" />
        </Button>
      </div>

      {/* Body */}
      {expanded && (
        <div className="p-3">
          {timelineEntry && <TimelineEntryDetail entry={timelineEntry} />}
          {frame && <FrameDetail frame={frame} />}
        </div>
      )}
    </section>
  )
}

function TimelineEntryDetail({ entry }: { entry: TimelineEntry }) {
  const { t } = useTranslation('session-viewer')
  return (
    <KvList>
      <KvRow label={t('timeline.tool')} mono align="start">
        {entry.kind && (
          <Badge variant={KIND_VARIANT[entry.kind] ?? 'neutral'}>
            {t(`timeline.${entry.kind}`, { defaultValue: entry.kind })}
          </Badge>
        )}
      </KvRow>
      {entry.tool_ref && (
        <KvRow label={t('detail.toolRef')} mono align="start">
          {entry.tool_ref}
        </KvRow>
      )}
      {entry.resource_ref && (
        <KvRow label={t('detail.resourceRef')} align="start">
          <RedactionToggle
            value={entry.resource_ref}
            permission="recording:session:admin"
          />
        </KvRow>
      )}
      {entry.mode && (
        <KvRow label={t('detail.mode')} mono>
          {entry.mode}
        </KvRow>
      )}
      {entry.source && (
        <KvRow label={t('detail.source')} mono>
          {entry.source}
        </KvRow>
      )}
      {entry.title && (
        <KvRow label={t('detail.titleField')} align="start">
          {entry.title}
        </KvRow>
      )}
      <KvRow label={t('detail.at')}>
        <RelTimeLabel ts={entry.at} />
      </KvRow>
    </KvList>
  )
}

function FrameDetail({ frame }: { frame: FrameDTO }) {
  const { t } = useTranslation('session-viewer')
  return (
    <KvList>
      <KvRow label={t('detail.frameIdx')} mono>
        {frame.idx}
      </KvRow>
      <KvRow label={t('detail.namespace')} mono align="start">
        {frame.namespace}
      </KvRow>
      <KvRow label={t('detail.method')} mono>
        {frame.method}
      </KvRow>
      <KvRow label={t('detail.pattern')} mono align="start">
        {frame.pattern}
      </KvRow>
      <KvRow label={t('detail.outcome')}>
        <Badge
          variant={
            frame.outcome === 'allowed'
              ? 'success'
              : frame.outcome === 'error'
                ? 'danger'
                : 'warning'
          }
        >
          {frame.outcome}
        </Badge>
      </KvRow>
      <KvRow label={t('detail.actor')} mono align="start">
        {frame.actor}
      </KvRow>
      {frame.act_as && (
        <KvRow label={t('detail.actAs')} mono align="start">
          {frame.act_as}
        </KvRow>
      )}
      {frame.params && Object.keys(frame.params).length > 0 && (
        <KvRow label={t('detail.params')} align="start">
          <RedactionToggle
            value={JSON.stringify(frame.params)}
            permission="recording:session:admin"
          />
        </KvRow>
      )}
      <KvRow label={t('detail.httpStatus')} mono>
        {frame.http_status}
      </KvRow>
      <KvRow label={t('detail.durMs')} mono>
        {formatLatency(frame.dur_ms)}
      </KvRow>
      <KvRow label={t('detail.hash')} align="start">
        <span className="break-all font-mono text-xs">{frame.hash}</span>
      </KvRow>
      <KvRow label={t('detail.prevHash')} align="start">
        <span className="break-all font-mono text-xs">{frame.prev_hash}</span>
      </KvRow>
      <KvRow label={t('detail.anchorSeq')} mono>
        {frame.anchor_seq ?? '—'}
      </KvRow>
      <KvRow label={t('detail.at')}>
        <RelTimeLabel ts={frame.at} />
      </KvRow>
    </KvList>
  )
}
