// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Log stream display — the core log rendering component. Renders log entries as
// dense monospace rows with level coloring. Auto-scrolls to the bottom when new
// entries arrive, unless the user has scrolled up to inspect history.
import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { Logs } from 'lucide-react'
import { EmptyState } from '@/components/ui/empty-state'
import { cn } from '@/lib/utils'
import type { LogEntry, LogLevel } from './types'

const LEVEL_TEXT: Record<LogLevel, string> = {
  DEBUG: 'text-muted-foreground',
  INFO: 'text-foreground',
  WARN: 'text-warning',
  ERROR: 'text-danger',
}

const LEVEL_BADGE: Record<LogLevel, string> = {
  DEBUG: 'text-muted-foreground',
  INFO: 'text-info',
  WARN: 'text-warning',
  ERROR: 'text-danger font-semibold',
}

/**
 * Render one structured attribute value as the text a log row shows.
 *
 * The engine already renders non-scalar attribute values to text and redacts
 * them at the broker (core/api/log_redact.go), so what arrives here is a string
 * for anything that could have carried an upstream secret. Objects and arrays
 * are still possible for grouped attributes, so they are stringified rather than
 * dropped — a row that silently omits part of the diagnosis is the defect this
 * whole surface exists to avoid.
 */
function fmtAttrValue(value: unknown): string {
  if (typeof value === 'string') return value
  if (value === null || value === undefined) return String(value)
  if (typeof value === 'object') {
    try {
      return JSON.stringify(value)
    } catch {
      return String(value)
    }
  }
  return String(value)
}

/** Format an ISO timestamp to a compact HH:mm:ss.SSS form for log display. */
function fmtTime(iso: string): string {
  try {
    const d = new Date(iso)
    const h = String(d.getHours()).padStart(2, '0')
    const m = String(d.getMinutes()).padStart(2, '0')
    const s = String(d.getSeconds()).padStart(2, '0')
    const ms = String(d.getMilliseconds()).padStart(3, '0')
    return `${h}:${m}:${s}.${ms}`
  } catch {
    return iso.slice(11, 23) || iso
  }
}

export interface LogStreamProps {
  entries: LogEntry[]
  autoScroll: boolean
}

export function LogStream({ entries, autoScroll }: LogStreamProps) {
  const { t } = useTranslation('logs')
  const containerRef = useRef<HTMLDivElement>(null)
  const bottomRef = useRef<HTMLDivElement>(null)

  // Auto-scroll to bottom when entries change and autoScroll is enabled.
  useEffect(() => {
    if (autoScroll && bottomRef.current) {
      bottomRef.current.scrollIntoView({ block: 'end' })
    }
  }, [entries.length, autoScroll])

  if (entries.length === 0) {
    // EmptyState, not a bare div: the shared primitive carries role="status"
    // so a resolved-but-empty stream is announced instead of silent (4.1.3).
    return (
      <EmptyState icon={<Logs />} title={t('stream.empty')} className="h-64" />
    )
  }

  return (
    <div
      ref={containerRef}
      className="max-h-[calc(100vh-280px)] min-h-64 overflow-y-auto bg-muted/50 rounded-lg p-2"
      role="log"
      aria-live="polite"
      aria-label={t('stream.ariaLabel')}
    >
      {entries.map((entry, i) => (
        <LogRow key={`${entry.timestamp}-${i}`} entry={entry} />
      ))}
      <div ref={bottomRef} />
    </div>
  )
}

function LogRow({ entry }: { entry: LogEntry }) {
  const { t } = useTranslation('logs')
  // Structured attributes ARE the diagnosis. Until this row painted only
  // timestamp/level/module/message, so the `err` an engine module logs — the one
  // thing an operator acts on — never reached the screen at all: it crossed the
  // wire, sat in the browser's network tab, and was rendered nowhere. The engine
  // now redacts every attribute at the broker before it is published, so showing
  // them is safe and NOT showing them is the remaining half of the defect.
  const attrs = Object.entries(entry.attrs ?? {})
  return (
    <div
      className={cn(
        'flex flex-wrap gap-2 px-1.5 py-px font-mono text-xs leading-5 hover:bg-muted/80',
        LEVEL_TEXT[entry.level],
      )}
    >
      {/* Timestamp */}
      <span className="shrink-0 text-muted-foreground tabular-nums">
        {fmtTime(entry.timestamp)}
      </span>

      {/* Level badge */}
      <span
        className={cn(
          'w-[3.5ch] shrink-0 text-center tabular-nums',
          LEVEL_BADGE[entry.level],
        )}
      >
        {entry.level === 'DEBUG'
          ? t('levelAbbrev.DEBUG')
          : entry.level === 'INFO'
            ? t('levelAbbrev.INFO')
            : entry.level === 'WARN'
              ? t('levelAbbrev.WARN')
              : t('levelAbbrev.ERROR')}
      </span>

      {/* Module */}
      {entry.module ? (
        <span className="shrink-0 text-accent-text">[{entry.module}]</span>
      ) : null}

      {/* Message */}
      <span className="min-w-0 break-all">{entry.message}</span>

      {/* Structured attributes — already redacted by the engine. */}
      {attrs.length > 0 ? (
        <span
          className="min-w-0 break-all text-muted-foreground"
          aria-label={t('stream.attrsAriaLabel')}
        >
          {attrs.map(([key, value]) => (
            <span key={key} className="mr-2">
              <span className="text-accent-text">{key}</span>
              {'='}
              {fmtAttrValue(value)}
            </span>
          ))}
        </span>
      ) : null}
    </div>
  )
}
