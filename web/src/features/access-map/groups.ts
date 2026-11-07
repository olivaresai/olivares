// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The map at a glance: a PURE transform over the built model (graph-model.ts) that
// keeps each side of the map to what reads at zoom 1. A side with more than
// MAX_PER_SIDE nodes is grouped: resources by their first folder below the paths'
// common root (or the host of a URL, or the kind), origins by the key the caller
// gives (sessions by their tool). The operator opens a group to see its members.
//
// It never hides a finding: a group is flagged when any member is, and the edge that
// stands for several keeps the riskiest category and mode of the edges it merges.
// It never invents data: every label is a segment of a ref the engine sent.
import type { Edge, Node } from '@xyflow/react'
import { layeredLayout } from '@/features/shared/graph/layout'
import {
  ACCESS_MAP_LAYOUT_OPTIONS,
  type AccessEdgeData,
  type EdgeCategory,
  type OriginNodeData,
  type ResourceNodeData,
} from './graph-model'

/** Nodes a side shows before it groups: two columns of eight rows at zoom 1. */
export const MAX_PER_SIDE = 16
/** Most members an opened group shows before the rest go into its "+N more"; fewer
 * when the side's other nodes need the room. */
export const MEMBERS_SHOWN = 12

/** How a node is grouped: `key` is stable (the address keeps it), `ref` is the whole
 * folder, host or kind the group stands for. */
export interface GroupKey {
  key: string
  label: string
  ref: string
}

export interface GroupMember {
  id: string
  label: string
  ref: string
}

/** What a group or "+N more" node carries on top of the node data it reuses. */
export interface GroupNodeData {
  cluster: true
  /** The key the address opens, absent on a "+N more" node. */
  groupKey?: string
  more: boolean
  clusterCount: number
  members: GroupMember[]
}

export interface GroupOptions {
  /** Keys of the groups the operator opened. */
  open: ReadonlySet<string>
  /** Group every side, whatever its size (the operator zoomed out). */
  force?: boolean
  /** The group of an origin. */
  originGroup: (data: OriginNodeData) => GroupKey
}

export interface GroupResult {
  nodes: Node[]
  edges: Edge[]
  /** At least one node on the map stands for several. */
  grouped: boolean
}

type NodeData = OriginNodeData | ResourceNodeData

const CATEGORY_RANK: Record<EdgeCategory, number> = {
  unexpected: 3,
  pending: 2,
  unused: 1,
  normal: 0,
}
const MODE_RISK: Record<string, number> = { readwrite: 3, write: 2, read: 1 }

const isUrl = (ref: string) => /^[a-z][a-z0-9+.-]*:\/\//i.test(ref)
const isPath = (ref: string) =>
  !isUrl(ref) && !/\s/.test(ref) && ref.includes('/')

