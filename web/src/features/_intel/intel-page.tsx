// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// IntelPage / SectionCard — the shared page chrome for the five intelligence views,
// so they all open with the same rhythm: an icon-chipped display title, a one-line
// description, right-aligned actions, an optional row of honesty notices, then the
// content. SectionCard is the titled panel the views compose their charts/tables in.
import type { LucideIcon } from 'lucide-react'
import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'

export function IntelPage({
  icon: Icon,
  title,
  description,
  actions,
  notices,
  children,
  className,
}: {
  icon?: LucideIcon
  title: ReactNode
  description?: ReactNode
  /** Right-aligned header controls (range picker, refresh, export…). */
  actions?: ReactNode
  /** A row of inline notices rendered between the header and the content. */
  notices?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <div className={cn('flex flex-col gap-5 pb-10', className)}>
      <header className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="flex items-start gap-3">
          {Icon ? (
            <span className="flex size-9 shrink-0 items-center justify-center rounded-lg bg-accent-soft text-accent-soft-foreground [&_svg]:size-5">
              <Icon />
            </span>
          ) : null}
          <div className="space-y-1">
            <h1 className="font-display text-xl font-semibold tracking-tight text-foreground">
              {title}
            </h1>
            {description ? (
              <p className="max-w-2xl text-sm text-muted-foreground">
                {description}
              </p>
            ) : null}
          </div>
        </div>
        {actions ? (
          <div className="flex shrink-0 flex-wrap items-center gap-2">
            {actions}
          </div>
        ) : null}
      </header>
      {notices ? <div className="flex flex-col gap-2">{notices}</div> : null}
      {children}
    </div>
  )
}

/** A titled panel — the building block the views drop charts/tables/lists into. */
export function SectionCard({
  title,
  description,
  actions,
  children,
  className,
  contentClassName,
  noPadding = false,
}: {
  title?: ReactNode
  description?: ReactNode
  actions?: ReactNode
  children: ReactNode
  className?: string
  contentClassName?: string
  /** Tables manage their own padding; pass true to let content go edge-to-edge. */
  noPadding?: boolean
}) {
  return (
    <Card className={cn('flex flex-col', className)}>
      {title || actions ? (
        <CardHeader>
          <div className="space-y-1">
            {title ? <CardTitle>{title}</CardTitle> : null}
            {description ? (
              <CardDescription>{description}</CardDescription>
            ) : null}
          </div>
          {actions ? (
            <div className="flex shrink-0 items-center gap-2">{actions}</div>
          ) : null}
        </CardHeader>
      ) : null}
      <CardContent className={cn(noPadding && 'p-0', contentClassName)}>
        {children}
      </CardContent>
    </Card>
  )
}
