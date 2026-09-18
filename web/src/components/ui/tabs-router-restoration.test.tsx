// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The tab strip against the router's scroll restoration (console-tab-scroll-restoration,
// 2026-09-06). These tests drive the REAL @tanstack/react-router — `scrollRestoration:
// true` as in app/router.tsx, a memory history, a route whose component mirrors the
// Console's tab setter — and wait for the router's own `onRendered` event, the moment its
// restoration subscriber writes cached scroll offsets back into the DOM, before asserting.
//
// TIMING, reproduced rather than assumed. The router snapshots scroll offsets at
// `onBeforeLoad`, synchronously inside `navigate()`, for every element that fired a
// `scroll` event since the previous render. A reveal happens BEFORE the navigation it
// leads to (focusin → the strip scrolls → Radix selects → the owner navigates), and a
// browser dispatches the strip's `scroll` event asynchronously, AFTER that snapshot: the
// cache therefore never holds the reveal, and what the router writes back after render is
// the strip's position from an earlier location. jsdom fires no scroll events at all, so
// each test dispatches, by hand and at that late moment, the one event Chromium delivered
// late in the browser evidence (an internal design note (not shipped),
// `known-horizontal-restoration`: 3/11 selected tabs visible at 390 px, scrollLeft 0).
//
// Geometry is the eleven /console tabs as measured at 390 px in that evidence; jsdom has
// no layout, so the boxes are mocked and follow the strip's scrollLeft like a real layout.
import { act, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
  useNavigate,
  useSearch,
  type AnyRouter,
} from '@tanstack/react-router'
import { useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { Tabs, TabsContent, TabsList, TabsTrigger } from './tabs'

/** /console at 390 px: [value, layout left at scrollLeft 0, width], strip 16–374. */
const CONSOLE_TABS: readonly [string, number, number][] = [
  ['people', 16, 127],
  ['agents', 147, 72],
  ['sso', 223, 88],
  ['scopes', 315, 107],
  ['roles', 426, 149],
  ['bindings', 579, 133],
  ['secrets', 716, 75],
  ['connectors', 795, 100],
  ['wsConnectors', 899, 177],
  ['apiKeys', 1080, 83],
  ['license', 1167, 135],
]
const STRIP = { left: 16, right: 374, clientWidth: 358, scrollWidth: 1286 }
const MAX_SCROLL = STRIP.scrollWidth - STRIP.clientWidth // 928
/** revealTab keeps a tab 36 px clear of the edge it scrolls toward. */
const CLEARANCE = 36
/** The scrollLeft the reveal writes for a tab hidden past the right edge, from 0. */
const revealFromZero = (value: string) => {
  const [, left, width] = CONSOLE_TABS.find((t) => t[0] === value)!
  return Math.min(MAX_SCROLL, left + width - STRIP.right + CLEARANCE)
}

/** The scrollLeft writes a keyboard walk over `order` produces, from the geometry alone. */
function expectedReveals(order: string[]): number[] {
  let scrollLeft = 0
  const writes: number[] = []
  for (const value of order) {
    const [, left, width] = CONSOLE_TABS.find((t) => t[0] === value)!
    const l = left - scrollLeft
    const r = l + width
    if (l < STRIP.left + CLEARANCE) {
      scrollLeft = Math.max(0, scrollLeft + l - STRIP.left - CLEARANCE)
      writes.push(scrollLeft)
    } else if (r > STRIP.right - CLEARANCE) {
      scrollLeft = Math.min(
        MAX_SCROLL,
        scrollLeft + r - STRIP.right + CLEARANCE,
      )
      writes.push(scrollLeft)
    }
  }
  return writes
}

function rect(left: number, width: number): DOMRect {
  return {
    x: left,
    y: 0,
    top: 0,
    bottom: 36,
    height: 36,
    left,
    right: left + width,
    width,
    toJSON: () => ({}),
  } as DOMRect
}

/** Give the strip and its tabs the 390 px console geometry, following scrollLeft. */
function layoutConsoleStrip(list: HTMLElement) {
  Object.defineProperty(list, 'scrollWidth', {
    configurable: true,
    get: () => STRIP.scrollWidth,
  })
  Object.defineProperty(list, 'clientWidth', {
    configurable: true,
    get: () => STRIP.clientWidth,
  })
  Object.defineProperty(list, 'getBoundingClientRect', {
    configurable: true,
    value: () => rect(STRIP.left, STRIP.clientWidth),
  })
  for (const [value, left, width] of CONSOLE_TABS) {
    const tab = list.querySelector(`[role="tab"][data-value="${value}"]`)!
    Object.defineProperty(tab, 'getBoundingClientRect', {
      configurable: true,
      value: () => rect(left - list.scrollLeft, width),
    })
  }
}

const visible = (tab: Element) => {
  const b = tab.getBoundingClientRect()
  return b.left >= STRIP.left - 0.5 && b.right <= STRIP.right + 0.5
}
const strip = () => screen.getByRole('tablist') as HTMLElement
const tab = (value: string) =>
  document.querySelector(`[role="tab"][data-value="${value}"]`) as HTMLElement

/** Resolves once the router has emitted `onRendered` for a location with this ?tab=. */
function rendered(router: AnyRouter, tabValue: string | undefined) {
  return new Promise<void>((resolve) => {
    const unsubscribe = router.subscribe('onRendered', (event) => {
      const search = event.toLocation.search as { tab?: string }
      if (search.tab === tabValue) {
        unsubscribe()
        resolve()
      }
    })
  })
}

/**
 * The strip as /console composes it. `setter` mirrors console-view.tsx: local state plus a
 * replace navigation with `?tab=`; `setterOptions` is what that navigation adds — the
 * product passes `resetScroll: false`, the parent passed nothing. `follow` mirrors a value
 * driven by the URL, which is how a Link or a command changes the tab from outside.
 */
function ConsoleLike({
  mode,
  setterOptions,
  orientation,
}: {
  mode: 'setter' | 'follow'
  setterOptions?: Record<string, unknown>
  orientation?: 'horizontal' | 'vertical'
}) {
  const navigate = useNavigate()
  const search = useSearch({ strict: false }) as { tab?: string }
  const [local, setLocal] = useState(search.tab ?? 'people')
  const value = mode === 'follow' ? (search.tab ?? 'people') : local
  const onValueChange = (next: string) => {
    if (mode === 'setter') setLocal(next)
    void navigate({
      search: (prev: Record<string, unknown>) => ({ ...prev, tab: next }),
      replace: true,
      ...setterOptions,
    } as never)
  }
  return (
    <Tabs value={value} onValueChange={onValueChange} orientation={orientation}>
      <TabsList
        aria-label="Console sections"
        // Geometry the moment the strip exists — before any effect, and before the
        // router's restore — as a browser's layout would be.
        ref={(node) => {
          if (node) layoutConsoleStrip(node)
        }}
        // The router names an element by its nth-child path unless it carries this
        // attribute. The shell mounts a scroll button BEFORE the strip once it is
        // scrolled ('both' overflow), which shifts that path; in a browser the scroll
        // event that triggers it arrives after the router's render, in jsdom the
        // MutationObserver's measure precedes it. A stable id removes that jsdom-only
        // difference so the router's write reaches the strip exactly as it did in the
        // browser evidence.
        data-scroll-restoration-id="console-tabs"
      >
        {CONSOLE_TABS.map(([v]) => (
          <TabsTrigger key={v} value={v} data-value={v}>
            {v}
          </TabsTrigger>
        ))}
      </TabsList>
      {CONSOLE_TABS.map(([v]) => (
        <TabsContent key={v} value={v}>
          panel {v}
        </TabsContent>
      ))}
    </Tabs>
  )
}

function makeRouter(
  initial: string,
  consoleProps: Parameters<typeof ConsoleLike>[0],
) {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const consoleRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/console',
    validateSearch: (raw: Record<string, unknown>) => ({
      tab: typeof raw.tab === 'string' ? raw.tab : undefined,
      q: typeof raw.q === 'string' ? raw.q : undefined,
    }),
    component: () => <ConsoleLike {...consoleProps} />,
  })
  const sessionsRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/sessions',
    component: () => <h1>sessions</h1>,
  })
  return createRouter({
    routeTree: rootRoute.addChildren([consoleRoute, sessionsRoute]),
    history: createMemoryHistory({ initialEntries: [initial] }),
    // As app/router.tsx: the element restoration under test is this option's.
    scrollRestoration: true,
  })
}

