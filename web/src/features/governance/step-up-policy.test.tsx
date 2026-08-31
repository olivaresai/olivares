// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ QUÉ ES ESTO EXACTAMENTE, CORREGIDO TRAS EL CONTRASTE: **defensa en profundidad sobre
// LECTURAS, no el arreglo de un camino vivo.** Escribí lo contrario y era falso, así que lo
// primero es deshacer mi propia afirmación.
//
// `governance` es el único módulo que emite `step_up_required`, cierto — pero lo emite en dos
// **ESCRITURAS**: `POST /breakglass` (`modules/governance/breakglass.go:181-189`) y
// `POST /approvals/{id}/decisions` (`approvals.go:572-580`). Y esas dos ya estaban bien
// resueltas antes de tocar nada, porque sus diálogos van por `usePrivilegedMutation`
// (`activate-dialog.tsx:91-99`, `decision-dialog.tsx:72-85`), que identifica el código antes de
// cualquier 403 y conserva las variables para reintentar.
//
// Las ocho ramas que añade esta sesión cuelgan de **GETs** —listas, detalle, rastro— que HOY no
// emiten ese código. Dicho sin adornos: **no había una misclasificación viva en estos ocho
// caminos**, y presentar el cambio como si la hubiera —«la pantalla de acceso de emergencia
// acusaba al operador»— era una afirmación que el motor no respalda.
//
// Entonces, ¿por qué se queda? Porque el defecto es de FORMA y sobrevive al día que alguien
// ponga el gate en una de esas lecturas: `isForbidden` es SÓLO el status (errors.ts:59-61) y la
// ceremonia se reconoce por el CÓDIGO (:77-79), así que el brazo de rol se lo tragaría entero.
// Es una guarda contra una regresión futura, y así se llama aquí. Lo que SÍ era un defecto real
// e independiente del gate está más abajo: el predicado `status === 403` de rutinas.
//
// Y una de las ocho no se podía encontrar buscando `isForbidden`:
// `routine-policies-view.tsx` tenía su PROPIO predicado `status === 403`, que colapsa las dos
// respuestas por construcción. Lo cacé porque el NOMBRE del helper coincidía, no el criterio —
// es el techo nº4 que declaré en la clase `knowledge`, aquí demostrado con un caso real.
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/lib/api/errors'

