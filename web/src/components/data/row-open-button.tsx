// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { MouseEvent, ReactNode } from 'react'
import { cn } from '@/lib/utils'

/**
 * THE KEYBOARD PATH INTO A CLICKABLE ROW — a real button in the primary cell.
 *
 * A table row may keep an `onClick` as a mouse convenience. That click is not a
 * keyboard path: `<tr>` is not a control, so Tab never lands on it and Enter
 * never opens it. This button does what the row click does; Tab reaches it and
 * Enter activates it. The row click may stay.
 */
export function RowOpenButton({
  onOpen,
  children,
  className,
  'aria-label': ariaLabel,
}: {
  onOpen: () => void
  children: ReactNode
  className?: string
  'aria-label'?: string
}) {
  function activate(event: MouseEvent<HTMLButtonElement>) {
    event.stopPropagation()
    onOpen()
  }

  return (
    <button
      type="button"
      aria-label={ariaLabel}
      className={cn(
        'min-h-6 min-w-0 max-w-full rounded-sm text-left outline-none',
        'focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
        className,
      )}
      onClick={activate}
    >
      {children}
    </button>
  )
}
