// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ «PRE-GATEADO» NO ES «CUBIERTO», Y ÉSTE ES EL INVARIANTE QUE LO FIJA.
//
// `RequireAssurance` decide sobre el `principal.aal` **cacheado**
// (`features/identity/assurance.tsx:49-78`). `whoami` tiene `staleTime` de 60 s y **ningún
// `refetchInterval`** (`lib/auth/context.tsx:68-78`), mientras el motor **degrada AAL3 a AAL1 a
// los 15 minutos** y recalcula el AAL efectivo en cada autenticación
// (`core/auth/assurance.go:31-54`). El cliente sólo trata el 401 globalmente; un 403 se entrega a
// la mutación (`lib/api/client.ts:167-184`).
//
// ⇒ **La caché puede decir AAL3 mientras el motor dice AAL1.** El pre-gate deja pasar, la
// escritura sale, el motor contesta `step_up_required` y una mutación escrita a mano lo pintaba
// en ROJO — obstáculo sin puerta.
//
// Los caminos, verificados POR RUTA y no por nombre de método (la trampa que me costó):
//
//   connector test  POST /v1/console/connectors/test     server.go:721 → handleTestConnector
//   SSO test        POST /v1/console/sso/**/test         server.go:672 → handleTestSSOConfig
//   license inst.   POST /v1/console/license             server.go:732 → handleInstallLicense
//   license quitar  DELETE /v1/console/license           server.go:733 → handleUninstallLicense
//   activación      POST /v1/console/activation/apply    server.go:742 → handleActivationApply
//
// Los cinco handlers llaman `s.requireAAL3(...)`, y el propio motor lo documenta en el bloque de
// rutas: «the writes (install/uninstall) additionally require an AAL3 step-up», «the apply write
// requires an AAL3 step-up».
//
// Esta celda NO conduce cada pantalla —eso lo hacen las suites de cada tab—: fija el invariante
// de FORMA (AST local) que las cinco comparten, porque el defecto reaparece en cuanto alguien
// escribe otra mutación a mano detrás del mismo pre-gate.
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'
import {
  formatInspection,
  inspectCeremonySource,
} from './step-up-ceremony-guard'

const AQUI = dirname(fileURLToPath(import.meta.url))
const SALUD = join(AQUI, '..', 'health')

/** Fuente sin comentarios, conservando los saltos de línea para que las posiciones valgan. */
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

const leer = (ruta: string) => sinComentarios(readFileSync(ruta, 'utf8'))

const SUJETOS: Array<[string, string]> = [
  ['connectors-tab.tsx', join(AQUI, 'connectors-tab.tsx')],
  ['sso-tab.tsx', join(AQUI, 'sso-tab.tsx')],
  ['license-tab.tsx', join(AQUI, 'license-tab.tsx')],
  ['system-health-tab.tsx', join(SALUD, 'system-health-tab.tsx')],
]

describe('las escrituras gateadas por AAL3 ofrecen la ceremonia, no un rojo', () => {
  it('cada sujeto conoce la ceremonia por su CÓDIGO', () => {
    for (const [nombre, ruta] of SUJETOS) {
      const lineas = leer(ruta)
      expect(
        lineas.some((l) => l.includes('isStepUpRequired')),
        `${nombre} no menciona isStepUpRequired`,
      ).toBe(true)
    }
  })

  it('⛔ y ninguno decide un 403 leyendo `status === 403` a pelo', () => {
    // `system-health-tab` lo hacía: `mutation.error.status === 403` trataba igual una negativa de
    // ROL y una ceremonia. `ApiError.isForbidden` (errors.ts:59-61) ES esa comparación, así que
    // escribirla a mano no aporta nada y sí reabre el defecto — y, peor, lo hace invisible a un
    // barrido que busque `isForbidden`.
    const culpables = SUJETOS.filter(([, ruta]) =>
      leer(ruta).some((l) => /\.status\s*===\s*403/.test(l)),
    ).map(([n]) => n)
    expect(culpables).toEqual([])
  })

  it('y el barrido MIRÓ algo: los cuatro sujetos existen y deciden sobre 403', () => {
    // ⛔ Un cero sobre cero ficheros no es un cero. Si un `join` se rompiera o un fichero se
    //    renombrara, las dos celdas de arriba pasarían sin haber leído nada.
    expect(SUJETOS.length).toBe(4)
    for (const [nombre, ruta] of SUJETOS) {
      const lineas = leer(ruta)
      expect(lineas.length, `${nombre} vacío`).toBeGreaterThan(100)
      expect(
        lineas.some(
          (l) => l.includes('isForbidden') || l.includes('isStepUpRequired'),
        ),
        `${nombre} no decide sobre ningún 403`,
      ).toBe(true)
    }
  })

  it('y CADA rama de ceremonia delega o pinta — no vale que lo haga otra del mismo fichero', () => {
    // Positive isStepUpRequired accesses are classified by AST: an imperative
    // catch/onError must call report and return in that branch; a mutation.error
    // predicate must mount StepUpRequiredState on its true arm; a pure classifier
    // must bind each call and paint StepUpRequiredState when the result is stepUp.
    for (const [nombre, ruta] of SUJETOS) {
      const insp = inspectCeremonySource(nombre, readFileSync(ruta, 'utf8'))
      expect(
        insp.references.length,
        `${nombre} has no positive isStepUpRequired access`,
      ).toBeGreaterThan(0)
      expect(insp.failures, formatInspection(insp)).toEqual([])
    }
  })
})

// Preserve the G3 controls against both live readers using the shared AST guard.
describe('read-classification discovery controls', () => {
  const source = readFileSync(join(AQUI, 'connectors-tab.tsx'), 'utf8')
  const inspectClassifier = (text: string) =>
    inspectCeremonySource('connectors-tab.tsx', text).references.filter(
      (reference) => reference.kind === 'classifier',
    )

  it('follows the live classifier to both independently rendered readers', () => {
    const references = inspectClassifier(source)
    expect(references).toHaveLength(1)
    expect(references[0]?.ok).toBe(true)
  })

  it.each(['catalogAdmission', 'rosterAdmission'])(
    'rejects a dropped %s ceremony arm',
    (reader) => {
      const changed = source.replace(
        `${reader} === 'stepUp'`,
        `${reader} === 'forbidden'`,
      )
      expect(changed).not.toBe(source)
      const references = inspectClassifier(changed)
      expect(references).toHaveLength(1)
      expect(references[0]?.ok).toBe(false)
    },
  )

  it('does not accept an unused classifier return as handling', () => {
    const changed = source.replaceAll(
      '= readAdmission(',
      '= ignoredClassification(',
    )
    expect(changed).not.toBe(source)
    const references = inspectClassifier(changed)
    expect(references).toHaveLength(1)
    expect(references[0]?.ok).toBe(false)
  })
})