// El sujeto es QUÉ ESTADO ELIGE la pantalla, no la mecánica del panel (que carga WebAuthn por
// `lazy`). Se sustituye por un marcador y se dice: lo que aquí se fija es la RUTA.
// ⛔ EL DOBLE CAPTURA `onElevated`, y no es un detalle: la primera versión sólo aceptaba
// `action`, así que las celdas habrían seguido verdes con el callback de reintento AUSENTE —
// justo lo que hace que la ceremonia sirva para algo. Lo señaló el contraste.
const elevados: Array<(() => void) | undefined> = []
vi.mock('@/components/layout/step-up-state', () => ({
  StepUpRequiredState: ({
    action,
    onElevated,
  }: {
    action: string
    onElevated?: () => void
  }) => {
    elevados.push(onElevated)
    return <div data-testid="ceremonia">{`ceremonia:${action}`}</div>
  },
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))

const authState = vi.hoisted(() => ({
  activeTenant: 't1' as string | null,
  can: (_p: string): boolean => true,
  principal: { kind: 'user', user_id: 'u1', aal: 1 } as Record<string, unknown>,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => authState }))

const api = vi.hoisted(() => ({
  listBreakGlass: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return {
    ...real,
    governanceApi: { ...(real.governanceApi as object), ...api },
  }
})

import { BreakGlassView } from './break-glass'

const AQUI = dirname(fileURLToPath(import.meta.url))
const ROL = 'isForbidden'
const CEREMONIA = 'isStepUpRequired'
const ACUSACION = /not authorized|forbidden|no tienes/i

/** Un `step_up_required` REAL, con el constructor que usa el cliente de API. */
const ceremonia = () =>
  new ApiError(403, 'step_up_required', 'assurance level too low')
/** Y una negativa de rol de verdad: mismo status, código distinto. */
const rol = () => new ApiError(403, 'forbidden', 'your role cannot do this')

const wrap = (ui: ReactNode) =>
  render(
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      {ui}
    </QueryClientProvider>,
  )

describe('break-glass — la pantalla que para una emergencia ofrece la ceremonia, no una acusación', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    elevados.length = 0
    authState.can = () => true
  })

  it('con `step_up_required` pinta la ceremonia y NO la acusación de rol', async () => {
    // ⛔ ALCANCE DE ESTA CELDA, DECLARADO PORQUE EL CONTRASTE ME LO SEÑALÓ: fuerza el código en
    // la respuesta de un **GET** que el motor NO usa hoy para emitirlo (lo emiten los dos POST
    // de `governance.go:456-475`). No demuestra, por tanto, un camino de producción vivo:
    // demuestra que si ese gate llega a esta lectura, la consola contesta bien. Es una prueba
    // de CONTRATO sobre la forma del error, no una reproducción de un fallo observado.
    api.listBreakGlass.mockRejectedValue(ceremonia())
    wrap(<BreakGlassView />)

    expect(await screen.findByTestId('ceremonia')).toBeInTheDocument()
    // La exclusión importa tanto como la presencia: el defecto que esta campaña arregla en otra
    // pantalla era enseñar las DOS cosas a la vez.
    expect(screen.queryByText(ACUSACION)).toBeNull()
  })

  it('y con un 403 de ROL sigue acusando — no he roto el otro camino', async () => {
    // ⛔ CONTROL NEGATIVO: sin él, «poner la ceremonia delante» se cumpliría igual borrando la
    //    rama de rol, y la pantalla mentiría cuando el operador SÍ carece del permiso.
    api.listBreakGlass.mockRejectedValue(rol())
    wrap(<BreakGlassView />)

    expect(await screen.findByText(ACUSACION)).toBeInTheDocument()
    expect(screen.queryByTestId('ceremonia')).toBeNull()
  })

  it('y `!canRead` sigue mandando ANTES que cualquier respuesta del motor', async () => {
    // ⛔ EL DEFECTO ESPEJO, y por eso es una celda y no un comentario: `can()` es un booleano
    //    del CLIENTE, no una respuesta del motor. Convertirlo en ceremonia ofrecería un
    //    step-up que no arregla nada — el permiso seguiría sin estar — y dejaría al operador
    //    dando vueltas en una ceremonia inútil.
    authState.can = () => false
    api.listBreakGlass.mockRejectedValue(ceremonia())
    wrap(<BreakGlassView />)

    expect(await screen.findByText(ACUSACION)).toBeInTheDocument()
    expect(screen.queryByTestId('ceremonia')).toBeNull()
  })

  it('y la ceremonia trae un reintento que REALMENTE vuelve a consultar', async () => {
    // ⛔ SIN ESTO, la pantalla podría ofrecer la ceremonia y dejar al operador en el mismo sitio
    //    tras completarla. El host ejecuta `retry?.()` (step-up-host.tsx:77-84) y la copy promete
    //    «the action resumes», así que un `onElevated` ausente es una promesa incumplida, no una
    //    omisión inocua. Se comprueba por EFECTO: al invocarlo, la consulta se repite.
    api.listBreakGlass.mockRejectedValue(ceremonia())
    wrap(<BreakGlassView />)
    await screen.findByTestId('ceremonia')

    const onElevated = elevados.at(-1)
    expect(onElevated).toBeTypeOf('function')

    const antes = api.listBreakGlass.mock.calls.length
    expect(antes).toBeGreaterThan(0) // ancla positiva: hubo consulta que repetir
    onElevated?.()
    await vi.waitFor(() =>
      expect(api.listBreakGlass.mock.calls.length).toBeGreaterThan(antes),
    )
  })

  it('los dos errores se distinguen por el CÓDIGO, no por el status', () => {
    expect(ceremonia().status).toBe(403)
    expect(rol().status).toBe(403)
    expect(ceremonia().isForbidden).toBe(true) // ⇦ la trampa entera, en una línea
    expect(ceremonia().isStepUpRequired).toBe(true)
    expect(rol().isStepUpRequired).toBe(false)
  })
})

// --- la guarda de CLASE ------------------------------------------------------

const fuentes = () =>
  readdirSync(AQUI)
    .filter((f) => f.endsWith('.tsx') || f.endsWith('.ts'))
    .filter((f) => !f.includes('.test.'))

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

const primerUso = (src: string, aguja: string) => {
  const lineas = sinComentarios(src)
  for (let i = 0; i < lineas.length; i++) if (lineas[i].includes(aguja)) return i
  return Number.POSITIVE_INFINITY
}

const apariciones = (src: string, aguja: string) =>
  sinComentarios(src).filter((l) => l.includes(aguja)).length

/**
 * ⛔ TECHOS DE ESTA GUARDA, DECLARADOS — los mismos cinco de la clase `knowledge`, y aquí uno
 * de ellos dejó de ser teórico:
 *
 *  1. No ve una SEGUNDA decisión del mismo fichero (compara el primer par). `break-glass.tsx`
 *     tiene CUATRO; por eso hay celdas de comportamiento además de esta guarda.
 *  2. Una condición en UNA SOLA LÍNEA pasa en los dos sentidos: `primerUso` guarda el número de
 *     línea y la guarda sólo culpa cuando ceremonia `>` rol.
 *  3. No despoja literales de cadena.
 *  4. **Sólo conoce estos dos nombres — y en esta clase eso escondió un caso real.**
 *     `routine-policies-view.tsx` tenía `function isForbidden(e) { return e.status === 403 }`:
 *     el criterio no lo habría visto nunca; lo vi porque el NOMBRE coincidía por casualidad.
 *     La celda de abajo cierra esa variante concreta para todo el directorio.
 *  5. `readdirSync(AQUI)` no baja a subdirectorios.
 */
