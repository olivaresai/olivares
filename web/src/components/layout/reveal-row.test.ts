// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// jsdom has no layout engine, so every box and scroll size below is stated by the test.
import { afterEach, describe, expect, it, vi } from 'vitest'
import { revealRow } from './reveal-row'

function box(top: number, height: number): DOMRect {
  return {
    top,
    height,
    bottom: top + height,
    left: 0,
    right: 0,
    width: 0,
    x: 0,
    y: top,
    toJSON: () => ({}),
  }
}

/** A scroll container, optionally holding all its content, that reports these sizes. */
function scroller(
  rect: DOMRect,
  {
    scrollHeight,
    clientHeight,
  }: { scrollHeight: number; clientHeight: number },
  scrollTop = 0,
): HTMLElement {
  const el = document.createElement('div')
  el.style.overflowY = 'auto'
  Object.defineProperty(el, 'scrollHeight', { value: scrollHeight })
  Object.defineProperty(el, 'clientHeight', { value: clientHeight })
  Object.defineProperty(el, 'scrollTop', {
    value: scrollTop,
    writable: true,
    configurable: true,
  })
  el.getBoundingClientRect = () => rect
  document.body.append(el)
  return el
}

function row(parent: HTMLElement, rect: DOMRect): HTMLElement {
  const el = document.createElement('a')
  el.getBoundingClientRect = () => rect
  parent.append(el)
  return el
}

const OVERFLOWING = { scrollHeight: 1000, clientHeight: 200 }

afterEach(() => {
  document.body.replaceChildren()
})

describe('revealRow', () => {
  it('scrolls the container down by exactly what hides the row below it', () => {
    const host = scroller(box(100, 200), OVERFLOWING)
    expect(revealRow(row(host, box(300, 40)))).toBe(true)
    expect(host.scrollTop).toBe(40)
  })

  it('scrolls the container up by exactly what hides the row above it', () => {
    const host = scroller(box(100, 200), OVERFLOWING, 80)
    expect(revealRow(row(host, box(50, 40)))).toBe(true)
    expect(host.scrollTop).toBe(30)
  })

  it('leaves the container alone when the row is already inside it', () => {
    const host = scroller(box(100, 200), OVERFLOWING, 25)
    expect(revealRow(row(host, box(150, 40)))).toBe(true)
    expect(host.scrollTop).toBe(25)
  })

  it('reports a row with no box yet as not revealed, and moves nothing', () => {
    const host = scroller(box(100, 200), OVERFLOWING, 25)
    expect(revealRow(row(host, box(0, 0)))).toBe(false)
    expect(host.scrollTop).toBe(25)
  })

  it('has nothing to move when no ancestor scrolls', () => {
    const host = scroller(box(100, 200), {
      scrollHeight: 200,
      clientHeight: 200,
    })
    expect(revealRow(row(host, box(900, 40)))).toBe(true)
    expect(host.scrollTop).toBe(0)
  })

  it('moves the container that really scrolls, not a nearer one that holds all its content', () => {
    // `overflow-x: hidden` makes a box's `overflow-y` compute to `auto` without making it scroll.
    const outer = scroller(box(100, 200), OVERFLOWING)
    const clip = scroller(box(100, 600), {
      scrollHeight: 600,
      clientHeight: 600,
    })
    outer.append(clip)
    expect(revealRow(row(clip, box(300, 40)))).toBe(true)
    expect(outer.scrollTop).toBe(40)
    expect(clip.scrollTop).toBe(0)
  })

  it('never calls scrollIntoView, which would move the focus starting point', () => {
    const scrollIntoView = vi.spyOn(Element.prototype, 'scrollIntoView')
    try {
      const host = scroller(box(100, 200), OVERFLOWING)
      revealRow(row(host, box(300, 40)))
      expect(scrollIntoView).not.toHaveBeenCalled()
    } finally {
      scrollIntoView.mockRestore()
    }
  })
})
