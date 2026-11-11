// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE METER: a 6 px fill on a 2 px rail, how much of a limit is used. The fill is the
// figure, in the second tone; amber when the caller says the reading is near its limit.
// The rail is the control boundary ink (3:1), so anyone can see where the limit ends; it
// is drawn as a background line, so it never clips the fill. The threshold
// stays with the reading that owns it (a provider's window, a budget), never here, so one
// figure cannot be amber in one place and calm in another.
//
// With a `label` it is a `meter` a screen reader names; without one it is decoration
// beside figures that already say the same in words.
import { cn } from '@/lib/utils'

export function Meter({
  value,
  warn = false,
  label,
  className,
}: {
  /** Percent used, 0-100; a reading past the limit fills the track, never more. */
  value: number
  warn?: boolean
  label?: string
  className?: string
}) {
  const percent = Math.min(100, Math.max(0, value))
  const named = label
    ? {
        role: 'meter',
        'aria-label': label,
        'aria-valuenow': Math.round(percent),
        'aria-valuemin': 0,
        'aria-valuemax': 100,
      }
    : { 'aria-hidden': true }
  return (
    <span
      data-slot="meter"
      {...named}
      className={cn(
        'inline-block h-1.5 rounded-full align-middle',
        'bg-[linear-gradient(var(--ctl-border),var(--ctl-border))] bg-size-[100%_2px] bg-center bg-no-repeat',
        className,
      )}
    >
      <span
        className={cn(
          'block h-full rounded-full',
          warn ? 'bg-warn' : 'bg-text-2',
        )}
        style={{ width: `${percent}%` }}
      />
    </span>
  )
}
