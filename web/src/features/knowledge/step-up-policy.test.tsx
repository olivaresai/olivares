// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ LA CLASE `knowledge`, QUE ES DE LECTURA CON UNA ESCRITURA DENTRO. SIETE decisiones en cinco
// ficheros —seis de lectura y la del diálogo de consulta, que es la que peor fallaba; esta
// cabecera decía «seis» porque me dejé fuera precisamente esa—
// leían `isForbidden` —que es SÓLO el status 403 (lib/api/errors.ts:59-61)— sin saber
// que un `step_up_required` viaja TAMBIÉN como 403 y se reconoce por el CÓDIGO
// (`isStepUpRequired`, :77-79). Consecuencia medida: la base de conocimiento, el producto de
// datos, el prompt, sus revisiones y la lista de memoria se sustituían por «no tienes
// autorización, pide acceso a un administrador» —falso y sin salida—, y el diálogo de consulta
// pintaba «denegado por …» A LA VEZ que se abría la ceremonia que resuelve el problema.
//
// Dos aserciones distintas, porque los dos lados fallan distinto:
//   · LECTURA  — la pantalla elige la CEREMONIA, no la acusación (celda de comportamiento).
//   · CLASE    — ningún fichero del directorio decide el rol sin conocer antes el aseguramiento
//                (barrido por POSICIÓN: dos de los seis sitios estaban en el MISMO fichero que
//                otro ya correcto, así que «el fichero lo menciona» no distingue nada).
import { readFileSync, readdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import type { ReactNode } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError } from '@/lib/api/errors'
import type { KbDTO } from './types'

// La ceremonia real carga WebAuthn y el bundle i18n de identidad por `lazy`. El sujeto de esta
// celda es QUÉ ESTADO ELIGE la pantalla, no la mecánica del panel, así que se sustituye por un
// marcador. Lo digo en vez de fingir que pruebo la ceremonia entera: lo que aquí se fija es la
// RUTA, y la ruta es justo lo que estaba mal.
vi.mock('@/components/layout/step-up-state', () => ({
  StepUpRequiredState: ({ action }: { action: string }) => (
    <div data-testid="ceremonia">{`ceremonia:${action}`}</div>
  ),
}))
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@/components/ui/toaster', () => ({
  toast: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  Toaster: () => null,
}))

const api = vi.hoisted(() => ({
  getKb: vi.fn(),
  listDocuments: vi.fn(),
  listLineage: vi.fn(),
}))
vi.mock('./api', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return { ...real, knowledgeApi: { ...(real.knowledgeApi as object), ...api } }
})

import { MemoryList } from './memory-list'
import { KbDetailSheet } from './kb-detail'

const AQUI = dirname(fileURLToPath(import.meta.url))
const ROL = 'isForbidden'
const CEREMONIA = 'isStepUpRequired'

/** Un `step_up_required` REAL: mismo constructor que usa el cliente de API. */
const ceremonia = () =>
  new ApiError(403, 'step_up_required', 'assurance level too low')
/** Y una denegación de rol de verdad: mismo status, código distinto. */
const rol = () => new ApiError(403, 'forbidden', 'your role cannot read this')

const lista = (error: unknown) =>
  render(
    <MemoryList
      entries={[]}
      isLoading={false}
      error={error}
      onRetry={() => {}}
      canWrite={false}
    />,
  )

