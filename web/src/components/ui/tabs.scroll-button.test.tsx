// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE SCROLL-END BUTTON MUST MOVE THE STRIP, AND THE STRIP MUST STAY WHERE IT WAS MOVED. With the
// scroll buttons beside the strip (in flow), a press that showed the start button for the first
// time shrank the strip's box; the strip's resize observer then revealed the selected tab, the
// first one, and wrote the scroll position back to 0. Every press was undone, and at 1280 px the
// Connectors tab of the control console could not be reached by pointer at all.
//
// jsdom has no layout, so this test models the one rule of the browser that matters here: the
// strip is as wide as its shell less the scroll-button slots beside it, and a change of that width
// is reported to the strip's ResizeObserver; a change of its scroll position, by the operator or by
// a script, fires a scroll event. The two repeat until nothing changes, as a browser's frames do.
// Tabs are laid out 112 px apart, 100 px wide, and each tab's box follows the scroll position.
import { act, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Tabs, TabsList, TabsTrigger } from './tabs'

const LABELS = [
  'Users & groups',
  'Agents',
  'SSO / IdP',
  'Workspaces',
  'Roles & delegation',
  'Source bindings',
  'Secrets',
  'Connectors',
  'Workspace connectors',
  'API keys',
  'License',
]
const SHELL_WIDTH = 800
const SLOT = 32
const STEP = 112
const TAB = 100

const observers = new Set<{ cb: ResizeObserverCallback }>()
class FakeResizeObserver {
  cb: ResizeObserverCallback
  constructor(cb: ResizeObserverCallback) {
    this.cb = cb
  }
  observe() {
    observers.add(this)
  }
  unobserve() {}
  disconnect() {
    observers.delete(this)
  }
}

beforeEach(() => {
  observers.clear()
  vi.stubGlobal('ResizeObserver', FakeResizeObserver)
})
afterEach(() => vi.unstubAllGlobals())

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

/**
 * The browser's layout for the strip. Returns `settle`, which plays the frames after an event:
 * a resize report when the strip's width changed, a scroll event when its position changed,
 * until neither changes.
 */
function layout(list: HTMLElement) {
  const shell = list.parentElement as HTMLElement
  const slots = () =>
    shell.querySelectorAll('[data-slot="tabs-scroll-button"]').length
  const clientWidth = () => SHELL_WIDTH - SLOT * slots()
  const scrollWidth = STEP * LABELS.length
  let left = 0
  // Any change of the scroll position during a frame queues one scroll event for that frame,
  // even when the position ends where it began (the browser's pending scroll event targets).
  let pendingScroll = false
  Object.defineProperty(list, 'scrollWidth', {
    configurable: true,
    get: () => scrollWidth,
  })
  Object.defineProperty(list, 'clientWidth', {
    configurable: true,
    get: clientWidth,
  })
  Object.defineProperty(list, 'scrollLeft', {
    configurable: true,
    get: () => left,
    set: (v: number) => {
      const next = Math.max(0, Math.min(v, scrollWidth - clientWidth()))
      if (next !== left) pendingScroll = true
      left = next
    },
  })
  const rect = (l: number, w: number) =>
    ({
      x: l,
      y: 0,
      top: 0,
      bottom: 36,
      height: 36,
      left: l,
      right: l + w,
      width: w,
      toJSON: () => ({}),
    }) as DOMRect
  Object.defineProperty(list, 'getBoundingClientRect', {
    configurable: true,
    value: () => rect(0, clientWidth()),
  })
  screen.getAllByRole('tab').forEach((tab, i) =>
    Object.defineProperty(tab, 'getBoundingClientRect', {
      configurable: true,
      value: () => rect(i * STEP - left, TAB),
    }),
  )
  let width = clientWidth()
  return () => {
    for (let frame = 0; frame < 8; frame++) {
      let changed = false
      act(() => {
        if (clientWidth() !== width) {
          width = clientWidth()
          changed = true
          for (const o of [...observers])
            o.cb([], o as unknown as ResizeObserver)
        }
        if (pendingScroll) {
          pendingScroll = false
          changed = true
          fireEvent.scroll(list)
        }
      })
      if (!changed) return
    }
  }
}

describe('the scroll-end button moves the strip, and the strip stays where it was moved', () => {
  it('each press advances the strip until its last tab is wholly in view', () => {
    render(<Strip />)
    const list = screen.getByRole('tablist')
    const shell = list.parentElement as HTMLElement
    const settle = layout(list)
    act(() => {
      fireEvent.scroll(list)
    })
    settle()
    const positions: number[] = []
    for (let press = 0; press < 8; press++) {
      const end = shell.querySelector<HTMLButtonElement>(
        '[data-slot="tabs-scroll-button"][data-direction="end"]',
      )
      if (!end || end.disabled) break
      act(() => {
        fireEvent.click(end)
      })
      // What the browser does next: the frames after the press.
      settle()
      positions.push(list.scrollLeft)
    }
    expect(positions.length).toBeGreaterThan(0)
    for (let i = 0; i < positions.length; i++)
      expect(
        positions[i],
        `press ${i + 1}: ${positions.join(', ')}`,
      ).toBeGreaterThan(i === 0 ? 0 : positions[i - 1])
    const last = screen.getAllByRole('tab').at(-1) as HTMLElement
    const lr = list.getBoundingClientRect()
    const tr = last.getBoundingClientRect()
    expect(tr.left).toBeGreaterThanOrEqual(lr.left)
    expect(tr.right).toBeLessThanOrEqual(lr.right)
  })

  it('a tab the button brought into view is selected by a pointer click', async () => {
    const user = userEvent.setup()
    render(<Strip />)
    const list = screen.getByRole('tablist')
    const shell = list.parentElement as HTMLElement
    const settle = layout(list)
    act(() => {
      fireEvent.scroll(list)
    })
    settle()
    const connectors = screen.getByRole('tab', { name: 'Connectors' })
    const visible = () => {
      const lr = list.getBoundingClientRect()
      const tr = connectors.getBoundingClientRect()
      return Math.min(tr.right, lr.right) - Math.max(tr.left, lr.left)
    }
    expect(visible()).toBeLessThanOrEqual(0)
    for (let press = 0; press < 8 && visible() < TAB; press++) {
      const end = shell.querySelector<HTMLButtonElement>(
        '[data-slot="tabs-scroll-button"][data-direction="end"]',
      )
      if (!end || end.disabled) break
      act(() => {
        fireEvent.click(end)
      })
      settle()
    }
    expect(visible()).toBe(TAB)
    await user.click(connectors)
    expect(connectors).toHaveAttribute('aria-selected', 'true')
  })

  it('the buttons never cover the strip: they stay beside it, as before', () => {
    render(<Strip />)
    const list = screen.getByRole('tablist')
    const settle = layout(list)
    act(() => {
      fireEvent.scroll(list)
    })
    settle()
    for (const b of document.querySelectorAll<HTMLElement>(
      '[data-slot="tabs-scroll-button"]',
    )) {
      expect(list.contains(b)).toBe(false)
      expect(b.parentElement?.className).not.toMatch(/\b(absolute|fixed)\b/)
    }
  })
})
