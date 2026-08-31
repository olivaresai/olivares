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
import { type ReactNode, useCallback, useEffect, useState } from 'react'
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

  useEffect(() => {
    setRfNodes((prev) => {
      const medidos = new Map(prev.map((n) => [n.id, n.measured]))
      return nodes.map((n) => {
        const measured = medidos.get(n.id)
        return measured ? { ...n, measured } : n
      })
    })
  }, [nodes])

  const onNodesChange = useCallback((changes: NodeChange[]) => {
    setRfNodes((prev) => applyNodeChanges(changes, prev))
  }, [])

  return { rfNodes, onNodesChange }
}

/**
 * Suelo de zoom del AJUSTE automático, **por llamante**.
 *
 * ⛔ POR QUÉ EXISTE, medido sobre el access map: sin suelo, `fitView` hace lo que se
 * le pide —meter el grafo entero— aunque para lograrlo tenga que encoger a ~0,12, y
 * a ese factor una etiqueta `text-xs` (12 px) se pinta a ~1,5 px. Un ajuste que no
 * se puede leer no ha ajustado nada.
 *
 * ⛔ Y POR QUÉ NO ES GLOBAL, que es la corrección: lo puse como constante compartida
 * y el contraste `sol max` (F-02) midió el precio — `GraphCanvas` lo usan también
 * health, capabilities y sigma, y a **health le recorta el overview inicial con 15
 * nodos** (473 px de grafo en 440 de lienzo). Esas vistas no pidieron esto y su
 * problema no es el mismo. Quien quiera suelo lo pide; el valor por defecto es no
 * tenerlo, que es exactamente lo que había antes de este cambio.
 */
export const LEGIBLE_FIT_MIN_ZOOM = 0.45

function FitOnChange({
  dep,
  minZoom,
}: {
  dep: string | undefined
  minZoom?: number
}) {
  const { fitView } = useReactFlow()
  useEffect(() => {
    // Defer to the next frame so nodes are measured before fitting.
    const id = requestAnimationFrame(() =>
      fitView({ padding: 0.18, minZoom, duration: 200 }),
    )
    return () => cancelAnimationFrame(id)
  }, [dep, fitView, minZoom])
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
  ariaLabel,
  children,
  className,
}: GraphCanvasProps) {
  const isDark = useIsDark()
  const { t } = useTranslation('common')
  const { rfNodes, onNodesChange } = useMeasurableNodes(nodes)
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
          nodes={rfNodes}
          onNodesChange={onNodesChange}
          edges={edges}
          nodeTypes={nodeTypes}
          edgeTypes={edgeTypes}
          colorMode={isDark ? 'dark' : 'light'}
          fitView
          fitViewOptions={{ padding: 0.18, minZoom: fitMinZoom }}
          // El SUELO va en `fitViewOptions`, no aquí: esta prop es el límite del
          // usuario, y subirla le impediría alejar para ver el conjunto — que es
          // justo el escape que el minimapa y los Controls ofrecen.
          minZoom={0.1}
          maxZoom={2.5}
          nodesDraggable={false}
          nodesConnectable={false}
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
            // ⛔ LA TERCERA RUTA DE `fitView`, y me la dejé. El contraste `sol max`
            // del 2026-08-31 (F-01, MEDIUM) la encontró: el botón «fit» de estos
            // controles llama a `fitView` por su cuenta, y sin esto volvía al
            // `minZoom` GLOBAL de 0,1 — o sea, el usuario podía reproducir con un
            // clic exactamente el defecto que las otras dos rutas ya no cometen.
            // Mi propia batería tenía un mutante por cada una de las OTRAS dos y
            // pasaba igual: una batería sólo prueba los casos que su autor imaginó.
            fitViewOptions={{ padding: 0.18, minZoom: fitMinZoom }}
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
          <FitOnChange dep={fitKey} minZoom={fitMinZoom} />
          {children}
        </ReactFlow>
      </ReactFlowProvider>
    </div>
  )
}
