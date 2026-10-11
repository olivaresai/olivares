// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Every registered route must appear in the documentation screenshot harness.
//
// Keep this guard rather than deriving the list. `console:walk` derives routes from the
// registry to avoid a manual list becoming stale when a screen is added. The screenshot
// list has a different requirement: each entry's `heading` is the view's actual h1 and
// proves the screen loaded. Without it, a skeleton screenshot passes file-existence checks.
// The registry does not contain that heading, and it cannot be invented.
//
// Keep the manual list with its witness, and guard against missing routes. Measured on
// 2026-08-18: after `/tenants` was added, the harness captured 108 images of 54 views and
// passed without capturing it, although the route appeared in the sidebar.
//
// The original denominator was wrong, corrected on 2026-08-18. This guard read
// `FEATURE_VIEWS` (53 routes), but the tree mounted 58. The missing five (`/login`, `/setup`,
// `/accept-invite`, `/settings`, `/status-page`) could never fail the guard, despite being
// the path a new customer follows before anything else and having no published PNGs.
//
// An oracle derived from one source cannot detect omissions in that source. the maintainer
// caught this by counting three independent route lists: registry 53, inventory 58,
// harness spec 53.
//
// The denominator is now the union of inventory and registry. Hiding a route in either
// list fails this test; concealing it would require removing it from both.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { FEATURE_VIEWS } from './registry'
import { ANONYMOUS_VIEWS } from './anonymous-registry'
import censo from './route-census.json'
import { ANONYMOUS_EXTENSION_ROUTES, EXTENSION_ROUTES } from './extensions'

const SPEC = resolve(__dirname, '../../e2e/docs-captures.spec.ts')

/** Toda ruta que el árbol MONTA, venga de donde venga. El censo lleva las patas públicas y
 *  `/settings`, que el registro de features no conoce; el registro es la fuente viva de las demás.
 *  Se unen a propósito: ninguna de las dos puede quedarse corta sola. */
const RUTAS_MONTADAS: { id: string; path: string }[] = [
  ...FEATURE_VIEWS.map((v) => ({ id: v.id, path: v.path })),
  ...ANONYMOUS_VIEWS.map((v) => ({ id: v.id, path: v.path })),
  ...(censo.paths as string[])
    .filter((p) => !FEATURE_VIEWS.some((v) => v.path === p))
    .map((p) => ({ id: `censo${p}`, path: p })),
]

/** Rutas que a propósito NO se capturan, con el motivo en la misma línea. Vacío hoy: si alguna
 *  llega a estarlo, el motivo se escribe aquí y no en un comentario suelto del spec. */
const SIN_CAPTURA: Record<string, string> = {
  // ⛔ EL MOTIVO CAMBIO EL 2026-08-29 Y NO ES UN MATIZ. Decia «se captura pero no se publica»,
  // y eso dejo de ser cierto: hoy NO SE CAPTURA. La toma de la invitacion viva —la que iba a
  // sustituir a esta— choca con step-up AAL3 al pulsar «Onboard user» (medido en dos corridas
  // completas, los dos temas agotan los 30 s), y debilitar AAL3 por una foto esta descartado.
  // Adjudicado por the planner: se retiran las DOS tomas y se borran los dos PNG que seguian
  // publicados —la guarda `no_publicar` impedia republicarlos pero nunca retiro los viejos—.
  //
  // ⚠ Se actualiza el MOTIVO en vez de dejarlo: una exencion cuya razon ya no describe el mundo
  //   es peor que no tenerla, porque el siguiente que la lea decidira sobre un hecho falso.
  '/accept-invite':
    'NO se captura: el estado con token exige sembrar una invitacion viva y eso requiere AAL3 (la sesion del arnes es AAL1); sin token la pantalla es un ERROR en rojo y publicarla seria autoridad falsa',
  // VACIO desde el 2026-08-20, y las dos entradas que habia NO se borran en silencio:
  //
  //   '/session-viewer/$id'  «parametrica: exige sembrar una sesion y navegar a su id (C10-02)»
  //   '/setup'               «redirige a /login sobre un estate ya instalado (medido); exige un
  //                           arranque sin sembrar»
  //
  // ⭐ Las dos eran CORRECTAS, y por eso se retiran quitandoles el MOTIVO en vez de levantando la
  //    excepcion — que es lo que habria pasado si alguien las hubiera leido como un olvido:
  //    · `/setup` lo fotografia ahora un SEGUNDO motor, con su propio `--data-dir` y SIN sembrar,
  //      con control positivo contra `/v1/server-info` (`setup_required: true`) ANTES de disparar.
  //      La declaracion vieja predecia exactamente el fallo que ese control impide: «alguien habria
  //      anadido /setup a VIEWS y el arnes habria guardado la pantalla de LOGIN etiquetada como el
  //      asistente de instalacion».
  //    · `/session-viewer/$id` navega a un id RESUELTO EN VIVO del estate sembrado, nunca a uno
  //      codificado: un id codificado sobrevive al dia en que cambian los ids y fotografia el
  //      estado de «no encontrada» en verde.
  //
  // Si vuelve a hacer falta una entrada aqui, el motivo se escribe en la misma linea y con su
  // medida — nunca en un comentario suelto del spec.
}

