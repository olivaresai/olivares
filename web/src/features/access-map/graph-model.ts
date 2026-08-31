// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { type Edge, MarkerType, type Node } from '@xyflow/react'
import {
  layeredLayout,
  type LayoutEdgeInput,
} from '@/features/shared/graph/layout'
import {
  type AccessEdge,
  type AccessMapMode,
  type DiffResponse,
  type GraphResponse,
  isWriteMode,
  modeToken,
} from './types'

/**
 * The visual model of the R/RW map: a PURE transform from the engine's node+edge
 * contract (+ optional permitted-vs-observed diff + the active filters) to React
 * Flow nodes and edges. Every encoding decision lives here so it is testable
 * without a DOM and identical between the live view and the screenshot:
 *
 *   mode        read = info(blue), write/readwrite = accent(copper, the risk),
 *               unknown = muted gray — RW edges are thicker and labelled.
 *   confidence  attributed = solid; approximate = DASHED + lower opacity, so the
 *               UI never renders an inference as if it were firm (ARCHITECTURE.md).
 *   diff        unexpected access (observed, not permitted) = DANGER, prominent,
 *               and the touched resource gets a danger ring; an unexpected access
 *               that is reconciliation-pending = AMBER ("pending"), never red
 *               (honest uncertainty); unused grant (permitted, never observed) =
 *               amber dashed, low emphasis. With the overlay on, plain edges dim
 *               so the findings pop.
 */

export interface OriginNodeData extends Record<string, unknown> {
  role: 'origin'
  label: string
  kind: string
  bridged: boolean
  dimmed: boolean
}

export interface ResourceNodeData extends Record<string, unknown> {
  role: 'resource'
  label: string
  kind: string
  resourceKind?: string
  coverageTier?: string
  hasUnexpected: boolean
  dimmed: boolean
}

export type EdgeCategory = 'normal' | 'unexpected' | 'pending' | 'unused'

export interface AccessEdgeData extends Record<string, unknown> {
  edge: AccessEdge
  category: EdgeCategory
  mode: AccessMapMode
  token: 'R' | 'RW' | 'W' | '?'
  color: string
  width: number
  dashed: boolean
  opacity: number
  showLabel: boolean
}

export interface AccessFilterState {
  /** Selected modes; empty = all. */
  modes: Set<string>
  /** `attributed` hides approximate edges (the "only firmly attributed" filter). */
  confidence: 'all' | 'attributed'
  /** Exact signal-source match, or null for all. */
  signalSource: string | null
  /** Free-text over redacted refs / kinds / signals. */
  search: string
}

export const emptyFilter = (): AccessFilterState => ({
  modes: new Set(),
  confidence: 'all',
  signalSource: null,
  search: '',
})

export interface BuildOptions {
  graph: GraphResponse
  /** When provided AND overlay is on, classifies edges and adds unused grants. */
  diff?: DiffResponse | null
  overlay?: boolean
  filter?: AccessFilterState
}

export interface BuildResult {
  nodes: Node[]
  edges: Edge[]
  stats: {
    edges: number
    origins: number
    resources: number
    read: number
    write: number
    approximate: number
    unexpected: number
    pending: number
    unused: number
  }
}

const C = {
  info: 'var(--color-info)',
  accent: 'var(--color-accent-text)',
  muted: 'var(--color-muted-foreground)',
  danger: 'var(--color-danger)',
  warning: 'var(--color-warning)',
}

function matchesFilter(e: AccessEdge, f: AccessFilterState): boolean {
  if (f.modes.size > 0 && !f.modes.has(e.mode)) return false
  if (f.confidence === 'attributed' && e.confidence !== 'attributed')
    return false
  if (f.signalSource && e.signal_source !== f.signalSource) return false
  if (f.search) {
    const q = f.search.toLowerCase()
    const hay = [
      e.origin_ref,
      e.resource_ref,
      e.tool_ref,
      e.resource_kind,
      e.origin_kind,
      e.signal_sources,
    ]
      .filter(Boolean)
      .join(' ')
      .toLowerCase()
    if (!hay.includes(q)) return false
  }
  return true
}

const ORIGIN_KINDS = new Set(['agent', 'session', 'identity'])
function isOrigin(kind: string): boolean {
  return ORIGIN_KINDS.has(kind)
}

