// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Check, Copy } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useCopy, type CopyState } from './use-copy'

/** The icon button of a copy: a check once the text is on the clipboard. */
export function CopyIcon({
  state,
  label,
  onCopy,
  className,
}: {
  state: CopyState
  label: string
  onCopy: () => void
  className?: string
}) {
  return (
    <button
      type="button"
      onClick={onCopy}
      aria-label={label}
      className={cn(
        'inline-flex size-7 shrink-0 items-center justify-center rounded-ctl text-text-3',
        'transition-colors duration-100 ease-out hover:bg-hover hover:text-text',
        'outline-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus',
        className,
      )}
    >
      {state === 'copied' ? (
        <Check className="size-3.5 text-ok" aria-hidden="true" />
      ) : (
        <Copy className="size-3.5" aria-hidden="true" />
      )}
    </button>
  )
}

/** One polite region for the result. A failure can stay visible (`showFailure`), so the
 * person knows to select the text by hand. `live` makes it a plain polite live region
 * rather than a `status` role: a thread holds one per message. */
export function CopyStatus({
  state,
  copied,
  failed,
  showFailure = false,
  live = false,
}: {
  state: CopyState
  copied: string
  failed: string
  showFailure?: boolean
  live?: boolean
}) {
  return (
    <span
      {...(live ? { 'aria-live': 'polite' as const } : { role: 'status' })}
      className={
        state === 'failed' && showFailure ? 'text-caption text-bad' : 'sr-only'
      }
    >
      {state === 'copied' ? copied : state === 'failed' ? failed : ''}
    </span>
  )
}

/** A copy action beside what it copies. `text` is read when it is pressed. Its result is
 * announced in a polite region that is always there (a region added already holding its
 * words is often not read), and a failure is shown, so the person selects by hand. */
export function CopyButton({
  text,
  label,
  copiedLabel,
  failedLabel,
  className,
}: {
  text: () => string
  label: string
  copiedLabel: string
  failedLabel: string
  className?: string
}) {
  const { state, copy } = useCopy()
  return (
    <>
      <CopyIcon
        state={state}
        label={label}
        onCopy={() => void copy(text())}
        className={className}
      />
      <CopyStatus
        state={state}
        copied={copiedLabel}
        failed={failedLabel}
        showFailure
        live
      />
    </>
  )
}