describe('governance — la negativa de ROL nunca se decide antes que la de ASEGURAMIENTO', () => {
  // ⛔ VACÍA A PROPÓSITO, y la celda de abajo es la que lo obligó. `group-members.tsx` estuvo exento
  //    mientras `#753` no aterrizaba; al componer la cola, `#753` entró, el fichero dejó de ser
  //    culpable y el control positivo se puso rojo — `expected 96 to be greater than 103` — exigiendo
  //    borrar la exención en vez de dejarla sobrevivir a su motivo. Es la SEGUNDA vez que esta celda
  //    cobra su promesa en la misma jornada: la hermana de `knowledge` caducó unas horas antes.
  //
  //    Se deja la constante, no la lista: quien vuelva a necesitar una exención escribe aquí el
  //    fichero Y su celda de caducidad. Una exención sin celda no entra.
  const CUBIERTO_POR_OTRO_PR: string[] = []

  it('el criterio distingue los casos, y no lo satisface un comentario', () => {
    const malo = 'const forbidden = e instanceof ApiError && e.isForbidden'
    expect(primerUso(malo, ROL)).toBeLessThan(primerUso(malo, CEREMONIA))

    const trampaBloque = `/* nota\n   que nombra isStepUpRequired sin asterisco\n   y sigue */\nconst f = e.isForbidden`
    expect(primerUso(trampaBloque, CEREMONIA)).toBe(Number.POSITIVE_INFINITY)

    const bueno = `  const s = e.isStepUpRequired\n  const f = !s && e.isForbidden`
    expect(primerUso(bueno, CEREMONIA)).toBeLessThan(primerUso(bueno, ROL))
  })

  it('y ninguna fuente de governance decide el rol sin mencionar antes el aseguramiento', () => {
    const culpables = fuentes()
      .filter((f) => !CUBIERTO_POR_OTRO_PR.includes(f))
      .map((f) => [f, readFileSync(join(AQUI, f), 'utf8')] as const)
      .filter(([, src]) => primerUso(src, ROL) !== Number.POSITIVE_INFINITY)
      .filter(([, src]) => primerUso(src, CEREMONIA) > primerUso(src, ROL))
      .map(([f]) => f)

    expect(culpables).toEqual([])
  })

  it('no hay ninguna exención viva, y si alguien añade una tendrá que darle caducidad', () => {
    // La celda anterior EXPIRÓ y cumplió su función. Lo que queda es la propiedad, no el caso:
    // mientras la lista esté vacía, no hay nada que pueda sobrevivir a su motivo.
    expect(CUBIERTO_POR_OTRO_PR).toEqual([])
  })

  it('⛔ y nadie vuelve a decidir el rol leyendo `status === 403` a pelo', () => {
    // LA VARIANTE QUE LA GUARDA DE TEXTO NO VE POR NOMBRE, cerrada por su propia forma.
    // `routine-policies-view.tsx` la tenía: un predicado local `error.status === 403` que
    // satisface un `step_up_required` sin nombrarlo jamás. Un barrido que busque `isForbidden`
    // puede recorrer el directorio entero y no encontrarla.
    //
    // `ApiError.isForbidden` (errors.ts:59-61) ES esa comparación, así que escribirla a mano no
    // aporta nada y sí reabre el defecto. La única lectura legítima de `.status` aquí sería
    // para OTRO status, no para 403.
    const culpables = fuentes()
      .map((f) => [f, readFileSync(join(AQUI, f), 'utf8')] as const)
      .filter(([, src]) =>
        sinComentarios(src).some((l) => /\.status\s*===\s*403/.test(l)),
      )
      .map(([f]) => f)

    expect(culpables).toEqual([])
  })

  it('y el barrido MIRÓ las DECISIONES, no sólo los ficheros', () => {
    // Contar ficheros deja pasar la pérdida de una de las cuatro decisiones de `break-glass`.
    // El denominador excluye lo que expresamente no cubro: apoyarse en ello no controla nada.
    const decisiones = fuentes()
      .filter((f) => !CUBIERTO_POR_OTRO_PR.includes(f))
      .map((f) => readFileSync(join(AQUI, f), 'utf8'))
      .reduce((n, src) => n + apariciones(src, ROL), 0)
    expect(decisiones).toBeGreaterThanOrEqual(8)
  })
})
