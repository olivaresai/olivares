// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { TextareaHTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

/**
 * Textarea — multi-line sibling of Input. Same hairline + ring idiom, relaxed line
 * height for readable wrapped prose / config blobs. `mono` switches to the tabular
 * monospace face for editing snippets, env files, or structured payloads.
 */
export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  /** Use the monospace, tabular-aligned face for config / snippets / payloads. */
  mono?: boolean
}

export function Textarea({ className, mono = false, ...props }: TextareaProps) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        'min-h-16 w-full rounded-md border border-border-strong bg-surface px-2.5 py-1.5',
        'text-sm leading-relaxed text-foreground placeholder:text-muted-foreground',
        'transition-colors outline-none',
        'focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 focus-visible:ring-offset-background',
        'aria-[invalid=true]:border-danger aria-[invalid=true]:ring-danger',
        'disabled:pointer-events-none disabled:opacity-50 disabled:bg-muted',
        mono && 'font-mono tabular-nums tracking-tight',
        className,
      )}
      {...props}
    />
  )
}
