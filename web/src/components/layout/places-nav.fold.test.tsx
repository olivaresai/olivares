// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE PLACES FOLD ON THE ROOM THEIR LABELS NEED, NOT ON A FIXED WIDTH (issue #2764). jsdom
// has no layout, so these tests supply the one browser fact the fold reads: the width of the
// places box and the width of the hidden rows the component measures (all labels, and the
// current place's label alone). A wrapped box folds the other labels to their icons; a box
// with no room folds the current one too. In both states every place keeps its accessible
// name, so the fold is a visual choice and never a loss of the link's name.
import { act, screen, within } from '@testing-library/react'
import type { ComponentProps } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'

const routerState = vi.hoisted(() => ({
  pathname: '/sessions',
  search: {} as Record<string, unknown>,
}))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({
    select,
  }: {
    select: (s: {
      location: { pathname: string; search: Record<string, unknown> }
    }) => unknown
  }) =>
    select({
      location: { pathname: routerState.pathname, search: routerState.search },
    }),
  useNavigate: () => () => undefined,
  Link: ({
    children,
    to,
    search,
    activeProps,
    activeOptions,
    ...props
  }: ComponentProps<'a'> & {
    to?: string
    search?: Record<string, string>
    activeProps?: Record<string, string>
    activeOptions?: { exact?: boolean }
  }) => {
    void activeOptions
    const href = search ? `${to}?${new URLSearchParams(search)}` : to
    return (
      <a href={href} {...props} {...activeProps}>
        {children}
      </a>
    )
  },
}))

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    can: () => true,
    grants: [],
    isSuperadmin: true,
    principal: { superadmin: true, grants: [] },
    activeTenant: null,
    setActiveTenant: () => {},
  }),
}))

import { PlacesNav } from './places-nav'

const observers = new Set<FakeResizeObserver>()
class FakeResizeObserver {
  cb: ResizeObserverCallback
  targets = new Set<Element>()
  constructor(cb: ResizeObserverCallback) {
    this.cb = cb
  }
  observe(target: Element) {
    this.targets.add(target)
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
afterEach(() => {
  vi.unstubAllGlobals()
  routerState.pathname = '/sessions'
  routerState.search = {}
})

const NAMES = ['Now', 'Sessions', 'Tools', 'Rules', 'Ledger'] as const

function rect(width: number): DOMRect {
  return {
    width,
    height: 36,
    x: 0,
    y: 0,
    top: 0,
    left: 0,
    right: width,
    bottom: 36,
    toJSON: () => ({}),
  } as DOMRect
}

function stubWidth(element: Element | null, width: number) {
  if (!element) throw new Error('missing measured element')
  Object.defineProperty(element, 'getBoundingClientRect', {
    configurable: true,
    value: () => rect(width),
  })
}

/** The browser's layout for one frame: the places box and the two rows it measures. */
function layout(
  container: HTMLElement,
  widths: { available: number; all: number; current: number },
) {
  const nav = within(container).getByRole('navigation', { name: 'Journeys' })
  stubWidth(nav.parentElement, widths.available)
  stubWidth(container.querySelector('[data-measure="full"]'), widths.all)
  stubWidth(container.querySelector('[data-measure="tight"]'), widths.current)
  act(() => {
    for (const observer of [...observers])
      observer.cb([], observer as unknown as ResizeObserver)
  })
}

function link(name: string) {
  return screen.getByRole('link', { name })
}

function labelOf(name: string) {
  return within(link(name)).getByText(name)
}

describe('the places fold on the width their labels need', () => {
  it('shows every label when the box has the room, and folds them when it does not', () => {
    const { container } = renderIntel(<PlacesNav />)
    // Room for the whole unfolded row.
    layout(container, { available: 700, all: 600, current: 300 })
    for (const name of NAMES) {
      expect(link(name)).toHaveAccessibleName(name)
      expect(link(name)).toHaveAttribute('title', name)
      expect(labelOf(name)).not.toHaveClass('sr-only')
    }
    // Room for the current place's label only: the other labels fold, the name stays.
    layout(container, { available: 500, all: 600, current: 300 })
    expect(labelOf('Sessions')).not.toHaveClass('sr-only')
    for (const name of NAMES.filter((name) => name !== 'Sessions')) {
      expect(labelOf(name)).toHaveClass('sr-only')
      expect(link(name)).toHaveAccessibleName(name)
      expect(link(name)).toHaveAttribute('title', name)
    }
    // No room even for the current label: every place folds, every name stays.
    layout(container, { available: 200, all: 600, current: 300 })
    for (const name of NAMES) {
      expect(labelOf(name)).toHaveClass('sr-only')
      expect(link(name)).toHaveAccessibleName(name)
      expect(link(name)).toHaveAttribute('title', name)
    }
  })
})

describe('the fold re-measures when the labels change', () => {
  it('observes the places box and both measured rows, and stops on unmount', () => {
    const { container, unmount } = renderIntel(<PlacesNav />)
    const nav = within(container).getByRole('navigation', { name: 'Journeys' })
    // A language, place-list, count or chip change resizes a measured row: only an
    // observer on that row makes the fold re-measure (measuring on mount alone would not).
    const observed = new Set(
      [...observers].flatMap((observer) => [...observer.targets]),
    )
    expect(observed).toEqual(
      new Set([
        nav.parentElement,
        container.querySelector('[data-measure="full"]'),
        container.querySelector('[data-measure="tight"]'),
      ]),
    )
    unmount()
    expect(observers.size).toBe(0)
  })
})
