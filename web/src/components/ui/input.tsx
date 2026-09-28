// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { InputHTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

/**
 * Input — the native-input primitive of the control plane. Flat, on the canvas, with the
 * control boundary (3:1), dense (h-8). Focus is the 2 px focus outline, never a glow; a
 * field that cannot take input is dashed and in the third text tone, never faded. The `invalid` path is driven by the
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
        'h-8 w-full rounded-ctl border border-ctl-border bg-canvas px-3 text-body text-text',
        'placeholder:text-text-3 transition-colors duration-100 ease-out outline-none',
        'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus',
        'aria-[invalid=true]:border-bad aria-[invalid=true]:focus-visible:outline-bad',
        'disabled:pointer-events-none disabled:border-dashed disabled:bg-surface disabled:text-text-3',
        'aria-disabled:cursor-not-allowed aria-disabled:border-dashed aria-disabled:bg-surface aria-disabled:text-text-3',
        'file:border-0 file:bg-transparent file:text-body file:font-medium file:text-foreground',
        mono && 'font-mono tabular-nums tracking-tight',
        className,
      )}
      {...props}
    />
  )
}