async function mount(
  initial: string,
  consoleProps: Parameters<typeof ConsoleLike>[0],
) {
  const router = makeRouter(initial, consoleProps)
  await act(async () => {
    render(<RouterProvider router={router} />)
  })
  await screen.findByRole('tablist')
  return router
}

/** Record every write to the strip's scrollLeft, whoever performs it. */
function trapScrollLeft(list: HTMLElement) {
  const proto = Object.getOwnPropertyDescriptor(
    Element.prototype,
    'scrollLeft',
  )!
  const writes: number[] = []
  Object.defineProperty(list, 'scrollLeft', {
    configurable: true,
    get() {
      return proto.get!.call(this) as number
    },
    set(v: number) {
      writes.push(v)
      proto.set!.call(this, v)
    },
  })
  return writes
}

let scrollTo: ReturnType<typeof vi.spyOn>
beforeEach(() => {
  // jsdom has no window.scrollTo; the router calls it to reset the page on a navigation
  // that takes part in restoration. Spied so a tab switch can be shown NOT to.
  scrollTo = vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
})
afterEach(() => {
  scrollTo.mockRestore()
})

describe('Tabs × router scroll restoration — the measured /console walk', () => {
  it('keyboard End → Home → ArrowRight walk: every selected tab stays visible; the router neither moves the strip nor the page', async () => {
    const user = userEvent.setup()
    const router = await mount('/console?tab=people', {
      mode: 'setter',
      setterOptions: { resetScroll: false },
    })
    const list = strip()
    expect(list.scrollLeft).toBe(0)
    const writes = trapScrollLeft(list)
    // The first render resets the window once, as any first load does; from here on
    // the walk must add none.
    scrollTo.mockClear()

    // End: the reveal writes 928 and Radix selects license → the owner navigates.
    let done = rendered(router, 'license')
    await user.tab()
    expect(document.activeElement).toBe(tab('people'))
    await user.keyboard('{End}')
    await act(() => done)
    expect(list.scrollLeft).toBe(MAX_SCROLL)
    // The browser's late scroll event: the router now tracks the strip (at 928).
    fireEvent.scroll(list)

    // Home: reveal writes 0, navigation snapshots the strip AT 0 under the license key
    // and copies that entry onto the people key; the walk below inherits it.
    done = rendered(router, 'people')
    await user.keyboard('{Home}')
    await act(() => done)
    expect(list.scrollLeft).toBe(0)
    fireEvent.scroll(list)

    // ArrowRight through all ten remaining tabs. In the browser evidence the fourth
    // ("scopes", 315–422) was the first to disappear: its reveal wrote 84, and the
    // router wrote 0 back after render. Here NO late scroll event is dispatched, which
    // is the ordering that lost every reveal in that evidence.
    const seen: { value: string; scrollLeft: number; visible: boolean }[] = []
    for (const [value] of CONSOLE_TABS.slice(1)) {
      done = rendered(router, value)
      await user.keyboard('{ArrowRight}')
      await act(() => done)
      const current = tab(value)
      expect(document.activeElement).toBe(current)
      expect(current).toHaveAttribute('aria-selected', 'true')
      seen.push({
        value,
        scrollLeft: list.scrollLeft,
        visible: visible(current),
      })
    }
    expect(seen.filter((s) => s.visible).map((s) => s.value)).toEqual(
      CONSOLE_TABS.slice(1).map(([v]) => v),
    )
    // The first hidden tab's reveal survived the router's render: 84, not 0.
    expect(seen.find((s) => s.value === 'scopes')!.scrollLeft).toBe(
      revealFromZero('scopes'),
    )
    // Every write to the strip was a reveal — none came from the router. The oracle is
    // the geometry alone: from the strip's current position, a tab past the right edge
    // is revealed at (its right edge − strip width + clearance), a tab past the left
    // edge at (its left edge − clearance), both clamped to the scroll range. A reveal
    // may run twice for one tab (focusin, then the data-state flip) and write the same
    // clamped value twice, so consecutive duplicates are folded; a router write of the
    // cached 0 after the 84 would survive the fold and break the sequence.
    const folded = writes.filter((w, i) => i === 0 || w !== writes[i - 1])
    expect(folded).toEqual(
      expectedReveals([
        'people', // the initial focus
        'license', // End
        'people', // Home
        ...CONSOLE_TABS.slice(1).map(([v]) => v), // ArrowRight ×10
      ]),
    )
    expect(
      writes.slice(writes.indexOf(revealFromZero('scopes'))),
    ).not.toContain(0)
    // No page jump: a tab switch never asked the router to reset the window.
    expect(scrollTo).not.toHaveBeenCalled()
  })

  it('parent witness — a setter WITHOUT resetScroll:false: the router writes the stale 0 back after the reveal, and the strip re-reveals in the same event', async () => {
    const user = userEvent.setup()
    const router = await mount('/console?tab=people', { mode: 'setter' })
    const list = strip()
    fireEvent.scroll(list) // tracked at 0, as after the Home reveal in the evidence
    const writes = trapScrollLeft(list)

    for (const value of ['agents', 'sso', 'scopes']) {
      const done = rendered(router, value)
      await user.keyboard(
        value === 'agents' ? '{Tab}{ArrowRight}' : '{ArrowRight}',
      )
      await act(() => done)
    }
    // The reproduced timing: reveal 84 first, then the router's stale 0 after render.
    const first84 = writes.indexOf(revealFromZero('scopes'))
    expect(first84).toBeGreaterThanOrEqual(0)
    expect(writes.slice(first84 + 1)).toContain(0)
    // And the ordered repair: the strip's own subscriber ran after the router's, so the
    // last write is the reveal again and the selected tab is visible.
    expect(writes.at(-1)).toBe(revealFromZero('scopes'))
    expect(list.scrollLeft).toBe(revealFromZero('scopes'))
    expect(visible(tab('scopes'))).toBe(true)
    expect(document.activeElement).toBe(tab('scopes'))
    // This is what resetScroll:false also removes: the window reset on every tab switch.
    expect(scrollTo).toHaveBeenCalled()
  })
})

