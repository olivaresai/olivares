// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// SigmaGraph — a WebGL (Sigma.js v3) renderer for the R/RW access graph AT SCALE.
// It sits behind the SAME built model (graph-model.buildGraph → RFNode[]/RFEdge[])
// that the polished React Flow view (GraphCanvas) consumes; access-map-view swaps
// to it above a node-count threshold. It renders ONLY the already-fetched,
// already-redacted contract — it never fetches, recomputes a relationship, or
// re-parses a redacted ref (RBAC / audit / minimal-data are upheld by rendering
// less, never by deriving more).
//
// HONEST SEMANTICS, mapped to WebGL (Sigma cannot draw dashes or rings):
//   - mode color: read = info, write/readwrite = accent (the risk), unknown =
//     muted — taken verbatim from each edge's `data.color` (a token var string).
//   - approximate confidence: React Flow renders these DASHED + dimmed. WebGL
//     edges HAVE NO DASH. To keep an inference visually DISTINCT from a firm
//     edge (never reading as certain), a `normal` edge whose `data.dashed` is set
//     is recolored to the confidence-approximate (slate) token. Honesty preserved
//     without the dash. (See EDGE color logic below.)
//   - diff findings: unexpected = danger, thick, high zIndex; pending = warning
//     (amber); unused = warning (amber), thin, low zIndex. Verbatim from the model.
//   - hasUnexpected resource: React Flow draws a danger ring + AlertTriangle.
//     WebGL can't ring a node, so the honest signal is danger COLOR + larger
//     SIZE + a forced label — "this resource has unexpected access" still pops.
//   - edge size = data.width; edge label = data.showLabel ? data.token : none.
//
// jsdom has no WebGL, so `new Sigma(...)` throws. The renderer is only constructed
// inside a `useEffect` guarded by the container ref; the graph BUILD is a pure
// helper (`buildSigmaGraph`) that is unit-tested without ever mounting Sigma.

import { type ReactNode, useEffect, useRef } from 'react'
import Sigma from 'sigma'
import { EdgeArrowProgram } from 'sigma/rendering'
import type { Settings as SigmaSettings } from 'sigma/settings'
import type { Edge as RFEdge, Node as RFNode } from '@xyflow/react'
import { Maximize2, ZoomIn, ZoomOut } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
// This is a SHARED component (exported from the `shared` barrel, which the root chunk
// pulls in): it must not translate out of a feature's namespace. It read
// `accessMap.controls.*`, so the zoom controls of any view other than the access map
// would announce `controls.zoomIn` to a screen reader. The strings moved to the
// `shared` namespace, which this module registers itself.
import '../i18n'
import { buildSigmaGraph, createColorResolver } from './sigma-graph-build'

export interface SigmaGraphProps {
  nodes: RFNode[]
  edges: RFEdge[]
  /** Node fill color as a CSS-var string (resolved to a literal internally). */
  nodeColor: (node: RFNode) => string
  onNodeClick?: (id: string, data: Record<string, unknown>) => void
  onEdgeClick?: (id: string) => void
  onPaneClick?: () => void
  ariaLabel?: string
  /** Re-fit the camera whenever this changes (e.g. the active filter set). */
  fitKey?: string
  /** Overlay panels rendered above the canvas (legend). */
  children?: ReactNode
  className?: string
}

const SIGMA_SETTINGS: Partial<SigmaSettings> = {
  defaultEdgeType: 'arrow',
  edgeProgramClasses: { arrow: EdgeArrowProgram },
  renderEdgeLabels: true,
  enableEdgeEvents: true,
  zIndex: true,
  minCameraRatio: 0.1,
  maxCameraRatio: 10,
  labelRenderedSizeThreshold: 6,
  labelDensity: 1,
  labelGridCellSize: 100,
}

/**
 * SigmaGraph — the WebGL chrome paralleling GraphCanvas: a focusable region with
 * keyboard pan/zoom (WCAG 2.5.7 — no drag required), an accessible Controls
 * overlay, and the legend children. Sigma is constructed in an effect (jsdom-safe).
 */
