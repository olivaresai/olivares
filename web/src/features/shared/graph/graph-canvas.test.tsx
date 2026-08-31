// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// C10-01 / VER-08 — EL MINIMAPA VACÍO, y por qué esta celda existe.
//
// Medido en navegador el 2026-08-17 contra la vista viva del access-map: el lienzo pintaba 7
// nodos y 4 aristas, y el minimapa pintaba CERO — su SVG tenía dos hijos, el <title> y la
// máscara, ni un <rect>. Ése es el «rectángulo gris vacío» que llevaba meses en las capturas del
// README, de docs-site/ y del thumbnail de YouTube.
//
// La causa: React Flow v12 descarta del minimapa todo nodo sin dimensiones, y las mira en el
// objeto que le pasa ESTA consola. Con `nodes` controlados y sin `onNodesChange`, las medidas
// nunca vuelven a ese objeto: el lienzo se pinta con los internos y el minimapa se queda a cero.
//
// jsdom no hace layout, así que esta celda NO mide píxeles: mide el CONTRATO que hace posible la
// medición — que el canvas acepte los cambios de dimensión y devuelva el nodo YA medido. Es lo
// único que jsdom puede afirmar de verdad, y es exactamente lo que estaba roto.
import { render, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type { ReactNode } from 'react'

// El doble captura las props que GraphCanvas le pasa a ReactFlow y expone `onNodesChange` para
// poder dispararlo como haría el ResizeObserver real al medir un nodo.
const capturado: {
  nodes?: unknown[]
  onNodesChange?: (changes: unknown[]) => void
  fitViewOptions?: { padding?: number; minZoom?: number }
  controlsFitViewOptions?: { padding?: number; minZoom?: number }
  minZoomProp?: number
} = {}
/** Cada llamada imperativa a `fitView`, con sus opciones. */
const ajustes: { padding?: number; minZoom?: number }[] = []

vi.mock('@xyflow/react', () => ({
  ReactFlow: ({
    nodes,
    onNodesChange,
    fitViewOptions,
    minZoom,
    children,
  }: {
    nodes: unknown[]
    onNodesChange?: (changes: unknown[]) => void
    fitViewOptions?: { padding?: number; minZoom?: number }
    minZoom?: number
    children?: ReactNode
  }) => {
    capturado.nodes = nodes
    capturado.onNodesChange = onNodesChange
    capturado.fitViewOptions = fitViewOptions
    capturado.minZoomProp = minZoom
    return <div data-testid="rf">{children}</div>
  },
  ReactFlowProvider: ({ children }: { children?: ReactNode }) => (
    <>{children}</>
  ),
  Background: () => null,
  BackgroundVariant: { Dots: 'dots' },
  Controls: ({
    fitViewOptions,
  }: {
    fitViewOptions?: { padding?: number; minZoom?: number }
  }) => {
    capturado.controlsFitViewOptions = fitViewOptions
    return null
  },
  MiniMap: () => null,
  useReactFlow: () => ({
    fitView: (o?: { padding?: number; minZoom?: number }) => {
      ajustes.push(o ?? {})
    },
  }),
  // El de verdad: aplicar un cambio de dimensiones deja `measured` en el nodo.
  applyNodeChanges: (
    changes: { id: string; dimensions?: unknown }[],
    nodes: { id: string }[],
  ) =>
    nodes.map((n) => {
      const c = changes.find((x) => x.id === n.id)
      return c?.dimensions ? { ...n, measured: c.dimensions } : n
    }),
}))

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string) => k }),
}))
vi.mock('./theme', () => ({ useIsDark: () => false }))

const { GraphCanvas, LEGIBLE_FIT_MIN_ZOOM } = await import('./graph-canvas')

const NODES = [
  { id: 'A1', type: 'origin', position: { x: 0, y: 0 }, data: {} },
  { id: 'R1', type: 'resource', position: { x: 300, y: 0 }, data: {} },
]

