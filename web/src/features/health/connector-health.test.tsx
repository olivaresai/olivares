// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ConnectorHealthResponse } from './types'
import './i18n'

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: 't1',
    can: () => true,
  }),
}))

const mockConnectorHealth = vi.fn()
// ⛔ El doble existe para poder afirmar EN POSITIVO. Mi primera versión de la celda sólo
// negaba la acusación dentro de un `waitFor`, y eso se cumple en el PRIMER TICK —antes de que
// la consulta rechace—: el mutante `const stepUp = false` sobrevivió con las seis verdes. Es
// mi propia regla («una aserción de AUSENCIA dentro de waitFor es vacua») y la repetí.
// ⛔ EL DOBLE EXPONE `onElevated`. Sin él la celda sólo CLASIFICA —ve que la ceremonia
// aparece— y no ve si lleva a alguna parte: `onElevated={undefined}` en el sitio productivo
// dejaría la celda verde. Es el mismo agujero que el contraste `sol max` me encontró cuatro
// veces en esta campaña, y lo destapó aquí un barrido de mis PROPIAS celdas.
vi.mock('@/features/identity/assurance', () => ({
  StepUpPanel: ({ onElevated }: { onElevated?: () => void }) => (
    <div>
      <span>step-up ceremony</span>
      <button type="button" onClick={() => onElevated?.()}>
        elevar
      </button>
    </div>
  ),
}))

vi.mock('./api', () => ({
  healthApi: {
    connectorHealth: (...args: unknown[]) => mockConnectorHealth(...args),
    status: vi.fn(),
    sla: vi.fn(),
    dependencies: vi.fn(),
    incidents: vi.fn(),
    events: vi.fn(),
    resolveIncident: vi.fn(),
    publicStatus: vi.fn(),
  },
  healthKeys: {
    all: (t: string | null) => ['h', t],
    status: (t: string | null, p?: unknown) => ['h', t, 'status', p ?? null],
    sla: (t: string | null, p?: unknown) => ['h', t, 'sla', p ?? null],
    dependencies: (t: string | null) => ['h', t, 'deps'],
    incidents: (t: string | null, p?: unknown) => ['h', t, 'inc', p ?? null],
    events: (t: string | null, p?: unknown) => ['h', t, 'ev', p ?? null],
    connectorHealth: (t: string | null) => ['h', t, 'connHealth'],
    publicStatus: () => ['publicStatus'],
  },
}))

function Wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
}

const sampleResponse: ConnectorHealthResponse = {
  items: [
    {
      name: 'aws-prod',
      kind: 'aws',
      title: 'AWS CloudTrail',
      tenant: 'acme',
      status: 'running',
      source_mode: 'live',
      enabled: true,
      poll_seconds: 300,
      error_count_24h: 0,
      avg_latency_ms: 0,
      trend: 'stable',
      health_state: 'healthy',
    },
    {
      name: 'gcp-staging',
      kind: 'gcp-audit',
      title: 'GCP Audit Logs',
      tenant: 'acme',
      status: 'failed',
      source_mode: 'export',
      enabled: true,
      error_count_24h: 5,
      avg_latency_ms: 0,
      trend: 'down',
      health_state: 'down',
    },
  ],
  summary: {
    total: 2,
    running: 1,
    failed: 1,
    stopped: 0,
    disabled: 0,
  },
  timestamp: '2026-07-01T12:00:00Z',
}

