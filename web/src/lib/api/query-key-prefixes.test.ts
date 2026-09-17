// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Exercise the central factories through QueryClient's actual invalidation and
// lookup behavior. Fixed legacy keys test compatibility; tenant and sibling-family
// controls test isolation. These tests cover cache matching, not a backend or UI.
import { describe, expect, it } from 'vitest'
import type { QueryClient } from '@tanstack/react-query'
import { createQueryClient, queryKeys } from './query'

type Clave = readonly unknown[]

const T = 'inquilino-A'
const OTRO = 'inquilino-B'
const P1 = { limit: 25 }
const P2 = { limit: 50, q: 'admin' }

interface Familia {
  nombre: string
  /** El constructor SIN params: tiene que salir el prefijo estable de familia + inquilino. */
  prefijo: (tenant: string | null) => Clave
  /** El mismo constructor CON params: clave concreta. */
  concreta: (tenant: string | null, params: unknown) => Clave
  /** Otra familia del MISMO inquilino, que la invalidación por prefijo no debe rozar. */
  hermana: (tenant: string | null) => Clave
  hermanaNombre: string
  /** Bytes lógicos de `concreta(T, P1)` en la preimagen. Fijados, no derivados. */
  bytesConcreta: Clave
  /** Bytes lógicos de `concreta(T, null)` en la preimagen. Fijados, no derivados. */
  bytesNulaExplicita: Clave
}

const FAMILIAS: Familia[] = [
  {
    nombre: 'users',
    prefijo: (t) => queryKeys.users(t),
    concreta: (t, p) => queryKeys.users(t, p),
    // `users` no tiene hermana bajo su propia raíz: su prefijo YA es familia + inquilino. La
    // frontera que se comprueba aquí es la de raíz distinta.
    hermana: (t) => queryKeys.agents.list(t, P1),
    hermanaNombre: 'agents.list',
    bytesConcreta: ['users', T, P1],
    bytesNulaExplicita: ['users', T, null],
  },
  {
    nombre: 'agents.list',
    prefijo: (t) => queryKeys.agents.list(t),
    concreta: (t, p) => queryKeys.agents.list(t, p),
    hermana: (t) => queryKeys.agents.detail(t, 'a-1'),
    hermanaNombre: 'agents.detail',
    bytesConcreta: ['agents', T, 'list', P1],
    bytesNulaExplicita: ['agents', T, 'list', null],
  },
  {
    nombre: 'accessGraph.edges',
    prefijo: (t) => queryKeys.accessGraph.edges(t),
    concreta: (t, p) => queryKeys.accessGraph.edges(t, p),
    hermana: (t) => queryKeys.accessGraph.drift(t, P1),
    hermanaNombre: 'accessGraph.drift',
    bytesConcreta: ['access-graph', T, 'edges', P1],
    bytesNulaExplicita: ['access-graph', T, 'edges', null],
  },
  {
    nombre: 'accessGraph.drift',
    prefijo: (t) => queryKeys.accessGraph.drift(t),
    concreta: (t, p) => queryKeys.accessGraph.drift(t, p),
    hermana: (t) => queryKeys.accessGraph.edges(t, P1),
    hermanaNombre: 'accessGraph.edges',
    bytesConcreta: ['access-graph', T, 'drift', P1],
    bytesNulaExplicita: ['access-graph', T, 'drift', null],
  },
  {
    nombre: 'audit.list',
    prefijo: (t) => queryKeys.audit.list(t),
    concreta: (t, p) => queryKeys.audit.list(t, p),
    hermana: (t) => queryKeys.audit.systemList(t, P1),
    hermanaNombre: 'audit.systemList',
    bytesConcreta: ['audit', T, 'list', P1],
    bytesNulaExplicita: ['audit', T, 'list', null],
  },
  {
    nombre: 'audit.systemList',
    prefijo: (t) => queryKeys.audit.systemList(t),
    concreta: (t, p) => queryKeys.audit.systemList(t, p),
    hermana: (t) => queryKeys.audit.list(t, P1),
    hermanaNombre: 'audit.list',
    bytesConcreta: ['audit', T, 'system-list', P1],
    bytesNulaExplicita: ['audit', T, 'system-list', null],
  },
]

/** Siembra una consulta bajo `clave` y devuelve la clave, para encadenar. */
function instalar(qc: QueryClient, clave: Clave, valor: unknown): Clave {
  qc.setQueryData(clave, valor)
  return clave
}

/**
 * ¿Quedó invalidada la consulta que vive EXACTAMENTE bajo `clave`?
 *
 * Revienta si no hay ninguna: una consulta que no se encuentra devolvería `false` y se leería como
 * «preservada», que es el falso verde que este banco existe para no dar.
 */
function invalidada(qc: QueryClient, clave: Clave): boolean {
  const estado = qc.getQueryState(clave)
  if (!estado)
    throw new Error(
      `no hay consulta instalada bajo ${JSON.stringify(clave)}: la sonda mide otra cosa`,
    )
  return estado.isInvalidated
}

