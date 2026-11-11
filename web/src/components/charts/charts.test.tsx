// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ReactNode } from 'react'
import { fireEvent, render } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { FALLBACK } from './chart-theme'
import { CategoryBarChart, ChartLegend } from './index'

// recharts' ResponsiveContainer waits for a ResizeObserver callback that the jsdom
// stub never fires, so it renders no children at all — the chart tree would never
// mount. A fixed-size passthrough lets the wrapper under test render; the cursor
// is planted per test, so nothing here depends on recharts drawing.
vi.mock('recharts', async (importOriginal) => {
  const actual = await importOriginal<typeof import('recharts')>()
  return {
    ...actual,
    ResponsiveContainer: ({ children }: { children?: ReactNode }) => (
      <div style={{ width: 800, height: 300 }}>{children}</div>
    ),
  }
})

const data = [
  { key: 'run:alpha', v: 140 },
  { key: 'run:beta', v: 90 },
]

/** recharts draws its band cursor only in a real browser: the accessibility layer
 * stamps the active bar index on the cursor the moment the chart surface takes
 * focus, and moves it under the arrow keys. The cursor the Enter handler reads is
 * planted here — the contract under test is the mapping from that stamped index to
 * the category; the real rendering is covered by the console e2e. */
function plantCursor(container: HTMLElement, index: number): HTMLElement {
  const wrap = container.querySelector<HTMLElement>(
    '[data-slot="category-bars"]',
  )!
  const cursor = document.createElement('div')
  cursor.setAttribute('data-cursor-index', String(index))
  wrap.appendChild(cursor)
  return cursor
}

/** The wrapper holds the focus the way the SVG surface does in the browser: the
 * handler accepts any focus inside the wrapper, not a focusable bar. */
function focusWrap(container: HTMLElement): HTMLElement {
  const wrap = container.querySelector<HTMLElement>(
    '[data-slot="category-bars"]',
  )!
  wrap.tabIndex = -1
  wrap.focus()
  return wrap
}

describe('CategoryBarChart keyboard', () => {
  it('turns Enter into onSelectCategory for the cursor-stamped index', () => {
    const onSelect = vi.fn()
    const { container } = render(
      <CategoryBarChart
        data={data}
        categoryKey="key"
        valueKey="v"
        onSelectCategory={onSelect}
      />,
    )
    plantCursor(container, 1)
    const dispatched = fireEvent.keyDown(focusWrap(container), { key: 'Enter' })
    expect(onSelect).toHaveBeenCalledTimes(1)
    expect(onSelect).toHaveBeenCalledWith('run:beta')
    // dispatchEvent returns false when a listener called preventDefault.
    expect(dispatched).toBe(false)
  })

  it('leaves Enter alone when no active index is stamped', () => {
    const onSelect = vi.fn()
    const { container } = render(
      <CategoryBarChart
        data={data}
        categoryKey="key"
        valueKey="v"
        onSelectCategory={onSelect}
      />,
    )
    const dispatched = fireEvent.keyDown(focusWrap(container), { key: 'Enter' })
    expect(onSelect).not.toHaveBeenCalled()
    // Not prevented: recharts' own Enter (tooltip pin) keeps working.
    expect(dispatched).toBe(true)
  })

  it('ignores Enter when the focus sits outside the chart wrapper', () => {
    const onSelect = vi.fn()
    const { container } = render(
      <CategoryBarChart
        data={data}
        categoryKey="key"
        valueKey="v"
        onSelectCategory={onSelect}
      />,
    )
    plantCursor(container, 0)
    // Focus stays on <body>: a keydown that reaches the wrapper from elsewhere
    // must not filter the chart.
    const wrap = container.querySelector<HTMLElement>(
      '[data-slot="category-bars"]',
    )!
    const dispatched = fireEvent.keyDown(wrap, { key: 'Enter' })
    expect(onSelect).not.toHaveBeenCalled()
    expect(dispatched).toBe(true)
  })

  it('ignores keys other than Enter', () => {
    const onSelect = vi.fn()
    const { container } = render(
      <CategoryBarChart
        data={data}
        categoryKey="key"
        valueKey="v"
        onSelectCategory={onSelect}
      />,
    )
    plantCursor(container, 0)
    const dispatched = fireEvent.keyDown(focusWrap(container), {
      key: 'ArrowRight',
    })
    expect(onSelect).not.toHaveBeenCalled()
    expect(dispatched).toBe(true)
  })

  it('leaves Enter alone when no handler is wired', () => {
    const { container } = render(
      <CategoryBarChart data={data} categoryKey="key" valueKey="v" />,
    )
    plantCursor(container, 0)
    const seen = vi.fn()
    const wrap = container.querySelector<HTMLElement>(
      '[data-slot="category-bars"]',
    )!
    wrap.addEventListener('keydown', seen)
    fireEvent.keyDown(wrap, { key: 'Enter' })
    expect(seen).toHaveBeenCalledTimes(1)
  })
})

describe('the chart frame (A2)', () => {
  it('keys its legend with round swatches', () => {
    const { container } = render(
      <ChartLegend
        items={[
          {
            key: 'claude',
            label: 'Claude Code',
            value: 28,
            color: 'var(--accent)',
          },
        ]}
      />,
    )
    const swatch = container.querySelector('li [aria-hidden]')!
    expect(swatch).toHaveClass('size-2', 'rounded-full')
    expect(swatch).not.toHaveClass('rounded-[2px]')
  })

  it('draws the axis line firmer than the grid, in the fallback palette too', () => {
    // The fallback is the dark theme's: its `--border-strong` and `--border`.
    expect(FALLBACK.border).toBe('rgba(255, 255, 255, 0.13)')
    expect(FALLBACK.grid).toBe('rgba(255, 255, 255, 0.07)')
  })
})
