// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Toda ruta registrada tiene entrada en el arnés de capturas de documentación.
//
// ⛔ POR QUÉ ESTA GUARDA Y NO DERIVAR LA LISTA. Lo natural sería que `docs-captures.spec.ts`
// sacara sus rutas del registro y dejara de poder quedarse corta — que es lo que hace
// `console:walk`, y su propia descripción dice por qué: *«rather than from a hand-written list
// that goes stale the day a screen is added»*.
//
// **Aquí no se puede, y el motivo es lo que hace buena a esa lista**: cada entrada lleva un
// `heading` — el h1 real de la vista — que es el TESTIGO de que la pantalla cargó. Sin él, una
// captura del esqueleto se guarda igual y pasa cualquier comprobación de «existe el fichero».
// Ese testigo no está en el registro y no se puede inventar.
//
// ⇒ Se conserva la lista escrita a mano CON su testigo, y se hace imposible que se quede corta.
//    Medido el 2026-08-18: añadí `/tenants` y el arnés capturó 108 imágenes de 54 vistas sin
//    incluirla, en verde — la ruta salía en el sidebar de la captura y no tenía captura propia.
//
// ⛔⛔ Y EL DENOMINADOR ERA LA LISTA EQUIVOCADA — corregido el 2026-08-18 con la medida delante.
// Esta guarda leía `FEATURE_VIEWS` (**53** rutas) y el árbol monta **58**: las cinco de diferencia
// —`/login`, `/setup`, `/accept-invite`, `/settings`, `/status-page`— **no podían romperla nunca**,
// porque no estaban en la lista contra la que comparaba. No es una laguna cualquiera: son el camino
// que recorre un cliente NUEVO antes de ver nada más, y ninguna tiene PNG publicado.
//
// Es exactamente el fallo que esta guarda existe para impedir, cometido por ella misma: **un oráculo
// sacado de una de las fuentes que vigila no puede ver lo que a esa fuente le falta.** Lo cazó
// the integrator contando las TRES listas de rutas que hay en el árbol y que no derivan una de otra
// (registro 53 · censo 58 · spec del arnés 53).
//
// ⇒ El denominador es ahora la **UNIÓN** de censo y registro. Una ruta escondida en cualquiera de
//   las dos rompe esta celda; para taparla habría que borrarla de las dos a la vez.
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { FEATURE_VIEWS } from './registry'
import censo from './route-census.json'

const SPEC = resolve(__dirname, '../../e2e/docs-captures.spec.ts')

/** Toda ruta que el árbol MONTA, venga de donde venga. El censo lleva las patas públicas y
 *  `/settings`, que el registro de features no conoce; el registro es la fuente viva de las demás.
 *  Se unen a propósito: ninguna de las dos puede quedarse corta sola. */
const RUTAS_MONTADAS: { id: string; path: string }[] = [
  ...FEATURE_VIEWS.map((v) => ({ id: v.id, path: v.path })),
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
