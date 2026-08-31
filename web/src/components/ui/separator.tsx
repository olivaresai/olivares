// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Root } from '@radix-ui/react-separator'
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Separator — a single hairline rule over Radix Separator. Defaults to a decorative
 * horizontal `h-px` line; set `orientation="vertical"` for a `w-px` divider (give it
 * a height via the parent, e.g. inside an `h-8` toolbar). Pass `decorative={false}`
 * when it semantically separates content groups so it surfaces to assistive tech.
 */
export function Separator({
  className,
  orientation = 'horizontal',
  decorative = true,
  ...props
}: ComponentProps<typeof Root>) {
  return (
    <Root
      orientation={orientation}
      decorative={decorative}
      className={cn(
        'bg-border shrink-0',
        orientation === 'horizontal' ? 'h-px w-full' : 'w-px h-full',
        className,
      )}
      {...props}
    />
  )
}
