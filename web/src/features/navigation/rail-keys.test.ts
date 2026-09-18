// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { parentIndex, railKey, type RailRow } from './rail-keys'

/** Overview · [Inventory, pinned] · area(open) · leaf · leaf · area(closed) · Settings */
const ROWS: RailRow[] = [
  { depth: 0, pinId: 'home' },
  { depth: 0, pinId: 'inventory' },
  { depth: 0, area: { id: 'ai', open: true } },
  { depth: 1, pinId: 'sessions' },
  { depth: 1, pinId: 'models' },
  { depth: 0, area: { id: 'system', open: false } },
  { depth: 0, pinId: 'settings' },
]

describe('railKey', () => {
  it('moves down and up, one row at a time', () => {
    expect(railKey('ArrowDown', ROWS, 0)).toEqual({ kind: 'focus', index: 1 })
    expect(railKey('ArrowUp', ROWS, 3)).toEqual({ kind: 'focus', index: 2 })
  })

  it('clamps at both ends instead of wrapping', () => {
    expect(railKey('ArrowUp', ROWS, 0)).toEqual({ kind: 'none' })
    expect(railKey('ArrowDown', ROWS, ROWS.length - 1)).toEqual({
      kind: 'none',
    })
  })

  it('jumps to the ends with Home and End, and does nothing when already there', () => {
    expect(railKey('Home', ROWS, 4)).toEqual({ kind: 'focus', index: 0 })
    expect(railKey('End', ROWS, 0)).toEqual({ kind: 'focus', index: 6 })
    expect(railKey('Home', ROWS, 0)).toEqual({ kind: 'none' })
    expect(railKey('End', ROWS, 6)).toEqual({ kind: 'none' })
  })

  it('opens a closed area with ArrowRight and enters an open one', () => {
    expect(railKey('ArrowRight', ROWS, 5)).toEqual({
      kind: 'expand',
      areaId: 'system',
    })
    expect(railKey('ArrowRight', ROWS, 2)).toEqual({ kind: 'focus', index: 3 })
  })

  it('does not move right from a leaf', () => {
    expect(railKey('ArrowRight', ROWS, 3)).toEqual({ kind: 'none' })
  })

  it('folds an open area with ArrowLeft and returns a leaf to its area', () => {
    expect(railKey('ArrowLeft', ROWS, 2)).toEqual({
      kind: 'fold',
      areaId: 'ai',
    })
    expect(railKey('ArrowLeft', ROWS, 4)).toEqual({ kind: 'focus', index: 2 })
  })

  it('does nothing left of a closed area or a top-level link', () => {
    expect(railKey('ArrowLeft', ROWS, 5)).toEqual({ kind: 'none' })
    expect(railKey('ArrowLeft', ROWS, 0)).toEqual({ kind: 'none' })
  })

  it('pins the focused row when it has something to pin, and only then', () => {
    expect(railKey('p', ROWS, 1)).toEqual({ kind: 'pin', index: 1 })
    expect(railKey('P', ROWS, 1)).toEqual({ kind: 'pin', index: 1 })
    // An area header names a directory page, not a module the operator pins.
    expect(railKey('p', ROWS, 2)).toEqual({ kind: 'none' })
  })

  it('leaves Enter and Space to the browser', () => {
    // The rows are links and buttons. Handling activation here would be a second copy of
    // behaviour the platform gives us, and it would break Ctrl-click and middle-click.
    expect(railKey('Enter', ROWS, 1)).toEqual({ kind: 'none' })
    expect(railKey(' ', ROWS, 1)).toEqual({ kind: 'none' })
  })

  it('never throws on an out-of-range active index', () => {
    // The rows change as areas open and close; an index left over from the previous
    // render must not take the keyboard down with it.
    expect(railKey('ArrowDown', ROWS, 99)).toEqual({ kind: 'none' })
    expect(railKey('ArrowUp', ROWS, -4)).toEqual({ kind: 'none' })
    expect(railKey('ArrowDown', [], 0)).toEqual({ kind: 'none' })
  })

  it('finds the area a leaf belongs to', () => {
    expect(parentIndex(ROWS, 4)).toBe(2)
    expect(parentIndex(ROWS, 0)).toBe(-1)
  })
})
