// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { type Edge, MarkerType, type Node } from '@xyflow/react'
import {
  layeredLayout,
  type LayoutEdgeInput,
} from '@/features/shared/graph/layout'
import { healthToken } from './health-state-badge'
import type { DepEdge, DepGraphResponse, DepNode, HealthState } from './types'

/**
 * The visual model of the dependency map: a PURE transform from the engine's
 * node+edge contract to React Flow nodes/edges. Every encoding decision lives here so
 * it is testable without a DOM and identical between the live view and any screenshot
 * (docs UI-CONTRACT-HEALTH §4,§8):
 *
 *   layer   session=0, agent=0, mcp=1, mcp_tool=2; an unknown kind → 1, so the
 *           who-calls-what story reads left→right (caller → MCP → tool).
 *   node    COLORED BY ITS HEALTH ANNOTATION, never by kind: healthy=green,
 *           degraded=amber, down=red, unknown=GRAY. A node with no check stays gray
 *           — it is never painted green (§8).
 *   edge    styled by `relation`: uses_mcp = info(blue), uses_tool = muted(slate),
 *           delegates_to = accent(copper, the agent→agent hop); width nudges up with
 *           observed_count so a busy dependency reads heavier.
 */

export interface DepNodeData extends Record<string, unknown> {
  ref: string
  kind: string
  health: HealthState
  /** The token color the node component paints its accent with. */
  color: string
}

export interface DepEdgeData extends Record<string, unknown> {
  edge: DepEdge
  relation: string
  color: string
  width: number
  /** Short relation token for the edge label ("MCP" / "tool" / "→"). */
  token: string
}

export interface DepBuildResult {
  nodes: Node[]
  edges: Edge[]
  stats: {
    nodes: number
    edges: number
    healthy: number
    degraded: number
    down: number
    observed: number
    unknown: number
  }
}

const REL_COLOR: Record<string, string> = {
  uses_mcp: 'var(--color-info)',
  uses_tool: 'var(--color-muted-foreground)',
  delegates_to: 'var(--color-accent-text)',
}

const REL_TOKEN: Record<string, string> = {
  uses_mcp: 'MCP',
  uses_tool: 'tool',
  delegates_to: '→',
}

/** Map a dependency-node kind to its layout layer (column). */
export function layerForKind(kind: string): number {
  switch (kind) {
    case 'session':
    case 'agent':
      return 0
    case 'mcp':
      return 1
    case 'mcp_tool':
      return 2
    default:
      return 1
  }
}

/** Width grows gently with observed_count (log-ish), so a busy edge reads heavier
 * without a single hot edge dwarfing the rest. */
function edgeWidth(count: number): number {
  const c = Math.max(1, count)
  return Math.min(4, 1.25 + Math.log10(c) * 1.1)
}

function styleEdge(e: DepEdge): DepEdgeData {
  const color = REL_COLOR[String(e.relation)] ?? REL_COLOR.uses_tool!
  return {
    edge: e,
    relation: e.relation,
    color,
    width: edgeWidth(e.observed_count),
    token: REL_TOKEN[String(e.relation)] ?? '·',
  }
}

export function buildDependencyGraph(graph: DepGraphResponse): DepBuildResult {
  // Index nodes; the layout only places nodes that actually exist.
  const present = new Set(graph.nodes.map((n) => n.id))
  const nodeByIdInput = new Map<string, DepNode>()
  for (const n of graph.nodes) nodeByIdInput.set(n.id, n)

  const layoutNodes: { id: string; layer: number }[] = []
  const layoutEdges: LayoutEdgeInput[] = []

  for (const n of graph.nodes) {
    layoutNodes.push({ id: n.id, layer: layerForKind(n.kind) })
  }
  for (const e of graph.edges) {
    if (present.has(e.source) && present.has(e.target)) {
      layoutEdges.push({ source: e.source, target: e.target })
    }
  }

  const layout = layeredLayout(layoutNodes, layoutEdges, {
    layerGapX: 340,
    nodeGapY: 72,
  })

  let healthy = 0
  let degraded = 0
  let down = 0
  let observed = 0
  let unknown = 0

  const flowNodes: Node[] = graph.nodes.map((n) => {
    const state = String(n.health)
    if (state === 'healthy') healthy++
    else if (state === 'degraded') degraded++
    else if (state === 'down') down++
    else if (state === 'observed') observed++
    else unknown++
    const data: DepNodeData = {
      ref: n.ref,
      kind: n.kind,
      health: n.health,
      color: healthToken(n.health),
    }
    return {
      id: n.id,
      type: 'depNode',
      position: layout.positions[n.id] ?? { x: 0, y: 0 },
      data,
    }
  })

  const flowEdges: Edge[] = []
  for (const e of graph.edges) {
    // Drop dangling edges whose endpoints aren't in this keyset page.
    if (!present.has(e.source) || !present.has(e.target)) continue
    const data = styleEdge(e)
    flowEdges.push({
      id: e.id,
      source: e.source,
      target: e.target,
      type: 'depEdge',
      data,
      markerEnd: {
        type: MarkerType.ArrowClosed,
        color: data.color,
        width: 15,
        height: 15,
      },
    })
  }

  return {
    nodes: flowNodes,
    edges: flowEdges,
    stats: {
      nodes: flowNodes.length,
      edges: flowEdges.length,
      healthy,
      degraded,
      down,
      observed,
      unknown,
    },
  }
}