describe('ConnectorHealthTab', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('un step_up_required pinta la CEREMONIA, no la acusación de rol', async () => {
    // ⛔ Lo que ninguna guarda de posición prueba: que la ceremonia SE PINTA. La guarda de
    // clase (`step-up-policy.test.ts`) fija que el rol no se decide primero en los cuatro
    // ficheros; esta celda comprueba que lo que sale a pantalla es la ceremonia y no la
    // acusación —«Health not authorized», la copy exacta de ESTA pantalla
    // (health/i18n/en.json:281)—, que es lo que el operador ve de verdad.
    const { ApiError } = await import('@/lib/api/errors')
    mockConnectorHealth.mockRejectedValue(
      new ApiError(403, 'step_up_required', 'assurance level too low'),
    )
    const { ConnectorHealthTab } = await import('./connector-health')
    render(
      <Wrapper>
        <ConnectorHealthTab tenant="t1" />
      </Wrapper>,
    )

    // Ancla POSITIVA: la ceremonia ESTÁ en pantalla. Sólo entonces la ausencia significa algo.
    expect(await screen.findByText('step-up ceremony')).toBeInTheDocument()
    expect(screen.queryByText('Health not authorized')).not.toBeInTheDocument()

    // Y la SALIDA: elevar reintenta la lectura refusada. Sin esto, una ceremonia cableada a
    // nada dejaría la celda verde.
    mockConnectorHealth.mockResolvedValue(sampleResponse)
    await userEvent.click(screen.getByRole('button', { name: /elevar/i }))
    await waitFor(() => expect(mockConnectorHealth).toHaveBeenCalledTimes(2))
  })

  it('y un 403 SIN código de ceremonia conserva la negativa de ROL', async () => {
    // Control negativo: la negativa de rol es CIERTA y se queda. Sin esta celda, mandar los
    // DOS 403 a la ceremonia también pasaría — el defecto simétrico.
    const { ApiError } = await import('@/lib/api/errors')
    mockConnectorHealth.mockRejectedValue(new ApiError(403, 'forbidden', 'no'))
    const { ConnectorHealthTab } = await import('./connector-health')
    render(
      <Wrapper>
        <ConnectorHealthTab tenant="t1" />
      </Wrapper>,
    )

    // La copy EXACTA de esta pantalla, medida en su bundle (health/i18n/en.json:281): no la
    // genérica de errors.json. Di por hecha la otra y la celda se rompió — con razón.
    expect(await screen.findByText('Health not authorized')).toBeInTheDocument()
  })

  it('renders connector rows when data is available', async () => {
    mockConnectorHealth.mockResolvedValue(sampleResponse)
    const { ConnectorHealthTab } = await import('./connector-health')
    render(
      <Wrapper>
        <ConnectorHealthTab tenant="t1" />
      </Wrapper>,
    )

    await waitFor(() => {
      expect(screen.getByText('AWS CloudTrail')).toBeTruthy()
    })
    expect(screen.getByText('GCP Audit Logs')).toBeTruthy()
    expect(screen.getByText('Live')).toBeTruthy()
    expect(screen.getByText('Export')).toBeTruthy()
  })

  it('filters connector health by source mode', async () => {
    mockConnectorHealth.mockResolvedValue(sampleResponse)
    const { ConnectorHealthTab } = await import('./connector-health')
    render(
      <Wrapper>
        <ConnectorHealthTab tenant="t1" />
      </Wrapper>,
    )

    await waitFor(() => {
      expect(screen.getByText('AWS CloudTrail')).toBeTruthy()
    })
    const user = userEvent.setup()
    await user.click(screen.getByRole('combobox', { name: /mode filter/i }))
    await user.click(screen.getByRole('option', { name: 'Live' }))
    expect(screen.getByText('AWS CloudTrail')).toBeTruthy()
    expect(screen.queryByText('GCP Audit Logs')).toBeNull()
  })

  it('renders summary tiles', async () => {
    mockConnectorHealth.mockResolvedValue(sampleResponse)
    const { ConnectorHealthTab } = await import('./connector-health')
    render(
      <Wrapper>
        <ConnectorHealthTab tenant="t1" />
      </Wrapper>,
    )

    await waitFor(() => {
      expect(screen.getAllByText('Running').length).toBeGreaterThanOrEqual(1)
      expect(screen.getAllByText('Failed').length).toBeGreaterThanOrEqual(1)
    })
  })

  it('renders empty state when no connectors', async () => {
    mockConnectorHealth.mockResolvedValue({
      items: [],
      summary: { total: 0, running: 0, failed: 0, stopped: 0, disabled: 0 },
      timestamp: '2026-07-01T12:00:00Z',
    })
    const { ConnectorHealthTab } = await import('./connector-health')
    render(
      <Wrapper>
        <ConnectorHealthTab tenant="t1" />
      </Wrapper>,
    )

    await waitFor(() => {
      expect(screen.getByText(/no connectors configured/i)).toBeTruthy()
    })
  })
})
