// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { HTMLAttributes } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import { StatusChip, StatusDot, type StatusTone } from './status-chip'

/**
 * StatusGlyph — the localized work-state wrapper around the shared dot chip.
 *
 * ⛔ IT ALWAYS RENDERS ITS WORD. Beside the shape by default; with `showLabel={false}` the
 *    shape is an image whose accessible name is the word. Color never carries a status
 *    alone. A blank word, or a status the component has no word for, throws at render.
 *
 * The working dot is a solid green dot; the word still says "Working" (C2-DESIGN §3).
 */
export type Status =
  'working' | 'needs-you' | 'done' | 'failed' | 'paused' | 'idle'

const WORD_KEY: Record<Status, string> = {
  working: 'ui.status.working',
  'needs-you': 'ui.status.needsYou',
  done: 'ui.status.done',
  failed: 'ui.status.failed',
  paused: 'ui.status.paused',
  idle: 'ui.status.idle',
}

/** Compatibility export for loading/checking callers; it uses the shared working dot. */
export function RunningRing({ className }: { className?: string }) {
  return <StatusDot tone="ok" className={className} />
}

const TONE: Record<Status, StatusTone> = {
  working: 'ok',
  'needs-you': 'warn',
  done: 'ok',
  failed: 'bad',
  paused: 'neutral',
  idle: 'neutral',
}

export interface StatusGlyphProps extends Omit<
  HTMLAttributes<HTMLSpanElement>,
  'children'
> {
  status: Status
  /** The word. Defaults to the localized word of the status; a blank word throws. */
  label?: string
  /** Show the word beside the shape (default), or carry it only as the accessible name. */
  showLabel?: boolean
  /** A detail after the word, such as the elapsed time: "Working · 6m 12s". */
  detail?: string
}

export function StatusGlyph({
  status,
  label,
  showLabel = true,
  detail,
  className,
  ...props
}: StatusGlyphProps) {
  const { t } = useTranslation('common')
  const key = WORD_KEY[status] as string | undefined
  const word = label ?? (key ? t(key) : undefined)
  if (!word || word.trim() === '') {
    throw new Error(
      `StatusGlyph needs its word: status "${String(status)}" has none.`,
    )
  }
  const name = detail ? `${word} · ${detail}` : word

  if (!showLabel) {
    return (
      <span
        role="img"
        aria-label={name}
        data-slot="status-glyph"
        data-status={status}
        className={cn('inline-flex shrink-0 items-center', className)}
        {...props}
      >
        <StatusDot tone={TONE[status]} />
      </span>
    )
  }

  return (
    <StatusChip
      tone={TONE[status]}
      data-slot="status-glyph"
      data-status={status}
      className={className}
      {...props}
    >
      <span>{word}</span>
      {detail ? ' ' : null}
      {detail ? (
        <span className="text-text-3 tabular-nums">· {detail}</span>
      ) : null}
    </StatusChip>
  )
}