describe('knowledge — una lectura gateada por aseguramiento ofrece la ceremonia, no una acusación', () => {
  // La acusación se reconoce por su COPY, que es la convención del fichero de al lado
  // (knowledge.test.tsx:217). Un `data-testid` no existe en `ForbiddenState` y añadirlo sólo
  // para esta celda sería cambiar el sujeto para que quepa en la prueba.
  const ACUSACION = /not authorized|forbidden/i

  it('con `step_up_required` pinta la ceremonia y NO la acusación de rol', () => {
    lista(ceremonia())
    expect(screen.getByTestId('ceremonia')).toBeInTheDocument()
    // ⛔ La mitad que de verdad falla si alguien invierte el orden otra vez. Sin ella, la celda
    //    seguiría verde con AMBAS cosas pintadas — que es exactamente el defecto del diálogo de
    //    consulta: denegación y salida a la vez.
    expect(screen.queryByText(ACUSACION)).toBeNull()
  })

  it('y con un 403 de ROL sigue pintando la acusación — no he roto el otro camino', () => {
    // ⛔ CONTROL NEGATIVO, y no es ceremonia: sin él, «mover la ceremonia delante» se cumpliría
    //    igual borrando la rama de rol, y la pantalla dejaría de decir la verdad cuando el
    //    operador SÍ carece del permiso.
    lista(rol())
    expect(screen.queryByTestId('ceremonia')).toBeNull()
    expect(screen.getByText(ACUSACION)).toBeInTheDocument()
  })

  it('y los dos errores se distinguen por el CÓDIGO, no por el status', () => {
    // El porqué de todo lo anterior, fijado: si algún día `isForbidden` dejara de ser cierto
    // para un step-up, estas celdas pasarían por un motivo distinto del que creo.
    expect(ceremonia().status).toBe(403)
    expect(rol().status).toBe(403)
    expect(ceremonia().isForbidden).toBe(true) // ⇦ la trampa entera, en una línea
    expect(ceremonia().isStepUpRequired).toBe(true)
    expect(rol().isStepUpRequired).toBe(false)
  })
})

// --- la SEGUNDA decisión del mismo fichero -----------------------------------
//
// ⛔ ESTA CELDA EXISTE PORQUE UN MUTANTE SOBREVIVIÓ, y sobrevivió por un motivo que la guarda de
// clase DECLARA no poder ver: compara la PRIMERA ceremonia con el PRIMER rol, así que en un
// fichero cuya primera decisión ya es correcta, invertir una SEGUNDA le pasa por debajo. Lo
// medí: cambiar `docsError.isStepUpRequired` en kb-detail dejó las 5 celdas en verde.
//
// Intentar cerrarlo endureciendo el texto ya falló antes en `health` —comparar por ocurrencia es
// idéntico al criterio global cuando la ceremonia está arriba—, así que se cierra por
// COMPORTAMIENTO: se conduce el componente REAL por la costura real, con `getKb` respondiendo y
// la lista de documentos gateada. No exporto `DetailBody` para llegar antes: cambiar la
// superficie del módulo para que quepa en la prueba es mover el sujeto, no medirlo.

const kbFixture: KbDTO = {
  id: 'kb1',
  name: 'engineering-handbook',
  classification: 'confidential',
  residency_region: 'eu',
  embed_policy: 'local_only',
  embed_model: 'local-hash',
  dim: 256,
  default_acl: ['group:eng', 'role:reviewer'],
  status: 'active',
  doc_count: 12,
  chunk_count: 340,
}

const wrap = (ui: ReactNode) =>
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      {ui}
    </QueryClientProvider>,
  )

describe('knowledge — la lista de documentos se gatea aparte de la base que la contiene', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.getKb.mockResolvedValue(kbFixture)
    api.listLineage.mockResolvedValue({ items: [] })
  })

  it('un `step_up_required` en los DOCUMENTOS ofrece la ceremonia, con la base ya cargada', async () => {
    api.listDocuments.mockRejectedValue(ceremonia())
    wrap(<KbDetailSheet kbId="kb1" open onOpenChange={() => {}} />)

    // Anclaje POSITIVO primero: si la base no cargó, lo de abajo no habla de la segunda
    // decisión sino de la primera, y la celda mediría otra cosa.
    expect(await screen.findByText(/engineering-handbook/)).toBeInTheDocument()
    expect(await screen.findByTestId('ceremonia')).toBeInTheDocument()
    // ⛔ Y LA EXCLUSIÓN, que faltaba: sin ella esta celda quedaría verde si un cambio pintara
    //    ceremonia Y acusación a la vez — que es EXACTAMENTE el defecto del diálogo de consulta
    //    que esta misma sesión arregla. Hoy no puede pasar porque el ternario es excluyente
    //    (kb-detail.tsx:336-341), pero eso es una propiedad del árbol de hoy, no de la celda.
    //    Lo señaló el contraste Codex sol max, y tenía razón.
    expect(screen.queryByText(/not authorized|forbidden/i)).toBeNull()
  })

  it('y un 403 de ROL en los documentos sigue acusando — el otro camino sigue vivo', async () => {
    api.listDocuments.mockRejectedValue(rol())
    wrap(<KbDetailSheet kbId="kb1" open onOpenChange={() => {}} />)

    expect(await screen.findByText(/engineering-handbook/)).toBeInTheDocument()
    expect(
      await screen.findByText(/not authorized|forbidden/i),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('ceremonia')).toBeNull()
  })
})

