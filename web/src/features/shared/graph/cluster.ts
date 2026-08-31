// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// clusterByHost — the documented level-of-detail (LOD) degradation path for the
// access map at scale (UI-CONTRACT-ACCESS-MAP "scale"). It is a PURE transform
// over the SAME built model the renderers consume (RFNode[] / RFEdge[]); it never
// fetches, never recomputes a relationship, and never invents data — it only
// AGGREGATES the already-fetched, already-redacted contract so a very large
// estate stays legible.
//
// Honesty rules (non-negotiable — a cluster must never lie):
//   - Origins are NEVER clustered. Only RESOURCE nodes collapse, by a derived
//     "host" key (leading segment of resourceKind, e.g. `postgres.table` →
//     `postgres`; `s3.bucket` → `s3`; falling back to `kind`). The host is a
//     facet already present in the redacted ref's kind — no new information.
//   - A cluster node is LABELLED `host (n)` and carries its member count, so it
//     is unmistakably "N items", never masquerading as a single resource.
//   - hasUnexpected = OR over members: if ANY member resource carries an
//     unexpected/pending finding, the whole cluster is flagged danger. A finding
//     is NEVER hidden by aggregation.
//   - The cluster's mode is the RISKIEST member's (RW > W > R > unknown), so the
//     collapsed edge never under-states the access it represents.
//   - Edges origin→resource are rewritten to origin→cluster and de-duplicated;
//     where several member edges collapse onto one origin→cluster edge, the
//     surviving edge takes the riskiest category/style so risk is preserved.
//
// Below `opts.threshold` it is a pure pass-through (clustered=false), so small
// estates keep the polished, fully-detailed view untouched.

import type { Edge as RFEdge, Node as RFNode } from '@xyflow/react'
import type {
  AccessEdgeData,
  EdgeCategory,
  OriginNodeData,
  ResourceNodeData,
} from '@/features/access-map/graph-model'
import type { AccessMapMode } from '@/features/access-map/types'

export interface ClusterOptions {
  /** Cluster only when the node count exceeds this. */
  threshold: number
}

export interface ClusterResult {
  nodes: RFNode[]
  edges: RFEdge[]
  /** True when aggregation actually happened (above threshold). */
  clustered: boolean
}

/** Derive the host key for a resource node: the leading dotted segment of its
 * resourceKind (the same facet `resourceIconKey` reads), or its kind. */
function hostKeyOf(data: ResourceNodeData): string {
  const k = (data.resourceKind ?? data.kind ?? '').trim()
  if (!k) return 'resource'
  const head = k.split('.', 1)[0]!.trim()
  return head || k
}

/** Risk ranking of an access mode: RW(3) > W(2) > R(1) > unknown(0). Higher wins
 * when several member edges collapse, so the cluster never under-states risk. */
const MODE_RISK: Record<string, number> = {
  readwrite: 3,
  write: 2,
  read: 1,
}
function modeRisk(mode: string): number {
  return MODE_RISK[mode] ?? 0
}

/** Category ranking so the surviving collapsed edge keeps the most prominent
 * finding (unexpected > pending > unused > normal). Mirrors graph-model styling. */
const CATEGORY_RANK: Record<EdgeCategory, number> = {
  unexpected: 3,
  pending: 2,
  unused: 1,
  normal: 0,
}

/**
 * clusterByHost aggregates resource nodes (and the edges into them) by host when
 * the graph is larger than the threshold. Pure and deterministic.
 */
