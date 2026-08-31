// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/**
 * Bespoke layered (Sugiyama-lite) graph layout — the access map and the health
 * dependency map both run on it.
 *
 * WHY NOT dagre/elk: the engine ships air-gapped and the access graph is
 * fundamentally LAYERED (origins → resources; session → mcp → tool). A small,
 * dependency-free left-to-right layered placement with barycenter crossing
 * reduction reads cleaner for the "who touches what" story than a generic
 * force/hierarchical blob, keeps the bundle lean, and is fully DETERMINISTIC — so
 * the marketing screenshot and the component tests are stable frame to frame.
 * (If a future estate produces deep, dense multi-hop graphs, elkjs is the
 * documented upgrade — see UI-CONTRACT-ACCESS-MAP "scale".)
 */

export interface LayoutNodeInput {
  id: string
  /** The column this node belongs to (0 = leftmost). The caller maps domain kind
   * → layer (origins=0, resources=1; or session=0, mcp=1, tool=2). */
  layer: number
}

export interface LayoutEdgeInput {
  source: string
  target: string
}

export interface LayoutOptions {
  /** Horizontal distance between layer columns (node anchor to node anchor). */
  layerGapX?: number
  /** Vertical distance between stacked nodes in a column. */
  nodeGapY?: number
  /** Barycenter ordering sweeps (more = fewer crossings, diminishing returns). */
  iterations?: number
  /**
   * Wrap a layer taller than this into side-by-side SUB-COLUMNS, the way text
   * wraps. 0 or omitted = never wrap, which is what every caller got before this
   * existed and still gets by default.
   *
   * ⛔ POR QUÉ EXISTE, medido: el access map de un estate de 56 orígenes apilaba
   * los 56 en UNA columna — 55 · nodeGapY(76) = **4180 px de alto por 360 de
   * ancho** dentro de un contenedor de ~630 px (`h-[70vh]`). `fitView` no tenía
   * más remedio que encoger a ~0,12, y con eso las etiquetas `text-xs` (12 px) se
   * pintaban a ~1,5 px: la vista más visible del producto salía como una columna
   * vertical ilegible. Envolviendo a 14 por sub-columna, esos 56 pasan a ~990 px.
   */
  maxPerColumn?: number
  /**
   * Horizontal distance between the sub-columns of ONE layer. Default 280, which
   * clears the widest node this layout serves (the access-map resource chip:
   * 190 px label + 28 px icon + gaps and padding ≈ 252 px).
   */
  subGapX?: number
}

export interface LayoutResult {
  positions: Record<string, { x: number; y: number }>
  /** Bounding size of the laid-out graph (top-left origin), for fit/centering. */
  width: number
  height: number
}

const DEFAULTS = {
  layerGapX: 320,
  nodeGapY: 84,
  iterations: 6,
  maxPerColumn: 0,
  subGapX: 280,
}

/**
 * layeredLayout places each node in its layer's column and orders nodes within a
 * column to minimize edge crossings (iterated barycenter), then centers shorter
 * columns vertically. Pure and deterministic for a given input order.
 */
