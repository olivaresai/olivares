// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { layeredLayout } from './layout'

describe('layeredLayout', () => {
  it('places later layers to the right of earlier ones', () => {
    const { positions } = layeredLayout(
      [
        { id: 'a', layer: 0 },
        { id: 'b', layer: 0 },
        { id: 'r', layer: 1 },
      ],
      [
        { source: 'a', target: 'r' },
        { source: 'b', target: 'r' },
      ],
    )
    expect(positions.a!.x).toBeLessThan(positions.r!.x)
    expect(positions.b!.x).toBeLessThan(positions.r!.x)
    // Same layer shares the column x.
    expect(positions.a!.x).toBe(positions.b!.x)
  })

  it('is deterministic for the same input', () => {
    const nodes = [
      { id: 'a', layer: 0 },
      { id: 'b', layer: 0 },
      { id: 'c', layer: 1 },
      { id: 'd', layer: 1 },
    ]
    const edges = [
      { source: 'a', target: 'c' },
      { source: 'b', target: 'd' },
    ]
    const one = layeredLayout(nodes, edges)
    const two = layeredLayout(nodes, edges)
    expect(one.positions).toEqual(two.positions)
  })

  it('handles an empty graph', () => {
    expect(layeredLayout([], [])).toEqual({
      positions: {},
      width: 0,
      height: 0,
    })
  })

  // ⛔ EL DEFECTO QUE ESTOS CUATRO CASOS FIJAN, medido sobre el access map: 56
  // orígenes se apilaban en UNA columna de 55 · 76 = 4180 px dentro de un
  // contenedor de ~630 px, `fitView` encogía a ~0,12 y las etiquetas de 12 px se
  // pintaban a ~1,5. El grafo estaba bien; era ilegible.
  describe('maxPerColumn (envoltura en sub-columnas)', () => {
    const tall = (n: number, layer: number, p: string) =>
      Array.from({ length: n }, (_, i) => ({ id: `${p}${i}`, layer }))
    const estate = [...tall(56, 0, 'o'), ...tall(18, 1, 'r')]
    const wires = Array.from({ length: 56 }, (_, i) => ({
      source: `o${i}`,
      target: `r${i % 18}`,
    }))
    const opts = { layerGapX: 360, nodeGapY: 76 }

    it('acota la altura de una capa alta en vez de apilarla', () => {
      const sin = layeredLayout(estate, wires, opts)
      const con = layeredLayout(estate, wires, { ...opts, maxPerColumn: 14 })
      // El sujeto del defecto, para que el caso diga contra qué compara.
      expect(sin.height).toBe(55 * 76)
      expect(con.height).toBe(13 * 76)
      expect(con.height).toBeLessThan(sin.height / 4)
    })

    // ⛔ EL ANCHO CON ENVOLTURA, y es el hueco que dejaba el resto de la bateria: el termino de
    //    sub-columna de `width` (`(subsIn(...) - 1) * subGapX`) solo es distinto de cero cuando HAY
    //    envoltura, y ningun caso lo miraba en ese estado — el de basura solo pedia `isFinite` y el
    //    de regresion fija `width` en la rama SIN envolver, donde el termino vale 0. Verificado por
    //    mutacion: quitando ese termino la caja delimitadora se queda 280 px (una sub-columna) A LA
    //    IZQUIERDA del nodo mas a la derecha, y la bateria entera seguia en verde. Y esa caja es
    //    justo la cantidad que el `fitView` de este lote usa para centrar: un ancho que no cubre sus
    //    propios nodos recorta el grafo en pantalla.
    it('el ancho ENVUELTO cubre el nodo mas a la derecha', () => {
      const r = layeredLayout(estate, wires, { ...opts, maxPerColumn: 14 })
      const derecho = Math.max(...Object.values(r.positions).map((p) => p.x))
      expect(derecho).toBeGreaterThan(opts.layerGapX)
      expect(r.width).toBe(derecho)
    })

    it('reparte la capa en sub-columnas y NO las solapa con la siguiente', () => {
      const { positions } = layeredLayout(estate, wires, {
        ...opts,
        maxPerColumn: 14,
      })
      const xs = (p: string) => [
        ...new Set(
          Object.entries(positions)
            .filter(([id]) => id.startsWith(p))
            .map(([, v]) => v.x),
        ),
      ]
      expect(xs('o')).toHaveLength(4) // 56 / 14
      expect(xs('r')).toHaveLength(2) // 18 / 14
      // ⛔ La guarda que un `x = l * layerGapX + sub * subGapX` ingenuo NO pasa:
      // con 4 sub-columnas la capa 0 llegaría a x=840 y la capa 1 empezaría en 360.
      expect(Math.max(...xs('o'))).toBeLessThan(Math.min(...xs('r')))
    })

    it('centra la sub-columna corta contra su propia capa', () => {
      const { positions } = layeredLayout(estate, wires, {
        ...opts,
        maxPerColumn: 14,
      })
      const bySub = new Map<number, number[]>()
      for (const [id, p] of Object.entries(positions)) {
        if (!id.startsWith('r')) continue
        bySub.set(p.x, [...(bySub.get(p.x) ?? []), p.y])
      }
      const cols = [...bySub.entries()].sort((a, b) => a[0] - b[0])
      expect(cols).toHaveLength(2)
      const mid = (ys: number[]) => (Math.min(...ys) + Math.max(...ys)) / 2
      // Las dos sub-columnas comparten centro vertical: la de 4 no cuelga del techo.
      expect(mid(cols[0]![1])).toBeCloseTo(mid(cols[1]![1]), 6)
    })

    // F-03 del contraste: las opciones son publicas, asi que un llamante puede
    // pasar basura. Lo que NO puede pasar es que la basura salga como posiciones.
    it('una opción no finita o fraccionaria no produce NaN ni posiciones rotas', () => {
      const basura = [
        { maxPerColumn: Number.NaN },
        { maxPerColumn: Number.POSITIVE_INFINITY },
        { maxPerColumn: 2.5 },
        { maxPerColumn: -3 },
        { maxPerColumn: 0 },
        { nodeGapY: Number.NaN },
        { subGapX: Number.NaN },
        { layerGapX: Number.POSITIVE_INFINITY },
      ]
      for (const extra of basura) {
        const r = layeredLayout(estate, wires, { ...opts, ...extra })
        expect(Object.keys(r.positions)).toHaveLength(estate.length)
        expect(
          Object.values(r.positions).every(
            (p) => Number.isFinite(p.x) && Number.isFinite(p.y),
          ),
          `posiciones no finitas con ${JSON.stringify(extra)}`,
        ).toBe(true)
        expect(Number.isFinite(r.width) && Number.isFinite(r.height)).toBe(true)
      }
    })

    // ⛔ Y LA FILA DE ARRIBA NO BASTA PARA EL SUELO, aunque lo parezca. El caso de basura solo
    //    pregunta «¿sale finito?», y eso es cierto con el suelo y sin el: borrando
    //    `Math.max(0, Math.floor(...))` la bateria entera seguia verde, y quitando SOLO el
    //    `Math.floor` o SOLO el `Math.max(0, …)` tambien. Un oraculo que no distingue el caso
    //    curado del roto no cubre nada. Aqui se afirma el COMPORTAMIENTO, que es lo unico que
    //    puede fallar: 2,5 filas no significa nada, asi que tiene que colocar como 2.
    it('una opcion FRACCIONARIA se comporta como su entero, no «casi»', () => {
      const dos = layeredLayout(estate, wires, { ...opts, maxPerColumn: 2 })
      const medio = layeredLayout(estate, wires, { ...opts, maxPerColumn: 2.5 })
      expect(medio.width).toBe(dos.width)
      expect(medio.height).toBe(dos.height)
      expect(medio.positions).toEqual(dos.positions)
    })

    // ⛔ Y 0 O NEGATIVO ES «NO ENVOLVER», que es el otro lado del mismo contrato. Se afirma contra
    //    la linea base sin envoltura, no contra si mismo. Salvedad dicha porque cuesta una linea y
    //    ahorra una lectura falsa: el ternario `maxPerColumn > 0 ? … : MAX_SAFE_INTEGER` ya absorbe
    //    el negativo aguas abajo, asi que este caso PIN A EL CONTRATO pero no mata por si solo la
    //    supresion del `Math.max(0, …)`. Lo que mata esa supresion es el caso fraccionario de
    //    arriba, via `Math.floor`.
    it('0 y negativo significan NO ENVOLVER, como la linea base', () => {
      const base = layeredLayout(estate, wires, opts)
      for (const mpc of [0, -3]) {
        const r = layeredLayout(estate, wires, { ...opts, maxPerColumn: mpc })
        expect(r.width, `maxPerColumn ${mpc}`).toBe(base.width)
        expect(r.height, `maxPerColumn ${mpc}`).toBe(base.height)
      }
    })

    it('sin la opción se comporta EXACTAMENTE como antes, y sin NaN', () => {
      // El camino por defecto calcula `sub * wrap`; con `Infinity` como centinela
      // eso sería `0 * Infinity = NaN` y TODOS los grafos que no envuelven —seis
      // llamantes— saldrían con posiciones NaN.
      const { positions, width, height } = layeredLayout(estate, wires, opts)
      expect(width).toBe(360)
      expect(height).toBe(4180)
      // ⛔ CONTAR PRIMERO. `every(...)` sobre un objeto VACIO es `true` (F-05.3 del
      // contraste): sin esta linea, un layout que no colocara NADA satisfacia el caso.
      expect(Object.keys(positions)).toHaveLength(estate.length)
      expect(
        Object.values(positions).every(
          (p) => Number.isFinite(p.x) && Number.isFinite(p.y),
        ),
      ).toBe(true)
      // Y la forma exacta de antes: dos columnas, una x por capa, paso de 76.
      expect(new Set(Object.values(positions).map((p) => p.x))).toEqual(
        new Set([0, 360]),
      )
    })
  })

  it('supports three layers (session -> mcp -> tool)', () => {
    const { positions } = layeredLayout(
      [
        { id: 's', layer: 0 },
        { id: 'm', layer: 1 },
        { id: 't', layer: 2 },
      ],
      [
        { source: 's', target: 'm' },
        { source: 'm', target: 't' },
      ],
    )
    expect(positions.s!.x).toBeLessThan(positions.m!.x)
    expect(positions.m!.x).toBeLessThan(positions.t!.x)
  })
})
