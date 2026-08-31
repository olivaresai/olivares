// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Pure, renderer-free building blocks for the Sigma WebGL access-graph view. These
// are split out of sigma-graph.tsx so they are trivially unit-testable (no Sigma,
// no GPU) and so the component file stays component-only (react-refresh). They
// carry EVERY honest semantic of the React Flow view into graphology attributes;
// the component just mounts a Sigma renderer over the graph they produce.

import Graph from 'graphology'
import type { Edge as RFEdge, Node as RFNode } from '@xyflow/react'
import type {
  AccessEdgeData,
  ResourceNodeData,
} from '@/features/access-map/graph-model'

/** Resolve a CSS-var color string (e.g. `var(--color-accent)`) to a concrete
 * literal via getComputedStyle, exactly like chart-theme.ts — Sigma's WebGL needs
 * literal colors, not `var(--x)`. The resolver memoizes per cssString and is
 * rebuilt (cache cleared) on a `.dark` flip. */
export type ColorResolver = (cssString: string) => string

/** A literal fallback palette (dark operator theme), used when there is no layout
 * engine (jsdom) — mirrors chart-theme.FALLBACK so tests stay truthful. */
const FALLBACK_COLORS: Record<string, string> = {
  'var(--color-accent)': '#f08000',
  'var(--color-accent-text)': '#f08000',
  'var(--color-info)': '#6fb6e6',
  'var(--color-danger)': '#f0857e',
  'var(--color-warning)': '#e7b65a',
  'var(--color-muted-foreground)': '#a1a1aa',
  'var(--color-graphite-400)': '#9c9ca3',
  'var(--color-confidence-approximate)': '#9aa3b0',
}

/** Parse the custom-property name out of a `var(--name)` string. */
function varName(cssString: string): string | null {
  const m = /^var\((--[\w-]+)\)$/.exec(cssString.trim())
  return m ? m[1]! : null
}

/** Build a fresh color resolver bound to the current document theme, with its own
 * cache. Call once per (re)paint of the theme; clear by building a new one. */
export function createColorResolver(): ColorResolver {
  const cache = new Map<string, string>()
  const style =
    typeof window !== 'undefined' && typeof document !== 'undefined'
      ? getComputedStyle(document.documentElement)
      : null
  return (cssString: string): string => {
    const cached = cache.get(cssString)
    if (cached !== undefined) return cached
    let resolved = ''
    const name = varName(cssString)
    if (style && name) resolved = style.getPropertyValue(name).trim()
    if (!resolved) resolved = FALLBACK_COLORS[cssString] ?? cssString
    cache.set(cssString, resolved)
    return resolved
  }
}

// --- node/edge sizing (honest emphasis, mirrors the React Flow weights) --------
const NODE_SIZE = { origin: 9, resource: 7, danger: 11 }
const APPROX_EDGE = 'var(--color-confidence-approximate)'

/**
 * buildSigmaGraph — PURE: turn the built RFNode[]/RFEdge[] model into a graphology
 * Graph carrying the resolved literal colors and the honest sizing. Unit-testable
 * with a stub resolver; never instantiates Sigma. The node/edge KEYS are the
 * contract ids, so click events look up straight into merged.edges.
 *
 * Honest-semantic mapping (Sigma WebGL can draw neither dashes nor node rings):
 *   - edge color: verbatim from data.color (read=info, write/rw=accent, unknown=
 *     muted), EXCEPT a `normal` + dashed (approximate) edge is recolored to the
 *     slate confidence token so an inference never reads as a firm edge.
 *   - findings keep danger (unexpected, high zIndex) / amber (pending, unused low).
 *   - edge size = data.width; label = data.showLabel ? data.token : undefined.
 *   - a hasUnexpected resource → danger color + larger size + forced label (the
 *     honest substitute for the danger ring the DOM view draws).
 */
export function buildSigmaGraph(
  nodes: RFNode[],
  edges: RFEdge[],
  resolve: ColorResolver,
  nodeColor: (node: RFNode) => string,
): Graph {
  const graph = new Graph({
    type: 'directed',
    multi: true,
    allowSelfLoops: true,
  })

  for (const n of nodes) {
    const d = n.data as Partial<ResourceNodeData>
    const isOrigin = (d.role as string | undefined) === 'origin'
    const hasUnexpected = !isOrigin && !!d.hasUnexpected
    const size = isOrigin
      ? NODE_SIZE.origin
      : hasUnexpected
        ? NODE_SIZE.danger
        : NODE_SIZE.resource
    // Keep the UNRESOLVED token (cssColor) on the node so a theme flip can
    // re-resolve it faithfully without recomputing nodeColor.
    const cssColor = nodeColor(n)
    graph.addNode(n.id, {
      // x,y are MANDATORY. Negate y so the deterministic top-to-bottom column
      // order from the layered layout is preserved (React Flow y grows downward;
      // Sigma's y grows upward).
      x: n.position.x,
      y: -n.position.y,
      size: hasUnexpected ? size + 1.5 : size,
      label: String(d.label ?? n.id),
      color: resolve(cssColor),
      cssColor,
      // forceLabel a flagged resource so the danger signal can't hide at low zoom.
      forceLabel: hasUnexpected,
      // Dimmed (non-finding under the overlay) → lower zIndex. WebGL can't reliably
      // blend opacity per-node, so emphasis is by size/zIndex.
      zIndex: hasUnexpected ? 3 : d.dimmed ? 0 : 1,
      // carry the original payload so the click handler emits the SAME selection.
      access: n.data,
    })
  }

  for (const e of edges) {
    const data = e.data as AccessEdgeData | undefined
    // Skip dangling edges whose endpoints were not added (defensive; the model is
    // internally consistent but neighbor merges could in theory race).
    if (!graph.hasNode(e.source) || !graph.hasNode(e.target)) continue

    let cssColor = data ? data.color : 'var(--color-muted-foreground)'
    // Approximate edges (normal + dashed) carry NO dash in WebGL — recolor to the
    // slate confidence token so an inference is visually distinct from a firm edge
    // and never reads as certain. Findings keep their danger/amber colors.
    if (data && data.category === 'normal' && data.dashed) {
      cssColor = APPROX_EDGE
    }

    // Use the contract edge id as the KEY so clickEdge → merged.edges lookup works.
    graph.addDirectedEdgeWithKey(e.id, e.source, e.target, {
      type: 'arrow',
      size: data ? Math.max(1, data.width) : 1,
      color: resolve(cssColor),
      cssColor,
      label: data && data.showLabel ? data.token : undefined,
      forceLabel: data ? data.showLabel : false,
      // Findings ride above plain edges; unused sits below.
      zIndex:
        data?.category === 'unexpected'
          ? 3
          : data?.category === 'pending'
            ? 2
            : data?.category === 'unused'
              ? 0
              : 1,
    })
  }

  return graph
}
