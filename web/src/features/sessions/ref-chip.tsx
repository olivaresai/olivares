// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A REFERENCE YOU CAN TAKE WITH YOU.
//
// Every id on the context pane is something the operator is about to paste somewhere
// else — into `olivares provider get`, into a ticket, into a message to whoever owns
// the workspace. Selecting a truncated id by hand is where a transcription error comes
// from, so the whole chip is one button that copies the WHOLE value.
//
// ⛔ IT IS NOT `HashChip`, and the difference is not cosmetic. That chip truncates in
//    the MIDDLE (`truncateHash`) because a fingerprint has no readable part; a
//    reference does — `ws-production`, `ppf_anthropic-team` — and cutting its middle
//    out destroys the only part an operator reads.
//
// ⛔ AND WHAT IT PAINTS IS THE DISTINGUISHING TAIL (`refTail`), NOT THE WHOLE VALUE. The
//    chip lives in a 280 px pane, so a 36-character uuid was already cut by the browser
//    — at its HEAD, which under uuid v7 is the minting timestamp and identical on every
//    row of one session's inspector. The tail tells them apart. The whole value is on
//    `title`, in the accessible name, and on the clipboard: the chip still copies it
//    entire, which is the reason it is a button and not a line of text.
//
// ⛔ AND AN ABSENT REFERENCE IS NOT AN EMPTY ONE. With nothing to copy this renders the
//    caller's own sentence for absence, never a button that copies "".
import { Check, Copy } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { refTail } from '@/features/shared/entity-names'
import { cn } from '@/lib/utils'
import './i18n'

export function RefChip({
  value,
  absent,
  className,
}: {
  value: string | null | undefined
  /** What to say when there is no reference — "none", "not declared", … */
  absent: ReactNode
  className?: string
}) {
  const { t } = useTranslation('sessions')
  const [copied, setCopied] = useState(false)

  if (!value)
    return <span className="text-caption text-muted-foreground">{absent}</span>

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1200)
    } catch {
      // Insecure context or a denied permission: the value is still on screen and
      // selectable. An operator does not need a toast about a clipboard.
    }
  }

  return (
    <button
      type="button"
      onClick={copy}
      data-testid="ref-chip"
      // The whole value, for a reader whose chip paints only its distinguishing tail —
      // and it is the value the click copies, which has not changed.
      title={value}
      aria-label={t('context.copyRef', { value })}
      className={cn(
        'group inline-flex max-w-full items-center gap-1.5 rounded-sm border border-border bg-muted px-1.5 py-0.5',
        'font-mono text-caption text-muted-foreground',
        'outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring',
        className,
      )}
    >
      <span className="truncate">{refTail(value)}</span>
      {copied ? (
        <Check className="size-3 shrink-0 text-success" aria-hidden />
      ) : (
        <Copy
          className="size-3 shrink-0 opacity-40 group-hover:opacity-100"
          aria-hidden
        />
      )}
      <span className="sr-only">
        {copied ? t('context.copied') : t('context.copy')}
      </span>
    </button>
  )
}
