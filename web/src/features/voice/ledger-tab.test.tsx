// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// El ledger de aperturas en la consola. Lo que estos testigos vigilan NO es que la tabla
// pinte filas —eso lo hace cualquier tabla— sino las tres afirmaciones que esta pantalla
// hace y que, si se rompen, mienten en la dirección cara:
//
//   1. una DENEGACIÓN no se pinta como estado tranquilo,
//   2. un ledger RECORTADO se declara recortado (o afirmaría que no hubo más rechazos),
//   3. la caché del ledger del tenant no es la de UNA sesión (o pintaría las decisiones
//      de una sesión como si fueran las del estate).
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import { renderIntel, userEvent } from '@/test/intel'
import '@/features/_intel'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 'demo', can: () => true }),
}))

const api = vi.hoisted(() => ({
  sessions: vi.fn(),
  policies: vi.fn(),
  allDecisions: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, voiceApi: { ...actual.voiceApi, ...api } }
})

import { DecisionsTable } from './components'
import { decisionsFixture } from './fixtures'
import { voiceKeys } from './api'
import { VoiceView } from './voice-view'
import type { VoiceDecision } from './types'
import './i18n'

const EMPTY = { items: [], has_more: false }

beforeEach(() => {
  vi.clearAllMocks()
  api.sessions.mockResolvedValue(EMPTY)
  api.policies.mockResolvedValue(EMPTY)
  api.allDecisions.mockResolvedValue({
    items: decisionsFixture,
    has_more: false,
  })
})

describe('DecisionsTable — una denegación no se pinta tranquila', () => {
  it('da a las tres denegaciones un variante de alarma, nunca el neutro', () => {
    renderIntel(<DecisionsTable decisions={decisionsFixture} />)
    const table = screen.getByRole('grid')

    // FIRES IF: alguien delega en `StatusBadge`, cuyo mapa NO conoce `blocked`,
    // `budget_blocked` ni `budget_throttled` y cae a `neutral` para las tres.
    const blocked = within(table).getByText('Blocked')
    expect(blocked.className).toMatch(/bg-danger-soft/)
    expect(blocked.className).not.toMatch(/bg-muted/)

    const budget = within(table).getByText('Blocked — budget')
    expect(budget.className).toMatch(/bg-danger-soft/)

    // Una limitación no es un bloqueo: el COLOR los distingue, y el TEXTO dice igual
    // de claro que la apertura no llegó a abrirse. Las dos cosas, no una.
    const throttled = within(table).getByText('Open denied — budget throttle')
    expect(throttled.className).toMatch(/bg-warning-soft/)
    expect(throttled.className).not.toMatch(/bg-danger-soft/)

    // ⛔ LOS TRES DESENLACES QUE NO TENÍAN TESTIGO. El más caro es `failed`: pintado en
    //    verde, un fallo del despachador se lee como una apertura que funcionó.
    const failed = within(table).getByText('Failed')
    expect(failed.className).toMatch(/bg-danger-soft/)
    expect(failed.className).not.toMatch(/bg-success-soft/)
    expect(within(table).getByText('Declared, not opened').className).toMatch(
      /bg-warning-soft/,
    )
    expect(within(table).getByText('Approval requested').className).toMatch(
      /bg-info-soft/,
    )

    // Control NEGATIVO en la misma tabla: la que SÍ salió bien va en verde, así que
    // el test no está comprobando «todo es rojo».
    const ok = within(table).getByText('Dispatched')
    expect(ok.className).toMatch(/bg-success-soft/)
  })

  it('muestra el literal crudo de un op_status que el motor añada mañana', () => {
    const futuro: VoiceDecision[] = [
      { ...decisionsFixture[1], id: 'vd-9999', op_status: 'quarantined' },
    ]
    renderIntel(<DecisionsTable decisions={futuro} />)
    const table = screen.getByRole('grid')
    // Ni blanco ni un color inventado: el valor tal cual, en neutro.
    const badge = within(table).getByText('quarantined')
    expect(badge.className).toMatch(/bg-muted/)
  })

  it('NO afirma que la sesión de una denegación no exista', () => {
    // Se añade una fila con el ref VACÍO: sin ella la columna nunca toma la rama del
    // caso ausente y el caso mediría solo las cuatro filas que sí lo traen.
    const conVacio: VoiceDecision[] = [
      ...decisionsFixture,
      { ...decisionsFixture[1], id: 'vd-0005', session_ref: '' },
    ]
    renderIntel(<DecisionsTable decisions={conVacio} />)
    const table = screen.getByRole('grid')
    // El ref se muestra; lo que no se hace es cruzarlo contra una lista de sesiones
    // que llega recortada por defecto y concluir una ausencia.
    expect(within(table).getByText('sess-ghost-77')).toBeInTheDocument()
    const body = table.textContent ?? ''
    expect(body).not.toMatch(/no session|sin sesión|not found/i)
  })

  it('trae el motivo que redacta el MOTOR, no una glosa de la consola', () => {
    renderIntel(<DecisionsTable decisions={decisionsFixture} />)
    const table = screen.getByRole('grid')
    expect(
      within(table).getByText('no allowing voice policy'),
    ).toBeInTheDocument()
    expect(
      within(table).getByText('open denied: budget cap reached'),
    ).toBeInTheDocument()
  })

  it('con cero filas dice que está vacío, no pinta una tabla en blanco', () => {
    renderIntel(<DecisionsTable decisions={[]} />)
    expect(screen.getByText(/No open decisions yet/i)).toBeInTheDocument()
  })
})