// --- la guarda de CLASE ------------------------------------------------------

const fuentes = () =>
  readdirSync(AQUI)
    .filter((f) => f.endsWith('.tsx') || f.endsWith('.ts'))
    .filter((f) => !f.includes('.test.'))

/**
 * Primera línea con un uso REAL de `aguja`, ignorando comentarios.
 *
 * ⛔ El despojado mira TAMBIÉN las líneas de continuación de un bloque, no sólo las que empiezan
 * por `//` o `*`. Mi primer barrido de este residuo sólo miraba el arranque de línea y declaró
 * culpable a `access-map-view.tsx`, que hace la ceremonia PRIMERO: la mención vivía en una línea
 * intermedia de un comentario largo. Un criterio que confunde prosa con código inventa trabajo y
 * desacredita al barrido entero.
 *
 * ⛔ TECHOS DE ESTA GUARDA, TODOS DECLARADOS. El primero ya lo sabía; los cuatro siguientes los
 * encontró el contraste Codex `sol max` y los escribo porque una guarda que promete un alcance
 * que no tiene es peor que no tenerla:
 *
 *  1. **No ve una SEGUNDA decisión en el mismo fichero.** Compara la primera ceremonia con el
 *     primer rol, así que con la ceremonia arriba, invertir una decisión posterior le pasa por
 *     debajo. Medido con un mutante en `kb-detail`: las cinco celdas iniciales siguieron verdes.
 *     Por eso existen las celdas de comportamiento de la lista de documentos. Endurecerlo
 *     comparando POR OCURRENCIA ya falló en `health`: con la ceremonia arriba, toda ocurrencia
 *     posterior la tiene «antes», y el criterio vuelve a ser el global.
 *  2. **Una condición en UNA SOLA LÍNEA pasa en los dos sentidos.** `primerUso` guarda el número
 *     de línea, así que `e.isForbidden || e.isStepUpRequired` y su inverso dan la MISMA posición
 *     y la guarda sólo culpa cuando ceremonia `>` rol. Una guarda de línea no ordena dentro de
 *     la línea.
 *  3. **No despoja literales de cadena.** Un `'isStepUpRequired'` dentro de una cadena, o un uso
 *     no relacionado, satisface el barrido igual que el de verdad.
 *  4. **Sólo conoce estos dos nombres.** Una decisión equivalente escrita como `status === 403`
 *     y `code === 'step_up_required'` es invisible para esta guarda.
 *  5. **`readdirSync(AQUI)` no baja a subdirectorios.** Si `knowledge/` crece con carpetas, sus
 *     ficheros quedan fuera del barrido sin que nada avise.
 */
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
  for (let i = 0; i < lineas.length; i++)
    if (lineas[i].includes(aguja)) return i
  return Number.POSITIVE_INFINITY
}

/** Cuántas DECISIONES usan la aguja — líneas, no ficheros. */
const apariciones = (src: string, aguja: string) =>
  sinComentarios(src).filter((l) => l.includes(aguja)).length

