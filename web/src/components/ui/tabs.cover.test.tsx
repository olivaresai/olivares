// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A tab that the strip only partly shows must be CLIPPED by the strip, never COVERED by
// another control. The scroll buttons used to sit over the strip's edge (absolutely
// positioned, above it), so at 1280 px the Connectors tab of /console showed its first
// letters under the scroll-end button, and a click on what was visible of it scrolled the
// strip instead of opening the tab. The buttons now take their own place beside the
// strip. jsdom has no layout, so this pins the arrangement; the click itself is measured
// in the browser (the capture vehicle clicks the tab at the centre of its visible part).
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Tabs, TabsList, TabsTrigger } from './tabs'

const LABELS = ['Users & groups', 'Agents', 'Secrets', 'Connectors', 'License']

function Strip() {
  return (
    <Tabs defaultValue="tab-0">
      <TabsList aria-label="Console sections">
        {LABELS.map((label, i) => (
          <TabsTrigger key={label} value={`tab-${i}`}>
            {label}
          </TabsTrigger>
        ))}
      </TabsList>
    </Tabs>
  )
}

function overflowing(list: HTMLElement, scrollLeft: number) {
  Object.defineProperty(list, 'scrollWidth', {
    configurable: true,
    get: () => 1100,
  })
  Object.defineProperty(list, 'clientWidth', {
    configurable: true,
    get: () => 600,
  })
  list.scrollLeft = scrollLeft
  fireEvent.scroll(list)
}

describe('the scroll buttons sit beside the strip, never over a tab', () => {
  it('each button takes its own place in the shell, in flow, not positioned over the strip', () => {
    render(<Strip />)
    const list = screen.getByRole('tablist')
    overflowing(list, 200)
    const shell = list.parentElement as HTMLElement
    const buttons = Array.from(
      shell.querySelectorAll<HTMLElement>('[data-slot="tabs-scroll-button"]'),
    )
    expect(buttons.map((b) => b.dataset.direction)).toEqual(['start', 'end'])
    for (const b of buttons) {
      // The button's box is a child of the shell beside the list, not inside it.
      const holder = b.parentElement as HTMLElement
      expect(holder.parentElement).toBe(shell)
      expect(list.contains(b)).toBe(false)
      for (
        let el: HTMLElement | null = b;
        el && el !== shell;
        el = el.parentElement
      ) {
        expect(el.className, b.dataset.direction).not.toMatch(
          /\b(absolute|fixed)\b/,
        )
        expect(el.className, b.dataset.direction).not.toMatch(/\bz-\d+\b/)
      }
      expect(holder).toHaveClass('shrink-0')
    }
    // Order in the shell: start button, the strip, end button.
    const kids = Array.from(shell.children)
    expect(kids.indexOf(buttons[0].parentElement as Element)).toBeLessThan(
      kids.indexOf(list),
    )
    expect(kids.indexOf(buttons[1].parentElement as Element)).toBeGreaterThan(
      kids.indexOf(list),
    )
  })

  it('a scroll button sits outside the segmented track, so the track still reads as one control', () => {
    render(<Strip />)
    const list = screen.getByRole('tablist')
    overflowing(list, 0)
    const end = document.querySelector<HTMLElement>(
      '[data-slot="tabs-scroll-button"]',
    )
    // The 1.0 strip is a segmented control (one muted track), not an underlined row: the
    // button's wrapper is a sibling of the track, never inside it and never ruled.
    expect(list.contains(end)).toBe(false)
    expect(end?.parentElement).not.toHaveClass('border-b')
    expect(list).toHaveClass('rounded-ctl', 'bg-hover')
  })
})
