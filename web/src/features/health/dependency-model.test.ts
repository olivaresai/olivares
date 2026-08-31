// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { buildDependencyGraph, layerForKind } from './dependency-model'
import type { DepGraphResponse } from './types'

const graph: DepGraphResponse = {
  nodes: [
    { id: 'sess-1', kind: 'session', ref: 'sess-1', health: 'unknown' },
    { id: 'agent-a', kind: 'agent', ref: 'agent-a', health: 'healthy' },
    { id: 'mcp-fs', kind: 'mcp', ref: 'mcp-fs', health: 'degraded' },
    { id: 'mcp-db', kind: 'mcp', ref: 'mcp-db', health: 'down' },
    { id: 'tool-read', kind: 'mcp_tool', ref: 'tool-read', health: 'unknown' },
  ],
  edges: [
    {
      id: 'e1',
      source: 'agent-a',
      target: 'mcp-fs',
      from_kind: 'agent',
      to_kind: 'mcp',
      relation: 'uses_mcp',
      observed_count: 12,
      first_seen_at: '2026-06-03T12:00:00Z',
      last_seen_at: '2026-06-03T12:30:00Z',
    },
    {
      id: 'e2',
      source: 'mcp-fs',
      target: 'tool-read',
      from_kind: 'mcp',
      to_kind: 'mcp_tool',
      relation: 'uses_tool',
      observed_count: 100,
      first_seen_at: '2026-06-03T12:00:00Z',
      last_seen_at: '2026-06-03T12:30:00Z',
    },
    {
      id: 'e3',
      source: 'sess-1',
      target: 'agent-a',
      from_kind: 'session',
      to_kind: 'agent',
      relation: 'delegates_to',
      observed_count: 3,
      first_seen_at: '2026-06-03T12:00:00Z',
      last_seen_at: '2026-06-03T12:30:00Z',
    },
    {
      // A dangling edge whose target is not in this keyset page — must be dropped.
      id: 'e-dangle',
      source: 'agent-a',
      target: 'mcp-elsewhere',
      from_kind: 'agent',
      to_kind: 'mcp',
      relation: 'uses_mcp',
      observed_count: 1,
      first_seen_at: '2026-06-03T12:00:00Z',
      last_seen_at: '2026-06-03T12:30:00Z',
    },
  ],
  has_more: false,
}

describe('layerForKind', () => {
  it('maps kinds to layers (session/agent=0, mcp=1, mcp_tool=2)', () => {
    expect(layerForKind('session')).toBe(0)
    expect(layerForKind('agent')).toBe(0)
    expect(layerForKind('mcp')).toBe(1)
    expect(layerForKind('mcp_tool')).toBe(2)
  })
  it('maps an unknown kind to layer 1', () => {
    expect(layerForKind('weird')).toBe(1)
  })
})

describe('buildDependencyGraph — node coloring by health (not kind)', () => {
  it('colors each node by its health annotation token', () => {
    const { nodes } = buildDependencyGraph(graph)
    const byId = (id: string) =>
      nodes.find((n) => n.id === id)!.data as { color: string; health: string }
    expect(byId('agent-a').color).toBe('var(--color-success)')
    expect(byId('mcp-fs').color).toBe('var(--color-warning)')
    expect(byId('mcp-db').color).toBe('var(--color-danger)')
  })

  it('NEVER paints an unknown (untracked) node green — it is the muted neutral', () => {
    const { nodes } = buildDependencyGraph(graph)
    const sess = nodes.find((n) => n.id === 'sess-1')!.data as {
      color: string
      health: string
    }
    const tool = nodes.find((n) => n.id === 'tool-read')!.data as {
      color: string
    }
    expect(sess.health).toBe('unknown')
    expect(sess.color).toBe('var(--color-muted-foreground)')
    expect(sess.color).not.toBe('var(--color-success)')
    expect(tool.color).toBe('var(--color-muted-foreground)')
  })

  it('counts health buckets', () => {
    const { stats } = buildDependencyGraph(graph)
    expect(stats.healthy).toBe(1)
    expect(stats.degraded).toBe(1)
    expect(stats.down).toBe(1)
    expect(stats.unknown).toBe(2)
    expect(stats.nodes).toBe(5)
  })
})

describe('buildDependencyGraph — "observed" is distinct from unknown and healthy', () => {
  const observedGraph: DepGraphResponse = {
    nodes: [
      // Seen alive by an edge, no declared check → "observed".
      { id: 'live-mcp', kind: 'mcp', ref: 'live-mcp', health: 'observed' },
      { id: 'actor-a', kind: 'agent', ref: 'actor-a', health: 'observed' },
      // Only ever named as a target, no liveness → "unknown".
      { id: 'never', kind: 'agent', ref: 'never', health: 'unknown' },
    ],
    edges: [],
    has_more: false,
  }

  it('paints an observed node with info, never green and never the unknown gray', () => {
    const { nodes } = buildDependencyGraph(observedGraph)
    const live = nodes.find((n) => n.id === 'live-mcp')!.data as {
      color: string
      health: string
    }
    expect(live.health).toBe('observed')
    expect(live.color).toBe('var(--color-info)')
    expect(live.color).not.toBe('var(--color-success)') // not fabricated healthy
    expect(live.color).not.toBe('var(--color-muted-foreground)') // not silent unknown
  })

  it('counts observed in its own bucket, distinct from unknown', () => {
    const { stats } = buildDependencyGraph(observedGraph)
    expect(stats.observed).toBe(2)
    expect(stats.unknown).toBe(1)
  })
})

describe('buildDependencyGraph — edges styled by relation', () => {
  it('assigns a color per relation', () => {
    const { edges } = buildDependencyGraph(graph)
    const e1 = edges.find((e) => e.id === 'e1')!.data as {
      color: string
      relation: string
    }
    const e2 = edges.find((e) => e.id === 'e2')!.data as {
      color: string
      relation: string
    }
    const e3 = edges.find((e) => e.id === 'e3')!.data as {
      color: string
      relation: string
    }
    expect(e1.relation).toBe('uses_mcp')
    expect(e1.color).toBe('var(--color-info)')
    expect(e2.relation).toBe('uses_tool')
    expect(e2.color).toBe('var(--color-muted-foreground)')
    expect(e3.relation).toBe('delegates_to')
    expect(e3.color).toBe('var(--color-accent-text)')
  })

  it('widens a busier edge (higher observed_count)', () => {
    const { edges } = buildDependencyGraph(graph)
    const e2 = edges.find((e) => e.id === 'e2')!.data as { width: number } // count 100
    const e3 = edges.find((e) => e.id === 'e3')!.data as { width: number } // count 3
    expect(e2.width).toBeGreaterThan(e3.width)
  })

  it('drops dangling edges whose endpoints are not in the page', () => {
    const { edges } = buildDependencyGraph(graph)
    expect(edges.find((e) => e.id === 'e-dangle')).toBeUndefined()
    expect(edges).toHaveLength(3)
  })
})

describe('buildDependencyGraph — layout', () => {
  it('places callers left of MCP servers left of tools', () => {
    const { nodes } = buildDependencyGraph(graph)
    const x = (id: string) => nodes.find((n) => n.id === id)!.position.x
    expect(x('agent-a')).toBeLessThan(x('mcp-fs'))
    expect(x('mcp-fs')).toBeLessThan(x('tool-read'))
  })
})