describe('los seis constructores centrales de claves de consulta', () => {
  for (const f of FAMILIAS) {
    describe(f.nombre, () => {
      it('omitir los params invalida los dos filtros de ese inquilino y preserva el otro inquilino y la familia hermana', async () => {
        const qc = createQueryClient()
        const filtroA = instalar(qc, f.concreta(T, P1), 'A')
        const filtroB = instalar(qc, f.concreta(T, P2), 'B')
        const otroInquilino = instalar(qc, f.concreta(OTRO, P1), 'C')
        const hermana = instalar(qc, f.hermana(T), 'D')

        await qc.invalidateQueries({ queryKey: f.prefijo(T) })

        expect(invalidada(qc, filtroA)).toBe(true)
        expect(invalidada(qc, filtroB)).toBe(true)
        expect(invalidada(qc, otroInquilino)).toBe(false)
        expect(invalidada(qc, hermana)).toBe(false)
      })

      it('una clave concreta no invalida otro filtro de la misma familia', async () => {
        const qc = createQueryClient()
        const filtroA = instalar(qc, f.concreta(T, P1), 'A')
        const filtroB = instalar(qc, f.concreta(T, P2), 'B')

        await qc.invalidateQueries({ queryKey: f.concreta(T, P1) })

        expect(invalidada(qc, filtroA)).toBe(true)
        expect(invalidada(qc, filtroB)).toBe(false)
      })

      it('`null` EXPLÍCITO es un filtro concreto, distinto de la ausencia, y el prefijo lo alcanza', async () => {
        const qc = createQueryClient()
        const nulaExplicita = instalar(qc, f.concreta(T, null), 'N')
        const filtroA = instalar(qc, f.concreta(T, P1), 'A')

        // Invalidar CON `null` explícito es invalidar ese filtro, no la familia.
        await qc.invalidateQueries({ queryKey: f.concreta(T, null) })
        expect(invalidada(qc, nulaExplicita)).toBe(true)
        expect(invalidada(qc, filtroA)).toBe(false)

        // Y el prefijo —la ausencia— sí cubre a los dos, `null` explícito incluido.
        const qc2 = createQueryClient()
        const nula2 = instalar(qc2, f.concreta(T, null), 'N')
        const a2 = instalar(qc2, f.concreta(T, P1), 'A')
        await qc2.invalidateQueries({ queryKey: f.prefijo(T) })
        expect(invalidada(qc2, nula2)).toBe(true)
        expect(invalidada(qc2, a2)).toBe(true)
      })

      it('el inquilino `null` sigue siendo una dimensión del inquilino, con frontera en los dos sentidos', async () => {
        const qc = createQueryClient()
        const sinInquilino = instalar(qc, f.concreta(null, P1), 'S')
        const conInquilino = instalar(qc, f.concreta(T, P1), 'A')

        await qc.invalidateQueries({ queryKey: f.prefijo(null) })
        expect(invalidada(qc, sinInquilino)).toBe(true)
        expect(invalidada(qc, conInquilino)).toBe(false)

        const qc2 = createQueryClient()
        const sin2 = instalar(qc2, f.concreta(null, P1), 'S')
        const con2 = instalar(qc2, f.concreta(T, P1), 'A')
        await qc2.invalidateQueries({ queryKey: f.prefijo(T) })
        expect(invalidada(qc2, con2)).toBe(true)
        expect(invalidada(qc2, sin2)).toBe(false)
      })

      // Preservación, no reparación: con params explícitos la clave tiene que seguir siendo la
      // MISMA que producía la preimagen. Se comprueba por la caché —se siembra bajo el literal
      // fijado y se lee con el constructor—, así que quien decide es el mismo hash que usa
      // react-query, no una comparación de arrays.
      it('con params explícitos conserva sus bytes lógicos, y `null` explícito también', () => {
        const qc = createQueryClient()
        qc.setQueryData(f.bytesConcreta, 'concreta')
        qc.setQueryData(f.bytesNulaExplicita, 'nula')

        expect(qc.getQueryData(f.concreta(T, P1))).toBe('concreta')
        expect(qc.getQueryData(f.concreta(T, null))).toBe('nula')
      })
    })
  }

  // El único consumidor real de los seis en `web/src` es `audit-view.tsx:173-174`, que pasa
  // `listParams` explícitos. Su clave no se mueve, y el prefijo de su familia la alcanza — que es
  // justo lo que antes no pasaba.
  it('la llamada real de audit-view conserva su clave y queda bajo el prefijo de su familia', async () => {
    const listParams = { limit: 100, actor: 'root' }
    for (const [nombre, clave] of [
      ['audit.list', queryKeys.audit.list(T, listParams)],
      ['audit.systemList', queryKeys.audit.systemList(T, listParams)],
    ] as const) {
      const qc = createQueryClient()
      instalar(qc, clave, nombre)
      const prefijo =
        nombre === 'audit.list'
          ? queryKeys.audit.list(T)
          : queryKeys.audit.systemList(T)

      await qc.invalidateQueries({ queryKey: prefijo })
      expect(invalidada(qc, clave)).toBe(true)
      // Y sigue cayendo bajo `audit.all`, que es el prefijo que ya documentaba el contrato.
      expect(
        qc.getQueryCache().findAll({ queryKey: queryKeys.audit.all(T) }).length,
      ).toBe(1)
    }
  })
})
