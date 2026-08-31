// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repo root.
//
// ⛔ TESTIGO DE VISTA MONTADA. El de transporte (`api-transport.test.ts`) prueba que el método
// MANDA el techo; éste prueba que la PANTALLA lo pide y que el aviso es ALCANZABLE desde ella.
// No son la misma celda: una sonda de fuente —un grep por `has_more`— habría dado verde con el
// aviso montado en una rama que no se renderiza nunca.
//
// Lo que protege: esta pestaña enseña RECUENTOS derivados de `items.length`. Con el motor
// recortando a 100 y sin aviso, «3 activos» no es una lista incompleta: es una cifra falsa
// presentada como censo.
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

const api = vi.hoisted(() => ({
  schedules: vi.fn(),
  subscriptions: vi.fn(),
  routes: vi.fn(),
  eventTypes: vi.fn(),
  matchTypes: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, automationsApi: api }
})

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

vi.mock('@tanstack/react-router', () => ({
  Link: ({ children }: { children?: ReactNode }) => <a href="#">{children}</a>,
}))

import { AutomationsView } from './automations-view'
import { EVIDENCE_PAGE } from './api'
import './i18n'

function wrap(ui: ReactNode) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const SIN_MAS = { items: [], has_more: false }

beforeEach(() => {
  for (const fn of Object.values(api)) fn.mockReset()
  api.schedules.mockResolvedValue(SIN_MAS)
  api.subscriptions.mockResolvedValue(SIN_MAS)
  api.routes.mockResolvedValue(SIN_MAS)
  api.eventTypes.mockResolvedValue({ event_types: [] })
  api.matchTypes.mockResolvedValue({ match_types: [] })
})

describe('AutomationsView — el recorte se declara, no se calla', () => {
  it('la pantalla pide el techo en los TRES raíles', async () => {
    wrap(<AutomationsView />)
    await waitFor(() => expect(api.schedules).toHaveBeenCalled())
    for (const fn of [api.schedules, api.subscriptions, api.routes]) {
      expect(fn).toHaveBeenCalledWith({ limit: EVIDENCE_PAGE })
    }
    // Y los catálogos NO piden techo: sus handlers no paginan.
    //
    // ⚠ No se comprueba con `toHaveBeenCalledWith()`. Estos dos se pasan a `queryFn` COMO
    // FUNCIÓN, así que react-query los invoca con su `QueryFunctionContext` — reciben un
    // argumento, sólo que no es nuestro. Ésa es justamente la razón de que los tres raíles que
    // sí paginan vayan envueltos en una lambda: sin ella, el «techo» que llegaría al cliente
    // sería el contexto del propio react-query.
    for (const fn of [api.eventTypes, api.matchTypes]) {
      expect(fn.mock.calls[0]?.[0] ?? {}).not.toHaveProperty('limit')
    }
  })

  it('con has_more el aviso SALE, y es alcanzable desde la vista montada', async () => {
    api.schedules.mockResolvedValue({
      items: [{ id: 's1', health: 'active' }],
      has_more: true,
    })
    wrap(<AutomationsView />)
    const avisos = await screen.findAllByText(/there are more|hay más/i)
    expect(avisos.length).toBeGreaterThan(0)
  })

  // ⛔ F-01 del contraste: declarar el recorte y seguir llamando «total» a la página es PEOR que
  // no declararlo — las dos afirmaciones juntas parecen una sola verificada. Con 1001 filas el
  // motor devuelve 1000 y marca has_more, y la tarjeta decía «hay más» y «1000 total».
  it('con has_more la cifra NO se llama «total»', async () => {
    api.schedules.mockResolvedValue({
      items: [{ id: 's1', health: 'active' }],
      has_more: true,
    })
    wrap(<AutomationsView />)
    await screen.findAllByText(/there are more|hay más/i)
    // ⛔ Anclado a la FORMA de la cifra (`^N total$`), no a la palabra suelta: el propio aviso
    // dice «Loaded N rows; there are more», así que un /loaded/ a secas casaba también con el
    // badge y contaba dos donde hay una. Medir la palabra no es medir la cifra.
    // Se cuenta, no se busca «ninguno»: hay TRES raíles y sólo uno está recortado. Los otros dos
    // siguen diciendo «0 total» con razón — su cifra sí es un total. Un `queryByText` sobre toda
    // la pantalla mediría los tres juntos y por eso reventaba con «found multiple».
    expect(screen.getAllByText(/^\d+ total$/i)).toHaveLength(2)
    expect(screen.getAllByText(/^\d+ loaded$/i)).toHaveLength(1)
  })

  it('sin has_more la cifra SÍ es un total, que es lo que la hace informativa', async () => {
    api.schedules.mockResolvedValue({
      items: [{ id: 's1', health: 'active' }],
      has_more: false,
    })
    wrap(<AutomationsView />)
    // ⛔ Se espera al ELEMENTO, no a la llamada. `waitFor(...toHaveBeenCalled())` sólo prueba que
    // la petición salió: la cifra aún no está pintada y el `getAllByText` mide una pantalla a
    // medio montar. Esperar la primera cifra sincroniza con el render, no con el transporte.
    await screen.findAllByText(/^\d+ total$/i)
    // Los tres: ninguno recortado, ninguna cifra degradada.
    expect(screen.getAllByText(/^\d+ total$/i)).toHaveLength(3)
    expect(screen.queryByText(/^\d+ loaded$/i)).toBeNull()
  })

  it('sin has_more NO sale: un aviso que sale siempre no declara nada', async () => {
    wrap(<AutomationsView />)
    await waitFor(() => expect(api.schedules).toHaveBeenCalled())
    expect(screen.queryByText(/there are more|hay más/i)).toBeNull()
  })
})
