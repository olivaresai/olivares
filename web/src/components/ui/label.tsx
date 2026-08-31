// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import * as LabelPrimitive from '@radix-ui/react-label'
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * Label — the form-row caption. Built on Radix Label so clicking it focuses its
 * control and `htmlFor` association is correct. Dims with a disabled `peer` so the
 * caption visually tracks the control state. `select-none` keeps double-click on a
 * field label from selecting text instead of focusing the input.
 */
export function Label({
  className,
  ...props
}: ComponentProps<typeof LabelPrimitive.Root>) {
  return (
    <LabelPrimitive.Root
      data-slot="label"
      className={cn(
        'text-sm font-medium text-foreground select-none',
        'peer-disabled:pointer-events-none peer-disabled:opacity-50',
        className,
      )}
      {...props}
    />
  )
}
