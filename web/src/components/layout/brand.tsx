// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { cn } from '@/lib/utils'

import {
  OLIVARES_WORDMARK_PATH,
  OLIVARES_WORDMARK_VIEW_BOX,
} from './wordmark-path'

/**
 * BrandMark — the Olivares "Ledger O": an O whose right side is a smooth arc and
 * whose left side is built from stacked audit-ledger rows, with ONE orange row =
 * the flagged WRITE (the access-graph drift that is the product's reason to exist).
 * This is the final brand glyph (brandv4, 2026-06-10), not the prototype ring.
 * The ink strokes take the current text color; the single flagged row is the brand
 * accent. Geometry is the lockup's, optically centered for a square icon frame.
 */
export function BrandMark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="3.5 2 28 28"
      className={cn('size-5', className)}
      aria-hidden="true"
      fill="none"
      strokeLinecap="round"
    >
      {/* the O's right side — a clean arc from 12 o'clock to 6 o'clock */}
      <path
        d="M16 5.5A10.5 10.5 0 0 1 16 26.5"
        stroke="currentColor"
        strokeWidth="3"
      />
      {/* the O's left side — stacked audit-ledger rows */}
      <path d="M8.4 10.5H13.2" stroke="currentColor" strokeWidth="2.6" />
      {/* the one flagged WRITE — the single orange accent */}
      <path d="M8.4 14.5H15.4" className="stroke-accent" strokeWidth="3.1" />
      <path d="M8.4 18.5H15" stroke="currentColor" strokeWidth="2.6" />
      <path d="M8.4 22.5H13.4" stroke="currentColor" strokeWidth="2.6" />
    </svg>
  )
}

/**
 * Wordmark — the brand glyph + outlined Geist SemiBold wordmark per the brand
 * manual. Lockups are outlined: no live text, no font dependency.
 */
export function Wordmark({
  className,
  compact = false,
}: {
  className?: string
  compact?: boolean
}) {
  return (
    <span className={cn('flex items-center gap-2 text-foreground', className)}>
      <BrandMark />
      {!compact && (
        <svg
          viewBox={OLIVARES_WORDMARK_VIEW_BOX}
          className="h-3.5 w-auto shrink-0"
          role="img"
          aria-label="Olivares AI"
          fill="currentColor"
          focusable="false"
        >
          <path d={OLIVARES_WORDMARK_PATH} />
        </svg>
      )}
    </span>
  )
}
