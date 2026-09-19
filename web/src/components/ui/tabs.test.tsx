// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Reach and overflow of the shared tab strip (console-ui-layout-density, 2026-09-06).
// jsdom has no layout engine, so overflow is SIMULATED by defining scrollWidth /
// clientWidth / getBoundingClientRect on the elements and dispatching the events a
// browser would; what is asserted is the component's REACTION — the affordance it
// shows, the scrollLeft it writes, the focus/selection every tab receives. The pixel
// truth of the same strip is measured in the browser evidence
// (an internal design note (not shipped)).
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, type Mock } from 'vitest'
import { Tabs, TabsContent, TabsList, TabsTrigger } from './tabs'

// The eleven labels of /console, the longest strip in the registry.
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

function Strip({
  value,
  defaultValue = 'tab-0',
  orientation,
  listClassName,
}: {
  value?: string
  defaultValue?: string
  orientation?: 'horizontal' | 'vertical'
  listClassName?: string
}) {
  return (
    <Tabs
      value={value}
      defaultValue={value === undefined ? defaultValue : undefined}
      orientation={orientation}
    >
      <TabsList aria-label="Console sections" className={listClassName}>
        {LABELS.map((label, i) => (
          <TabsTrigger key={label} value={`tab-${i}`}>
            {label}
          </TabsTrigger>
        ))}
      </TabsList>
      {LABELS.map((label, i) => (
        <TabsContent key={label} value={`tab-${i}`}>
          panel {i}
        </TabsContent>
      ))}
    </Tabs>
  )
}

function layout(
  el: Element,
  metrics: {
    scrollWidth?: number
    clientWidth?: number
    rect?: Partial<DOMRect>
  },
) {
  if (metrics.scrollWidth !== undefined)
    Object.defineProperty(el, 'scrollWidth', {
      configurable: true,
      get: () => metrics.scrollWidth,
    })
  if (metrics.clientWidth !== undefined)
    Object.defineProperty(el, 'clientWidth', {
      configurable: true,
      get: () => metrics.clientWidth,
    })
  if (metrics.rect) {
    const r = {
      x: 0,
      y: 0,
      top: 0,
      bottom: 36,
      height: 36,
      left: 0,
      right: 0,
      width: 0,
      toJSON: () => ({}),
      ...metrics.rect,
    } as DOMRect
    Object.defineProperty(el, 'getBoundingClientRect', {
      configurable: true,
      value: () => r,
    })
  }
}

/** A tab rect that follows the strip's scrollLeft, as a real layout would. */
function tabLayout(tab: Element, list: Element, left: number, width: number) {
  Object.defineProperty(tab, 'getBoundingClientRect', {
    configurable: true,
    value: () => {
      const l = left - list.scrollLeft
      return {
        x: l,
        y: 0,
        top: 0,
        bottom: 36,
        height: 36,
        left: l,
        right: l + width,
        width,
        toJSON: () => ({}),
      } as DOMRect
    },
  })
}

const shell = () =>
  document.querySelector('[data-slot="tabs-list-shell"]') as HTMLElement
const scrollButtons = () =>
  Array.from(
    document.querySelectorAll<HTMLButtonElement>(
      '[data-slot="tabs-scroll-button"]',
    ),
  )

