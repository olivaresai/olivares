// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Unit tests for the Sigma WebGL renderer's PURE pieces. jsdom has no WebGL, so we
// NEVER instantiate Sigma here — we test `buildSigmaGraph` (the graphology builder
// that carries every honest semantic into node/edge attributes) and the color
// resolver guards. This proves the contract mapping without a GPU.
import type { Edge as RFEdge, Node as RFNode } from '@xyflow/react'
import { describe, expect, it } from 'vitest'
import type {
  AccessEdgeData,
  OriginNodeData,
  ResourceNodeData,
} from '@/features/access-map/graph-model'
import type { AccessMapMode } from '@/features/access-map/types'
import { buildSigmaGraph, createColorResolver } from './sigma-graph-build'

// A deterministic stub resolver: echoes the css token so assertions read clearly.
const echo = (css: string) => css

function origin(
  id: string,
  opts: { dimmed?: boolean } = {},
): RFNode<OriginNodeData> {
  return {
    id,
    type: 'origin',
    position: { x: 0, y: 100 },
    data: {
      role: 'origin',
      label: id,
      kind: 'agent',
      bridged: true,
      dimmed: !!opts.dimmed,
    },
  }
}

function resource(
  id: string,
  opts: { hasUnexpected?: boolean; dimmed?: boolean; y?: number } = {},
): RFNode<ResourceNodeData> {
  return {
    id,
    type: 'resource',
    position: { x: 360, y: opts.y ?? 100 },
    data: {
      role: 'resource',
      label: id,
      kind: 'postgres.table',
      resourceKind: 'postgres.table',
      hasUnexpected: !!opts.hasUnexpected,
      dimmed: !!opts.dimmed,
    },
  }
}

function edge(
  id: string,
  source: string,
  target: string,
  partial: Partial<AccessEdgeData>,
): RFEdge<AccessEdgeData> {
  const mode = (partial.mode ?? 'read') as AccessMapMode
  return {
    id,
    source,
    target,
    type: 'access',
    data: {
      edge: {} as AccessEdgeData['edge'],
      category: partial.category ?? 'normal',
      mode,
      token: partial.token ?? 'R',
      color: partial.color ?? 'var(--color-info)',
      width: partial.width ?? 1.5,
      dashed: partial.dashed ?? false,
      opacity: partial.opacity ?? 0.95,
      showLabel: partial.showLabel ?? false,
    },
  }
}

const nodeColor = (node: RFNode): string => {
  const d = node.data as { role?: string; hasUnexpected?: boolean }
  if (d.role === 'origin') return 'var(--color-accent)'
  if (d.hasUnexpected) return 'var(--color-danger)'
  return 'var(--color-graphite-400)'
}