function styleEdge(
  e: AccessEdge,
  category: EdgeCategory,
  dimNonFindings: boolean,
): AccessEdgeData {
  const token = modeToken(e.mode)
  const approximate = e.confidence === 'approximate'
  let color: string
  let width: number
  let dashed = approximate
  let opacity = approximate ? 0.55 : 0.95
  let showLabel = isWriteMode(e.mode)

  if (category === 'unexpected') {
    color = C.danger
    width = 2.75
    dashed = false
    opacity = 1
    showLabel = true
  } else if (category === 'pending') {
    color = C.warning
    width = 2.5
    dashed = true
    opacity = 1
    showLabel = true
  } else if (category === 'unused') {
    color = C.warning
    width = 1.5
    dashed = true
    opacity = 0.85
    showLabel = false
  } else {
    // normal
    color = isWriteMode(e.mode)
      ? C.accent
      : e.mode === 'read'
        ? C.info
        : C.muted
    width = isWriteMode(e.mode) ? 2.25 : e.mode === 'read' ? 1.5 : 1.25
    if (dimNonFindings) {
      opacity = 0.16
      width = 1
      showLabel = false
    }
  }

  return {
    edge: e,
    category,
    mode: e.mode,
    token,
    color,
    width,
    dashed,
    opacity,
    showLabel,
  }
}

