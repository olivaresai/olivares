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
// TEXTUAL que las cinco comparten, porque el defecto es de FORMA y reaparece en cuanto alguien
// escribe otra mutación a mano detrás del mismo pre-gate.
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

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
    // ⛔ ESTA CELDA MIRABA EL FICHERO Y UN MUTANTE SOBREVIVIÓ. Preguntaba «¿hay algún `report(`
    //    o `usePrivilegedMutation` en el fichero?», y `connectors-tab` tiene un
    //    `usePrivilegedMutation` en la mutación de AL LADO: cambiar el `report(err, …)` del
    //    `test` por un `toast.error` propio la dejaba verde. Es el mismo defecto que ya corregí
    //    dos veces en esta campaña —contar FICHEROS en vez de DECISIONES— y esta vez lo cazó mi
    //    propio mutante, no el contraste.
    //
    //    Dos formas legítimas que la primera versión no distinguía, y las dos están en el árbol:
    //      · uso NEGADO (`!e.isStepUpRequired`): es el predicado de ROL, no una rama de ceremonia;
    //      · booleano DERIVADO (`const necesitaCeremonia = …`): la ceremonia se pinta más abajo,
    //        donde se usa el nombre, así que ninguna ventana de líneas puede verla.
    const VENTANA = 3
    const HACE_ALGO = /report\(|<StepUpRequiredState/

    // ⛔ EL ÁMBITO SE MIDE POR LLAVES, NO POR UN NÚMERO DE LÍNEAS — y esto lo rompió el
    //    FORMATEADOR, no un cambio de conducta. `prettier` partió
    //    `report(err, guardarReanudacion(...))` en cuatro renglones y empujó el `return` fuera de
    //    una ventana de 3, así que la guarda acusó a `connectors-tab:692` de «delega y NO sale»
    //    con el `return` intacto dos líneas más abajo. Es la tercera vez en esta campaña que una
    //    ventana por LÍNEAS me miente, y ahora hay un trinquete que va a reformatear el árbol en
    //    tandas: una guarda que dependa del ancho de línea es una guarda que caduca sola.
    const bloqueDesde = (ls: string[], desde: number): string => {
      let prof = 0
      const trozo: string[] = []
      for (let k = desde; k < Math.min(ls.length, desde + 60); k++) {
        const l = ls[k] ?? ''
        trozo.push(l)
        prof += (l.match(/\{/g) ?? []).length - (l.match(/\}/g) ?? []).length
        if (k > desde && prof <= 0) break
      }
      return trozo.join('\n')
    }

    for (const [nombre, ruta] of SUJETOS) {
      const lineas = leer(ruta)
      const ramas = lineas
        .map((l, i) => [l, i] as const)
        .filter(([l]) =>
          l
            .replace(/![\s\w.]*isStepUpRequired/g, '')
            .includes('isStepUpRequired'),
        )
      expect(
        ramas.length,
        `${nombre} no tiene ninguna rama de ceremonia sin negar`,
      ).toBeGreaterThan(0)

      for (const [linea, i] of ramas) {
        const ventana = bloqueDesde(lineas, i)
        // ⛔ DELEGAR NO BASTA: HAY QUE SALIR. Mutante nombrado por el contraste y REPRODUCIDO
        //    antes de arreglar nada: quitando el `return` que sigue a `report(...)` en
        //    connectors-tab, la guarda seguía viendo `report(` junto a `isStepUpRequired` y daba
        //    verde, mientras el control caía al `toast.error` de abajo — es decir, la ceremonia
        //    se abría Y se pintaba el rojo. Reportar y seguir es el defecto que esta campaña
        //    persigue, sólo que escrito en dos líneas en vez de una.
        //
        //    La rama JSX (`<StepUpRequiredState`) no lleva `return`: es un brazo de ternario y
        //    la exclusión se la da el propio operador `?:`. Por eso el requisito sólo se exige
        //    al camino imperativo.
        if (/report\(/.test(ventana) && !/\breturn\b/.test(ventana)) {
          expect(
            false,
            `${nombre}:${i + 1} delega la ceremonia y NO sale — el control cae al error de abajo`,
          ).toBe(true)
        }
        if (HACE_ALGO.test(ventana)) continue

        const decl =
          linea.match(/const\s+(\w+)\s*=/) ??
          lineas[i - 1]?.match(/const\s+(\w+)\s*=/)
        const nombreVar = decl?.[1]
        const usado =
          nombreVar !== undefined &&
          lineas.some(
            (l, j) =>
              j > i &&
              l.includes(nombreVar) &&
              HACE_ALGO.test(lineas.slice(j, j + 1 + VENTANA).join('\n')),
          )
        expect(
          usado,
          `${nombre}:${i + 1} reconoce la ceremonia y no hace nada con ella`,
        ).toBe(true)
      }
    }
  })
})
