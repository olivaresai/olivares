// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ ESTA CELDA EXISTE PORQUE UNA CELDA DE COMPORTAMIENTO CUBRE UN SITIO, NO UNA CLASE.
// El arreglo de esta sesión toca TRECE escrituras en siete ficheros, y una prueba que pulsa
// el formulario de datasets fija exactamente una: las otras doce podrían volver al handler a
// mano sin que nada se pusiera rojo. Ya me pasó —«pulsar 5 de 12 llamantes deja vivo el que
// no pulsas»—, así que la clase se vigila por lo que la define: NINGUNA escritura de
// `model-ops` vuelve a ramificar por `isForbidden` a mano.
//
// La política vive en `useFailedActionReporter` (lib/hooks/use-privileged-mutation.ts:33-59)
// y su propio comentario dice para qué nació: los handlers escritos a mano «colapsaban las
// dos primeras respuestas» —ceremonia y rol— «en: tu rol no puede hacer esto», que es FALSO
// para un step-up y manda al operador a pedir un permiso que ya tiene.
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const AQUI = dirname(fileURLToPath(import.meta.url))

/**
 * ⛔ LA GUARDA VA POR LO QUE EL DEFECTO PRODUCE, NO POR CÓMO SE ESCRIBE.
 *
 * Mi primera versión buscaba el ACCESO textual `err.isForbidden`, y el contraste `sol max` la
 * atravesó sin despeinarse: le basta con renombrar la variable. Aplicó este mutante al borrado
 * de datasets y las 45 celdas de model-ops siguieron VERDES:
 *
 *     onError: (failure) => {
 *       if (failure instanceof ApiError && failure.isForbidden) {
 *         toast.warning(t('common:privileged.notAuthorizedToast'))
 *         return
 *       }
 *       report(failure)
 *     }
 *
 * Y con él, cuatro formas más: desestructurar (`const { isForbidden } = err`), un alias, el
 * acceso por corchetes, o saltarse el getter con `status === 403`. Una guarda que enumera
 * sintaxis siempre va por detrás de quien la escribe.
 *
 * Lo que NINGUNA de esas formas puede evitar es el RESULTADO: para acusar al operador hay que
 * enseñarle la acusación, y la acusación es una clave de copy concreta. Así que el invariante
 * es: en `model-ops` nadie pinta `privileged.notAuthorizedToast` — sólo la política común
 * (lib/hooks/use-privileged-mutation.ts:51), que antes de llegar ahí ya ha separado la
 * ceremonia del rol. Es la misma diferencia que entre vigilar el `if` y vigilar la salida.
 */
const ACUSACION = ['privileged', 'notAuthorizedToast'].join('.')

// Y el acceso textual se conserva como SEGUNDA red, no como la única: coge la forma canónica
// aunque alguien invente una acusación con otra copy. El `(?<![.\w])` evita acusar a la
// lectura legítima `query.error.isForbidden`; sin él la batería salía roja antes de mutar nada.
const RAMA_DE_ESCRITURA =
  /(?<![.\w])(?:err|error|e|failure|cause|reason)\s*\.\s*isForbidden\b/

const fuentes = () =>
  readdirSync(AQUI)
    .filter((f) => f.endsWith('.tsx') || f.endsWith('.ts'))
    .filter((f) => !f.includes('.test.'))

describe('model-ops — el reporte de una escritura refusada tiene UN solo hogar', () => {
  it('encuentra la acusación cuando la hay (control POSITIVO)', () => {
    // Sin esto, un patrón que no case con NADA daría verde sobre un árbol lleno de defectos.
    expect(`toast.warning(t('common:${ACUSACION}'))`).toContain(ACUSACION)
    // Y la forma EXACTA que el contraste usó para atravesar la versión anterior:
    const delContraste = `
      onError: (failure) => {
        if (failure instanceof ApiError && failure.isForbidden) {
          toast.warning(t('common:privileged.notAuthorizedToast'))
          return
        }
        report(failure)
      }`
    expect(delContraste.includes(ACUSACION)).toBe(true)
    expect(RAMA_DE_ESCRITURA.test(delContraste)).toBe(true)
  })

  it('y no confunde la rama de escritura con la lectura de una consulta', () => {
    // Sin esto, un patrón que no case con NADA daría verde sobre un árbol lleno de defectos,
    // que es la forma más cara de aprobar sin mirar.
    const fabricado = `
      onError: (err) => {
        if (err instanceof ApiError && err.isForbidden) {
          toast.warning(t('common:privileged.notAuthorizedToast'))
          return
        }
      }`
    expect(RAMA_DE_ESCRITURA.test(fabricado)).toBe(true)
    // Y NO casa con la lectura de una consulta, que es legítima.
    expect(
      RAMA_DE_ESCRITURA.test(
        'if (query.error instanceof ApiError && query.error.isForbidden)',
      ),
    ).toBe(false)
  })

  it('y ninguna fuente de model-ops acusa al operador por su cuenta', () => {
    const leidas = fuentes().map(
      (f) => [f, readFileSync(join(AQUI, f), 'utf8')] as const,
    )
    // 1) nadie pinta la acusación: el invariante que no depende de la sintaxis
    expect(
      leidas.filter(([, src]) => src.includes(ACUSACION)).map(([f]) => f),
    ).toEqual([])
    // 2) y nadie conserva la rama canónica: segunda red, por si la acusación cambia de copy
    const culpables = leidas
      .filter(([, src]) => RAMA_DE_ESCRITURA.test(src))
      .map(([f]) => f)

    expect(culpables).toEqual([])
    // Y el barrido MIRÓ algo: un cero sobre cero ficheros no es un cero.
    expect(fuentes().length).toBeGreaterThan(5)
  })
})