export function clusterByHost(
  nodes: RFNode[],
  edges: RFEdge[],
  opts: ClusterOptions,
): ClusterResult {
  if (nodes.length <= opts.threshold) {
    return { nodes, edges, clustered: false }
  }

  // 1. Partition nodes; map every resource id → its cluster (host) id.
  const origins: RFNode[] = []
  const resourceToCluster = new Map<string, string>()
  // Per-cluster accumulator: keeps the host, member count, OR'd unexpected flag,
  // the riskiest member's resourceKind/coverage, and the worst dimmed state.
  interface ClusterAcc {
    id: string
    host: string
    count: number
    hasUnexpected: boolean
    /** Riskiest member's mode rank — drives the representative resourceKind. */
    bestRisk: number
    resourceKind: string
    coverageTier?: string
    dimmed: boolean
    /** Stable layout anchor: average of member positions (deterministic). */
    sumX: number
    sumY: number
  }
  const clusters = new Map<string, ClusterAcc>()

  for (const n of nodes) {
    const role = (n.data as { role?: string }).role
    if (role !== 'resource') {
      origins.push(n)
      continue
    }
    const data = n.data as ResourceNodeData
    const host = hostKeyOf(data)
    const clusterId = `cluster:${host}`
    resourceToCluster.set(n.id, clusterId)

    const risk = modeRisk(data.resourceKind ?? '')
    const acc = clusters.get(clusterId)
    if (!acc) {
      clusters.set(clusterId, {
        id: clusterId,
        host,
        count: 1,
        hasUnexpected: !!data.hasUnexpected,
        bestRisk: risk,
        resourceKind: data.resourceKind ?? data.kind,
        coverageTier: data.coverageTier,
        dimmed: !!data.dimmed,
        sumX: n.position.x,
        sumY: n.position.y,
      })
    } else {
      acc.count += 1
      acc.hasUnexpected = acc.hasUnexpected || !!data.hasUnexpected
      acc.sumX += n.position.x
      acc.sumY += n.position.y
      // A cluster stays dimmed only if EVERY member is dimmed (any live member
      // keeps it visible — never dim away a node the overlay wants to show).
      acc.dimmed = acc.dimmed && !!data.dimmed
    }
  }

  // 2. Rewrite edges origin→resource into origin→cluster and de-duplicate,
  //    keeping the riskiest mode/category on the surviving edge. The OR over
  //    member edges' modes also drives each cluster's representative mode.
  interface EdgeAcc {
    edge: RFEdge
    risk: number
    catRank: number
    /** Riskiest member mode seen for the cluster (to flag its mode). */
  }
  const collapsed = new Map<string, EdgeAcc>()
  // Track the riskiest MODE that lands on each cluster so the node's danger /
  // labelling reflects "the worst thing that touches it".
  const clusterMode = new Map<string, AccessMapMode>()

  for (const e of edges) {
    const targetCluster = resourceToCluster.get(e.target)
    // Source must be an origin (origins are not clustered); if for any reason a
    // source was a resource that got clustered, rewrite it too so we never dangle.
    const source = resourceToCluster.get(e.source) ?? e.source
    const target = targetCluster ?? e.target
    if (!targetCluster) {
      // Edge into a non-resource (should not happen in this bipartite graph) —
      // keep it verbatim but still remap a clustered source.
      const key = `${source}→${target}#${e.id}`
      collapsed.set(key, {
        edge: { ...e, source, target },
        risk: -1,
        catRank: -1,
      })
      continue
    }

    const data = e.data as AccessEdgeData | undefined
    const risk = data ? modeRisk(data.mode) : 0
    const catRank = data ? CATEGORY_RANK[data.category] : 0

    // Track the riskiest mode onto the cluster node.
    const prevMode = clusterMode.get(targetCluster)
    if (!prevMode || risk > modeRisk(prevMode)) {
      if (data) clusterMode.set(targetCluster, data.mode)
    }

    const key = `${source}→${target}`
    const existing = collapsed.get(key)
    if (
      !existing ||
      catRank > existing.catRank ||
      (catRank === existing.catRank && risk > existing.risk)
    ) {
      collapsed.set(key, {
        edge: {
          ...e,
          id: existing
            ? existing.edge.id
            : `${e.id}` /* keep a stable, lookupable id */,
          source,
          target,
        },
        risk,
        catRank,
      })
    }
  }

  // 3. Materialize cluster nodes. Label = `host (n)`; danger flag preserved.
  const clusterNodes: RFNode[] = []
  for (const acc of clusters.values()) {
    // Resolve the cluster's representative mode (riskiest edge into it) so the
    // resourceKind label reflects the worst access; default to its accumulated
    // resourceKind otherwise.
    const data: ResourceNodeData = {
      role: 'resource',
      label: `${acc.host} (${acc.count})`,
      kind: acc.resourceKind,
      resourceKind: acc.resourceKind,
      coverageTier: acc.coverageTier,
      hasUnexpected: acc.hasUnexpected,
      dimmed: acc.dimmed,
      // Cluster-only metadata (honest "this is N items"). Consumers may read it;
      // existing consumers ignore unknown keys (Record<string, unknown>).
      cluster: true,
      clusterHost: acc.host,
      clusterCount: acc.count,
    }
    clusterNodes.push({
      id: acc.id,
      type: 'resource',
      position: { x: acc.sumX / acc.count, y: acc.sumY / acc.count },
      data,
    })
  }

  const outNodes = [...origins, ...clusterNodes]
  const outEdges = [...collapsed.values()].map((a) => a.edge)

  return { nodes: outNodes, edges: outEdges, clustered: true }
}

/** Re-export the origin guard shape for callers/tests that need the predicate. */
export function isOriginNode(node: RFNode): boolean {
  return (node.data as OriginNodeData | ResourceNodeData).role === 'origin'
}
