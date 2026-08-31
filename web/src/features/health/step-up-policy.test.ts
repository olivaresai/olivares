// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ LA CLASE DE **LECTURA**, y su forma es distinta de la de escritura. Aquí nadie pinta un
// toast: seis sitios de `health` sustituían la pantalla por `ForbiddenState` —«no tienes
// autorización, pide acceso a un administrador»— leyendo `isForbidden`, que es SÓLO el status
// 403 (lib/api/errors.ts:59). Un `step_up_required` lo satisface también, así que la vista de
// salud entera desaparecía detrás de una acusación falsa, sin salida.
//
// Dos de los seis lo hacían dentro de un BOOLEANO derivado (`const forbidden = …`), que es la
// forma que un barrido por ternarios no ve: por eso el invariante se comprueba por POSICIÓN
// sobre el fichero, no por la sintaxis de la rama.
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const AQUI = dirname(fileURLToPath(import.meta.url))
const ROL = 'isForbidden'
const CEREMONIA = 'isStepUpRequired'

const fuentes = () =>
  readdirSync(AQUI)
    .filter((f) => f.endsWith('.tsx') || f.endsWith('.ts'))
    .filter((f) => !f.includes('.test.'))

/** Líneas con un uso REAL (no una mención en comentario) de `aguja`. */
const usos = (src: string, aguja: string) =>
  src
    .split('\n')
    .map((l, i) => [l, i] as const)
    .filter(([l]) => !l.trim().startsWith('//') && l.includes(aguja))
    .map(([, i]) => i)

const primerUso = (src: string, aguja: string) =>
  usos(src, aguja)[0] ?? Number.POSITIVE_INFINITY

/**
 * ⛔ EL TECHO DE ESTA GUARDA, DECLARADO. Comprueba que un fichero que decide el rol MENCIONA
 * antes el aseguramiento. Eso caza lo que de verdad reaparece —un fichero nuevo, o uno viejo
 * al que alguien añade `isForbidden` sin ceremonia— y NO puede cazar «la ceremonia está en
 * otra decisión del mismo fichero»: el contraste `sol max` lo demostró mutando el ternario de
 * SLA en un fichero cuya primera decisión sí la tiene, y las 79 celdas siguieron verdes.
 *
 * Intenté cerrarlo comparando POR OCURRENCIA —cada uso del rol con algún uso anterior de la
 * ceremonia— y mi propio control negativo me lo tumbó: con la ceremonia arriba, TODA
 * ocurrencia posterior la tiene «antes», así que el criterio es idéntico al global. «Antes en
 * el fichero» no puede significar «en la misma decisión», y un texto no ve un ámbito.
 *
 * El techo se declara, y se cubre donde se puede: `connector-health` tiene celda que EJERCE
 * su camino (connector-health.test.tsx). Los ternarios de SLA y de Timeline NO la tienen, y
 * digo por qué en vez de fingirlo: su consulta está detrás de un `Select` de Radix que en
 * jsdom no se conduce sin pelea, así que una celda mía ahí mediría el Select, no el sujeto.
 * Queda como RESIDUO CON NOMBRE para el integrador. Una guarda honesta que dice lo que no ve
 * vale más que una que promete un alcance que no tiene.
 */
describe('health — la negativa de ROL nunca se decide antes que la de ASEGURAMIENTO', () => {
  it('el criterio distingue los dos casos, y no lo satisface un comentario', () => {
    // Control POSITIVO y NEGATIVO en la misma celda: sin el primero, un criterio que no case
    // con nada daría verde sobre un árbol lleno de defectos; sin el segundo, bastaría con
    // MENCIONAR la ceremonia en un comentario para aprobar — y estos ficheros están llenos de
    // comentarios que la nombran, incluidos los que acabo de escribir.
    const malo =
      'const forbidden = error instanceof ApiError && error.isForbidden'
    expect(primerUso(malo, ROL)).toBeLessThan(primerUso(malo, CEREMONIA))

    const trampa = `  // la ceremonia (isStepUpRequired) iría aquí\n  const forbidden = e.isForbidden`
    expect(primerUso(trampa, CEREMONIA)).toBe(Number.POSITIVE_INFINITY)
    expect(primerUso(trampa, ROL)).toBeLessThan(primerUso(trampa, CEREMONIA))

    const bueno = `  const stepUp = e.isStepUpRequired\n  const forbidden = !stepUp && e.isForbidden`
    expect(primerUso(bueno, CEREMONIA)).toBeLessThan(primerUso(bueno, ROL))

    // Y lo que esta guarda NO ve, escrito como caso para que nadie lo lea como cubierto: con
    // la ceremonia arriba, una SEGUNDA decisión de rol más abajo pasa igual. Eso lo cubren
    // las celdas de comportamiento, no este barrido.
    const dosComponentes = [
      '  const stepUp = e.isStepUpRequired',
      '  const forbidden = !stepUp && e.isForbidden',
      '  // …otro componente…',
      '  const otro = e.isForbidden ? <ForbiddenState /> : null',
    ].join('\n')
    expect(primerUso(dosComponentes, CEREMONIA)).toBeLessThan(
      primerUso(dosComponentes, ROL),
    )
  })

  it('y ningún fichero de health decide el rol SIN mencionar antes el aseguramiento', () => {
    const culpables = fuentes()
      .map((f) => [f, readFileSync(join(AQUI, f), 'utf8')] as const)
      .filter(([, src]) => primerUso(src, ROL) !== Number.POSITIVE_INFINITY)
      .filter(([, src]) => primerUso(src, CEREMONIA) > primerUso(src, ROL))
      .map(([f]) => f)

    expect(culpables).toEqual([])
    // Y el barrido MIRÓ algo: un cero sobre cero ficheros no es un cero, y un cero sobre
    // ficheros que no mencionan el rol tampoco dice nada de esta clase.
    const conRol = fuentes().filter(
      (f) =>
        primerUso(readFileSync(join(AQUI, f), 'utf8'), ROL) !==
        Number.POSITIVE_INFINITY,
    )
    expect(conRol.length).toBeGreaterThanOrEqual(4)
  })
})
