// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

export type ArrowKey = 'ArrowUp' | 'ArrowDown' | 'ArrowLeft' | 'ArrowRight'

interface Placed {
  id: string
  position: { x: number; y: number }
}

/**
 * The connected node an arrow key moves to: among the nodes joined to `from` by an
 * edge (either direction), the nearest one strictly in that direction, the other axis
 * counting double so "right" prefers the neighbor most directly to the right. Null when
 * no connected node lies that way, so the key does nothing rather than jump somewhere
 * unconnected.
 */
export function connectedNeighbor(
  nodes: readonly Placed[],
  edges: readonly { source: string; target: string }[],
  from: string,
  key: ArrowKey,
): string | null {
  const here = nodes.find((n) => n.id === from)
  if (!here) return null
  const linked = new Set<string>()
  for (const e of edges) {
    if (e.source === from) linked.add(e.target)
    else if (e.target === from) linked.add(e.source)
  }
  const horizontal = key === 'ArrowLeft' || key === 'ArrowRight'
  const sign = key === 'ArrowLeft' || key === 'ArrowUp' ? -1 : 1
  let best: { id: string; score: number } | null = null
  for (const n of nodes) {
    if (n.id === from || !linked.has(n.id)) continue
    const dx = n.position.x - here.position.x
    const dy = n.position.y - here.position.y
    const along = (horizontal ? dx : dy) * sign
    if (along <= 0) continue
    const score = along + 2 * Math.abs(horizontal ? dy : dx)
    if (!best || score < best.score) best = { id: n.id, score }
  }
  return best?.id ?? null
}
