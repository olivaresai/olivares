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
        'flex flex-wrap items-center gap-1.5 break-words text-sm text-muted-foreground',
        className,
      )}
      {...props}
    />
  )
}

export function BreadcrumbItem({ className, ...props }: ComponentProps<'li'>) {
  return (
    <li
      className={cn('inline-flex items-center gap-1.5', className)}
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
        'rounded-sm outline-none transition-colors duration-100 ease-out hover:text-foreground',
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
      className={cn('inline-flex', className)}
      {...props}
    >
      {children ?? <ChevronRight className="size-3.5 text-muted-foreground" />}
    </li>
  )
}

export function BreadcrumbPage({
  className,
  ...props
}: ComponentProps<'span'>) {
  return (
    <span
      role="link"
      aria-disabled="true"
      aria-current="page"
      className={cn('font-medium text-foreground', className)}
      {...props}
    />
  )
}
