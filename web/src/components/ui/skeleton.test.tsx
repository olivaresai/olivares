// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Skeleton } from './skeleton'

describe('Skeleton', () => {
  it('is a static block on the selection fill: no pulse anywhere', () => {
    const { container } = render(<Skeleton className="h-9 w-full" />)
    const el = container.firstChild as HTMLElement
    expect(el).toHaveClass('rounded-[6px]', 'bg-active', 'h-9', 'w-full')
    expect(el).not.toHaveClass('animate-pulse')
    // The motion opt-outs existed only to tame the pulse; without it nothing
    // animation-shaped remains.
    expect(el.className).not.toMatch(/animate|motion-reduce|data-motion/i)
  })

  it('still reads as loading to assistive tech and the readiness guard', () => {
    // e2e-release/surface.ts keys its first-hour guard on `[aria-busy=true]`;
    // the static block must keep that contract even though nothing animates.
    const { container } = render(<Skeleton className="h-4 w-24" />)
    expect(container.firstChild).toHaveAttribute('aria-busy', 'true')
  })
})
