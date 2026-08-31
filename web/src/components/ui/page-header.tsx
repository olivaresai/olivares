// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { LucideIcon } from 'lucide-react'
import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

/**
 * PageHeader — the consistent title block every management view opens with: an
 * optional icon chip, a display title, a one-line description, and an optional
 * right-aligned action slot (a primary "create" button, a refresh, etc.). Keeping
 * it a single primitive means all of share the same heading rhythm.
 */
export interface PageHeaderProps {
  title: ReactNode
  description?: ReactNode
  icon?: LucideIcon
  /** Right-aligned actions (buttons, filters). */
  actions?: ReactNode
  className?: string
}

export function PageHeader({
  title,
  description,
  icon: Icon,
  actions,
  className,
}: PageHeaderProps) {
  return (
    <div
      className={cn(
        'flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between',
        className,
      )}
    >
      <div className="flex items-start gap-3">
        {Icon && (
          <div className="mt-0.5 flex size-9 shrink-0 items-center justify-center rounded-lg bg-accent-soft text-accent-soft-foreground [&_svg]:size-5">
            <Icon />
          </div>
        )}
        <div className="min-w-0">
          <h1 className="font-display text-xl font-semibold tracking-tight text-foreground">
            {title}
          </h1>
          {description != null && (
            <p className="text-sm text-muted-foreground">{description}</p>
          )}
        </div>
      </div>
      {actions != null && (
        <div className="flex shrink-0 items-center gap-2">{actions}</div>
      )}
    </div>
  )
}