describe('VoiceView — la pestaña del ledger', () => {
  // La pestaña no es la de por defecto, así que hay que abrirla: `TabsContent` no monta
  // lo que no está activo. Si esto se olvida, los asertos de abajo pasarían a medir «no
  // aparece» por una razón que no es la suya.
  async function abrirLedger() {
    const user = userEvent.setup()
    await user.click(await screen.findByRole('tab', { name: 'Decisions' }))
  }

  it('pide el ledger del tenant CON límite y pinta la denegación sin sesión', async () => {
    renderIntel(<VoiceView />)
    await waitFor(() => expect(api.allDecisions).toHaveBeenCalled())
    // FIRES IF: alguien quita el `limit` — el motor pagina a 100 en silencio.
    expect(api.allDecisions).toHaveBeenCalledWith({ limit: 1000 })
    await abrirLedger()
    expect(await screen.findByText('sess-ghost-77')).toBeInTheDocument()
  })

  it('DECLARA el recorte cuando el motor dice que hay más', async () => {
    api.allDecisions.mockResolvedValue({
      items: decisionsFixture,
      has_more: true,
    })
    renderIntel(<VoiceView />)
    await abrirLedger()
    // Se comprueba la CIFRA, no solo la palabra: un aviso que dice «las 100 más
    // recientes» cuando se pidieron 1000 miente sobre cuánto se está viendo.
    const aviso = await screen.findByText(/TRUNCATED/i)
    expect(aviso.textContent).toMatch(/1000/)
    expect(aviso.textContent).not.toMatch(/\b100\b/)
  })

  it('y NO lo declara cuando no lo hay — el contrafactual en la otra dirección', async () => {
    renderIntel(<VoiceView />)
    await abrirLedger()
    expect(await screen.findByText('sess-ghost-77')).toBeInTheDocument()
    expect(screen.queryByText(/TRUNCATED/i)).not.toBeInTheDocument()
  })

  it('la clave del ledger no puede coincidir con la de una sesión llamada `all`', () => {
    // ⛔ ESTE CASO ES DE LA FÁBRICA DE CLAVES, NO DE LA PANTALLA, y el cambio no es de
    //    comodidad: la versión de render de este mismo caso ESCAPABA al mutante.
    //    Sembrando `decisions(t,'all')` y mirando la tabla, las claves comparadas eran
    //    `[...,'decisions','all',{limit}]` (5 segmentos, porque la vista SIEMPRE pasa
    //    params) contra `[...,'decisions','all']` (4): distintas incluso CON el defecto,
    //    así que el caso pasaba en las dos direcciones y no medía nada.
    //
    //    La colisión vive en la rama SIN params de la fábrica, y ahí es donde se mira.
    expect(voiceKeys.ledger('t1')).not.toEqual(voiceKeys.decisions('t1', 'all'))
    expect(voiceKeys.ledger('t1', { limit: 10 })).not.toEqual(
      voiceKeys.decisions('t1', 'all'),
    )
    // Control POSITIVO de que la comparación discrimina: dos llamadas iguales SÍ dan
    // la misma clave, así que `not.toEqual` no está pasando por comparar mal.
    expect(voiceKeys.ledger('t1')).toEqual(voiceKeys.ledger('t1'))
    // ⛔ Y EL TENANT. Sin este aserto, quitar el tenant de la clave —dos organizaciones
    //    compartiendo la entrada de caché del ledger— no mataba a nadie.
    expect(voiceKeys.ledger('t1')).not.toEqual(voiceKeys.ledger('t2'))
    expect(voiceKeys.ledger('t1', { limit: 10 })).not.toEqual(
      voiceKeys.ledger('t2', { limit: 10 }),
    )
  })
})