/** ⛔ ESCAPE COMPLETO, y su ausencia hacía INSATISFACIBLE a la mitad paramétrica de este guardián.
 *
 *  La versión anterior escapaba `/` y `-` y nada más: `v.path.replace(/[/-]/g, '\\$&')`. Para
 *  `/session-viewer/$id` eso produce el patrón `path:\s*'\/session\-viewer\/$id'`, donde el `$`
 *  **sigue siendo el ancla de fin de cadena** de la expresión regular. Ese patrón no puede casar
 *  nunca, con el literal delante o sin él.
 *
 *  ⇒ La consecuencia no es cosmética: **la ÚNICA forma de poner verde este guardián para una ruta
 *  paramétrica era declararla en `SIN_CAPTURA`**. La excepción que había ahí tenía su motivo
 *  escrito y era CIERTO (hacía falta sembrar una sesión), pero además estaba tapando este defecto:
 *  quien quitara el motivo se habría encontrado el guardián rojo igual, sin entender por qué.
 *
 *  Verificado por mutación en las dos direcciones (2026-08-20): con el escape completo, quitar el
 *  literal `path: '/session-viewer/$id'` del spec pone la celda ROJA nombrando esa ruta; con el
 *  literal puesto, verde. Con el escape viejo, roja en los dos casos — que es la definición de un
 *  guardián que no mide.
 */
const escapaRegex = (v: string) => v.replace(/[.*+?^${}()|[\]\\/-]/g, '\\$&')

/* ⛔ EL PATRON VA ANCLADO POR LA IZQUIERDA, y la mitad de esto la aporto un contraste externo.
 *
 *  Sin `(^|[^A-Za-z])`, `notpath: '/x'` —o cualquier propiedad que TERMINE en `path`— cuenta como
 *  declaracion. Es la misma forma que `check-sigpipe-booleans` tuvo que anclar cuando contaba la
 *  segunda barra de `||` como tuberia.
 *
 *  ⚠ Y lo que el contraste dijo de mas, con su refutacion, porque una correccion a medias es peor
 *  que ninguna: afirmo que al patron «le falta la comilla de cierre», de modo que «una ruta es
 *  prefijo valido de cualquier otra» (`/work` casando `/workspace`). **No es cierto en este arbol**
 *  — la comilla final esta puesta, en HEAD y en el commit que el propio informe declara auditado.
 *  Medido en vez de discutido:
 *
 *      patron CON comilla final,  /work contra "path: '/workspace'"  ->  false
 *      patron SIN comilla final,  el mismo caso                      ->  true
 *      control positivo, /workspace contra el mismo texto            ->  true
 *
 *  Sus mutantes corrieron sobre una reconstruccion suya que perdio la comilla. **Lo demas de ese
 *  hallazgo SI se sostiene y esta adoptado**: este guardian lee BYTES del fichero, asi que no
 *  distingue codigo ejecutable de comentario ni de objeto muerto —yo mismo me comi ese error hoy
 *  contando `executive` como vivo cuando vive dentro del comentario que lo retira— y **no comprueba
 *  que la entrada lleve `heading`**, que es la razon declarada de conservar la lista a mano.
 *  Cerrar eso exige leer el AST o importar la estructura que los tests consumen, y es trabajo
 *  propio, no una linea mas de regex.
 */
describe('cobertura del arnés de capturas', () => {
  const spec = readFileSync(SPEC, 'utf8')

  it('toda ruta registrada tiene entrada en docs-captures.spec.ts', () => {
    const faltan = RUTAS_MONTADAS.filter(
      (v) =>
        !(v.path in SIN_CAPTURA) &&
        ![...EXTENSION_ROUTES, ...ANONYMOUS_EXTENSION_ROUTES].some(
          (route) => route.path === v.path && route.heading.trim() !== '',
        ) &&
        !new RegExp(`(^|[^A-Za-z])path:\\s*'${escapaRegex(v.path)}'`, 'm').test(
          spec,
        ),
    ).map((v) => `${v.id}: ${v.path}`)
    expect(
      faltan,
      `Estas rutas están montadas y el arnés de capturas no las conoce, así que la documentación ` +
        `pública nunca las enseña:\n  ${faltan.join('\n  ')}\n` +
        `Añade cada una a VIEWS en web/e2e/docs-captures.spec.ts CON su \`heading\` — el h1 real de ` +
        `la vista, que es lo que impide guardar una captura del esqueleto — o decláralas en ` +
        `SIN_CAPTURA con el motivo.`,
    ).toEqual([])
  })

  // CONTROL QUE NO DEBE DISPARAR: la aserción de arriba mira el SPEC, no el disco. Si mirase los
  // PNG, pasaría a fallar en cualquier árbol donde no se hayan generado — y un test que exige
  // artefactos binarios para pasar acaba desactivado.
  it('no depende de que las capturas estén generadas', () => {
    expect(spec.length).toBeGreaterThan(1000)
  })

  // CONTROL POSITIVO de la unión, y no es ceremonia: si un día `route-census.json` se leyera vacío
  // —renombrado, `resolveJsonModule` apagado, la clave cambiada de nombre— el `filter` de arriba
  // daría cero, la unión colapsaría al registro y esta guarda volvería silenciosamente al
  // denominador corto que acaba de costarnos cinco rutas. Esto lo convierte en rojo.
  it('la unión aporta rutas que el registro no tiene', () => {
    const soloCenso = RUTAS_MONTADAS.filter(
      (r) => !FEATURE_VIEWS.some((v) => v.path === r.path),
    ).map((r) => r.path)
    expect(
      soloCenso.length,
      'route-census.json ya no aporta ninguna ruta que el registro no tenga. O el censo se ha ' +
        'quedado sin leer (y esta guarda ha vuelto al denominador corto), o todas las patas ' +
        'públicas se han registrado como features — compruébalo antes de relajar esta celda.',
    ).toBeGreaterThan(0)
    expect(soloCenso).toContain('/login')
  })
})