describe('Tabs — Radix semantics are untouched', () => {
  it('renders one tablist with every tab, roving tabindex and aria-selected', () => {
    render(<Strip />)
    const list = screen.getByRole('tablist', { name: 'Console sections' })
    const tabs = screen.getAllByRole('tab')
    expect(tabs).toHaveLength(LABELS.length)
    expect(list).toHaveAttribute('data-orientation', 'horizontal')
    expect(tabs[0]).toHaveAttribute('aria-selected', 'true')
    for (const tab of tabs.slice(1))
      expect(tab).toHaveAttribute('aria-selected', 'false')
    expect(screen.getByRole('tabpanel')).toHaveTextContent('panel 0')
  })

  it('keeps the roving tabindex: once focused, only the active tab is the stop', async () => {
    const user = userEvent.setup()
    render(<Strip />)
    const tabs = screen.getAllByRole('tab')
    await user.tab()
    expect(tabs[0]).toHaveAttribute('tabindex', '0')
    for (const tab of tabs.slice(1))
      expect(tab).toHaveAttribute('tabindex', '-1')
  })

  it('puts className on the shell and every other prop on the tablist', () => {
    render(<Strip listClassName="mx-3 mt-2" />)
    expect(shell()).toHaveClass('mx-3', 'mt-2')
    expect(screen.getByRole('tablist')).not.toHaveClass('mx-3')
    expect(screen.getByRole('tablist')).toHaveAttribute(
      'aria-label',
      'Console sections',
    )
    // The shell is not an ARIA landmark or widget: the tablist stays the only role.
    expect(shell()).not.toHaveAttribute('role')
  })
})

describe('Tabs — every tab is reachable and activatable by keyboard', () => {
  it('ArrowRight visits and selects all eleven tabs from one Tab stop; End/Home jump', async () => {
    const user = userEvent.setup()
    render(<Strip />)
    const tabs = screen.getAllByRole('tab')
    await user.tab()
    expect(document.activeElement).toBe(tabs[0])
    const visited = new Set<string>([tabs[0].textContent ?? ''])
    for (let i = 1; i < LABELS.length; i++) {
      await user.keyboard('{ArrowRight}')
      const active = document.activeElement as HTMLElement
      expect(active).toBe(tabs[i])
      // Automatic activation: focus IS selection, so the panel follows the focus.
      expect(active).toHaveAttribute('aria-selected', 'true')
      expect(screen.getByRole('tabpanel')).toHaveTextContent(`panel ${i}`)
      visited.add(active.textContent ?? '')
    }
    expect([...visited]).toEqual(LABELS)
    await user.keyboard('{Home}')
    expect(document.activeElement).toBe(tabs[0])
    await user.keyboard('{End}')
    expect(document.activeElement).toBe(tabs[LABELS.length - 1])
    expect(tabs[LABELS.length - 1]).toHaveAttribute('aria-selected', 'true')
    // The strip only ever takes ONE Tab stop; leaving it lands outside the tabs.
    await user.tab()
    expect(document.activeElement?.getAttribute('role')).not.toBe('tab')
  })
})

describe('Tabs — overflow affordance', () => {
  it('reports the edge that hides tabs and shows a pointer button only there', () => {
    render(<Strip />)
    const list = screen.getByRole('tablist')
    expect(shell()).toHaveAttribute('data-overflow', 'none')
    expect(scrollButtons()).toHaveLength(0)

    layout(list, { scrollWidth: 1100, clientWidth: 600 })
    list.scrollLeft = 0
    fireEvent.scroll(list)
    expect(shell()).toHaveAttribute('data-overflow', 'end')
    expect(scrollButtons().map((b) => b.dataset.direction)).toEqual(['end'])

    list.scrollLeft = 200
    fireEvent.scroll(list)
    expect(shell()).toHaveAttribute('data-overflow', 'both')
    expect(scrollButtons().map((b) => b.dataset.direction)).toEqual([
      'start',
      'end',
    ])

    list.scrollLeft = 500
    fireEvent.scroll(list)
    expect(shell()).toHaveAttribute('data-overflow', 'start')
    expect(scrollButtons().map((b) => b.dataset.direction)).toEqual(['start'])

    layout(list, { scrollWidth: 600, clientWidth: 600 })
    fireEvent.scroll(list)
    expect(shell()).toHaveAttribute('data-overflow', 'none')
    expect(scrollButtons()).toHaveLength(0)
  })

  it('scroll buttons are pointer-only (aria-hidden, no Tab stop) and move the strip', () => {
    render(<Strip />)
    const list = screen.getByRole('tablist')
    layout(list, { scrollWidth: 1100, clientWidth: 600 })
    list.scrollLeft = 0
    fireEvent.scroll(list)
    const [end] = scrollButtons()
    expect(end).toHaveAttribute('aria-hidden', 'true')
    expect(end).toHaveAttribute('tabindex', '-1')
    fireEvent.click(end)
    // 60 % of the visible strip, never less than 80 px (jsdom has no scrollTo, so
    // the button takes the instant path; a browser animates it).
    expect(list.scrollLeft).toBe(360)
    fireEvent.scroll(list)
    const [start] = scrollButtons()
    expect(start.dataset.direction).toBe('start')
    fireEvent.click(start)
    expect(list.scrollLeft).toBe(0)
  })

  it('leaves a vertical list alone', () => {
    render(<Strip orientation="vertical" />)
    const list = screen.getByRole('tablist')
    expect(list).toHaveAttribute('data-orientation', 'vertical')
    layout(list, { scrollWidth: 1100, clientWidth: 600 })
    fireEvent.scroll(list)
    expect(shell()).toHaveAttribute('data-overflow', 'none')
    expect(scrollButtons()).toHaveLength(0)
  })
})

