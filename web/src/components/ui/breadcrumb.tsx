// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Slot } from '@radix-ui/react-slot'
import { ChevronRight } from 'lucide-react'
import type { ComponentProps } from 'react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'

/**
 * Breadcrumb — the trail-of-context primitive for a control-plane page header.
 * A semantic <nav>/<ol> structure: muted links collapse the path, the final
 * BreadcrumbPage marks the current location with aria-current="page". Minimal,
 * monospace-friendly, accessible — compose product labels at the call site.
 *
 * ONE LINE, ALWAYS. Measured on the rendered console (console-ui-current-baseline,
 * 2026-09-06): the list was `flex-wrap` inside the fixed 48 px topbar, so at 1024 px the
 * trail "Overview › Control console" broke onto two lines and at 390 px it painted over
 * the page header below the bar. A trail that wraps in a fixed-height bar is not a
 * layout choice, it is an overlap. The list is therefore `flex-nowrap` with `min-w-0`
 * items, and the page crumb truncates with an ellipsis; the FULL text stays the
 * accessible name (CSS truncation never changes the accessibility tree) and travels in
 * `title` for pointer users. Callers decide which crumb gives way first with the usual
 * flex-shrink weights on the items.
 */
export function Breadcrumb({ ...props }: ComponentProps<'nav'>) {
  const { t } = useTranslation('common')
  // props spread last so a caller can still override the landmark label.
  return <nav aria-label={t('a11y.breadcrumb')} {...props} />
}

export function BreadcrumbList({ className, ...props }: ComponentProps<'ol'>) {
  return (
    <ol
      className={cn(
        'flex min-w-0 flex-nowrap items-center gap-1.5 text-body text-muted-foreground',
        className,
      )}
      {...props}
    />
  )
}

export function BreadcrumbItem({ className, ...props }: ComponentProps<'li'>) {
  return (
    <li
      className={cn('inline-flex min-w-0 items-center gap-1.5', className)}
      {...props}
    />
  )
}

export interface BreadcrumbLinkProps extends ComponentProps<'a'> {
  /** Render as the child element (Radix Slot) — e.g. a router Link. */
  asChild?: boolean
}

export function BreadcrumbLink({
  className,
  asChild = false,
  ...props
}: BreadcrumbLinkProps) {
  const Comp = asChild ? Slot : 'a'
  return (
    <Comp
      className={cn(
        'min-w-0 truncate rounded-sm outline-none transition-colors duration-100 ease-out hover:text-foreground',
        'focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
        className,
      )}
      {...props}
    />
  )
}

export function BreadcrumbSeparator({
  className,
  children,
  ...props
}: ComponentProps<'li'>) {
  return (
    <li
      role="presentation"
      aria-hidden="true"
      className={cn('inline-flex shrink-0', className)}
      {...props}
    >
      {children ?? <ChevronRight className="size-3.5 text-muted-foreground" />}
    </li>
  )
}

export function BreadcrumbPage({
  className,
  children,
  title,
  ...props
}: ComponentProps<'span'>) {
  // The visible text may be cut with an ellipsis; the full label is still the
  // accessible name (it is the text content) and is offered as a tooltip. A caller
  // that passes its own `title` wins.
  const fullTitle =
    title ?? (typeof children === 'string' ? children : undefined)
  return (
    <span
      role="link"
      aria-disabled="true"
      aria-current="page"
      title={fullTitle}
      className={cn('min-w-0 truncate font-medium text-foreground', className)}
      {...props}
    >
      {children}
    </span>
  )
}
