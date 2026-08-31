// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync, statSync, existsSync } from 'node:fs'
import { join, resolve } from 'node:path'

/**
 * TRINQUETE · los transportes crudos leen el inquilino ANTES de esperar la renovación.
 *
 * Estos caminos rodean `apiFetch`, así que NO heredan la fijación de `apiFetchWithMeta`
 * (`lib/api/client.ts`). Cada uno hace `await ensureFreshSession()` y compone sus cabeceras a
 * mano; si la lectura del inquilino queda DEBAJO de esa espera, un cambio de organización
 * durante la renovación manda la petición a la organización nueva — y uno de estos caminos es un
 * `POST` que genera un bundle de soporte, o sea una escritura.
 *
 * Es un trinquete de FUENTE a propósito: el defecto es una cuestión de ORDEN dentro de ocho
 * funciones distintas, y montar ocho pruebas de comportamiento costaría mucho más que esto y
 * cubriría menos, porque el noveno sitio que alguien añada mañana no tendría la suya.
 */

function ficheros(dir: string, acc: string[] = []): string[] {
  for (const e of readdirSync(dir)) {
    if (e === 'node_modules' || e === 'dist') continue
    const p = join(dir, e)
    if (statSync(p).isDirectory()) ficheros(p, acc)
    else if (/\.tsx?$/.test(p) && !/\.test\.tsx?$/.test(p)) acc.push(p)
  }
  return acc
}

const LEE_INQUILINO =
  /const\s+tenant\s*=\s*useTenantStore\.getState\(\)\.activeTenant/

describe('transportes crudos y el inquilino', () => {
  it('todos leen el inquilino antes de `await ensureFreshSession()`', () => {
    const candidatas = [
      resolve(process.cwd(), 'src'),
      resolve(process.cwd(), 'web/src'),
    ]
    const raiz = candidatas.find((c) => existsSync(c))
    expect(
      raiz,
      `no encuentro el árbol en ${candidatas.join(' ni ')}`,
    ).toBeTruthy()

    let visitados = 0
    let esperas = 0
    const infractores: string[] = []

    for (const f of ficheros(raiz as string)) {
      visitados++
      const src = readFileSync(f, 'utf8')
      if (!src.includes('ensureFreshSession()')) continue
      const lineas = src.split('\n')
      for (let i = 0; i < lineas.length; i++) {
        if (!/await\s+ensureFreshSession\(\)/.test(lineas[i])) continue
        esperas++
        // ¿Se lee el inquilino ANTES, cerca (misma función)? Y NO después.
        const antes = lineas
          .slice(Math.max(0, i - 12), i)
          .some((l) => LEE_INQUILINO.test(l))
        const despues = lineas
          .slice(i + 1, i + 16)
          .some((l) => LEE_INQUILINO.test(l))
        // Un camino que no usa inquilino en absoluto no es infractor.
        const usaInquilino = antes || despues
        if (usaInquilino && !antes) infractores.push(`${f}:${i + 1}`)
      }
    }

    // ⛔ COTAS DENTRO DE LA CASILLA DEL VEREDICTO: demuestran que recorrió el árbol y que
    //    encontró las esperas que dice vigilar. Sin ellas, un barrido roto diría «cero
    //    infractores» sobre un árbol lleno.
    expect(visitados).toBeGreaterThan(400)
    expect(esperas).toBeGreaterThanOrEqual(8)
    expect(infractores).toEqual([])
  })

  it('CONTROL de la sonda — reconoce un infractor y no acusa a un inocente', () => {
    const infractor = [
      '  await ensureFreshSession()',
      '  const headers = new Headers()',
      '  const tenant = useTenantStore.getState().activeTenant',
    ]
    const bueno = [
      '  const tenant = useTenantStore.getState().activeTenant',
      '  await ensureFreshSession()',
      '  const headers = new Headers()',
    ]
    const juzga = (ls: string[]): boolean => {
      const i = ls.findIndex((l) => /await\s+ensureFreshSession\(\)/.test(l))
      const antes = ls
        .slice(Math.max(0, i - 12), i)
        .some((l) => LEE_INQUILINO.test(l))
      const despues = ls.slice(i + 1, i + 16).some((l) => LEE_INQUILINO.test(l))
      return (antes || despues) && !antes
    }
    expect(juzga(infractor)).toBe(true)
    expect(juzga(bueno)).toBe(false)
  })
})