describe('knowledge — la negativa de ROL nunca se decide antes que la de ASEGURAMIENTO', () => {
  it('el criterio distingue los casos, y no lo satisface un comentario', () => {
    const malo = 'const forbidden = e instanceof ApiError && e.isForbidden'
    expect(primerUso(malo, ROL)).toBeLessThan(primerUso(malo, CEREMONIA))

    const trampaLinea = `  // la ceremonia (isStepUpRequired) iría aquí\n  const f = e.isForbidden`
    expect(primerUso(trampaLinea, CEREMONIA)).toBe(Number.POSITIVE_INFINITY)

    // ⛔ EL FALSO POSITIVO QUE ME COMÍ, como caso: continuación de bloque.
    const trampaBloque = `/* nota\n   que nombra isStepUpRequired sin asterisco\n   y sigue */\nconst f = e.isForbidden`
    expect(primerUso(trampaBloque, CEREMONIA)).toBe(Number.POSITIVE_INFINITY)
    expect(primerUso(trampaBloque, ROL)).toBeLessThan(Number.POSITIVE_INFINITY)

    const bueno = `  const s = e.isStepUpRequired\n  const f = !s && e.isForbidden`
    expect(primerUso(bueno, CEREMONIA)).toBeLessThan(primerUso(bueno, ROL))
  })

  // ⛔ VACÍA A PROPÓSITO, y la celda de abajo es la que lo obligó. `lineage-detail.tsx` estuvo
  //    exento mientras `#753` no aterrizaba; al componer la cola, `#753` entró, el fichero dejó de
  //    ser culpable y el control positivo se puso rojo — `expected 79 to be greater than 86` —
  //    exigiendo borrar la exención en vez de dejarla sobrevivir a su motivo.
  //
  //    Se deja la constante, no la lista: quien vuelva a necesitar una exención escribe aquí el
  //    fichero Y su celda de caducidad, que es el trato. Una exención sin celda no entra.
  const CUBIERTO_POR_OTRO_PR: string[] = []

  it('y ninguna fuente de knowledge decide el rol sin mencionar antes el aseguramiento', () => {
    // ⚠ EXCEPCIÓN CON NOMBRE Y PR: `lineage-detail.tsx` lo arregla el #753 (rama). Tocarlo
    //   aquí fabricaría un conflicto en un fichero que ya tiene dueño.
    const culpables = fuentes()
      .filter((f) => !CUBIERTO_POR_OTRO_PR.includes(f))
      .map((f) => [f, readFileSync(join(AQUI, f), 'utf8')] as const)
      .filter(([, src]) => primerUso(src, ROL) !== Number.POSITIVE_INFINITY)
      .filter(([, src]) => primerUso(src, CEREMONIA) > primerUso(src, ROL))
      .map(([f]) => f)

    expect(culpables).toEqual([])
  })

  it('no hay ninguna exención viva, y si alguien añade una tendrá que darle caducidad', () => {
    // La celda anterior EXPIRÓ y cumplió su función: `#753` arregló `lineage-detail.tsx`, el
    // control positivo enrojeció y la exención se borró. Lo que queda aquí es la propiedad, no
    // el caso: mientras la lista esté vacía, no hay nada que pueda sobrevivir a su motivo.
    expect(CUBIERTO_POR_OTRO_PR).toEqual([])
  })

  it('y el barrido MIRÓ las DECISIONES, no sólo los ficheros', () => {
    // ⛔ CORREGIDO TRAS EL CONTRASTE, en dos cosas:
    //    (a) contaba FICHEROS, no decisiones, así que perder una de las dos de `kb-detail` o de
    //        `prompt-detail` no bajaba el número y el `toEqual([])` seguía pareciendo un cero
    //        medido;
    //    (b) NO aplicaba la exclusión de lineage al denominador, así que el fichero que
    //        expresamente no cubro ayudaba a alcanzar el mínimo. Un control anti-vacuidad que
    //        se apoya en lo excluido no controla nada.
    //
    //    Y de paso: son SIETE decisiones, no seis. La cabecera de este fichero decía seis
    //    porque conté los seis sitios de LECTURA y me dejé fuera el de escritura del diálogo
    //    de consulta, que es justo el que peor fallaba.
    const decisiones = fuentes()
      .filter((f) => !CUBIERTO_POR_OTRO_PR.includes(f))
      .map((f) => readFileSync(join(AQUI, f), 'utf8'))
      .reduce((n, src) => n + apariciones(src, ROL), 0)
    expect(decisiones).toBeGreaterThanOrEqual(7)
  })
})
