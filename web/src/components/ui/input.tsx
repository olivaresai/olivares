// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { InputHTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

/**
 * Input — the native-input primitive of the control plane. Flat, hairline-bordered,
 * dense (h-8). Focus is a ring, never a glow. The `invalid` path is driven by the
 * standard `aria-invalid` attribute so react-hook-form / Radix can wire it without a
 * bespoke prop. `mono` switches to font-mono + tabular-nums for ids / hashes / tokens
 * / IPs / CIDRs where character alignment matters.
 */
export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  /** Use the monospace, tabular-aligned face for ids / hashes / tokens / IP / CIDR. */
  mono?: boolean
}

export function Input({
  className,
  mono = false,
  type = 'text',
  ...props
}: InputProps) {
  return (
    <input
      type={type}
      data-slot="input"
      className={cn(
        'h-8 w-full rounded-md border border-border-strong bg-surface px-2.5 text-sm text-foreground',
        'placeholder:text-muted-foreground transition-colors outline-none',
        'focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 focus-visible:ring-offset-background',
        'aria-[invalid=true]:border-danger aria-[invalid=true]:ring-danger',
        'disabled:pointer-events-none disabled:opacity-50 disabled:bg-muted',
        'file:border-0 file:bg-transparent file:text-sm file:font-medium file:text-foreground',
        mono && 'font-mono tabular-nums tracking-tight',
        className,
      )}
      {...props}
    />
  )
}
