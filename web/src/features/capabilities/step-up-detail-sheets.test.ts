// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ ESTO ES DEFENSA EN PROFUNDIDAD, Y LO DIGO PRIMERO PORQUE EN ESTA CAMPAÑA YA PRESENTÉ DOS
// VECES COMO «CAMINO VIVO» ALGO QUE NO LO ERA (y las dos refutadas por el contraste).
//
// ⛔ ESCRIBÍ AQUÍ QUE LOS EMISORES ERAN DOS CONJUNTOS. SON CUATRO — el contraste lo refutó y la
// cifra importa, porque de ella depende qué es defensivo y qué no:
//   · las 21 llamadas a `requireAAL3` de `core/api`, todas convergiendo en
//     `middleware.go:298-300` — cerradas en la rama de #784;
//   · `modules/governance`, dos ESCRITURAS (`breakglass.go:187`, `approvals.go:579`), correctas
//     desde siempre porque sus diálogos van por `usePrivilegedMutation`;
//   · `modules/deploy`, que tiene su PROPIO `requireStepUp` (`helpers.go:73-76`) alcanzado desde
//     `handleApply` y `handleRetire` (`lifecycle.go:216`, `:443`) — la consola ya lo trataba, y
//     lo dice en `deploy/definition-detail.tsx:237-243`; mi censo era lo que estaba mal;
//   · `core/auth`, que devuelve `ErrStepUpRequired` FUERA de `requireAAL3` en el ciclo de
//     credenciales (`webauthn.go:234`, `:307`, `:473`) — ahí sí había un defecto vivo, y esta
//     rama lo arregla en `identity/privileged-login.tsx`.
//
// Las seis hojas de detalle de aquí cuelgan de rutas de MÓDULO que **hoy no emiten ese código**.
// Se arreglan porque el defecto es de FORMA —`isForbidden` es SÓLO el status 403
// (`lib/api/errors.ts:59-61`) y la ceremonia se reconoce por el CÓDIGO (`:71-79`)— y sobrevive al
// día en que el gate llegue a una de ellas. No porque alguien lo esté sufriendo ahora.
//
// La guarda es TEXTUAL y por eso enumera sus techos abajo. Quién responde bien cuando el error
// llega de verdad lo fijan las celdas de comportamiento de `knowledge`, `governance` y `console`.
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

const AQUI = dirname(fileURLToPath(import.meta.url))
const FEATURES = join(AQUI, '..')

/** Fuente sin comentarios, conservando los saltos para que las posiciones valgan. */
const sinComentarios = (src: string): string[] => {
  let enBloque = false
  return src.split('\n').map((linea) => {
    let l = linea
    if (enBloque) {
      const fin = l.indexOf('*/')
      if (fin === -1) return ''
      l = l.slice(fin + 2)
      enBloque = false
    }
    const ini = l.indexOf('/*')
    if (ini !== -1) {
      const fin = l.indexOf('*/', ini + 2)
      if (fin === -1) {
        enBloque = true
        l = l.slice(0, ini)
      } else {
        l = l.slice(0, ini) + l.slice(fin + 2)
      }
    }
    const sl = l.indexOf('//')
    if (sl !== -1) l = l.slice(0, sl)
    return l
  })
}

const SUJETOS = [
  'capabilities/server-detail.tsx',
  'capabilities/tool-pins.tsx',
  'catalog/entry-detail.tsx',
  'catalog/instance-detail.tsx',
  'deploy/definition-detail.tsx',
  'deploy/revisions.tsx',
]

const leer = (rel: string) =>
  sinComentarios(readFileSync(join(FEATURES, rel), 'utf8'))

const primera = (lineas: string[], aguja: string) => {
  const i = lineas.findIndex((l) => l.includes(aguja))
  return i === -1 ? Number.POSITIVE_INFINITY : i
}

/**
 * ⛔ TECHOS DECLARADOS, los mismos que en las clases anteriores y por los mismos motivos:
 *  1. No ve una SEGUNDA decisión en el mismo fichero (compara el primer par). Ninguno de estos
 *     seis tiene dos hoy — se comprueba abajo, para que deje de ser cierto en voz alta si un día
 *     alguien añade la segunda.
 *  2. Una condición en UNA SOLA LÍNEA pasa en los dos sentidos: las posiciones empatan.
 *  3. No despoja literales de cadena.
 *  4. Sólo conoce estos dos nombres: una decisión escrita como `status === 403` es invisible —
 *     y esa forma EXISTE en el árbol (la tenía `routine-policies-view`), así que la celda de
 *     abajo la prohíbe explícitamente en estos seis.
 */
describe('las seis hojas de detalle ofrecen la ceremonia antes que la acusación', () => {
  it('cada una conoce la ceremonia, y ANTES que el rol', () => {
    for (const rel of SUJETOS) {
      const l = leer(rel)
      const rol = primera(l, 'isForbidden')
      const ceremonia = primera(l, 'isStepUpRequired')
      expect(
        rol,
        `${rel} ya no decide el rol — ¿sigue siendo sujeto?`,
      ).not.toBe(Number.POSITIVE_INFINITY)
      expect(ceremonia, `${rel} no conoce la ceremonia`).toBeLessThan(rol)
    }
  })

  it('y la ceremonia se PINTA, no sólo se menciona', () => {
    // Sin esto, «menciona isStepUpRequired» se satisface con un booleano derivado que nadie usa.
    for (const rel of SUJETOS) {
      const src = leer(rel).join('\n')
      expect(src, `${rel} nombra la ceremonia y no la pinta`).toContain(
        '<StepUpRequiredState',
      )
      // Y con reintento: el panel promete que la acción se reanuda (i18n common:
      // privileged.stepUp.description) y el host ejecuta `retry?.()`.
      expect(src, `${rel} pinta la ceremonia sin reintento`).toContain(
        'onElevated',
      )
    }
  })

  it('⛔ y ninguna decide un 403 leyendo `status === 403` a pelo', () => {
    // La forma que ningún barrido de `isForbidden` encuentra, y que EXISTÍA en este árbol.
    const culpables = SUJETOS.filter((rel) =>
      leer(rel).some((l) => /\.status\s*===\s*403/.test(l)),
    )
    expect(culpables).toEqual([])
  })

  it('y el barrido MIRÓ seis ficheros con UNA decisión cada uno', () => {
    // ⛔ Anti-vacuidad por DECISIONES, no por ficheros: si una de las seis se partiera en dos
    //    decisiones, el techo nº1 dejaría de cubrir la segunda y quiero enterarme aquí.
    expect(SUJETOS.length).toBe(6)
    for (const rel of SUJETOS) {
      const usos = leer(rel).filter((l) => l.includes('isForbidden')).length
      expect(usos, `${rel} tiene ${usos} decisiones de rol, no 1`).toBe(1)
    }
  })
})
