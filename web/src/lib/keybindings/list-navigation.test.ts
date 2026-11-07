// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { afterEach, describe, expect, it } from 'vitest'
import { firstFocusableGrid, moveFirstList } from './list-navigation'

function makeGrid(): HTMLTableElement {
  const grid = document.createElement('table')
  grid.setAttribute('role', 'grid')
  grid.tabIndex = 0
  document.body.appendChild(grid)
  return grid
}

afterEach(() => {
  document.body.innerHTML = ''
})

describe('moveFirstList', () => {
  it('focuses the first grid and re-dispatches the arrow it stands for', () => {
    const grid = makeGrid()
    const seen: string[] = []
    grid.addEventListener('keydown', (e) => seen.push((e as KeyboardEvent).key))
    expect(moveFirstList('ArrowDown')).toBe(true)
    expect(document.activeElement).toBe(grid)
    expect(seen).toEqual(['ArrowDown'])
    expect(moveFirstList('ArrowUp')).toBe(true)
    expect(seen).toEqual(['ArrowDown', 'ArrowUp'])
  })

  it('skips a hidden grid for the next one', () => {
    const hiddenHost = document.createElement('div')
    hiddenHost.hidden = true
    const hidden = makeGrid()
    hiddenHost.appendChild(hidden)
    document.body.insertBefore(hiddenHost, document.body.firstChild)
    const shown = makeGrid()
    const seen: string[] = []
    shown.addEventListener('keydown', (e) =>
      seen.push((e as KeyboardEvent).key),
    )
    expect(moveFirstList('ArrowDown')).toBe(true)
    expect(document.activeElement).toBe(shown)
    expect(seen).toEqual(['ArrowDown'])
  })

  it('answers false when the page has no grid', () => {
    expect(moveFirstList('ArrowDown')).toBe(false)
    expect(firstFocusableGrid()).toBeNull()
  })

  it('skips an inert grid for the next one', () => {
    const inertHost = document.createElement('div')
    inertHost.setAttribute('inert', '')
    const inert = makeGrid()
    inertHost.appendChild(inert)
    document.body.insertBefore(inertHost, document.body.firstChild)
    const shown = makeGrid()
    expect(moveFirstList('ArrowDown')).toBe(true)
    expect(document.activeElement).toBe(shown)
  })

  it('does not move a list behind an open menu', () => {
    const grid = makeGrid()
    const seen: string[] = []
    grid.addEventListener('keydown', (e) => seen.push((e as KeyboardEvent).key))
    const menu = document.createElement('div')
    menu.setAttribute('role', 'menu')
    document.body.appendChild(menu)
    expect(moveFirstList('ArrowDown')).toBe(false)
    expect(seen).toEqual([])
    expect(document.activeElement).not.toBe(grid)
  })

  it('reports false when the grid never took focus', () => {
    // No tabIndex: not a focusable area, so focus() is a no-op. The shell must
    // not swallow the key on a grid that never received it.
    const grid = document.createElement('table')
    grid.setAttribute('role', 'grid')
    document.body.appendChild(grid)
    expect(moveFirstList('ArrowDown')).toBe(false)
    expect(document.activeElement).not.toBe(grid)
  })
})
