// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { buildGraph, emptyFilter } from './graph-model'
import type { AccessEdge, DiffResponse, GraphResponse } from './types'

function edge(over: Partial<AccessEdge> & Pick<AccessEdge, 'id'>): AccessEdge {
  return {
    origin_kind: 'agent',
    origin_id: 'A1',
    origin_ref: 'agent-1',
    resource_id: 'R1',
    resource_kind: 'postgres.table',
    resource_ref: 'appdb.public.t1',
    mode: 'read',
    signal_source: 'pg_audit',
    signal_sources: 'pg_audit',
    confidence: 'attributed',
    bridged: true,
    observed: true,
    permitted: true,
    occurrence_count: 1,
    first_seen: '2026-06-03T12:00:00Z',
    last_seen: '2026-06-03T12:30:00Z',
    ...over,
  }
}

const graph: GraphResponse = {
  nodes: [
    { id: 'A1', kind: 'agent', ref: 'agent-1' },
    { id: 'R1', kind: 'postgres.table', ref: 'appdb.public.t1' },
    { id: 'R2', kind: 'postgres.table', ref: 'appdb.public.secrets' },
    { id: 'R3', kind: 's3.bucket', ref: 'backups' },
    { id: 'R4', kind: 'postgres.table', ref: 'appdb.public.audit' },
  ],
  edges: [
    edge({
      id: 'e1',
      resource_id: 'R1',
      resource_ref: 'appdb.public.t1',
      mode: 'read',
      confidence: 'attributed',
    }),
    edge({
      id: 'e2',
      resource_id: 'R2',
      resource_ref: 'appdb.public.secrets',
      mode: 'readwrite',
      confidence: 'attributed',
      signal_source: 'otel',
      observed: true,
      permitted: false,
    }),
    edge({
      id: 'e3',
      resource_id: 'R3',
      resource_ref: 'backups',
      mode: 'read',
      confidence: 'approximate',
      signal_source: 'ebpf',
    }),
    edge({
      id: 'e4',
      resource_id: 'R4',
      resource_ref: 'appdb.public.audit',
      mode: 'write',
      confidence: 'attributed',
      signal_source: 'ebpf',
      observed: true,
      permitted: false,
    }),
  ],
  has_more: false,
}

const diff: DiffResponse = {
  unexpected_accesses: [
    {
      kind: 'unexpected_access',
      reconciliation_pending: false,
      edge: graph.edges[1]!,
    },
    {
      kind: 'unexpected_access',
      reconciliation_pending: true,
      edge: graph.edges[3]!,
    },
  ],
  unused_grants: [
    {
      kind: 'unused_grant',
      edge: edge({
        id: 'g1',
        resource_id: 'R5',
        resource_ref: 'appdb.public.unused',
        mode: 'read',
        permitted: true,
        observed: false,
      }),
    },
  ],
  unexpected_count: 2,
  unused_count: 1,
}

describe('buildGraph — encoding', () => {
  it('colors read blue and write/readwrite copper-and-thicker', () => {
    const { edges } = buildGraph({ graph, filter: emptyFilter() })
    const e1 = edges.find((e) => e.id === 'e1')!.data as {
      color: string
      width: number
      token: string
    }
    const e2 = edges.find((e) => e.id === 'e2')!.data as {
      color: string
      width: number
      token: string
    }
    expect(e1.color).toBe('var(--color-info)')
    expect(e1.token).toBe('R')
    expect(e2.color).toBe('var(--color-accent-text)')
    expect(e2.token).toBe('RW')
    // Risk (RW) is more prominent than read.
    expect(e2.width).toBeGreaterThan(e1.width)
  })

  it('renders approximate confidence as dashed + dimmer, never firm', () => {
    const { edges } = buildGraph({ graph, filter: emptyFilter() })
    const e3 = edges.find((e) => e.id === 'e3')!.data as {
      dashed: boolean
      opacity: number
    }
    const e1 = edges.find((e) => e.id === 'e1')!.data as {
      dashed: boolean
      opacity: number
    }
    expect(e3.dashed).toBe(true)
    expect(e1.dashed).toBe(false)
    expect(e3.opacity).toBeLessThan(e1.opacity)
  })

  it('counts origins, resources, modes and approximate', () => {
    const { stats } = buildGraph({ graph, filter: emptyFilter() })
    expect(stats.origins).toBe(1)
    expect(stats.resources).toBe(4)
    expect(stats.read).toBe(2)
    expect(stats.write).toBe(2) // readwrite + write
    expect(stats.approximate).toBe(1)
  })

  it('lays out origins left of resources', () => {
    const { nodes } = buildGraph({ graph, filter: emptyFilter() })
    const a = nodes.find((n) => n.id === 'A1')!
    const r = nodes.find((n) => n.id === 'R1')!
    expect(a.position.x).toBeLessThan(r.position.x)
    expect(a.type).toBe('origin')
    expect(r.type).toBe('resource')
  })
})