describe('GraphCanvas y las dimensiones que el minimapa necesita', () => {
  /**
   * EL CONTROL: el canvas acepta `onNodesChange`. Sin él, React Flow v12 no tiene por dónde
   * devolver las medidas y el minimapa se queda vacío — que es el defecto que esta celda cierra.
   *
   * EL MUTANTE: quita `onNodesChange={onNodesChange}` del <ReactFlow> de graph-canvas.tsx.
   * Esta afirmación falla. VERIFICADO.
   */
  it('le da a React Flow por dónde devolver las medidas', () => {
    render(<GraphCanvas nodes={NODES as never} edges={[]} />)
    expect(capturado.onNodesChange).toBeTypeOf('function')
  })

  /**
   * EL CONTROL, y es el que de verdad manda: cuando llega una medida, el nodo que el canvas
   * devuelve a React Flow la LLEVA. Un `onNodesChange` que existiera pero tirara el cambio
   * satisfaría la celda de arriba y fallaría ésta.
   *
   * EL MUTANTE: que `onNodesChange` sea un no-op (`() => {}`). Falla aquí. VERIFICADO.
   */
  it('el nodo vuelve a React Flow con la medida puesta', async () => {
    render(<GraphCanvas nodes={NODES as never} edges={[]} />)
    capturado.onNodesChange?.([
      { id: 'A1', type: 'dimensions', dimensions: { width: 148, height: 56 } },
    ])
    await waitFor(() => {
      const a1 = (capturado.nodes as { id: string; measured?: unknown }[]).find(
        (n) => n.id === 'A1',
      )
      expect(a1?.measured).toEqual({ width: 148, height: 56 })
    })
  })

  /**
   * EL CONTROL: re-sembrar desde las props NO borra la medida ya tomada.
   *
   * Sin esto el arreglo dura hasta el primer filtro: el padre reconstruye el array de nodos, el
   * efecto vuelve a sembrar y el minimapa se vacía otra vez — el mismo defecto, con menos
   * testigos porque sólo aparece después de interactuar.
   *
   * EL MUTANTE: en `useMeasurableNodes`, sembrar con `setRfNodes(nodes)` a secas. Falla aquí.
   * NO DISPARA EN LA OTRA DIRECCIÓN: un gancho que ignorase las props nuevas conservaría la
   * medida y fallaría al no traer el nodo nuevo, que es lo que comprueba la última afirmación.
   */
  it('conserva la medida cuando el padre reconstruye los nodos', async () => {
    const { rerender } = render(
      <GraphCanvas nodes={NODES as never} edges={[]} />,
    )
    capturado.onNodesChange?.([
      { id: 'A1', type: 'dimensions', dimensions: { width: 148, height: 56 } },
    ])
    await waitFor(() =>
      expect(
        (capturado.nodes as { id: string; measured?: unknown }[]).find(
          (n) => n.id === 'A1',
        )?.measured,
      ).toBeTruthy(),
    )

    // El padre reconstruye: mismos ids, objetos NUEVOS (lo que pasa al cambiar un filtro).
    const reconstruidos = [
      { id: 'A1', type: 'origin', position: { x: 0, y: 0 }, data: {} },
      { id: 'R1', type: 'resource', position: { x: 300, y: 0 }, data: {} },
      { id: 'R2', type: 'resource', position: { x: 300, y: 90 }, data: {} },
    ]
    rerender(<GraphCanvas nodes={reconstruidos as never} edges={[]} />)

    await waitFor(() => {
      const ns = capturado.nodes as { id: string; measured?: unknown }[]
      expect(ns).toHaveLength(3)
      expect(ns.find((n) => n.id === 'A1')?.measured).toEqual({
        width: 148,
        height: 56,
      })
    })
  })

  /**
   * ⛔ EL DEFECTO, medido sobre el access map: `fitView` sin suelo hace lo que se le
   * pide —meter el grafo entero— aunque para lograrlo encoja a ~0,12. A ese factor
   * una etiqueta `text-xs` (12 px) se pinta a ~1,5 px: el ajuste «funcionaba» y la
   * vista era ilegible. Un ajuste que no se puede leer no ha ajustado nada.
   *
   * EL CONTROL: las DOS rutas de ajuste llevan el suelo. Son dos y no una — la
   * declarativa (`fitViewOptions`, primer render) y la imperativa (`FitOnChange`,
   * cuando cambian los datos) — y arreglar sólo una deja la otra encogiendo.
   *
   * EL MUTANTE: quitar `minZoom: FIT_MIN_ZOOM` de cualquiera de las dos. Falla aquí.
   */
  it('el ajuste nunca encoge por debajo del suelo legible', async () => {
    ajustes.length = 0
    render(
      <GraphCanvas
        nodes={NODES as never}
        edges={[]}
        fitKey="k1"
        fitMinZoom={LEGIBLE_FIT_MIN_ZOOM}
      />,
    )

    // Ruta declarativa: la que usa el primer render.
    expect(capturado.fitViewOptions?.minZoom).toBeGreaterThanOrEqual(0.45)

    // Ruta imperativa: la que corre cuando cambia `fitKey`.
    await waitFor(() => expect(ajustes.length).toBeGreaterThan(0))
    expect(
      ajustes.every((o) => (o.minZoom ?? 0) >= 0.45),
      `fitView() se llamó sin suelo: ${JSON.stringify(ajustes)}`,
    ).toBe(true)

    // ⛔ LA TERCERA RUTA, que yo me habia dejado y encontro el contraste `sol max`
    // (F-01, MEDIUM, 2026-08-31): el boton «fit» de `Controls` llama a `fitView`
    // por su cuenta. Sin su propio suelo volvia al `minZoom` global de 0,1, o sea
    // que un clic reproducia el defecto que las otras dos rutas ya no cometen.
    //
    // Las dos aserciones de arriba pasaban con esa ruta rota: una bateria solo
    // prueba los casos que su autor imagino, y por eso este caso existe.
    expect(capturado.controlsFitViewOptions?.minZoom).toBeGreaterThanOrEqual(
      0.45,
    )

    // ⛔ ESTE ORACULO ERA VACUO y lo dijo el contraste (F-05.2): la frase promete
    // que el usuario conserva el alejamiento manual y lo que comprobaba era el
    // `padding`. El escape lo da la PROP `minZoom`, que es la que hay que mirar —
    // subirla a 0,45, o quitarla y caer en el default 0,5 de React Flow, rompia el
    // escape y el caso pasaba igual.
    expect(capturado.minZoomProp).toBeLessThanOrEqual(0.1)
    expect(capturado.fitViewOptions?.padding).toBe(0.18)

    // Y un suelo no es «cuanto mas alto mejor»: `Infinity` satisfacia un `>=0.45`
    // suelto (F-05.4) y dejaria el grafo inmanejable. Se acota por los dos lados.
    for (const z of [
      capturado.fitViewOptions?.minZoom,
      capturado.controlsFitViewOptions?.minZoom,
      ...ajustes.map((o) => o.minZoom),
    ]) {
      expect(z).toBeGreaterThanOrEqual(0.45)
      expect(z).toBeLessThanOrEqual(1)
    }
  })

  /**
   * ⛔ LA OTRA MITAD DEL CONTRATO, y es la que el contraste `sol max` (F-02) hizo
   * necesaria: el suelo era una constante compartida, y `GraphCanvas` lo usan
   * tambien health, capabilities y sigma. Medido alli: a health le RECORTA el
   * overview inicial con 15 nodos (473 px de grafo en 440 de lienzo). Esas vistas
   * no pidieron esto y su problema no es el mismo.
   *
   * EL MUTANTE: volver a poner el suelo por defecto (`fitMinZoom = 0.45` o la
   * constante en el sitio de la prop). Falla aqui.
   */
  it('sin pedirlo, NO hay suelo: quien no lo pide se comporta como antes', async () => {
    ajustes.length = 0
    render(<GraphCanvas nodes={NODES as never} edges={[]} fitKey="k2" />)
    expect(capturado.fitViewOptions?.minZoom).toBeUndefined()
    expect(capturado.controlsFitViewOptions?.minZoom).toBeUndefined()
    await waitFor(() => expect(ajustes.length).toBeGreaterThan(0))
    expect(ajustes.every((o) => o.minZoom === undefined)).toBe(true)
  })
})