export function buildGraph({
  graph,
  diff,
  overlay = false,
  filter = emptyFilter(),
}: BuildOptions): BuildResult {
  const overlayOn = overlay && !!diff
  const nodeRef = new Map<string, string>()
  for (const n of graph.nodes) if (n.ref) nodeRef.set(n.id, n.ref)

  // Classify edges by drift category (only meaningful with the overlay on).
  const unexpected = new Map<string, boolean /* pending */>()
  if (overlayOn && diff) {
    for (const d of diff.unexpected_accesses) {
      unexpected.set(d.edge.id, !!d.reconciliation_pending)
    }
  }

  type Entry = { e: AccessEdge; category: EdgeCategory }
  const entries: Entry[] = []
  for (const e of graph.edges) {
    if (!matchesFilter(e, filter)) continue
    let category: EdgeCategory = 'normal'
    if (overlayOn && unexpected.has(e.id)) {
      category = unexpected.get(e.id) ? 'pending' : 'unexpected'
    }
    entries.push({ e, category })
  }
  // The overlay also surfaces unused grants (permitted, never observed) — they are
  // not in /graph (that is observed-only), so add them from /drift.
  if (overlayOn && diff) {
    for (const d of diff.unused_grants) {
      if (!matchesFilter(d.edge, filter)) continue
      entries.push({ e: d.edge, category: 'unused' })
    }
  }

  // Derive the node set from the surviving edges' endpoints.
  const dimNonFindings = overlayOn
  // DANGER ON THE RESOURCE IS A CLAIM, AND IT MUST MATCH THE EDGE'S OWN. This set
  // used to fold `pending` in with `unexpected`, so a resource touched ONLY by an undecided
  // edge got the danger ring, halo, icon and triangle — while its own edge was drawn amber
  // two pixels away. That is a fourth independent derivation of "this is a finding", after
  // the sheet's headline, the sheet's explanation and the side list, and it contradicted the
  // other three. A pending finding is amber everywhere or the amber means nothing.
  const resourceUnexpected = new Set<string>()
  for (const { e, category } of entries) {
    if (category === 'unexpected') {
      resourceUnexpected.add(e.resource_id)
    }
  }

  const nodeData = new Map<string, OriginNodeData | ResourceNodeData>()
  const layoutNodes: { id: string; layer: number }[] = []
  const layoutEdges: LayoutEdgeInput[] = []

  const addNode = (
    id: string,
    kind: string,
    ref: string | undefined,
    role: 'origin' | 'resource',
    extra?: Partial<ResourceNodeData>,
  ) => {
    if (nodeData.has(id)) {
      if (role === 'resource' && extra) {
        const existing = nodeData.get(id) as ResourceNodeData
        existing.hasUnexpected = existing.hasUnexpected || !!extra.hasUnexpected
      }
      return
    }
    const label = ref || nodeRef.get(id) || shortId(id)
    if (role === 'origin') {
      nodeData.set(id, {
        role,
        label,
        kind,
        bridged: true,
        dimmed: dimNonFindings,
      })
      layoutNodes.push({ id, layer: 0 })
    } else {
      nodeData.set(id, {
        role,
        label,
        kind,
        resourceKind: kind,
        coverageTier: extra?.coverageTier,
        hasUnexpected: !!extra?.hasUnexpected,
        dimmed: dimNonFindings,
      })
      layoutNodes.push({ id, layer: 1 })
    }
  }

  let read = 0
  let write = 0
  let approximate = 0
  let unexpectedCount = 0
  let pendingCount = 0
  let unusedCount = 0

  const flowEdges: Edge[] = []
  for (const { e, category } of entries) {
    addNode(e.origin_id, e.origin_kind, e.origin_ref, 'origin')
    addNode(
      e.resource_id,
      e.resource_kind ?? 'resource',
      e.resource_ref,
      'resource',
      {
        coverageTier: e.coverage_tier,
        hasUnexpected: resourceUnexpected.has(e.resource_id),
      },
    )
    layoutEdges.push({ source: e.origin_id, target: e.resource_id })

    const data = styleEdge(e, category, dimNonFindings && category === 'normal')
    if (e.mode === 'read') read++
    if (isWriteMode(e.mode)) write++
    if (e.confidence === 'approximate') approximate++
    if (category === 'unexpected') unexpectedCount++
    if (category === 'pending') pendingCount++
    if (category === 'unused') unusedCount++

    flowEdges.push({
      id: e.id,
      source: e.origin_id,
      target: e.resource_id,
      type: 'access',
      data,
      markerEnd: {
        type: MarkerType.ArrowClosed,
        color: data.color,
        width: 16,
        height: 16,
      },
    })
  }

  const layout = layeredLayout(layoutNodes, layoutEdges, {
    layerGapX: 360,
    nodeGapY: 76,
    // ⛔ SIN ESTO la vista más visible del producto sale como una columna vertical
    // ilegible. Un estate de 56 orígenes los apilaba en UNA columna: 55 · 76 =
    // 4180 px de alto por 360 de ancho, dentro de un contenedor de ~630 px
    // (`access-map-view.tsx` `h-[70vh]`). `fitView` encogía a ~0,12 y las etiquetas
    // `text-xs` (12 px) se pintaban a ~1,5 px. Con 14 por sub-columna esos 56 pasan
    // a 988 px y el conjunto queda 1480 × 988 (medido, no estimado).
    //
    // 14 no es un número redondo: es la altura que cabe legible en el contenedor.
    //
    // ⛔ CORREGIDO tras el contraste `sol max` (F-07): AQUÍ DECÍA que por encima de
    // ~400 nodos manda `WEBGL_NODE_THRESHOLD` y «este valor sólo gobierna el tramo
    // que React Flow dibuja». **Es falso.** `buildGraph` corre ANTES de elegir
    // renderer (`access-map-view.tsx:138-164`) y Sigma copia `n.position.x/y` tal
    // cual (`sigma-graph-build.ts:94-111`), así que la envoltura le llega igual. Lo
    // que NO le llega es el suelo de zoom, que es del lienzo de React Flow. No se ha
    // medido que la envoltura empeore Sigma; lo que se corrige es la afirmación.
    maxPerColumn: 14,
  })

  const flowNodes: Node[] = layoutNodes.map(({ id }) => {
    const data = nodeData.get(id)!
    return {
      id,
      type: data.role === 'origin' ? 'origin' : 'resource',
      position: layout.positions[id] ?? { x: 0, y: 0 },
      data,
      // Resources sit on the right; let the layout drive exact placement.
    }
  })

  const origins = layoutNodes.filter((n) => n.layer === 0).length
  const resources = layoutNodes.length - origins

  return {
    nodes: flowNodes,
    edges: flowEdges,
    stats: {
      edges: flowEdges.length,
      origins,
      resources,
      read,
      write,
      approximate,
      unexpected: unexpectedCount,
      pending: pendingCount,
      unused: unusedCount,
    },
  }
}

/** A short, stable id label (first 8 chars) when no redacted ref is available. */
function shortId(id: string): string {
  return id.length > 8 ? id.slice(0, 8) : id
}

/** All distinct signal sources present in a graph (for the filter dropdown). */
export function distinctSignalSources(graph: GraphResponse): string[] {
  const set = new Set<string>()
  for (const e of graph.edges) if (e.signal_source) set.add(e.signal_source)
  return [...set].sort()
}

/** Export `isOrigin` for tests / neighbor expansion. */
export { isOrigin }
