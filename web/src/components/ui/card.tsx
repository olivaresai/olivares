// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ComponentProps, ElementType } from 'react'
import { cn } from '@/lib/utils'

/**
 * Card — the panel primitive. A `surface` plate one step above the page with the card
 * shadow and no outline (the shadow carries its own contact edge in both themes). Its
 * border paints nothing, so a caller's warm edge (`border-warning/50`) still shows. The
 * header is parted from the content by space alone; the footer keeps its hairline. It
 * names itself `data-slot="card"`, so what sits inside (a table) can lie flat. Compose
 * Header / Title / Description / Content / Footer; the header lays out a left title block
 * and right-aligned actions via `justify-between`, so dropping a Button as the last header
 * child just works.
 */
export function Card({ className, ...props }: ComponentProps<'div'>) {
  return (
    <div
      data-slot="card"
      className={cn(
        'rounded-card border border-transparent bg-surface shadow-[var(--shadow-card)]',
        className,
      )}
      {...props}
    />
  )
}

export function CardHeader({ className, ...props }: ComponentProps<'div'>) {
  return (
    <div
      data-slot="card-header"
      className={cn('flex items-start justify-between gap-3 p-4', className)}
      {...props}
    />
  )
}

/**
 * CardTitle — a section heading. Renders an `<h2>` by DEFAULT: a card title
 * sits directly under the page `<h1>` (PageHeader / IntelPage), so h2 keeps the
 * heading outline gap-free (h1 → h2) instead of the old h3 that skipped a level.
 * Pass `as` to override for genuinely nested sections (e.g. `as="h3"` for a card
 * inside an already-h2 section, or `as="p"` for a non-heading label card).
 */
export function CardTitle({
  as: Tag = 'h2',
  className,
  ...props
}: { as?: ElementType } & ComponentProps<'h2'>) {
  return <Tag className={cn('text-heading', className)} {...props} />
}

export function CardDescription({ className, ...props }: ComponentProps<'p'>) {
  return (
    <p
      className={cn('text-body text-muted-foreground', className)}
      {...props}
    />
  )
}

/** Under a header the content starts at the header's padding rather than adding its own,
 *  so a header alone, or one over a flush table, keeps the space below it. */
export function CardContent({ className, ...props }: ComponentProps<'div'>) {
  return (
    <div
      className={cn('p-4 [[data-slot=card-header]+&]:pt-0', className)}
      {...props}
    />
  )
}

export function CardFooter({ className, ...props }: ComponentProps<'div'>) {
  return (
    <div
      className={cn(
        'flex items-center gap-2 p-4 border-t border-border',
        className,
      )}
      {...props}
    />
  )
}
