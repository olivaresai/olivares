// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import {
  applyNodeChanges,
  Background,
  BackgroundVariant,
  Controls,
  type Edge,
  type EdgeTypes,
  MiniMap,
  type Node,
  type NodeChange,
  type NodeTypes,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
} from '@xyflow/react'
import {
  type ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import { useTranslation } from 'react-i18next'
import '@xyflow/react/dist/style.css'
import { cn } from '@/lib/utils'
import { useIsDark } from './theme'

/**
 * GraphCanvas — the shared React Flow chrome for the access map and the
 * health dependency map. It fixes the interaction model (pan/zoom, no drag,
 * selectable), the minimap/controls/background, and theme tracking, so each view
 * only supplies its typed nodes/edges and overlay panels. The graph is read-only
 * (nodesDraggable=false) — it presents the engine's data; it never edits it
 * (ARCHITECTURE.md) — which also keeps the layout deterministic for the screenshot.
 */
export interface GraphCanvasProps {
  nodes: Node[]
  edges: Edge[]
  nodeTypes?: NodeTypes
  edgeTypes?: EdgeTypes
  onNodeClick?: (node: Node) => void
  onEdgeClick?: (edge: Edge) => void
  onPaneClick?: () => void
  /** Minimap dot color per node (defaults to a muted token). */
  minimapColor?: (node: Node) => string
  /** A value that, when it changes, re-runs fitView (e.g. the active filter set). */
  fitKey?: string
  /**
   * Suelo del AJUSTE automático, opt-in. Sin él, `fitView` encoge lo que haga falta
   * para meter el grafo entero — y a partir de cierto factor eso deja las etiquetas
   * ilegibles. Pásalo sólo si tu grafo puede crecer hasta ahí: a un grafo pequeño le
   * RECORTA el overview inicial en vez de arreglarle nada (medido: health, 15 nodos).
   * `LEGIBLE_FIT_MIN_ZOOM` es el valor calibrado para las etiquetas `text-xs`.
   */
  fitMinZoom?: number
  /** Ceiling of the automatic fit, opt-in. With `fitMinZoom` equal to it the page always
   * lands on exactly that zoom, so a level-of-detail caller that reads `onViewportZoom`
   * is never told a zoom its own refit chose and cannot flip back and forth. */
  fitMaxZoom?: number
  /** Floor of the zoom the USER can reach (default 0.1). A caller whose labels are
   * 12 px at zoom 1 raises it to 1 while it shows its coarsest level of detail, so
   * there is nothing left to shrink the text. */
  minZoom?: number
  /** The zoom the viewport settled on after a pan, wheel, pinch or control press.
   * A level-of-detail caller reads it to group what it can no longer draw legibly. */
  onViewportZoom?: (zoom: number) => void
  /** Minimum clearance between measured nodes in each existing column. Opt in
   * when labels wrap; the original layout remains the minimum row spacing. */
  verticalNodeGap?: number
  /** Accessible name for the graph region (a textual alternative — WCAG 1.1.1).
   *  Set it and the wrapper becomes a labelled group for assistive tech. */
  ariaLabel?: string
  /** Overlay panels rendered inside the canvas (legend, filters, counts). */
  children?: ReactNode
  className?: string
}

/**
 * ⛔ POR QUÉ ESTE GANCHO EXISTE — el minimapa salía VACÍO en todas las pantallas de grafo, y así
 *    llevaba meses en el material de lanzamiento.
 *
 * Medido en navegador el 2026-08-17 contra la vista viva: el grafo pintaba **7 nodos y 4 aristas**
 * y el minimapa pintaba **0** — su SVG tenía dos hijos, el `<title>` y la máscara, ni un `<rect>`.
 * De ahí sale el «rectángulo gris vacío» que aparece en las capturas del access-map del README, de
 * `docs-site/` y del thumbnail de YouTube: no es el héroe de marca, que está sano, ni un fallo de
 * temporización de la captura. Es esto, y es determinista.
 *
 * LA CAUSA: `MiniMapNodes` descarta cada nodo con `!nodeHasDimensions(node)`, y el nodo que mira
 * es `node.internals.userNode` — el objeto que le pasa ESTA consola. Con `nodes` totalmente
 * controlados y SIN `onNodesChange`, React Flow v12 nunca devuelve las medidas a ese objeto: el
 * lienzo se pinta con los internos (por eso el grafo se ve bien) y el minimapa se queda a cero.
 *
 * POR QUÉ NO SE ARREGLA PONIENDO `width`/`height` EN EL NODO, que fue lo primero que probé: en el
 * navegador el minimapa pasó de 0 a 7 nodos —confirma la causa— pero forzó los nodos a un tamaño
 * fijo, `style.width: 200px`, y **el texto quedó cortado** (`scrollWidth > clientWidth` en los
 * cuatro que medí, contra los 148-220 px que medían al dimensionarse por contenido). Arregla el
 * minimapa rompiendo las etiquetas.
 *
 * LO QUE HACE ESTE GANCHO: deja que las medidas vuelvan al objeto del nodo sin tocar su tamaño.
 * Y al re-sembrar desde las props **conserva `measured` por id**, porque si el padre reconstruye
 * el array —lo normal cuando cambia un filtro— un re-sembrado ingenuo borraría la medida y el
 * minimapa volvería a vaciarse en cada cambio, que es el mismo defecto con menos testigos.
 */
function useMeasurableNodes(nodes: Node[]) {
  const [rfNodes, setRfNodes] = useState<Node[]>(nodes)
  const [prevNodes, setPrevNodes] = useState(nodes)

  if (nodes !== prevNodes) {
    setPrevNodes(nodes)
    setRfNodes((prev) => {
      const medidos = new Map(prev.map((n) => [n.id, n.measured]))
      return nodes.map((n) => {
        const measured = medidos.get(n.id)
        return measured ? { ...n, measured } : n
      })
    })
  }

  const onNodesChange = useCallback((changes: NodeChange[]) => {
    setRfNodes((prev) => applyNodeChanges(changes, prev))
  }, [])

  return { rfNodes, onNodesChange }
}

/** Preserve column/order and expand only rows whose measured labels need room.
 * Derived from the original positions so a shorter label can reclaim its space. */
function spaceMeasuredNodes(nodes: Node[], gap: number): Node[] {
  const columns = new Map<number, Node[]>()
  for (const node of nodes) {
    const column = columns.get(node.position.x) ?? []
    column.push(node)
    columns.set(node.position.x, column)
  }
  const positions = new Map<string, number>()
  for (const column of columns.values()) {
    column.sort((a, b) => a.position.y - b.position.y)
    let bottom = -Infinity
    for (const node of column) {
      const y = Math.max(node.position.y, bottom + gap)
      positions.set(node.id, y)
      bottom = y + (node.measured?.height ?? 0)
    }
  }
  return nodes.map((node) => {
    const y = positions.get(node.id)!
    return y === node.position.y
      ? node
      : { ...node, position: { ...node.position, y } }
  })
}

/**
 * Minimum zoom for automatic fitting, **per caller**.
 *
 * ⛔ WHY THIS EXISTS, measured on the access map: without a floor, `fitView` does what
 * it is asked — fit the whole graph — even when that shrinks it to about 0.12, which
 * paints a `text-xs` label (12 px) at about 1.5 px. A fit that cannot be read has not
 * fitted anything.
 *
 * The value is 1: the smallest text on the canvas is 12 px at zoom 1, and painted size
 * equals size × zoom, so 1 is the lowest value at which the page's chosen fit never
 * paints text below 12 px. A graph that does not fit at that zoom is panned; showing
 * everything is the grouped-detail level's job.
 *
 * Callers opt into this floor because applying it globally clips other views.
 * For example, health's 15-node overview needs 473 px of graph in 440 px of canvas.
 * Views without a legibility requirement keep the default with no zoom floor.
 */
export const LEGIBLE_FIT_MIN_ZOOM = 1

function FitOnChange({
  dep,
  baseDep,
  minZoom,
  maxZoom,
}: {
  dep: string | undefined
  baseDep: string | undefined
  minZoom?: number
  maxZoom?: number
}) {
  const { fitView, getViewport, getNodes, getNodesBounds, setCenter } =
    useReactFlow()
  const previousMaxZoom = useRef(maxZoom)
  const previousBaseDep = useRef(baseDep)
  const preserveZoom = useRef(false)
  useEffect(() => {
    const baseChanged = previousBaseDep.current !== baseDep
    previousBaseDep.current = baseDep
    const ceilingRemoved =
      previousMaxZoom.current !== undefined && maxZoom === undefined
    previousMaxZoom.current = maxZoom
    if (maxZoom !== undefined || baseChanged) preserveZoom.current = false
    else if (ceilingRemoved && getViewport().zoom >= (minZoom ?? 0)) {
      // Removing the ceiling is the detailed half of an LOD switch. Preserve a
      // deliberate zoom-in through the new nodes and their measurements too.
      preserveZoom.current = true
    }
    if (preserveZoom.current) {
      const nodes = getNodes()
      if (nodes.length === 0) return
      const bounds = getNodesBounds(nodes)
      const zoom = getViewport().zoom
      // Recenter on the new layout without undoing the operator's pinch or wheel.
      void setCenter(
        bounds.x + bounds.width / 2,
        bounds.y + bounds.height / 2,
        { zoom, duration: 200 },
      )
      return
    }
    // Defer to the next frame so nodes are measured before fitting.
    const id = requestAnimationFrame(() =>
      fitView({ padding: 0.18, minZoom, maxZoom, duration: 200 }),
    )
    return () => cancelAnimationFrame(id)
  }, [
    dep,
    baseDep,
    fitView,
    getViewport,
    getNodes,
    getNodesBounds,
    setCenter,
    minZoom,
    maxZoom,
  ])
  return null
}

export function GraphCanvas({
  nodes,
  edges,
  nodeTypes,
  edgeTypes,
  onNodeClick,
  onEdgeClick,
  onPaneClick,
  minimapColor,
  fitKey,
  fitMinZoom,
  fitMaxZoom,
  minZoom = 0.1,
  onViewportZoom,
  verticalNodeGap,
  ariaLabel,
  children,
  className,
}: GraphCanvasProps) {
  const isDark = useIsDark()
  const controlZoomPending = useRef(false)
  const { t } = useTranslation('common')
  const { rfNodes, onNodesChange } = useMeasurableNodes(nodes)
  const renderedNodes = useMemo(
    () =>
      verticalNodeGap === undefined
        ? rfNodes
        : spaceMeasuredNodes(rfNodes, verticalNodeGap),
    [rfNodes, verticalNodeGap],
  )
  const measuredFitKey =
    verticalNodeGap === undefined
      ? fitKey
      : `${fitKey ?? ''}|${renderedNodes.map((node) => `${node.id}:${node.position.y}:${node.measured?.height ?? 0}`).join('|')}`
  return (
    <div
      role={ariaLabel ? 'group' : undefined}
      aria-label={ariaLabel}
      className={cn(
        'relative h-full w-full overflow-hidden rounded-lg border border-border bg-surface',
        className,
      )}
    >
      <ReactFlowProvider>
        <ReactFlow
          nodes={renderedNodes}
          onNodesChange={onNodesChange}
          edges={edges}
          nodeTypes={nodeTypes}
          edgeTypes={edgeTypes}
          colorMode={isDark ? 'dark' : 'light'}
          fitView
          fitViewOptions={{
            padding: 0.18,
            minZoom: fitMinZoom,
            maxZoom: fitMaxZoom,
          }}
          // El SUELO va en `fitViewOptions`, no aquí: esta prop es el límite del
          // usuario, y subirla le impediría alejar para ver el conjunto — que es
          // justo el escape que el minimapa y los Controls ofrecen.
          minZoom={minZoom}
          maxZoom={2.5}
          onMoveEnd={(event, viewport) => {
            // fitView and control-button zooms both end with a null source event.
            // Only the latter are operator intent: automatic layout must not switch LOD.
            if (event !== null || controlZoomPending.current)
              onViewportZoom?.(viewport.zoom)
            controlZoomPending.current = false
          }}
          nodesDraggable={false}
          nodesConnectable={false}
          // Forcing focusability gives nodes tabIndex=0 and role="group", but
          // these nodes have no ariaLabel. Keep keyboard navigation in the named
          // "Map as a list" view instead of adding up to 500 unnamed tab stops.
          elementsSelectable
          panOnScroll
          selectionOnDrag={false}
          proOptions={{ hideAttribution: false }}
          onNodeClick={(_, n) => onNodeClick?.(n)}
          onEdgeClick={(_, e) => onEdgeClick?.(e)}
          onPaneClick={() => onPaneClick?.()}
        >
          <Background
            variant={BackgroundVariant.Dots}
            gap={22}
            size={1}
            className="text-border-strong"
          />
          <Controls
            showInteractive={false}
            onZoomIn={() => {
              controlZoomPending.current = true
            }}
            onZoomOut={() => {
              controlZoomPending.current = true
            }}
            // Controls calls fitView independently, so its fit button must use
            // this canvas's zoom limits as well.
            fitViewOptions={{
              padding: 0.18,
              minZoom: fitMinZoom,
              maxZoom: fitMaxZoom,
            }}
            // WCAG 2.2 SC 2.5.8: keep every control button ≥24×24 CSS px. The
            // zoom-in / zoom-out / fit-view buttons are also the single-pointer,
            // non-drag alternative to wheel-zoom / drag-pan (SC 2.5.7).
            className="!shadow-md [&_button]:!min-h-6 [&_button]:!min-w-6 [&_button]:!border-border [&_button]:!bg-surface [&_button]:!text-muted-foreground hover:[&_button]:!bg-muted"
          />
          <MiniMap
            pannable
            zoomable
            ariaLabel={t('a11y.graphMinimap')}
            nodeColor={minimapColor ?? (() => 'var(--color-graphite-400)')}
            maskColor="color-mix(in oklab, var(--overlay) 40%, transparent)"
            className="!bg-elevated"
          />
          <FitOnChange
            dep={measuredFitKey}
            baseDep={fitKey}
            minZoom={fitMinZoom}
            maxZoom={fitMaxZoom}
          />
          {children}
        </ReactFlow>
      </ReactFlowProvider>
    </div>
  )
}
