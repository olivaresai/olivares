// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE NAME OF AN ICON BUTTON, SHOWN. An icon button carries its name in `aria-label` for a
// screen reader and in a tooltip for everyone else, on hover and on keyboard focus. A
// button that is disabled cannot take focus, so its REASON rides on a focusable span around
// it: the tooltip is then reachable by keyboard too.
import type { ReactElement } from 'react'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'

export function IconTip({
  label,
  children,
}: {
  label: string
  children: ReactElement
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>{children}</TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  )
}

/** A disabled control with its reason: focusable, so a keyboard reaches the words. */
export function DisabledTip({
  reason,
  children,
}: {
  reason: string
  children: ReactElement
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          tabIndex={0}
          data-slot="disabled-tip"
          className="inline-flex rounded-ctl outline-none focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-focus"
        >
          {children}
          <span className="sr-only">{reason}</span>
        </span>
      </TooltipTrigger>
      <TooltipContent>{reason}</TooltipContent>
    </Tooltip>
  )
}
