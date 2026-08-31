// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { Edge as RFEdge, Node as RFNode } from '@xyflow/react'
import { describe, expect, it } from 'vitest'
import type {
  AccessEdgeData,
  OriginNodeData,
  ResourceNodeData,
} from '@/features/access-map/graph-model'
import type { AccessMapMode } from '@/features/access-map/types'
import { clusterByHost } from './cluster'

// --- tiny builders mirroring the built model shape (graph-model.buildGraph) ----

function origin(id: string): RFNode<OriginNodeData> {
  return {
    id,
    type: 'origin',
    position: { x: 0, y: 0 },
    data: {
      role: 'origin',
      label: id,
      kind: 'agent',
      bridged: true,
      dimmed: false,
    },
  }
}

function resource(
  id: string,
  resourceKind: string,
  opts: { hasUnexpected?: boolean; x?: number; y?: number } = {},
): RFNode<ResourceNodeData> {
  return {
    id,
    type: 'resource',
    position: { x: opts.x ?? 100, y: opts.y ?? 0 },
    data: {
      role: 'resource',
      label: id,
      kind: resourceKind,
      resourceKind,
      hasUnexpected: !!opts.hasUnexpected,
      dimmed: false,
    },
  }
}

function edge(
  id: string,
  source: string,
  target: string,
  mode: AccessMapMode,
  category: AccessEdgeData['category'] = 'normal',
): RFEdge<AccessEdgeData> {
  return {
    id,
    source,
    target,
    type: 'access',
    data: {
      // Only the fields the transform reads matter; the rest are placeholders.
      edge: {} as AccessEdgeData['edge'],
      category,
      mode,
      token:
        mode === 'read'
          ? 'R'
          : mode === 'readwrite'
            ? 'RW'
            : mode === 'write'
              ? 'W'
              : '?',
      color: '',
      width: 1,
      dashed: false,
      opacity: 1,
      showLabel: false,
    },
  }
}

describe('clusterByHost', () => {
  it('passes through unchanged below the threshold (clustered=false)', () => {
    const nodes = [origin('A'), resource('R1', 'postgres.table')]
    const edges = [edge('e1', 'A', 'R1', 'read')]
    const out = clusterByHost(nodes, edges, { threshold: 10 })
    expect(out.clustered).toBe(false)
    expect(out.nodes).toBe(nodes)
    expect(out.edges).toBe(edges)
  })

  it('above threshold: groups resources by host with correct counts, origins untouched', () => {
    const nodes = [
      origin('A'),
      origin('B'),
      resource('R1', 'postgres.table'),
      resource('R2', 'postgres.index'),
      resource('R3', 's3.bucket'),
    ]
    const edges = [
      edge('e1', 'A', 'R1', 'read'),
      edge('e2', 'A', 'R2', 'read'),
      edge('e3', 'B', 'R3', 'read'),
    ]
    // threshold below node count → cluster.
    const out = clusterByHost(nodes, edges, { threshold: 3 })
    expect(out.clustered).toBe(true)

    // Origins are NOT clustered.
    const originIds = out.nodes
      .filter((n) => (n.data as OriginNodeData).role === 'origin')
      .map((n) => n.id)
    expect(originIds.sort()).toEqual(['A', 'B'])

    // Two clusters: postgres (2 members) and s3 (1 member).
    const clusters = out.nodes.filter(
      (n) => (n.data as ResourceNodeData).role === 'resource',
    )
    const byId = new Map(
      clusters.map((c) => [c.id, c.data as ResourceNodeData]),
    )
    expect(byId.get('cluster:postgres')?.clusterCount).toBe(2)
    expect(byId.get('cluster:postgres')?.label).toBe('postgres (2)')
    expect(byId.get('cluster:s3')?.clusterCount).toBe(1)
    expect(byId.get('cluster:s3')?.label).toBe('s3 (1)')
  })

  it('flags a cluster hasUnexpected when ANY member carries a finding (never hides it)', () => {
    const nodes = [
      origin('A'),
      resource('R1', 'postgres.table', { hasUnexpected: false }),
      resource('R2', 'postgres.table', { hasUnexpected: true }),
      resource('R3', 'postgres.table'),
    ]
    const edges = [
      edge('e1', 'A', 'R1', 'read'),
      edge('e2', 'A', 'R2', 'readwrite', 'unexpected'),
      edge('e3', 'A', 'R3', 'read'),
    ]
    const out = clusterByHost(nodes, edges, { threshold: 2 })
    const pg = out.nodes.find((n) => n.id === 'cluster:postgres')!
    expect((pg.data as ResourceNodeData).hasUnexpected).toBe(true)
    expect((pg.data as ResourceNodeData).clusterCount).toBe(3)
  })

  it('the surviving collapsed edge keeps the riskiest category and mode (RW/unexpected wins over R/normal)', () => {
    const nodes = [
      origin('A'),
      resource('R1', 'postgres.table'),
      resource('R2', 'postgres.table'),
    ]
    const edges = [
      edge('e1', 'A', 'R1', 'read', 'normal'),
      edge('e2', 'A', 'R2', 'readwrite', 'unexpected'),
    ]
    const out = clusterByHost(nodes, edges, { threshold: 2 })
    // Both A→postgres edges collapse onto one origin→cluster edge.
    const clusterEdges = out.edges.filter(
      (e) => e.target === 'cluster:postgres',
    )
    expect(clusterEdges).toHaveLength(1)
    const data = clusterEdges[0]!.data as AccessEdgeData
    expect(data.category).toBe('unexpected')
    expect(data.mode).toBe('readwrite')
  })

  it('falls back to kind when resourceKind has no dotted host segment', () => {
    const nodes = [
      origin('A'),
      resource('R1', 'redis'),
      resource('R2', 'redis'),
      resource('R3', 'redis'),
    ]
    const edges = [
      edge('e1', 'A', 'R1', 'read'),
      edge('e2', 'A', 'R2', 'read'),
      edge('e3', 'A', 'R3', 'read'),
    ]
    const out = clusterByHost(nodes, edges, { threshold: 2 })
    const cluster = out.nodes.find(
      (n) => (n.data as ResourceNodeData).role === 'resource',
    )!
    expect(cluster.id).toBe('cluster:redis')
    expect((cluster.data as ResourceNodeData).clusterCount).toBe(3)
  })

  it('cluster position is the deterministic average of its members', () => {
    const nodes = [
      origin('A'),
      resource('R1', 'postgres.table', { x: 100, y: 0 }),
      resource('R2', 'postgres.table', { x: 100, y: 200 }),
      resource('R3', 's3.bucket', { x: 100, y: 400 }),
    ]
    const edges = [
      edge('e1', 'A', 'R1', 'read'),
      edge('e2', 'A', 'R2', 'read'),
      edge('e3', 'A', 'R3', 'read'),
    ]
    const out = clusterByHost(nodes, edges, { threshold: 2 })
    const pg = out.nodes.find((n) => n.id === 'cluster:postgres')!
    expect(pg.position).toEqual({ x: 100, y: 100 })
  })
})