export function SigmaGraph({
  nodes,
  edges,
  nodeColor,
  onNodeClick,
  onEdgeClick,
  onPaneClick,
  ariaLabel,
  fitKey,
  children,
  className,
}: SigmaGraphProps) {
  const { t } = useTranslation('shared')
  const containerRef = useRef<HTMLDivElement>(null)
  const sigmaRef = useRef<Sigma | null>(null)
  // Keep the latest callbacks in a ref so the renderer's (one-time) event wiring
  // always calls the current handlers without re-creating the renderer. Updated
  // in an effect (never during render).
  const handlersRef = useRef({ onNodeClick, onEdgeClick, onPaneClick })
  useEffect(() => {
    handlersRef.current = { onNodeClick, onEdgeClick, onPaneClick }
  }, [onNodeClick, onEdgeClick, onPaneClick])

  // Construct / tear down the renderer when the data identity changes.
  useEffect(() => {
    const container = containerRef.current
    if (!container) return

    let resolver = createColorResolver()
    const graph = buildSigmaGraph(nodes, edges, resolver, nodeColor)

    let renderer: Sigma
    try {
      renderer = new Sigma(graph, container, SIGMA_SETTINGS)
    } catch {
      // No WebGL context (jsdom / headless without GL) — render nothing rather
      // than crash. The pure builder above is what tests exercise.
      return
    }
    sigmaRef.current = renderer

    // Wire events to the (live) handlers.
    renderer.on('clickNode', ({ node }) => {
      const data = graph.getNodeAttribute(node, 'access') as Record<
        string,
        unknown
      >
      handlersRef.current.onNodeClick?.(node, data ?? {})
    })
    renderer.on('clickEdge', ({ edge }) => {
      handlersRef.current.onEdgeClick?.(edge)
    })
    renderer.on('clickStage', () => {
      handlersRef.current.onPaneClick?.()
    })

    // Re-resolve token colors on a `.dark` flip (mirror chart-theme.ts): a fresh
    // resolver (cleared cache) re-resolves each element's STORED css token to the
    // new literal, then refresh. Every node/edge carries its unresolved cssColor.
    const observer = new MutationObserver(() => {
      resolver = createColorResolver()
      const g = renderer.getGraph()
      g.forEachNode((id, attrs) => {
        const token = attrs.cssColor as string | undefined
        if (token) g.setNodeAttribute(id, 'color', resolver(token))
      })
      g.forEachEdge((id, attrs) => {
        const token = attrs.cssColor as string | undefined
        if (token) g.setEdgeAttribute(id, 'color', resolver(token))
      })
      renderer.refresh()
    })
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['class'],
    })

    return () => {
      observer.disconnect()
      renderer.kill()
      sigmaRef.current = null
    }
    // nodeColor is stable (defined in the parent's render but pure); we key the
    // effect on the data so a content change rebuilds the graph + renderer.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nodes, edges])

  // Re-fit the camera when the filter set (fitKey) changes, without a rebuild.
  useEffect(() => {
    const renderer = sigmaRef.current
    if (!renderer) return
    renderer.getCamera().animatedReset({ duration: 300 })
  }, [fitKey])

  // Accessible zoom/pan controls (also the keyboard handler targets).
  const zoomIn = () =>
    sigmaRef.current?.getCamera().animatedZoom({ duration: 200 })
  const zoomOut = () =>
    sigmaRef.current?.getCamera().animatedUnzoom({ duration: 200 })
  const reset = () =>
    sigmaRef.current?.getCamera().animatedReset({ duration: 300 })

  const onKeyDown = (e: React.KeyboardEvent) => {
    const cam = sigmaRef.current?.getCamera()
    if (!cam) return
    const state = cam.getState()
    const step = 0.1 * state.ratio
    switch (e.key) {
      case 'ArrowUp':
        cam.animate({ y: state.y - step }, { duration: 100 })
        break
      case 'ArrowDown':
        cam.animate({ y: state.y + step }, { duration: 100 })
        break
      case 'ArrowLeft':
        cam.animate({ x: state.x - step }, { duration: 100 })
        break
      case 'ArrowRight':
        cam.animate({ x: state.x + step }, { duration: 100 })
        break
      case '+':
      case '=':
        zoomIn()
        break
      case '-':
      case '_':
        zoomOut()
        break
      case '0':
        reset()
        break
      default:
        return
    }
    e.preventDefault()
  }

  return (
    <div
      role={ariaLabel ? 'group' : undefined}
      aria-label={ariaLabel}
      tabIndex={0}
      onKeyDown={onKeyDown}
      className={cn(
        'relative h-full w-full overflow-hidden rounded-lg border border-border bg-surface outline-none focus-visible:ring-2 focus-visible:ring-ring',
        className,
      )}
    >
      {/* The Sigma canvas mounts here. */}
      <div ref={containerRef} className="absolute inset-0" />

      {/* Accessible controls — single-pointer, non-drag alternative to wheel/drag
          (WCAG 2.5.7) and each ≥24×24px (SC 2.5.8). */}
      <div className="absolute bottom-3 right-3 z-10 flex flex-col gap-1">
        <ControlButton label={t('graph.controls.zoomIn')} onClick={zoomIn}>
          <ZoomIn />
        </ControlButton>
        <ControlButton label={t('graph.controls.zoomOut')} onClick={zoomOut}>
          <ZoomOut />
        </ControlButton>
        <ControlButton label={t('graph.controls.reset')} onClick={reset}>
          <Maximize2 />
        </ControlButton>
      </div>

      {children}
    </div>
  )
}

function ControlButton({
  label,
  onClick,
  children,
}: {
  label: string
  onClick: () => void
  children: ReactNode
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      onClick={onClick}
      className="flex size-7 items-center justify-center rounded-md border border-border bg-surface text-muted-foreground shadow-sm transition-colors hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none [&_svg]:size-4"
    >
      {children}
    </button>
  )
}
