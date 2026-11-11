// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'
import { fieldWell } from './input'

/**
 * Textarea — multi-line sibling of Input: the same field well, with a relaxed line height for
 * readable wrapped prose / config blobs. `mono` switches to the tabular monospace face for
 * editing snippets, env files, or structured payloads.
 */
export interface TextareaProps extends ComponentProps<'textarea'> {
  /** Use the monospace, tabular-aligned face for config / snippets / payloads. */
  mono?: boolean
}

export function Textarea({ className, mono = false, ...props }: TextareaProps) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        fieldWell,
        'min-h-16 px-[11px] py-[9px] leading-[21px]',
        'placeholder:text-text-3 read-only:shadow-none',
        mono && 'font-mono tabular-nums tracking-tight',
        className,
      )}
      {...props}
    />
  )
}
