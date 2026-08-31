// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  Corner,
  Root,
  Scrollbar,
  Thumb,
  Viewport,
} from '@radix-ui/react-scroll-area'
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * ScrollArea — custom thin scrollbars over Radix ScrollArea, so dense panels and
 * code/log surfaces scroll without the OS chrome stealing space or clashing with the
 * warm-graphite theme. The bar is a hairline gutter; the thumb is a `border-strong`
 * pill that fades in on hover/scroll. Pass `orientation` to opt into a horizontal bar.
 */
export function ScrollArea({
  className,
  children,
  orientation = 'vertical',
  ...props
}: ComponentProps<typeof Root> & {
  orientation?: 'vertical' | 'horizontal' | 'both'
}) {
  return (
    <Root className={cn('relative overflow-hidden', className)} {...props}>
      <Viewport className="h-full w-full rounded-[inherit] outline-none">
        {children}
      </Viewport>
      {(orientation === 'vertical' || orientation === 'both') && (
        <ScrollBar orientation="vertical" />
      )}
      {(orientation === 'horizontal' || orientation === 'both') && (
        <ScrollBar orientation="horizontal" />
      )}
      <Corner className="bg-transparent" />
    </Root>
  )
}

export function ScrollBar({
  className,
  orientation = 'vertical',
  ...props
}: ComponentProps<typeof Scrollbar>) {
  return (
    <Scrollbar
      orientation={orientation}
      className={cn(
        'flex touch-none select-none p-px transition-opacity duration-150 ease-out',
        'data-[state=hidden]:opacity-0',
        orientation === 'vertical' &&
          'h-full w-2 border-l border-l-transparent',
        orientation === 'horizontal' &&
          'h-2 w-full flex-col border-t border-t-transparent',
        className,
      )}
      {...props}
    >
      <Thumb className="relative flex-1 rounded-full bg-border-strong" />
    </Scrollbar>
  )
}
