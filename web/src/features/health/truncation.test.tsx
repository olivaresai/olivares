// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Las dos listas de salud DECLARAN su propio recorte. Lo que vigilan estos casos no es que
// pinten filas, sino la afirmación que hacían por omisión: sin `limit` el motor pagina a
// 100 y contesta `has_more: true` que nadie miraba, así que la pantalla enseñaba las
// primeras cien y NO decía que hubiera más. En una vista de incidentes eso se lee como «no
// hay más incidentes abiertos», que es exactamente la frase con la que alguien se va a
// dormir.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { EventDTO, IncidentDTO, StatusDTO } from './types'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

const api = vi.hoisted(() => ({ incidents: vi.fn(), events: vi.fn() }))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, healthApi: { ...actual.healthApi, ...api } }
})

import { Incidents } from './incidents'
import { ReliabilityTimeline } from './reliability-timeline'
import './i18n'

const incidente: IncidentDTO = {
  id: 'inc-1',
  subject_kind: 'agent',
  subject_ref: 'agent-prod',
  kind: 'down',
  severity: 'high',
  state: 'open',
  opened_at: '2026-08-20T10:00:00Z',
  summary: 'no heartbeat',
}

const evento: EventDTO = {
  id: 'ev-1',
  subject_kind: 'agent',
  subject_ref: 'agent-prod',
  prev_state: 'up',
  state: 'down',
  cause: 'missed_heartbeat',
  latency_ms: -1,
  occurred_at: '2026-08-20T10:00:00Z',
}

const sujeto: StatusDTO = {
  id: 'st-1',
  name: 'prod',
  subject_kind: 'agent',
  subject_ref: 'agent-prod',
  state: 'down',
  desired_status: 'active',
  expected_interval_seconds: 300,
  grace_factor: 2,
} as StatusDTO

function pinta(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return {
    qc,
    ...render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>),
  }
}

beforeEach(() => {
  vi.clearAllMocks()
  api.incidents.mockResolvedValue({ items: [incidente], has_more: false })
  api.events.mockResolvedValue({ items: [evento], has_more: false })
})

describe('Incidents — la lista dice cuánto NO está viendo', () => {
  it('pide el techo real del motor, no el de por defecto', async () => {
    pinta(<Incidents tenant="t1" />)
    await waitFor(() => expect(api.incidents).toHaveBeenCalled())
    // FIRES IF: alguien quita el `limit` — el motor pagina a 100 en silencio.
    expect(api.incidents).toHaveBeenCalledWith({ state: 'open', limit: 1000 })
  })

  it('DECLARA el recorte cuando el motor dice que hay más, con la cifra', async () => {
    api.incidents.mockResolvedValue({ items: [incidente], has_more: true })
    pinta(<Incidents tenant="t1" />)
    const aviso = await screen.findByText(/Showing the first/i)
    expect(aviso.textContent).toMatch(/1000/)
    expect(aviso.textContent).not.toMatch(/\b100\b/)
    // Y el aviso dice lo que de verdad importa: que el resto vacío NO prueba nada.
    expect(aviso.getAttribute('title') ?? '').toMatch(/NOT proof/i)
  })

  it('y NO lo declara cuando no lo hay — el contrafactual en la otra dirección', async () => {
    pinta(<Incidents tenant="t1" />)
    expect(await screen.findByText('agent-prod')).toBeInTheDocument()
    expect(screen.queryByText(/Showing the first/i)).not.toBeInTheDocument()
  })
})

describe('Incidents — el aviso no sobrevive a un error posterior', () => {
  it('con datos truncados y un refetch fallido, el aviso NO se queda flotando', async () => {
    api.incidents.mockResolvedValue({ items: [incidente], has_more: true })
    const { qc } = pinta(<Incidents tenant="t1" />)
    expect(await screen.findByText(/Showing the first/i)).toBeInTheDocument()

    // El refetch falla: TanStack conserva la `data` anterior junto al error nuevo, y la
    // tabla pasa a enseñar sólo el error. Un aviso de recorte sobre una tabla vacía habla
    // de datos que ya no están a la vista.
    api.incidents.mockRejectedValue(new Error('boom'))
    await qc.refetchQueries()
    await waitFor(() =>
      expect(screen.queryByText(/Showing the first/i)).not.toBeInTheDocument(),
    )
  })
})

describe('ReliabilityTimeline — una línea cortada por arriba no es «no pasó nada»', () => {
  const sel = { subject_kind: 'agent', subject_ref: 'agent-prod' } as const

  it('pide el techo real del motor', async () => {
    pinta(
      <ReliabilityTimeline
        tenant="t1"
        subjects={[sujeto]}
        selected={sel}
        onSelect={() => {}}
      />,
    )
    await waitFor(() => expect(api.events).toHaveBeenCalled())
    expect(api.events).toHaveBeenCalledWith({
      subject_kind: 'agent',
      subject_ref: 'agent-prod',
      limit: 1000,
    })
  })

  it('DECLARA el recorte, y NO lo declara cuando no lo hay', async () => {
    api.events.mockResolvedValue({ items: [evento], has_more: true })
    const { unmount } = pinta(
      <ReliabilityTimeline
        tenant="t1"
        subjects={[sujeto]}
        selected={sel}
        onSelect={() => {}}
      />,
    )
    const aviso = await screen.findByText(/Showing the first/i)
    // FIRES IF: la cifra miente aquí también. El caso de incidentes lo exigía y éste no,
    // así que el mutante simétrico ESCAPABA — lo devolvió el contraste.
    expect(aviso.textContent).toMatch(/1000/)
    expect(aviso.textContent).not.toMatch(/\b100\b/)
    const title = aviso.getAttribute('title') ?? ''
    expect(title).toMatch(/not an event that did not happen/i)
    // ⛔ Y LA DIRECCIÓN EN EL TIEMPO, que es lo que el contraste refutó: la primera página
    //    va por `id ASC` (UUIDv7), así que lo que se corta es lo MÁS RECIENTE. El texto
    //    anterior decía «un tramo ANTERIOR», que apunta justo al revés.
    expect(title).toMatch(/most recent/i)
    expect(title).not.toMatch(/earlier stretch/i)
    unmount()

    api.events.mockResolvedValue({ items: [evento], has_more: false })
    pinta(
      <ReliabilityTimeline
        tenant="t1"
        subjects={[sujeto]}
        selected={sel}
        onSelect={() => {}}
      />,
    )
    await waitFor(() => expect(api.events).toHaveBeenCalledTimes(2))
    expect(screen.queryByText(/Showing the first/i)).not.toBeInTheDocument()
  })
})
