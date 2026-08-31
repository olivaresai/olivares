// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ LA CLASE, NO EL FICHERO. Tres escrituras de `killswitch` ramificaban por `isForbidden`
// —que es SÓLO el status 403 (lib/api/errors.ts:59)— sin saber que un `step_up_required` lo
// satisface también, así que acusaban al operador de no tener un permiso que SÍ tiene, y
// justo en la pantalla que para la flota. Una celda de comportamiento fija UNA de las tres;
// la guarda de abajo fija las tres y las que vengan.
//
// Y aquí el invariante NO puede ser «nadie pinta la acusación», como en `model-ops`:
// `reenable-dialog` la conserva a propósito porque la suya lleva `description: err.message`
// —el mensaje del motor explicando la salida— y `report` sólo pone la advertencia pelada.
// Perder ese texto sería cambiar un mensaje exacto por uno genérico. El invariante correcto
// es el ORDEN: la acusación nunca aparece sin la ceremonia delante.
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const AQUI = dirname(fileURLToPath(import.meta.url))
const ACUSACION = ['privileged', 'notAuthorizedToast'].join('.')
const CEREMONIA = 'isStepUpRequired'
// `report(` cubre el caso en que la ceremonia se delega entera en la política común
// (lib/hooks/use-privileged-mutation.ts:37-46), que es donde `isStepUpRequired` vive de verdad.
const DELEGA = /\breport\(/

const fuentes = () =>
  readdirSync(AQUI)
    .filter((f) => f.endsWith('.tsx') || f.endsWith('.ts'))
    .filter((f) => !f.includes('.test.'))

/** Dónde aparece por primera vez, o Infinity si no aparece. */
const primeraVez = (src: string, aguja: string) => {
  const i = src.indexOf(aguja)
  return i === -1 ? Number.POSITIVE_INFINITY : i
}

describe('killswitch — la acusación de rol nunca va sin la ceremonia delante', () => {
  it('el criterio distingue los dos casos (control POSITIVO y NEGATIVO)', () => {
    // Sin esto, un criterio que no case con NADA daría verde sobre un árbol lleno de defectos.
    const malo = `
      onError: (err) => {
        if (err instanceof ApiError && err.isForbidden) {
          toast.warning(t('common:${ACUSACION}'))
          return
        }
      }`
    expect(malo.includes(ACUSACION)).toBe(true)
    expect(primeraVez(malo, CEREMONIA)).toBe(Number.POSITIVE_INFINITY)
    expect(DELEGA.test(malo)).toBe(false)

    const bueno = `
      onError: (err) => {
        if (err instanceof ApiError && err.isStepUpRequired) { report(err); return }
        if (err instanceof ApiError && err.isForbidden) {
          toast.warning(t('common:${ACUSACION}'), { description: err.message })
          return
        }
      }`
    expect(primeraVez(bueno, CEREMONIA)).toBeLessThan(
      primeraVez(bueno, ACUSACION),
    )
  })

  it('y ninguna fuente de killswitch la enseña sin la ceremonia antes', () => {
    const culpables = fuentes()
      .map((f) => [f, readFileSync(join(AQUI, f), 'utf8')] as const)
      .filter(([, src]) => src.includes(ACUSACION))
      .filter(
        ([, src]) => primeraVez(src, CEREMONIA) > primeraVez(src, ACUSACION),
      )
      .map(([f]) => f)

    expect(culpables).toEqual([])
    // Y el barrido MIRÓ algo: un cero sobre cero ficheros no es un cero.
    expect(fuentes().length).toBeGreaterThan(5)
  })

  it('y las que delegan del todo no vuelven a ramificar por su cuenta', () => {
    // La otra mitad: `engage-card` y `killswitch-view` no conservan copy propia, así que su
    // rama entera vive en la política común. Si alguna vuelve a escribir la acusación a mano,
    // la celda de arriba ya la coge; ésta fija que la delegación EXISTE, para que retirarla
    // en silencio —dejando el error sin reportar— tampoco pase inadvertido.
    for (const f of ['engage-card.tsx', 'killswitch-view.tsx']) {
      const src = readFileSync(join(AQUI, f), 'utf8')
      expect(DELEGA.test(src)).toBe(true)
      expect(src.includes(ACUSACION)).toBe(false)
    }
  })
})