/** The resource grouping key: first folder below the common root, URL host or kind. */
function resourceGroups(nodes: readonly Node[]): (d: NodeData) => GroupKey {
  const dirs = nodes
    .map((n) => (n.data as NodeData).ref)
    .filter(isPath)
    .map((ref) => ref.split('/').slice(0, -1))
  const root = dirs.reduce<string[]>((common, dir, i) => {
    if (i === 0) return dir
    let n = 0
    while (n < common.length && common[n] === dir[n]) n++
    return common.slice(0, n)
  }, [])
  return (d) => {
    const url = /^([a-z][a-z0-9+.-]*:\/\/)([^/?#]+)/i.exec(d.ref)
    if (url)
      return { key: `url:${url[2]}`, label: url[2]!, ref: url[1]! + url[2] }
    if (isPath(d.ref)) {
      const rest = d.ref.split('/').slice(root.length)
      const dir = rest.length > 1 ? [...root, rest[0]!] : root
      const label = dir.filter(Boolean).at(-1) ?? '/'
      const ref = dir.join('/') || '/'
      return { key: `dir:${ref}`, label, ref }
    }
    const kind = String((d as ResourceNodeData).resourceKind ?? d.kind)
    const head = kind.split('.', 1)[0] || kind
    return { key: `kind:${head}`, label: head, ref: kind }
  }
}

interface Entry {
  node: Node
  leaves: Node[]
  flagged: boolean
  pinned: boolean
  degree: number
}

const byRank = (a: Entry, b: Entry) =>
  Number(b.flagged) - Number(a.flagged) ||
  Number(b.pinned) - Number(a.pinned) ||
  b.degree - a.degree ||
  String((a.node.data as NodeData).label).localeCompare(
    String((b.node.data as NodeData).label),
    undefined,
    { numeric: true },
  )

function member(n: Node): GroupMember {
  const d = n.data as NodeData
  return { id: n.id, label: d.label, ref: d.ref }
}

function groupNode(
  id: string,
  label: string,
  ref: string,
  leaves: Node[],
  extra: { groupKey?: string; more: boolean },
): Node {
  const datas = leaves.map((n) => n.data as NodeData)
  const data = {
    ...datas[0]!,
    label,
    ref,
    dimmed: datas.every((d) => d.dimmed),
    ...(datas[0]!.role === 'resource'
      ? {
          hasUnexpected: datas.some(
            (d) => (d as ResourceNodeData).hasUnexpected,
          ),
        }
      : {}),
    cluster: true,
    ...extra,
    clusterCount: leaves.length,
    members: leaves.map(member),
  } satisfies NodeData & GroupNodeData
  return { id, type: data.role, position: { x: 0, y: 0 }, data }
}

/**
 * groupAccessGraph returns the nodes and edges to draw. A side within MAX_PER_SIDE is
 * drawn as it is unless `force`; when nothing is grouped the input comes back as is.
 */
export function groupAccessGraph(
  nodes: Node[],
  edges: Edge[],
  opts: GroupOptions,
): GroupResult {
  const degree = new Map<string, number>()
  const flagged = new Set<string>()
  for (const e of edges) {
    degree.set(e.source, (degree.get(e.source) ?? 0) + 1)
    degree.set(e.target, (degree.get(e.target) ?? 0) + 1)
    if ((e.data as AccessEdgeData | undefined)?.category !== 'normal') {
      flagged.add(e.source)
      flagged.add(e.target)
    }
  }
  const leaf = (node: Node, pinned = false): Entry => ({
    node,
    leaves: [node],
    flagged: flagged.has(node.id),
    pinned,
    degree: degree.get(node.id) ?? 0,
  })
  const entryOf = (node: Node, leaves: Node[], pinned = false): Entry => ({
    node,
    leaves,
    flagged: leaves.some((n) => flagged.has(n.id)),
    pinned,
    degree: leaves.reduce((sum, n) => sum + (degree.get(n.id) ?? 0), 0),
  })

  const origins = nodes.filter((n) => (n.data as NodeData).role === 'origin')
  const resources = nodes.filter((n) => (n.data as NodeData).role !== 'origin')
  const resourceKey = resourceGroups(resources)

  const side = (
    members: Node[],
    keyOf: (d: NodeData) => GroupKey,
    tag: string,
  ): Entry[] => {
    if (!opts.force && members.length <= MAX_PER_SIDE)
      return members.map((n) => leaf(n))
    const groups = new Map<string, { k: GroupKey; nodes: Node[] }>()
    for (const n of members) {
      const k = keyOf(n.data as NodeData)
      const g = groups.get(k.key)
      if (g) g.nodes.push(n)
      else groups.set(k.key, { k, nodes: [n] })
    }
    // An opened group takes the room the side's other nodes leave, so opening one
    // never pushes the others into a "+N more".
    const isOpen = (key: string, size: number) =>
      size > 1 && !opts.force && opts.open.has(key)
    const opened = [...groups.values()].filter((g) =>
      isOpen(g.k.key, g.nodes.length),
    ).length
    const room = MAX_PER_SIDE - (groups.size - opened)
    const shown = Math.min(
      MEMBERS_SHOWN,
      Math.max(3, Math.floor(room / Math.max(opened, 1)) - 1),
    )
    const entries: Entry[] = []
    for (const { k, nodes: inGroup } of groups.values()) {
      if (inGroup.length === 1) entries.push(leaf(inGroup[0]!))
      else if (isOpen(k.key, inGroup.length)) {
        const ranked = inGroup.map((n) => leaf(n, true)).sort(byRank)
        const cut = ranked.length > shown + 1 ? shown : ranked.length
        entries.push(...ranked.slice(0, cut))
        const rest = ranked.slice(cut).map((e) => e.node)
        if (rest.length > 0)
          entries.push(
            entryOf(
              groupNode(`more:${k.key}`, `+${rest.length}`, k.ref, rest, {
                more: true,
              }),
              rest,
              true,
            ),
          )
      } else
        entries.push(
          entryOf(
            groupNode(`group:${k.key}`, k.label, k.ref, inGroup, {
              groupKey: k.key,
              more: false,
            }),
            inGroup,
          ),
        )
    }
    if (entries.length <= MAX_PER_SIDE) return entries
    const ranked = [...entries].sort(byRank)
    const kept = ranked.slice(0, MAX_PER_SIDE - 1)
    const rest = ranked.slice(MAX_PER_SIDE - 1).flatMap((e) => e.leaves)
    const label = `+${rest.length}`
    return [
      ...kept,
      entryOf(
        groupNode(`more:${tag}`, label, label, rest, { more: true }),
        rest,
      ),
    ]
  }

  const shown = [
    ...side(origins, (d) => opts.originGroup(d as OriginNodeData), 'origins'),
    ...side(resources, resourceKey, 'resources'),
  ]
  const visibleOf = new Map<string, string>()
  for (const e of shown)
    for (const n of e.leaves) visibleOf.set(n.id, e.node.id)
  if (shown.every((e) => e.leaves.length === 1 && e.leaves[0] === e.node))
    return { nodes, edges, grouped: false }

  // Edges between the same two drawn nodes become one, as wide as their count says
  // and as risky as the riskiest of them.
  const merged = new Map<
    string,
    { source: string; target: string; list: Edge[] }
  >()
  for (const e of edges) {
    const source = visibleOf.get(e.source) ?? e.source
    const target = visibleOf.get(e.target) ?? e.target
    const key = `${source}>${target}`
    const m = merged.get(key)
    if (m) m.list.push(e)
    else merged.set(key, { source, target, list: [e] })
  }
  const outEdges = [...merged].map(([key, { source, target, list }]) => {
    const rank = (e: Edge) => {
      const d = e.data as AccessEdgeData | undefined
      return d ? CATEGORY_RANK[d.category] * 10 + (MODE_RISK[d.mode] ?? 0) : 0
    }
    const worst = list.reduce((a, b) => (rank(b) > rank(a) ? b : a))
    if (list.length === 1) return { ...worst, source, target }
    const data = worst.data as AccessEdgeData
    const width = Math.min(data.width + Math.log2(list.length) * 1.5, 8)
    // The arrowhead is sized in stroke widths: shrink it as the stroke grows, so a
    // wide edge keeps the same arrow as a thin one.
    const marker = worst.markerEnd
    const scale = data.width / width
    return {
      ...worst,
      id: `agg:${key}`,
      source,
      target,
      data: { ...data, count: list.length, width },
      markerEnd:
        typeof marker === 'object'
          ? {
              ...marker,
              width: (marker.width ?? 16) * scale,
              height: (marker.height ?? 16) * scale,
            }
          : marker,
    }
  })

  const outNodes = shown.map((e) => e.node)
  const layout = layeredLayout(
    outNodes.map((n) => ({
      id: n.id,
      layer: (n.data as NodeData).role === 'origin' ? 0 : 1,
    })),
    outEdges,
    ACCESS_MAP_LAYOUT_OPTIONS,
  )
  return {
    nodes: outNodes.map((n) => ({
      ...n,
      position: layout.positions[n.id] ?? n.position,
    })),
    edges: outEdges,
    grouped: true,
  }
}
