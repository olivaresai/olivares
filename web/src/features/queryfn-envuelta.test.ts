// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ⛔ UNA `queryFn` PASADA POR REFERENCIA SE CONVIERTE EN UN DEFECTO EL DÍA QUE SU MÉTODO GANA UN
// PARÁMETRO, y no antes: por eso nadie la mira.
//
// react-query llama a la `queryFn` con su CONTEXTO como primer argumento —`{ client, queryKey,
// signal, meta, … }`—. Mientras el método del cliente no acepte nada, es inofensivo. En cuanto
// acepta un `query`, ese contexto **viaja como query string**.
//
// No es teórico: el 2026-08-22 añadí `limit?` a `consoleApi.listWorkspaces` y el conmutador de
// workspaces lo pasaba por referencia. **Lo cazó el compilador por casualidad** —los tipos no
// casaban—, pero con una firma más laxa (`opts?: Record<string, unknown>`) habría compilado y el
// contexto entero habría acabado en la URL. La distancia entre «inofensivo» y «defecto» es una
// firma que otro cambia meses después, en otro fichero.
//
// La regla es barata: envolver cuesta cinco caracteres y quita la clase entera.
import { readFileSync, readdirSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

const RAIZ = resolve(__dirname, '..')

function ficheros(dir: string, out: string[] = []): string[] {
  for (const n of readdirSync(dir)) {
    const p = join(dir, n)
    if (statSync(p).isDirectory()) ficheros(p, out)
    else if (/\.tsx?$/.test(n) && !n.includes('.test.')) out.push(p)
  }
  return out
}

/**
 * `queryFn` que NO es una función escrita ahí mismo. Tres formas, y las tres se le escapaban a la
 * primera versión —que exigía la propiedad entera en una línea con exactamente un espacio—:
 *
 *   queryFn:\n  api.x,                        partida en dos líneas
 *   queryFn: api.x as QueryFunction,            con aserción de tipo
 *   const queryFn = api.x; useQuery({ queryFn }) forma abreviada
 *
 * Ninguna existe hoy en el árbol; la primera versión de esta guarda tampoco las habría visto
 * mañana. Las nombró el contraste externo con el mutante escrito.
 */
export function referenciasPeladas(src: string): string[] {
  // Los comentarios se vacían antes de mirar: `queryFn: /* ojo */ api.x` es la misma referencia.
  const limpio = src
    .replace(/\/\*[\s\S]*?\*\//g, ' ')
    .replace(/\/\/[^\n]*/g, ' ')
  const fuera: string[] = []
  // 1 · `queryFn:` + identificador, en la misma línea o en la siguiente, con o sin `as`.
  for (const m of limpio.matchAll(
    /queryFn\s*:\s*([A-Za-z_][\w.]*)\s*(?:as\s+[\w<>[\]|.\s]+?)?\s*(?=[,}\n]|$)/g,
  )) {
    const resto = limpio.slice((m.index ?? 0) + m[0].length).trimStart()
    // Una llamada (`api.x()`) o una flecha no son referencias peladas.
    if (resto.startsWith('(') || resto.startsWith('=>')) continue
    fuera.push(m[1])
  }
  // 2 · la forma abreviada `{ …, queryFn, … }`, que no lleva dos puntos.
  for (const _m of limpio.matchAll(/[{,]\s*queryFn\s*(?=[,}])/g)) {
    fuera.push('queryFn (forma abreviada)')
  }
  return fuera
}

describe('toda queryFn va envuelta', () => {
  it('ninguna se pasa por referencia', () => {
    const infractores: string[] = []
    let visitados = 0
    let vistas = 0
    for (const f of ficheros(RAIZ)) {
      visitados++
      const src = readFileSync(f, 'utf8')
      vistas += [...src.matchAll(/queryFn:/g)].length
      for (const ref of referenciasPeladas(src)) {
        infractores.push(`${f.slice(RAIZ.length + 1)}  queryFn: ${ref}`)
      }
    }
    // Las cotas van DENTRO de la casilla del veredicto: una guarda que no demuestra que MIRÓ
    // no falla, certifica.
    expect(visitados).toBeGreaterThan(300)
    expect(vistas).toBeGreaterThan(200)

    expect(infractores).toEqual([])
  })

  /** ⛔ El autotest, en las dos direcciones: sin él, un `matchAll` que no case sale verde siempre. */
  it('la guarda caza el caso malo y deja pasar el bueno', () => {
    expect(
      referenciasPeladas('    queryFn: consoleApi.listWorkspaces,'),
    ).toEqual(['consoleApi.listWorkspaces'])
    expect(referenciasPeladas('    queryFn: listOrgs')).toEqual(['listOrgs'])
    // BUENO: envuelta, con y sin argumentos.
    expect(
      referenciasPeladas('    queryFn: () => consoleApi.listWorkspaces(),'),
    ).toEqual([])
    expect(
      referenciasPeladas('    queryFn: () => api.x({ limit: 1000 }),'),
    ).toEqual([])
    // BUENO: la forma con contexto declarado a propósito.
    expect(
      referenciasPeladas('    queryFn: ({ signal }) => api.x(signal),'),
    ).toEqual([])

    // ⛔ LAS FUGAS QUE EL CONTRASTE ESCRIBIÓ, y que la primera versión dejaba pasar.
    expect(referenciasPeladas('  queryFn:\n    api.x,')).toEqual(['api.x'])
    expect(referenciasPeladas('  queryFn: api.x as QueryFunction,')).toEqual([
      'api.x',
    ])
    expect(referenciasPeladas('  queryFn: /* a propósito */ api.x,')).toEqual([
      'api.x',
    ])
    expect(referenciasPeladas('useQuery({ queryFn })')).toEqual([
      'queryFn (forma abreviada)',
    ])
    // Y el caso bueno partido en dos líneas NO se caza.
    expect(referenciasPeladas('  queryFn: () =>\n    api.x(),')).toEqual([])
  })
})
