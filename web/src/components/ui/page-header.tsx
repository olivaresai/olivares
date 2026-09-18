// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { LucideIcon } from 'lucide-react'
import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'
import { PagePrimaryActionSlot, PageSecondaryActionsSlot } from './page-actions'

/**
 * PageHeader — the ONE title block of a management view: an optional icon chip, the
 * page `<h1>`, a one-line description, a row of secondary controls and, last and
 * rightmost, THE PRIMARY ACTION.
 *
 * ⛔ WHY `primaryAction` IS ITS OWN SLOT AND NOT "the first child of `actions`".
 *    A review of the console recorded the defect this closes: the front door offered
 *    "six read-only cards, no action anywhere on the page". A single `actions` bag cannot be measured — a header with a range
 *    picker in it looks, to any test and to any census, exactly like a header with a
 *    "New policy" button in it. A named slot can: `page-header.test.tsx` asserts that
 *    the primary action is the LAST control in the header, and a screen that offers
 *    nothing says so by leaving the slot empty rather than by hiding it among filters.
 *
 *    A TABBED screen fills this slot from inside its active tab, through
 *    `PagePrimaryAction` — see page-actions.tsx for why that is a slot and not a prop.
 *
 * ⛔ AND WHY THE HEADING NO LONGER SPELLS ITS OWN TYPE. It used to be
 *    `font-display text-xl font-semibold tracking-tight` written by hand — four
 *    decisions, repeated in six places with three different sizes (measured 2026-09-17:
 *    `text-xl` here and in IntelPage, `text-lg` in login/setup/accept-invite/tenant-gate,
 *    `text-2xl` in settings and the status page). `text-display` is one token that
 *    carries size, leading, tracking and weight together (web/tokens/primitives.tokens.json,
 *    the `type` group), so the ladder moves in one place or not at all.
 */
export interface PageHeaderProps {
  title: ReactNode
  description?: ReactNode
  icon?: LucideIcon
  /**
   * Secondary controls: filters, a range picker, an export, a refresh. Never the
   * verb the page exists for — that is `primaryAction`.
   */
  actions?: ReactNode
  /**
   * THE verb of this screen, rendered last so it is the rightmost control in the
   * header and the first one a reader's eye lands on coming off the title.
   * Omit it only when the screen genuinely offers nothing to do.
   */
  primaryAction?: ReactNode
  /** Notices rendered under the header (honesty markers, caveats, partial reads). */
  notices?: ReactNode
  className?: string
  /**
   * The element the title row is rendered as. `div` by default, because that is what
   * this primitive has always emitted and 32 views depend on it; `IntelPage` passes
   * `header`, which is what ITS 26 views have always emitted. Neither is a landmark
   * inside `<main>`, so the a11y inventory is unchanged either way — the prop exists
   * so consolidating the two headers changed no DOM, and that is checkable.
   */
  as?: 'header' | 'div'
}

export function PageHeader({
  title,
  description,
  icon: Icon,
  actions,
  primaryAction,
  notices,
  className,
  as: Tag = 'div',
}: PageHeaderProps) {
  // The control row always renders: a TABBED screen declares its verb from inside the
  // active tab (`PagePrimaryAction`), which mounts after this header and cannot be seen
  // from here. An empty row is a zero-width flex item and moves nothing.
  return (
    <div className={cn('flex flex-col gap-3', className)}>
      <Tag className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="flex items-start gap-3">
          {Icon && (
            <span className="mt-0.5 flex size-9 shrink-0 items-center justify-center rounded-lg bg-accent-soft text-accent-soft-foreground [&_svg]:size-5">
              <Icon />
            </span>
          )}
          <div className="min-w-0">
            <h1 className="font-display text-display text-foreground">
              {title}
            </h1>
            {description != null && (
              <p className="mt-1 max-w-2xl text-body text-muted-foreground">
                {description}
              </p>
            )}
          </div>
        </div>
        <div className="flex shrink-0 flex-wrap items-center gap-2">
          {actions}
          <PageSecondaryActionsSlot />
          {primaryAction}
          <PagePrimaryActionSlot />
        </div>
      </Tag>
      {notices != null && <div className="flex flex-col gap-2">{notices}</div>}
    </div>
  )
}
