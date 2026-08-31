// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// ⛔ ESTE FICHERO NO EXISTÍA, Y POR ESO EL FALLO SOBREVIVIÓ.
//
// Medido el 2026-08-15 sobre las 128 mutaciones de la consola: `resolveIncident` era la ÚNICA sin
// `onError` y sin lectura de `isError`. El operador pulsaba «resolver», la petición fallaba, la
// fila se quedaba igual y la pantalla no decía nada — indistinguible de «todavía no ha cargado».
// En una vista de INCIDENTES es el peor sitio posible para un fallo mudo: quien la usa se va
// creyendo que ha cerrado algo que sigue abierto.
//
// El caso mide el COMPORTAMIENTO (¿se reporta el fallo?), no la forma de escribirlo, así que
// sobrevive a que mañana se cambie `useFailedActionReporter` por otro mecanismo mientras siga
// habiendo alguno. Y lleva su CONTROL NEGATIVO: el camino de éxito no debe reportar nada, porque
// «reporta siempre» pasaría el caso positivo mintiendo.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { Incidents } from './incidents'
import './i18n'

const mockIncidents = vi.fn()
const mockResolve = vi.fn()
const mockReport = vi.fn()

vi.mock('./api', () => ({
  healthApi: {
    incidents: (...a: unknown[]) => mockIncidents(...a),
    resolveIncident: (...a: unknown[]) => mockResolve(...a),
    publicStatus: vi.fn(),
    connectorHealth: vi.fn(),
    status: vi.fn(),
    sla: vi.fn(),
    dependencies: vi.fn(),
    events: vi.fn(),
  },
  healthKeys: {
    all: (t: string | null) => ['h', t],
    incidents: (t: string | null, p?: unknown) => ['h', t, 'inc', p ?? null],
  },
}))

// El permiso se concede: sin él no se pinta el botón y el caso no mediría nada.
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ can: () => true }),
}))

vi.mock('@/lib/hooks/use-privileged-mutation', () => ({
  useFailedActionReporter: () => mockReport,
}))

function Wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
}

const unIncidente = {
  items: [
    {
      id: 'inc-1',
      subject_ref: 'connector/acme',
      state: 'open',
      severity: 'major',
      opened_at: '2026-08-15T10:00:00Z',
    },
  ],
}

describe('Incidents · resolver un incidente no puede fallar en silencio', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockIncidents.mockResolvedValue(unIncidente)
  })

  it('cuando la petición RECHAZA, el fallo se reporta al operador', async () => {
    mockResolve.mockRejectedValue(new Error('boom'))
    render(<Incidents tenant="t1" />, { wrapper: Wrapper })

    const boton = await screen.findByRole('button', {
      name: /resolve|resolver/i,
    })
    await userEvent.click(boton)

    // Ésta es la aserción que el defecto rompía: sin `onError`, `mockReport` nunca se llamaba.
    await waitFor(() => expect(mockReport).toHaveBeenCalledTimes(1))
    expect(mockResolve).toHaveBeenCalledWith('inc-1')
  })

  it('CONTROL NEGATIVO: cuando la petición tiene ÉXITO no se reporta nada', async () => {
    // Sin este caso, «reporta siempre» pasaría el anterior y la pantalla mentiría al revés.
    mockResolve.mockResolvedValue({})
    render(<Incidents tenant="t1" />, { wrapper: Wrapper })

    const boton = await screen.findByRole('button', {
      name: /resolve|resolver/i,
    })
    await userEvent.click(boton)

    await waitFor(() => expect(mockResolve).toHaveBeenCalledWith('inc-1'))
    expect(mockReport).not.toHaveBeenCalled()
  })
})
