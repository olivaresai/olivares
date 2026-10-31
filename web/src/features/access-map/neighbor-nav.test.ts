// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { connectedNeighbor } from './neighbor-nav'

// Two origins on the left, three resources on the right; a1 reaches r1 and r2, a2 reaches r2.
const nodes = [
  { id: 'a1', position: { x: 0, y: 0 } },
  { id: 'a2', position: { x: 0, y: 100 } },
  { id: 'r1', position: { x: 360, y: 0 } },
  { id: 'r2', position: { x: 360, y: 100 } },
  { id: 'r3', position: { x: 360, y: 200 } },
]
const edges = [
  { source: 'a1', target: 'r1' },
  { source: 'a1', target: 'r2' },
  { source: 'a2', target: 'r2' },
]

describe('connectedNeighbor', () => {
  it('moves to the connected node that lies that way, and prefers the straightest', () => {
    expect(connectedNeighbor(nodes, edges, 'a1', 'ArrowRight')).toBe('r1')
    expect(connectedNeighbor(nodes, edges, 'r2', 'ArrowLeft')).toBe('a2')
    // r2 is connected and lies below a1, though not straight below it.
    expect(connectedNeighbor(nodes, edges, 'a1', 'ArrowDown')).toBe('r2')
    expect(connectedNeighbor(nodes, edges, 'a1', 'ArrowUp')).toBeNull()
  })

  it('follows an edge from either end', () => {
    expect(connectedNeighbor(nodes, edges, 'r2', 'ArrowLeft')).toBe('a2')
    expect(connectedNeighbor(nodes, edges, 'r1', 'ArrowLeft')).toBe('a1')
  })

  it('never jumps to a node it is not connected to', () => {
    expect(connectedNeighbor(nodes, edges, 'r3', 'ArrowLeft')).toBeNull()
    expect(connectedNeighbor(nodes, edges, 'r3', 'ArrowUp')).toBeNull()
  })

  it('moves vertically among connected nodes of one column', () => {
    const column = [
      { id: 'a1', position: { x: 0, y: 0 } },
      { id: 'a2', position: { x: 0, y: 50 } },
      { id: 'a3', position: { x: 0, y: 120 } },
    ]
    const links = [
      { source: 'a1', target: 'a2' },
      { source: 'a1', target: 'a3' },
    ]
    expect(connectedNeighbor(column, links, 'a1', 'ArrowDown')).toBe('a2')
    expect(connectedNeighbor(column, links, 'a3', 'ArrowUp')).toBe('a1')
    expect(connectedNeighbor(column, links, 'a2', 'ArrowDown')).toBeNull()
  })

  it('answers null for a node that is not on the map', () => {
    expect(connectedNeighbor(nodes, edges, 'gone', 'ArrowRight')).toBeNull()
  })
})