describe('Tabs — the focused and the selected tab stay visible', () => {
  it('reveals a focused tab by scrolling the strip, never the page', async () => {
    const user = userEvent.setup()
    render(<Strip />)
    const list = screen.getByRole('tablist')
    const tabs = screen.getAllByRole('tab')
    layout(list, {
      scrollWidth: 1100,
      clientWidth: 600,
      rect: { left: 0, right: 600, width: 600 },
    })
    // The last tab sits 100 px past the right edge of the 600 px strip.
    tabLayout(tabs[10], list, 620, 80)
    list.scrollLeft = 0
    const scrollIntoView = Element.prototype.scrollIntoView as Mock
    const before = scrollIntoView.mock.calls.length
    await user.tab()
    await user.keyboard('{End}')
    expect(document.activeElement).toBe(tabs[10])
    // right edge 700 > 600 − 36 clearance ⇒ scrollLeft += 700 − 600 + 36
    expect(list.scrollLeft).toBe(136)
    expect(scrollIntoView.mock.calls.length).toBe(before)

    // And back: the first tab (layout left 0, width 80) is now hidden under the
    // START fade at scrollLeft 136; Home focuses it and the strip scrolls left.
    tabLayout(tabs[0], list, 0, 80)
    await user.keyboard('{Home}')
    expect(document.activeElement).toBe(tabs[0])
    // left −136 < 0 + 36 ⇒ scrollLeft += −136 − 36 ⇒ clamped at 0
    expect(list.scrollLeft).toBe(0)
  })

  it('reveals a focused-but-not-selected tab (manual activation) — focus alone counts', async () => {
    const user = userEvent.setup()
    render(
      <Tabs defaultValue="tab-0" activationMode="manual">
        <TabsList aria-label="Manual">
          {LABELS.map((label, i) => (
            <TabsTrigger key={label} value={`tab-${i}`}>
              {label}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>,
    )
    const list = screen.getByRole('tablist')
    const tabs = screen.getAllByRole('tab')
    layout(list, {
      scrollWidth: 1100,
      clientWidth: 600,
      rect: { left: 0, right: 600, width: 600 },
    })
    tabLayout(tabs[1], list, 560, 100)
    list.scrollLeft = 0
    await user.tab()
    await user.keyboard('{ArrowRight}')
    expect(document.activeElement).toBe(tabs[1])
    // Manual activation: the selection did NOT move, so only the focus path can reveal.
    expect(tabs[1]).toHaveAttribute('aria-selected', 'false')
    expect(tabs[0]).toHaveAttribute('aria-selected', 'true')
    // right 660 > 600 − 36 ⇒ scrollLeft += 660 − 600 + 36
    expect(list.scrollLeft).toBe(96)
  })

  it('reveals the tab an owner selects (deep link / controlled value)', async () => {
    const { rerender } = render(<Strip value="tab-0" />)
    const list = screen.getByRole('tablist')
    const tabs = screen.getAllByRole('tab')
    layout(list, {
      scrollWidth: 1100,
      clientWidth: 600,
      rect: { left: 0, right: 600, width: 600 },
    })
    tabLayout(tabs[10], list, 900, 80)
    list.scrollLeft = 0
    rerender(<Strip value="tab-10" />)
    expect(tabs[10]).toHaveAttribute('aria-selected', 'true')
    // The MutationObserver delivers after the commit: right 980 > 600 − 36.
    await waitFor(() => expect(list.scrollLeft).toBe(980 - 600 + 36))
  })

  it('does not move a strip that has no layout or no overflow', async () => {
    const user = userEvent.setup()
    render(<Strip />)
    const list = screen.getByRole('tablist')
    const tabs = screen.getAllByRole('tab')
    list.scrollLeft = 0
    await user.tab()
    await user.keyboard('{End}')
    expect(document.activeElement).toBe(tabs[10])
    expect(list.scrollLeft).toBe(0)
    layout(list, {
      scrollWidth: 600,
      clientWidth: 600,
      rect: { left: 0, right: 600, width: 600 },
    })
    tabLayout(tabs[5], list, 700, 80)
    await user.keyboard('{Home}')
    act(() => tabs[5].focus())
    expect(list.scrollLeft).toBe(0)
  })

  it('re-measures when tabs mount or unmount', async () => {
    function Conditional({ live }: { live: boolean }) {
      return (
        <Tabs defaultValue="overview">
          <TabsList>
            <TabsTrigger value="overview">Overview</TabsTrigger>
            {live && <TabsTrigger value="live">Live</TabsTrigger>}
            <TabsTrigger value="details">Details</TabsTrigger>
          </TabsList>
          <TabsContent value="overview">o</TabsContent>
          <TabsContent value="live">l</TabsContent>
          <TabsContent value="details">d</TabsContent>
        </Tabs>
      )
    }
    const { rerender } = render(<Conditional live={false} />)
    const list = screen.getByRole('tablist')
    expect(screen.getAllByRole('tab')).toHaveLength(2)
    layout(list, { scrollWidth: 400, clientWidth: 300 })
    await act(async () => {
      rerender(<Conditional live />)
    })
    expect(screen.getAllByRole('tab')).toHaveLength(3)
    await waitFor(() => expect(shell()).toHaveAttribute('data-overflow', 'end'))
  })
})

// ---------------------------------------------------------------------------
// Geometry changes (independent review F1, 2026-09-06): a resize, a sidebar rail, a
// font landing or a label changing language moves the boxes without any selection
// or focus event. jsdom's ResizeObserver stub (src/test/setup.ts) never calls back,
// so these tests install a controllable one and fire it themselves, after moving the
// mocked boxes the way a real layout would.
describe('Tabs — the current tab is revealed again after geometry changes', () => {
  type Callback = (
    entries: ResizeObserverEntry[],
    observer: ResizeObserver,
  ) => void
  const observers: { cb: Callback; targets: Set<Element> }[] = []
  const original = window.ResizeObserver

  beforeEach(() => {
    observers.length = 0
    class ControllableResizeObserver {
      private rec = { cb: (() => {}) as Callback, targets: new Set<Element>() }
      constructor(cb: Callback) {
        this.rec.cb = cb
        observers.push(this.rec)
      }
      observe(target: Element) {
        this.rec.targets.add(target)
      }
      unobserve(target: Element) {
        this.rec.targets.delete(target)
      }
      disconnect() {
        this.rec.targets.clear()
      }
    }
    window.ResizeObserver =
      ControllableResizeObserver as unknown as typeof ResizeObserver
  })
  afterEach(() => {
    window.ResizeObserver = original
  })

  /** Fire every live observer as a browser would after layout changed. */
  const fireResize = () =>
    act(() => {
      for (const o of observers) if (o.targets.size) o.cb([], o as never)
    })

  it('observes the strip and every tab, and re-observes after tabs mount', async () => {
    const { rerender } = render(<Strip />)
    const list = screen.getByRole('tablist')
    const live = observers.filter((o) => o.targets.size)
    expect(live).toHaveLength(1)
    expect(live[0].targets.has(list)).toBe(true)
    for (const tab of screen.getAllByRole('tab'))
      expect(live[0].targets.has(tab)).toBe(true)
    rerender(<Strip />)
    expect(observers.filter((o) => o.targets.size)).toHaveLength(1)
  })

  it('the reviewer case: selected AND focused last tab, strip shrinks 1440 → 390, tab comes back into the strip', async () => {
    const user = userEvent.setup()
    render(<Strip />)
    const list = screen.getByRole('tablist')
    const tabs = screen.getAllByRole('tab')
    // 1440: the strip is 1152 px wide (264–1416), the last tab sits at 1281–1416.
    layout(list, {
      scrollWidth: 1286,
      clientWidth: 1152,
      rect: { left: 264, right: 1416, width: 1152 },
    })
    tabLayout(tabs[10], list, 1281 + 134, 135)
    list.scrollLeft = 134
    await user.tab()
    await user.keyboard('{End}')
    expect(tabs[10]).toHaveAttribute('aria-selected', 'true')
    expect(document.activeElement).toBe(tabs[10])
    expect(list.scrollLeft).toBe(134)
    // 390: the strip is now 16–374 (358 px); with scrollLeft still 134 the same tab
    // reports 1033–1168, entirely outside — as measured.
    layout(list, {
      scrollWidth: 1286,
      clientWidth: 358,
      rect: { left: 16, right: 374, width: 358 },
    })
    tabLayout(tabs[10], list, 1033 + 134, 135)
    expect(tabs[10].getBoundingClientRect().left).toBe(1033)
    fireResize()
    // right 1168 > 374 − 36 ⇒ scrollLeft += 1168 − 374 + 36 = 830 ⇒ 964, clamped to
    // max 1286 − 358 = 928 ⇒ the tab now ends at 1168 − (928 − 134) = 374, inside.
    expect(list.scrollLeft).toBe(928)
    const box = tabs[10].getBoundingClientRect()
    expect(box.left).toBeGreaterThanOrEqual(16)
    expect(box.right).toBeLessThanOrEqual(374)
    // Nothing else moved: selection, focus and the page untouched.
    expect(tabs[10]).toHaveAttribute('aria-selected', 'true')
    expect(document.activeElement).toBe(tabs[10])
    expect((Element.prototype.scrollIntoView as Mock).mock.calls.length).toBe(0)
  })

  it('with focus elsewhere, the SELECTED tab is the one revealed', async () => {
    render(
      <>
        <Strip value="tab-10" />
        <button type="button">elsewhere</button>
      </>,
    )
    const list = screen.getByRole('tablist')
    const tabs = screen.getAllByRole('tab')
    layout(list, {
      scrollWidth: 1286,
      clientWidth: 358,
      rect: { left: 16, right: 374, width: 358 },
    })
    tabLayout(tabs[10], list, 1033 + 134, 135)
    list.scrollLeft = 134
    screen.getByRole('button', { name: 'elsewhere' }).focus()
    fireResize()
    expect(list.scrollLeft).toBe(928)
    expect(document.activeElement).toBe(
      screen.getByRole('button', { name: 'elsewhere' }),
    )
    expect(tabs[10]).toHaveAttribute('aria-selected', 'true')
  })

  it('manual activation: the FOCUSED (unselected) tab is kept visible, without selecting it or moving focus', async () => {
    const user = userEvent.setup()
    render(
      <Tabs defaultValue="tab-0" activationMode="manual">
        <TabsList aria-label="Manual">
          {LABELS.map((label, i) => (
            <TabsTrigger key={label} value={`tab-${i}`}>
              {label}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>,
    )
    const list = screen.getByRole('tablist')
    const tabs = screen.getAllByRole('tab')
    layout(list, {
      scrollWidth: 1100,
      clientWidth: 600,
      rect: { left: 0, right: 600, width: 600 },
    })
    // Tab 0 (selected) sits at the start; tab 3 (to be focused) at 300–400.
    tabLayout(tabs[0], list, 0, 100)
    tabLayout(tabs[3], list, 300, 100)
    list.scrollLeft = 0
    await user.tab()
    await user.keyboard('{ArrowRight}{ArrowRight}{ArrowRight}')
    expect(document.activeElement).toBe(tabs[3])
    expect(tabs[3]).toHaveAttribute('aria-selected', 'false')
    expect(tabs[0]).toHaveAttribute('aria-selected', 'true')
    // The strip narrows to 250 px: tab 3 (300–400) is now outside, tab 0 is not.
    layout(list, {
      scrollWidth: 1100,
      clientWidth: 250,
      rect: { left: 0, right: 250, width: 250 },
    })
    fireResize()
    // right 400 > 250 − 36 ⇒ scrollLeft += 400 − 250 + 36 = 186
    expect(list.scrollLeft).toBe(186)
    expect(document.activeElement).toBe(tabs[3])
    expect(tabs[3]).toHaveAttribute('aria-selected', 'false')
    expect(tabs[0]).toHaveAttribute('aria-selected', 'true')
  })

  it('an ordinary pointer/touch scroll of the strip is NOT snapped back', () => {
    render(<Strip value="tab-0" />)
    const list = screen.getByRole('tablist')
    const tabs = screen.getAllByRole('tab')
    layout(list, {
      scrollWidth: 1100,
      clientWidth: 600,
      rect: { left: 0, right: 600, width: 600 },
    })
    tabLayout(tabs[0], list, 0, 100)
    // The operator scrolls the selected tab out of view on purpose.
    list.scrollLeft = 400
    fireEvent.scroll(list)
    expect(list.scrollLeft).toBe(400)
    expect(shell()).toHaveAttribute('data-overflow', 'both')
  })

  it('a tab that mounts later (child geometry change) re-reveals the current tab', async () => {
    function Conditional({ live }: { live: boolean }) {
      return (
        <Tabs value="details">
          <TabsList>
            <TabsTrigger value="overview">Overview</TabsTrigger>
            {live && <TabsTrigger value="live">Live</TabsTrigger>}
            <TabsTrigger value="details">Details</TabsTrigger>
          </TabsList>
        </Tabs>
      )
    }
    const { rerender } = render(<Conditional live={false} />)
    const list = screen.getByRole('tablist')
    layout(list, {
      scrollWidth: 400,
      clientWidth: 300,
      rect: { left: 0, right: 300, width: 300 },
    })
    // Selected "Details" sits at 200–300 (visible); once "Live" mounts it is pushed
    // to 320–420, outside the 300 px strip.
    const details = screen.getByRole('tab', { name: 'Details' })
    tabLayout(details, list, 200, 100)
    list.scrollLeft = 0
    // The mounted tab also makes the strip's content 120 px longer.
    layout(list, { scrollWidth: 520, clientWidth: 300 })
    tabLayout(details, list, 320, 100)
    await act(async () => {
      rerender(<Conditional live />)
    })
    // MutationObserver (childList) delivers after the commit.
    await waitFor(() => expect(list.scrollLeft).toBe(420 - 300 + 36))
  })

  it('vertical lists ignore geometry changes as before', () => {
    render(<Strip orientation="vertical" value="tab-10" />)
    const list = screen.getByRole('tablist')
    layout(list, {
      scrollWidth: 1286,
      clientWidth: 358,
      rect: { left: 16, right: 374, width: 358 },
    })
    tabLayout(screen.getAllByRole('tab')[10], list, 1033, 135)
    list.scrollLeft = 0
    fireResize()
    expect(list.scrollLeft).toBe(0)
  })
})
