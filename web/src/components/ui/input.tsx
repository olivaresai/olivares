// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ComponentProps } from 'react'
import { cn } from '@/lib/utils'

/**
 * The field well (direction A2): a recessed fill and its inner edge inside a boundary anyone can
 * see (3:1). The boundary darkens under the pointer and takes the 2 px focus ring on itself; an
 * invalid field draws it in the failure tone; a field that cannot take input is faded to 45 %.
 * Input, Textarea and the Select and Combobox triggers wear it, so a form reads as one material.
 */
export const fieldWell = cn(
  'w-full min-w-0 rounded-ctl border border-ctl-border bg-[var(--well)] text-body text-text shadow-[var(--shadow-well)]',
  'outline-none transition-[color,background-color,border-color,box-shadow] ease-out',
  'hover:border-text-2',
  'focus-visible:border-focus focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-focus',
  'aria-[invalid=true]:border-bad aria-[invalid=true]:hover:border-bad aria-[invalid=true]:focus-visible:outline-bad',
  'disabled:pointer-events-none disabled:opacity-45',
  'aria-disabled:cursor-not-allowed aria-disabled:opacity-45',
)

/**
 * Input — the native-input primitive of the control plane: a field well as high as the buttons
 * beside it (the density token; 44 px on a phone). A read-only value sits flat in its well. The
 * `invalid` path is driven by the standard `aria-invalid` attribute so react-hook-form / Radix
 * can wire it without a bespoke prop. `mono` switches to font-mono + tabular-nums for ids /
 * hashes / tokens / IPs / CIDRs where character alignment matters.
 */
export interface InputProps extends ComponentProps<'input'> {
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
        fieldWell,
        'h-[var(--density-control)] px-[11px] text-ellipsis max-md:min-h-11',
        'placeholder:text-text-3 read-only:shadow-none',
        'file:border-0 file:bg-transparent file:text-body file:font-medium file:text-foreground',
        mono && 'font-mono tabular-nums tracking-tight',
        className,
      )}
      {...props}
    />
  )
}