export function layeredLayout(
  nodes: LayoutNodeInput[],
  edges: LayoutEdgeInput[],
  opts: LayoutOptions = {},
): LayoutResult {
  const crudo = { ...DEFAULTS, ...opts }
  // ⛔ SANEADO EN LA PUERTA (F-03 del contraste `sol max`): las opciones son públicas
  // y un `NaN`, un `Infinity` o un `maxPerColumn: 2.5` se propagaban a TODAS las
  // posiciones sin que nada lo dijera — un grafo entero en NaN se pinta como un grafo
  // vacío, que es la peor forma de fallar. Un valor que no es un número finito no es
  // una preferencia: es un error del llamante, y aquí se cae al de por defecto.
  const num = (v: number, porDefecto: number) =>
    Number.isFinite(v) ? v : porDefecto
  const layerGapX = num(crudo.layerGapX, DEFAULTS.layerGapX)
  const nodeGapY = num(crudo.nodeGapY, DEFAULTS.nodeGapY)
  const subGapX = num(crudo.subGapX, DEFAULTS.subGapX)
  const iterations = Math.max(
    0,
    Math.floor(num(crudo.iterations, DEFAULTS.iterations)),
  )
  // `maxPerColumn` además cuenta FILAS: fraccionario no significa nada y 0/negativo
  // es «no envolver», que es su valor por defecto.
  const maxPerColumn = Math.max(
    0,
    Math.floor(num(crudo.maxPerColumn, DEFAULTS.maxPerColumn)),
  )
  if (nodes.length === 0) return { positions: {}, width: 0, height: 0 }

  // Group node ids by layer, preserving input order as the stable seed.
  const layerOf = new Map<string, number>()
  for (const n of nodes) layerOf.set(n.id, n.layer)
  const maxLayer = Math.max(...nodes.map((n) => n.layer))
  const columns: string[][] = Array.from({ length: maxLayer + 1 }, () => [])
  for (const n of nodes) columns[n.layer]!.push(n.id)

  // Adjacency limited to edges whose endpoints are both present.
  const present = new Set(nodes.map((n) => n.id))
  const outAdj = new Map<string, string[]>()
  const inAdj = new Map<string, string[]>()
  for (const e of edges) {
    if (!present.has(e.source) || !present.has(e.target)) continue
    ;(outAdj.get(e.source) ?? outAdj.set(e.source, []).get(e.source)!).push(
      e.target,
    )
    ;(inAdj.get(e.target) ?? inAdj.set(e.target, []).get(e.target)!).push(
      e.source,
    )
  }

  const orderIndex = (col: string[]) => {
    const idx = new Map<string, number>()
    col.forEach((id, i) => idx.set(id, i))
    return idx
  }

  // Iterated barycenter: alternate forward (order a column by its left neighbors)
  // and backward (by its right neighbors) sweeps.
  for (let it = 0; it < iterations; it++) {
    const forward = it % 2 === 0
    if (forward) {
      for (let l = 1; l <= maxLayer; l++) {
        sortByBarycenter(columns[l]!, columns[l - 1]!, inAdj)
      }
    } else {
      for (let l = maxLayer - 1; l >= 0; l--) {
        sortByBarycenter(columns[l]!, columns[l + 1]!, outAdj)
      }
    }
  }

  // Position: x by layer (plus its sub-columns), y by order within the sub-column,
  // centering shorter columns against the tallest.
  //
  // ⚠ `wrap` is a FINITE sentinel, not Infinity, and that is load-bearing: the
  // no-wrap path computes `sub * wrap`, and `0 * Infinity` is NaN — every position
  // in every graph that does not wrap would come out NaN. MAX_SAFE_INTEGER keeps
  // the same arithmetic honest (floor(i/w)=0, i%w=i, min(len,w)=len).
  const wrap = maxPerColumn > 0 ? maxPerColumn : Number.MAX_SAFE_INTEGER
  const subsIn = (n: number) => Math.max(1, Math.ceil(n / wrap))
  const rowsIn = (n: number) => Math.min(n, wrap)

  const height = Math.max(
    0,
    ...columns.map((c) => (rowsIn(c.length) - 1) * nodeGapY),
  )

  // Each layer starts `layerGapX` after the LAST sub-column of the one before it.
  // ⛔ NO es `l * layerGapX`: en cuanto una capa ocupa más de una sub-columna, esa
  // fórmula la solapa con la siguiente — con 56 orígenes en 4 sub-columnas de 280,
  // la cuarta cae en x=840 mientras la capa de recursos empezaría en x=360.
  const layerX: number[] = []
  let cursorX = 0
  columns.forEach((col, l) => {
    layerX[l] = cursorX
    cursorX += (subsIn(col.length) - 1) * subGapX + layerGapX
  })

  const positions: Record<string, { x: number; y: number }> = {}
  columns.forEach((col, l) => {
    const layerHeight = (rowsIn(col.length) - 1) * nodeGapY
    const yLayer = (height - layerHeight) / 2
    col.forEach((id, i) => {
      const sub = Math.floor(i / wrap)
      const row = i % wrap
      // The last sub-column is usually short; center it against its own layer, the
      // same instinct this layout already had for short columns.
      const inThisSub = Math.min(col.length - sub * wrap, wrap)
      const subHeight = (inThisSub - 1) * nodeGapY
      positions[id] = {
        x: layerX[l]! + sub * subGapX,
        y:
          yLayer +
          (height === 0 ? 0 : (layerHeight - subHeight) / 2) +
          row * nodeGapY,
      }
    })
  })

  return {
    positions,
    // Anchor-to-anchor, as before: the rightmost layer's last sub-column.
    width:
      layerX[maxLayer]! + (subsIn(columns[maxLayer]!.length) - 1) * subGapX,
    height,
  }

  function sortByBarycenter(
    col: string[],
    neighborCol: string[],
    adj: Map<string, string[]>,
  ) {
    const nIdx = orderIndex(neighborCol)
    const bary = new Map<string, number>()
    col.forEach((id, i) => {
      const ns = adj.get(id) ?? []
      const positionsOfNeighbors = ns
        .map((n) => nIdx.get(n))
        .filter((v): v is number => v !== undefined)
      bary.set(
        id,
        positionsOfNeighbors.length
          ? positionsOfNeighbors.reduce((a, b) => a + b, 0) /
              positionsOfNeighbors.length
          : i, // no neighbors → keep current slot (stable)
      )
    })
    // Stable sort: ties keep their relative order (decorate-sort-undecorate).
    col
      .map((id, i) => ({ id, b: bary.get(id)!, i }))
      .sort((a, b) => a.b - b.b || a.i - b.i)
      .forEach((entry, i) => {
        col[i] = entry.id
      })
  }
}
