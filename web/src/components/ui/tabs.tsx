// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Content, List, Root, Trigger } from '@radix-ui/react-tabs'
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Tabs — ledger/underline navigation over Radix Tabs (not a pill switcher). The list
 * is a hairline-bottomed strip; the active trigger lifts to `foreground` with a copper
 * underline that overlaps the strip border (`-mb-px` + `border-b-2`). Color-only
 * transitions keep it calm; Radix supplies roving-tabindex and ARIA for free.
 */
export function Tabs({ className, ...props }: ComponentProps<typeof Root>) {
  return <Root className={cn('flex flex-col', className)} {...props} />
}

export function TabsList({ className, ...props }: ComponentProps<typeof List>) {
  return (
    <List
      className={cn(
        'inline-flex h-9 items-center gap-1 border-b border-border overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden',
        className,
      )}
      {...props}
    />
  )
}

export function TabsTrigger({
  className,
  ...props
}: ComponentProps<typeof Trigger>) {
  return (
    <Trigger
      className={cn(
        'inline-flex h-9 items-center -mb-px px-3 text-sm font-medium whitespace-nowrap',
        'border-b-2 border-transparent text-muted-foreground transition-colors duration-100 ease-out',
        'hover:text-foreground',
        'data-[state=active]:text-foreground data-[state=active]:border-accent-text',
        'outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
        'disabled:pointer-events-none disabled:opacity-50',
        className,
      )}
      {...props}
    />
  )
}

export function TabsContent({
  className,
  ...props
}: ComponentProps<typeof Content>) {
  return (
    <Content
      className={cn(
        'pt-4 outline-none',
        'focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
        className,
      )}
      {...props}
    />
  )
}