describe('Tabs × router scroll restoration — navigations the setter does not own', () => {
  it('a ?tab= change from outside (Link, command, URL-driven value): the router restores the stale strip, the strip reveals the new tab, focus is not touched', async () => {
    const router = await mount('/console?tab=people', { mode: 'follow' })
    const list = strip()
    fireEvent.scroll(list) // tracked at 0
    const outside = document.body
    expect(document.activeElement).toBe(outside)

    const done = rendered(router, 'license')
    await act(async () => {
      await router.navigate({
        to: '/console',
        search: { tab: 'license' },
      } as never)
      await done
    })
    expect(tab('license')).toHaveAttribute('aria-selected', 'true')
    expect(list.scrollLeft).toBe(MAX_SCROLL)
    expect(visible(tab('license'))).toBe(true)
    // No focus theft: revealing never focuses.
    expect(document.activeElement).toBe(outside)
  })

  it('Back to a cached location remounts the strip: the selected tab is visible after the router restored its cache', async () => {
    const user = userEvent.setup()
    const router = await mount('/console?tab=people', {
      mode: 'setter',
      setterOptions: { resetScroll: false },
    })
    fireEvent.scroll(strip()) // tracked at 0
    let done = rendered(router, 'license')
    await user.click(tab('license'))
    await act(() => done)
    expect(strip().scrollLeft).toBe(MAX_SCROLL)
    // Its scroll event is never tracked (lost to the render, as in the browser), so the
    // license location keeps the stale 0 it inherited from people.

    done = rendered(router, undefined)
    await act(async () => {
      await router.navigate({ to: '/sessions' } as never)
      await done
    })
    expect(screen.queryByRole('tablist')).toBeNull()

    done = rendered(router, 'license')
    await act(async () => {
      router.history.back()
      await done
    })
    await screen.findByRole('tablist')
    await act(async () => {})
    expect(tab('license')).toHaveAttribute('aria-selected', 'true')
    expect(strip().scrollLeft).toBe(MAX_SCROLL)
    expect(visible(tab('license'))).toBe(true)
  })

  it('an operator scroll is NOT snapped back by a navigation whose restore leaves the strip where it was', async () => {
    const router = await mount('/console?tab=people', { mode: 'follow' })
    const list = strip()
    // The operator pans the selected tab out of view on purpose; the router tracks it.
    list.scrollLeft = 400
    fireEvent.scroll(list)
    expect(visible(tab('people'))).toBe(false)

    // A same-view navigation that does not change the tab (a filter, say): the router
    // snapshots 400 and restores 400 — unchanged, so the strip stays under the
    // operator's control even though the selected tab is hidden.
    const done = rendered(router, 'people')
    await act(async () => {
      await router.navigate({
        to: '/console',
        search: { tab: 'people', q: 'deny' },
      } as never)
      await done
    })
    expect(list.scrollLeft).toBe(400)
    expect(visible(tab('people'))).toBe(false)
  })

  it('a vertical list is left alone by the router path as by every other', async () => {
    const router = await mount('/console?tab=people', {
      mode: 'follow',
      orientation: 'vertical',
    })
    const list = strip()
    expect(list).toHaveAttribute('data-orientation', 'vertical')
    fireEvent.scroll(list)
    const done = rendered(router, 'license')
    await act(async () => {
      await router.navigate({
        to: '/console',
        search: { tab: 'license' },
      } as never)
      await done
    })
    expect(tab('license')).toHaveAttribute('aria-selected', 'true')
    expect(list.scrollLeft).toBe(0)
  })
})
