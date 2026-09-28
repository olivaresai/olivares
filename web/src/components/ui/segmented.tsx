// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Info } from 'lucide-react'
import {
  useId,
  useRef,
  type HTMLAttributes,
  type KeyboardEvent,
  type ReactNode,
} from 'react'
import { cn } from '@/lib/utils'

/**
 * Segmented — one choice among two to five, side by side: List or Table, a time range, a
 * theme. It is a radio group: the group is one tab stop (the checked option), the arrow keys
 * move the choice and wrap, Home and End go to the ends, and a disabled option is skipped
 * and does not act. Every option says whether it is checked (`aria-checked`); the checked
 * one is raised on the group's surface.
 *
 * ⛔ A DISABLED OPTION CARRIES ITS REASON. The type asks for `reason` beside `disabled`, the
 *    reason is visible text under the group, and the option points at it with
 *    `aria-describedby`; a blank reason throws at render, as DisabledReason does.
 *
 * Use Tabs when the choice switches a panel of content; use Segmented when it changes how
 * the same content is shown or filtered.
 */
export type SegmentedOption<V extends string = string> = {
  value: V
  label: ReactNode
  icon?: ReactNode
} & (
  | { disabled?: false; reason?: never }
  | {
      disabled: true
      /** Why the option cannot be chosen now. Visible text; never blank. */
      reason: string
    }
)

export interface SegmentedProps<V extends string = string> extends Omit<
  HTMLAttributes<HTMLDivElement>,
  'onChange' | 'defaultValue'
> {
  options: ReadonlyArray<SegmentedOption<V>>
  value: V
  onValueChange: (value: V) => void
  size?: 'sm' | 'base'
  /** The group's name; required when no visible label points at the group. */
  'aria-label'?: string
}

export function Segmented<V extends string = string>({
  options,
  value,
  onValueChange,
  size = 'base',
  className,
  ...props
}: SegmentedProps<V>) {
  const refs = useRef<Array<HTMLButtonElement | null>>([])
  const reasonId = useId()
  for (const o of options)
    if (o.disabled && (typeof o.reason !== 'string' || o.reason.trim() === ''))
      throw new Error(
        'Segmented needs reason text for a disabled option: a disabled control must say why it cannot act.',
      )
  const enabled = options
    .map((o, i) => (o.disabled ? -1 : i))
    .filter((i) => i >= 0)

  const move = (from: number, to: number) => {
    const option = options[to]
    if (!option || option.disabled || to === from) return
    onValueChange(option.value)
    refs.current[to]?.focus()
  }

  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>, i: number) => {
    const at = enabled.indexOf(i)
    if (at < 0 || enabled.length === 0) return
    let next: number | null = null
    switch (event.key) {
      case 'ArrowRight':
      case 'ArrowDown':
        next = enabled[(at + 1) % enabled.length]
        break
      case 'ArrowLeft':
      case 'ArrowUp':
        next = enabled[(at - 1 + enabled.length) % enabled.length]
        break
      case 'Home':
        next = enabled[0]
        break
      case 'End':
        next = enabled[enabled.length - 1]
        break
    }
    if (next === null) return
    event.preventDefault()
    move(i, next)
  }

  const checkedIndex = options.findIndex((o) => o.value === value)
  const tabStop = checkedIndex >= 0 ? checkedIndex : (enabled[0] ?? -1)

  const reasons = options
    .map((o, i) => (o.disabled ? { i, text: o.reason } : null))
    .filter((r): r is { i: number; text: string } => r !== null)

  const group = (
    <div
      role="radiogroup"
      data-slot="segmented"
      className={cn(
        'inline-flex max-w-full gap-0.5 rounded-[9px] border border-line bg-surface p-0.5',
        className,
      )}
      {...props}
    >
      {options.map((option, i) => {
        const checked = option.value === value
        return (
          <button
            key={option.value}
            ref={(el) => {
              refs.current[i] = el
            }}
            type="button"
            role="radio"
            aria-checked={checked}
            aria-disabled={option.disabled || undefined}
            aria-describedby={option.disabled ? `${reasonId}-${i}` : undefined}
            tabIndex={i === tabStop ? 0 : -1}
            data-state={checked ? 'on' : 'off'}
            onClick={() => {
              if (!option.disabled && !checked) onValueChange(option.value)
            }}
            onKeyDown={(event) => onKeyDown(event, i)}
            className={cn(
              'inline-flex min-w-0 items-center justify-center gap-1.5 rounded-[7px] font-medium whitespace-nowrap',
              size === 'sm'
                ? 'h-6 px-2 text-overline'
                : 'h-7 px-2.5 text-caption',
              'text-text-2 transition-colors duration-100 ease-out outline-none',
              'hover:text-text',
              'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus',
              'data-[state=on]:bg-raised data-[state=on]:text-text data-[state=on]:shadow-[0_0_0_1px_var(--line-strong)]',
              'aria-disabled:cursor-not-allowed aria-disabled:text-text-3 aria-disabled:hover:text-text-3',
              '[&_svg]:size-3.5 [&_svg]:shrink-0',
            )}
          >
            {option.icon}
            {option.label}
          </button>
        )
      })}
    </div>
  )

  if (reasons.length === 0) return group
  return (
    <div
      data-slot="segmented-field"
      className="inline-flex max-w-full flex-col items-start gap-1.5"
    >
      {group}
      {reasons.map((r) => (
        <p
          key={r.i}
          id={`${reasonId}-${r.i}`}
          data-slot="disabled-reason-text"
          className="m-0 flex min-w-0 items-start gap-1.5 text-caption text-text-2"
        >
          <Info
            className="mt-0.5 size-3.5 shrink-0 text-text-3"
            aria-hidden="true"
          />
          <span className="min-w-0">{r.text}</span>
        </p>
      ))}
    </div>
  )
}
