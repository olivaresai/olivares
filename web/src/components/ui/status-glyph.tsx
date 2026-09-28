// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Circle, CircleCheck, CircleX, Pause } from 'lucide-react'
import type { HTMLAttributes, ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'

/**
 * StatusGlyph — the shape and the word of a work state: a ring for running work (the one
 * status in orange, because it is the product alive, not a problem), a yellow diamond for
 * work that needs the operator, a green check, a red cross, a pause bar and an open circle.
 *
 * ⛔ IT ALWAYS RENDERS ITS WORD. Beside the shape by default; with `showLabel={false}` the
 *    shape is an image whose accessible name is the word. Color never carries a status
 *    alone. A blank word, or a status the component has no word for, throws at render.
 *
 * The running ring turns once per 1.2 s and stops under reduced motion (the operating
 * system's preference or the console's Reduce motion setting); the word still says
 * "Working".
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

const SHAPE_CLASS = 'size-3.5 shrink-0'

/**
 * The running ring: orange, one turn per 1.2 s, still under reduced motion. Decoration: the
 * word beside it (or the name of the glyph that holds it) says what it means.
 */
export function RunningRing({ className }: { className?: string }) {
  return (
    <span
      data-shape="ring"
      aria-hidden="true"
      className={cn(
        SHAPE_CLASS,
        'inline-block rounded-full border-2 border-accent-soft border-t-accent border-r-accent',
        'animate-spin [animation-duration:1.2s] motion-reduce:animate-none [[data-motion=reduce]_&]:animate-none',
        className,
      )}
    />
  )
}

function Shape({ status }: { status: Status }): ReactNode {
  switch (status) {
    case 'working':
      return <RunningRing />
    case 'needs-you':
      return (
        <span
          data-shape="diamond"
          aria-hidden="true"
          className="m-0.5 inline-block size-2.5 shrink-0 rotate-45 rounded-[2px] bg-warn"
        />
      )
    case 'done':
      return (
        <CircleCheck
          aria-hidden="true"
          className={cn(SHAPE_CLASS, 'text-ok')}
        />
      )
    case 'failed':
      return (
        <CircleX aria-hidden="true" className={cn(SHAPE_CLASS, 'text-bad')} />
      )
    case 'paused':
      return (
        <Pause aria-hidden="true" className={cn(SHAPE_CLASS, 'text-text-3')} />
      )
    case 'idle':
      return (
        <Circle aria-hidden="true" className={cn(SHAPE_CLASS, 'text-text-3')} />
      )
  }
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
        <Shape status={status} />
      </span>
    )
  }

  return (
    <span
      data-slot="status-glyph"
      data-status={status}
      className={cn(
        'inline-flex min-w-0 items-center gap-1.5 text-caption font-medium whitespace-nowrap text-text',
        className,
      )}
      {...props}
    >
      <Shape status={status} />
      <span>{word}</span>
      {detail ? ' ' : null}
      {detail ? (
        <span className="text-text-3 tabular-nums">· {detail}</span>
      ) : null}
    </span>
  )
}
