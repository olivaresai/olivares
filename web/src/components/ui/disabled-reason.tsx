// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { Info } from 'lucide-react'
import {
  cloneElement,
  isValidElement,
  useId,
  type KeyboardEvent,
  type ReactElement,
  type ReactNode,
  type SyntheticEvent,
} from 'react'
import { cn } from '@/lib/utils'
import { Input } from './input'
import { Textarea } from './textarea'

/**
 * DisabledReason — wraps ONE control and, while the control cannot act, owns the visible
 * text that says why and what to do. The control points at that text with
 * `aria-describedby`, so a screen reader hears the reason on arrival and a sighted operator
 * reads it under (or beside) the control. A reason is never a tooltip.
 *
 * ⛔ IT REFUSES TO RENDER WITHOUT REASON TEXT, disabled or not. `reason` is a string so the
 *    component can check it: an empty or blank reason throws at render. A disabled control
 *    without a visible reason is the defect this component exists to make impossible.
 *
 * Two forms:
 * - `aria` (default): `aria-disabled="true"`. The control keeps its place in the tab order,
 *   so the reason is announced to whoever reaches it; this component refuses the activation
 *   (click, pointer down, Enter and Space) before the control's own handlers run, which also
 *   stops a Radix trigger from opening.
 * - `native`: the `disabled` attribute, for a control whose tests read it (the Work and
 *   communications acts keep it).
 *
 * While the control can act, the wrapper adds nothing to it and renders no reason node.
 * A reason for a concealed object must not describe the object ("You cannot open this").
 */
export interface DisabledReasonProps {
  /** Whether the wrapped control cannot act now. */
  disabled: boolean
  /** Why the control cannot act and what to do. Visible text; never blank. */
  reason: string
  /** The next step beside the reason: a link or a quiet button ("Sign in"). */
  action?: ReactNode
  /** `aria` keeps the control focusable; `native` sets the disabled attribute. */
  mode?: 'aria' | 'native'
  /** `block` puts the reason under the control; `inline` beside it. */
  layout?: 'block' | 'inline'
  /** Exactly one control: a Button, a trigger, a Switch, an Input. */
  children: ReactElement<ControlProps>
  className?: string
}

interface ControlProps {
  'aria-describedby'?: string
  'aria-disabled'?: boolean | 'true' | 'false'
  disabled?: boolean
  readOnly?: boolean
  onClick?: (event: SyntheticEvent) => void
  onPointerDown?: (event: SyntheticEvent) => void
  onKeyDown?: (event: KeyboardEvent) => void
}

const TEXT_FIELDS = new Set<unknown>(['input', 'textarea', Input, Textarea])

const ACTIVATION_KEYS = new Set(['Enter', ' ', 'ArrowDown', 'ArrowUp'])

function refuse(event: SyntheticEvent) {
  event.preventDefault()
  event.stopPropagation()
}

export function DisabledReason({
  disabled,
  reason,
  action,
  mode = 'aria',
  layout = 'block',
  children,
  className,
}: DisabledReasonProps) {
  const reasonId = useId()
  if (typeof reason !== 'string' || reason.trim() === '') {
    throw new Error(
      'DisabledReason needs reason text: a disabled control must say why it cannot act.',
    )
  }
  if (!isValidElement(children)) {
    throw new Error('DisabledReason wraps exactly one control element.')
  }

  if (!disabled) {
    return children
  }

  const own = children.props
  const describedBy = [own['aria-describedby'], reasonId]
    .filter(Boolean)
    .join(' ')
  const control =
    mode === 'native'
      ? cloneElement(children, {
          disabled: true,
          'aria-describedby': describedBy,
        })
      : cloneElement(children, {
          'aria-disabled': 'true',
          'aria-describedby': describedBy,
          // A text field stays readable and focusable, and takes no input.
          ...(TEXT_FIELDS.has(children.type) ? { readOnly: true } : null),
          onClick: refuse,
          onPointerDown: refuse,
          onKeyDown: (event: KeyboardEvent) => {
            if (ACTIVATION_KEYS.has(event.key)) {
              refuse(event)
              return
            }
            own.onKeyDown?.(event)
          },
        })

  return (
    <div
      data-slot="disabled-reason"
      className={cn(
        layout === 'inline'
          ? 'flex flex-wrap items-center gap-x-3 gap-y-1.5'
          : 'flex flex-col items-start gap-1.5',
        className,
      )}
    >
      {control}
      <p
        id={reasonId}
        data-slot="disabled-reason-text"
        className="m-0 flex min-w-0 items-start gap-1.5 text-caption text-text-2"
      >
        <Info
          className="mt-0.5 size-3.5 shrink-0 text-text-3"
          aria-hidden="true"
        />
        <span className="min-w-0">
          {reason}
          {action ? (
            <>
              {' '}
              <span className="text-accent-text [&_a]:underline [&_a]:underline-offset-[3px]">
                {action}
              </span>
            </>
          ) : null}
        </span>
      </p>
    </div>
  )
}
