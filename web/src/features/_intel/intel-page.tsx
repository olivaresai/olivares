// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// IntelPage / SectionCard — the shared page chrome for the intelligence views, so they
// all open with the same rhythm: the shared `PageHeader` (icon chip, display title,
// one-line description, secondary controls, THE primary action) and its row of honesty
// notices, then the content. SectionCard is the titled panel the views compose their
// charts/tables in.
import type { LucideIcon } from 'lucide-react'
import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'
import { PageHeader } from '@/components/ui/page-header'
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
  primaryAction,
  notices,
  children,
  className,
}: {
  icon?: LucideIcon
  title: ReactNode
  description?: ReactNode
  /** Right-aligned secondary controls (range picker, refresh, export…). */
  actions?: ReactNode
  /** THE verb of this screen, rendered last. See PageHeader for why it is its own slot. */
  primaryAction?: ReactNode
  /** A row of inline notices rendered between the header and the content. */
  notices?: ReactNode
  children: ReactNode
  className?: string
}) {
  return (
    <div className={cn('flex flex-col gap-5 pb-10', className)}>
      {/* ⛔ THE HEADER IS `PageHeader`, NOT A SECOND COPY OF IT (D21). Until 2026-09-17
          this file carried its own `<header>` + `<h1>` + description + actions, and
          `components/ui/page-header.tsx` carried another — the same block, written
          twice, and the two had already drifted (this one padded its description to
          `max-w-2xl`, that one did not). The console then had three heading sizes for
          one heading: `text-xl` in these two, `text-lg` on the auth screens and in the
          tenant gate, `text-2xl` in settings and the status page. One primitive, one
          type token, and the drift has nowhere to live. `as="header"` keeps the exact
          element these 26 views have always emitted, so no landmark or heading moved. */}
      <PageHeader
        as="header"
        icon={Icon}
        title={title}
        description={description}
        actions={actions}
        primaryAction={primaryAction}
        notices={notices}
      />
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
  const hasHeading = Boolean(title || description)
  return (
    <Card className={cn('flex flex-col', className)}>
      {title || actions ? (
        // The row wraps: the heading keeps a readable 16rem before the actions move
        // below it, and the actions wrap among themselves inside the card. Wide cards
        // still place both side by side.
        <CardHeader className="flex-wrap">
          <div
            className={cn('min-w-0 flex-1 space-y-1', hasHeading && 'basis-64')}
          >
            {title ? <CardTitle>{title}</CardTitle> : null}
            {description ? (
              <CardDescription>{description}</CardDescription>
            ) : null}
          </div>
          {actions ? (
            <div className="flex min-w-0 max-w-full flex-wrap items-center gap-2">
              {actions}
            </div>
          ) : null}
        </CardHeader>
      ) : null}
      <CardContent className={cn(noPadding && 'p-0', contentClassName)}>
        {children}
      </CardContent>
    </Card>
  )
}