describe('buildSigmaGraph', () => {
  it('builds a directed graph keyed by the contract ids (for click lookups)', () => {
    const nodes = [origin('A'), resource('R')]
    const edges = [edge('e1', 'A', 'R', { mode: 'read' })]
    const g = buildSigmaGraph(nodes, edges, echo, nodeColor)
    expect(g.order).toBe(2)
    expect(g.size).toBe(1)
    expect(g.hasNode('A')).toBe(true)
    expect(g.hasNode('R')).toBe(true)
    // Edge key === contract id → access-map-view resolves it in merged.edges.
    expect(g.hasEdge('e1')).toBe(true)
    expect(g.source('e1')).toBe('A')
    expect(g.target('e1')).toBe('R')
  })

  it('negates y so the top-to-bottom column order is preserved', () => {
    const nodes = [resource('top', { y: 0 }), resource('bottom', { y: 200 })]
    const g = buildSigmaGraph(nodes, [], echo, nodeColor)
    // React Flow y grows down; Sigma y grows up. After negation, the visually
    // higher node ("top", y=0) has the GREATER Sigma y.
    expect(g.getNodeAttribute('top', 'y')).toBeGreaterThan(
      g.getNodeAttribute('bottom', 'y') as number,
    )
  })

  it('colors edges by mode: read=info, write/rw=accent, unknown=muted (verbatim)', () => {
    const nodes = [origin('A'), resource('R1'), resource('R2'), resource('R3')]
    const edges = [
      edge('er', 'A', 'R1', { mode: 'read', color: 'var(--color-info)' }),
      edge('ew', 'A', 'R2', {
        mode: 'readwrite',
        color: 'var(--color-accent)',
      }),
      edge('eu', 'A', 'R3', {
        mode: 'unknown',
        color: 'var(--color-muted-foreground)',
      }),
    ]
    const g = buildSigmaGraph(nodes, edges, echo, nodeColor)
    expect(g.getEdgeAttribute('er', 'color')).toBe('var(--color-info)')
    expect(g.getEdgeAttribute('ew', 'color')).toBe('var(--color-accent)')
    expect(g.getEdgeAttribute('eu', 'color')).toBe(
      'var(--color-muted-foreground)',
    )
  })

  it('recolors approximate (normal+dashed) edges to the slate confidence token (no dash in WebGL)', () => {
    const nodes = [origin('A'), resource('R')]
    // An inferred read edge: model gives it the info color but dashed=true.
    const edges = [
      edge('ea', 'A', 'R', {
        mode: 'read',
        color: 'var(--color-info)',
        dashed: true,
      }),
    ]
    const g = buildSigmaGraph(nodes, edges, echo, nodeColor)
    // It must NOT read as a firm info edge — it is recolored slate.
    expect(g.getEdgeAttribute('ea', 'color')).toBe(
      'var(--color-confidence-approximate)',
    )
    expect(g.getEdgeAttribute('ea', 'cssColor')).toBe(
      'var(--color-confidence-approximate)',
    )
  })

  it('keeps finding colors and does NOT slate-recolor a dashed PENDING edge', () => {
    const nodes = [origin('A'), resource('R1'), resource('R2'), resource('R3')]
    const edges = [
      edge('ux', 'A', 'R1', {
        category: 'unexpected',
        mode: 'readwrite',
        color: 'var(--color-danger)',
        width: 2.75,
      }),
      // pending is dashed in React Flow but is a FINDING, not an inference → amber stays.
      edge('pd', 'A', 'R2', {
        category: 'pending',
        mode: 'write',
        color: 'var(--color-warning)',
        dashed: true,
      }),
      edge('un', 'A', 'R3', {
        category: 'unused',
        mode: 'read',
        color: 'var(--color-warning)',
        dashed: true,
      }),
    ]
    const g = buildSigmaGraph(nodes, edges, echo, nodeColor)
    expect(g.getEdgeAttribute('ux', 'color')).toBe('var(--color-danger)')
    expect(g.getEdgeAttribute('pd', 'color')).toBe('var(--color-warning)')
    expect(g.getEdgeAttribute('un', 'color')).toBe('var(--color-warning)')
    // zIndex: unexpected above plain, unused below.
    expect(g.getEdgeAttribute('ux', 'zIndex')).toBeGreaterThan(
      g.getEdgeAttribute('un', 'zIndex') as number,
    )
  })

  it('edge size = data.width and label only when showLabel', () => {
    const nodes = [origin('A'), resource('R1'), resource('R2')]
    const edges = [
      edge('e1', 'A', 'R1', { width: 2.75, showLabel: true, token: 'RW' }),
      edge('e2', 'A', 'R2', { width: 1.5, showLabel: false }),
    ]
    const g = buildSigmaGraph(nodes, edges, echo, nodeColor)
    expect(g.getEdgeAttribute('e1', 'size')).toBe(2.75)
    expect(g.getEdgeAttribute('e1', 'label')).toBe('RW')
    expect(g.getEdgeAttribute('e2', 'size')).toBe(1.5)
    expect(g.getEdgeAttribute('e2', 'label')).toBeUndefined()
  })

  it('flags a hasUnexpected resource with danger color + larger size + forced label', () => {
    const nodes = [origin('A'), resource('R', { hasUnexpected: true })]
    const g = buildSigmaGraph(nodes, [], echo, nodeColor)
    expect(g.getNodeAttribute('R', 'color')).toBe('var(--color-danger)')
    expect(g.getNodeAttribute('R', 'forceLabel')).toBe(true)
    // Larger than a plain resource (honest "this resource has unexpected access").
    const plain = buildSigmaGraph(
      [origin('A'), resource('P')],
      [],
      echo,
      nodeColor,
    )
    expect(g.getNodeAttribute('R', 'size') as number).toBeGreaterThan(
      plain.getNodeAttribute('P', 'size') as number,
    )
  })

  it('origins use accent, plain resources use graphite, and store the unresolved token', () => {
    const g = buildSigmaGraph([origin('A'), resource('R')], [], echo, nodeColor)
    expect(g.getNodeAttribute('A', 'color')).toBe('var(--color-accent)')
    expect(g.getNodeAttribute('A', 'cssColor')).toBe('var(--color-accent)')
    expect(g.getNodeAttribute('R', 'color')).toBe('var(--color-graphite-400)')
  })

  it('carries the original node payload so clicks emit the same selection', () => {
    const g = buildSigmaGraph([origin('A')], [], echo, nodeColor)
    const access = g.getNodeAttribute('A', 'access') as OriginNodeData
    expect(access.role).toBe('origin')
    expect(access.kind).toBe('agent')
    expect(access.label).toBe('A')
  })

  it('skips edges whose endpoints are missing (defensive, no throw)', () => {
    const nodes = [origin('A')] // target R is absent
    const edges = [edge('dangling', 'A', 'R', {})]
    const g = buildSigmaGraph(nodes, edges, echo, nodeColor)
    expect(g.size).toBe(0)
    expect(g.hasEdge('dangling')).toBe(false)
  })
})

describe('createColorResolver', () => {
  it('falls back to the dark literal palette when there is no layout engine (jsdom)', () => {
    // jsdom returns '' for custom properties → the FALLBACK_COLORS literal wins.
    const resolve = createColorResolver()
    expect(resolve('var(--color-accent)')).toBe('#f08000')
    expect(resolve('var(--color-danger)')).toBe('#f0857e')
    expect(resolve('var(--color-confidence-approximate)')).toBe('#9aa3b0')
  })

  it('returns a non-var string unchanged (literal passthrough)', () => {
    const resolve = createColorResolver()
    expect(resolve('#abcdef')).toBe('#abcdef')
  })
})