describe('buildGraph — filters', () => {
  it('filters by mode', () => {
    const f = emptyFilter()
    f.modes = new Set(['read'])
    const { edges } = buildGraph({ graph, filter: f })
    expect(edges.map((e) => e.id).sort()).toEqual(['e1', 'e3'])
  })

  it('"only attributed" hides approximate edges', () => {
    const f = emptyFilter()
    f.confidence = 'attributed'
    const { edges } = buildGraph({ graph, filter: f })
    expect(edges.find((e) => e.id === 'e3')).toBeUndefined()
  })

  it('searches redacted refs', () => {
    const f = emptyFilter()
    f.search = 'secrets'
    const { edges } = buildGraph({ graph, filter: f })
    expect(edges.map((e) => e.id)).toEqual(['e2'])
  })

  it('filters by signal source', () => {
    const f = emptyFilter()
    f.signalSource = 'ebpf'
    const { edges } = buildGraph({ graph, filter: f })
    expect(edges.map((e) => e.id).sort()).toEqual(['e3', 'e4'])
  })
})

describe('buildGraph — permitted vs observed overlay', () => {
  it('classifies unexpected (danger) and pending (amber) and adds unused grants', () => {
    const { edges, stats } = buildGraph({
      graph,
      diff,
      overlay: true,
      filter: emptyFilter(),
    })
    const e2 = edges.find((e) => e.id === 'e2')!.data as {
      category: string
      color: string
    }
    const e4 = edges.find((e) => e.id === 'e4')!.data as {
      category: string
      color: string
      dashed: boolean
    }
    const g1 = edges.find((e) => e.id === 'g1')!.data as {
      category: string
      color: string
    }

    expect(e2.category).toBe('unexpected')
    expect(e2.color).toBe('var(--color-danger)')
    // reconciliation_pending → amber "pending", NEVER red.
    expect(e4.category).toBe('pending')
    expect(e4.color).toBe('var(--color-warning)')
    // unused grant is surfaced even though it is not in the observed graph.
    expect(g1.category).toBe('unused')
    expect(g1.color).toBe('var(--color-warning)')

    expect(stats.unexpected).toBe(1)
    expect(stats.pending).toBe(1)
    expect(stats.unused).toBe(1)
  })

  it('marks the touched resource node as having unexpected access', () => {
    const { nodes } = buildGraph({
      graph,
      diff,
      overlay: true,
      filter: emptyFilter(),
    })
    const r2 = nodes.find((n) => n.id === 'R2')!.data as {
      hasUnexpected: boolean
    }
    const r1 = nodes.find((n) => n.id === 'R1')!.data as {
      hasUnexpected: boolean
    }
    expect(r2.hasUnexpected).toBe(true)
    expect(r1.hasUnexpected).toBe(false)
  })

  it('dims plain edges so the findings pop', () => {
    const { edges } = buildGraph({
      graph,
      diff,
      overlay: true,
      filter: emptyFilter(),
    })
    const e1 = edges.find((e) => e.id === 'e1')!.data as {
      opacity: number
      category: string
    }
    expect(e1.category).toBe('normal')
    expect(e1.opacity).toBeLessThan(0.3)
  })

  it('does not classify when overlay is off (no diff applied)', () => {
    const { edges } = buildGraph({
      graph,
      diff,
      overlay: false,
      filter: emptyFilter(),
    })
    expect(
      edges.every(
        (e) => (e.data as { category: string }).category === 'normal',
      ),
    ).toBe(true)
    expect(edges.find((e) => e.id === 'g1')).toBeUndefined()
  })

  // ⛔ ESTE CASO VIGILA EL LLAMANTE, no el algoritmo. `layout.test.ts` prueba que
  // `maxPerColumn` envuelve; esto prueba que el access map LO PIDE. Sin él, borrar
  // la opción de `buildGraph` devolvería la vista a la columna de 4180 px y ninguna
  // batería caería: el defecto no estaba en el layout, estaba en no usarlo.
  it('un estate alto no sale como una sola columna vertical', () => {
    const origins = 56
    const resources = 18
    const big: GraphResponse = {
      nodes: [
        ...Array.from({ length: origins }, (_, i) => ({
          id: `O${i}`,
          kind: 'agent',
          ref: `agent-${i}`,
        })),
        ...Array.from({ length: resources }, (_, i) => ({
          id: `R${i}`,
          kind: 'postgres.table',
          ref: `appdb.public.t${i}`,
        })),
      ],
      edges: Array.from({ length: origins }, (_, i) =>
        edge({
          id: `b${i}`,
          origin_id: `O${i}`,
          origin_ref: `agent-${i}`,
          resource_id: `R${i % resources}`,
          resource_ref: `appdb.public.t${i % resources}`,
        }),
      ),
    } as GraphResponse

    const { nodes } = buildGraph({
      graph: big,
      overlay: false,
      filter: emptyFilter(),
    })
    const ys = nodes.map((n) => n.position.y)
    const xs = [...new Set(nodes.map((n) => n.position.x))]
    const alto = Math.max(...ys) - Math.min(...ys)
    const ancho = Math.max(...xs) - Math.min(...xs)

    // Sin envolver serían 55 · 76 = 4180 px de alto por 360 de ancho.
    expect(alto).toBeLessThanOrEqual(1100)
    expect(xs.length).toBeGreaterThan(2) // más columnas que capas ⇒ hubo envoltura
    // Y lo que de verdad se pide: que quepa sin ser una tira vertical.
    expect(alto / ancho).toBeLessThan(1.5)
  })
})
