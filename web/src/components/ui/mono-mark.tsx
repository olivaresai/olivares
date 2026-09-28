// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { HTMLAttributes } from 'react'
import { cn } from '@/lib/utils'

/**
 * MonoMark — the letters of a tool, a workspace or an account in a rounded square, drawn in
 * the interface face: no vendor logo reaches the console. It stands beside the name it
 * abbreviates, so by default it is decoration (`aria-hidden`); `labelled` makes it an image
 * named by `name` for the rare place it stands alone.
 *
 * `hue` (1 to 5) is the account color, drawn as a dot on the corner. An account color
 * always appears next to the account name, so the dot never carries meaning alone.
 */
export interface MonoMarkProps extends Omit<
  HTMLAttributes<HTMLSpanElement>,
  'children'
> {
  /** The full name the letters abbreviate. */
  name: string
  /** One to three letters; by default the initials of the first two words of `name`. */
  letters?: string
  size?: 'sm' | 'base' | 'lg'
  hue?: 1 | 2 | 3 | 4 | 5
  /** An image named by `name`, for a mark that stands without its name. */
  labelled?: boolean
}

const SIZE = {
  sm: 'size-5 rounded-[6px] text-[9px]',
  base: 'size-7 rounded-[8px] text-[11px]',
  lg: 'size-10 rounded-[11px] text-[14px]',
} as const

/** The account hues: five fixed colors, each beside the account's name. */
const HUE = {
  1: 'bg-[#5b8def]',
  2: 'bg-[#3fb68b]',
  3: 'bg-[#c87fe0]',
  4: 'bg-[#e0b341]',
  5: 'bg-[#4fb3c8]',
} as const

function initialsOf(name: string): string {
  const words = name
    .split(/[\s\-_./]+/)
    .map((w) => w.trim())
    .filter(Boolean)
  const first = words.slice(0, 2).map((w) => Array.from(w)[0] ?? '')
  return first.join('').toUpperCase()
}

export function MonoMark({
  name,
  letters,
  size = 'base',
  hue,
  labelled = false,
  className,
  ...props
}: MonoMarkProps) {
  const text = (letters ?? initialsOf(name)).slice(0, 3)
  return (
    <span
      data-slot="mono-mark"
      {...(labelled
        ? { role: 'img', 'aria-label': name }
        : { 'aria-hidden': true })}
      className={cn(
        'relative inline-grid shrink-0 place-items-center border border-line-strong bg-raised',
        'leading-none font-bold tracking-[0.02em] text-text',
        SIZE[size],
        className,
      )}
      {...props}
    >
      {text}
      {hue ? (
        <span
          data-slot="mono-mark-hue"
          data-hue={hue}
          aria-hidden="true"
          className={cn(
            'absolute -right-[3px] -bottom-[3px] size-2.5 rounded-full border-2 border-canvas',
            HUE[hue],
          )}
        />
      ) : null}
    </span>
  )
}
