// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { PublicStatusResponse } from './types'
import './i18n'

const mockPublicStatus = vi.fn()
vi.mock('./api', () => ({
  healthApi: {
    publicStatus: (...args: unknown[]) => mockPublicStatus(...args),
    connectorHealth: vi.fn(),
    status: vi.fn(),
    sla: vi.fn(),
    dependencies: vi.fn(),
    incidents: vi.fn(),
    events: vi.fn(),
    resolveIncident: vi.fn(),
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

const operationalResponse: PublicStatusResponse = {
  status: 'operational',
  components: [
    { name: 'api', status: 'operational' },
    { name: 'store', status: 'operational' },
    { name: 'connectors', status: 'operational' },
    { name: 'ingest', status: 'operational' },
  ],
  timestamp: '2026-07-01T12:00:00Z',
}

const degradedResponse: PublicStatusResponse = {
  status: 'degraded',
  components: [
    { name: 'api', status: 'operational' },
    { name: 'store', status: 'operational' },
    { name: 'connectors', status: 'degraded' },
    { name: 'ingest', status: 'operational' },
  ],
  timestamp: '2026-07-01T12:00:00Z',
}

/** A correct install with no optional provider: nothing is broken, but the
 *  console must still name what is unprovisioned. */
const notConfiguredResponse: PublicStatusResponse = {
  status: 'not_configured',
  components: [
    { name: 'api', status: 'operational' },
    { name: 'knowledge', status: 'not_configured' },
    { name: 'store', status: 'operational' },
    { name: 'connectors', status: 'operational' },
    { name: 'ingest', status: 'operational' },
  ],
  timestamp: '2026-08-05T12:00:00Z',
}

describe('StatusPage', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders all-operational status', async () => {
    mockPublicStatus.mockResolvedValue(operationalResponse)
    const { StatusPage } = await import('./status-page')
    render(
      <Wrapper>
        <StatusPage />
      </Wrapper>,
    )

    await waitFor(() => {
      expect(screen.getByText(/all systems operational/i)).toBeTruthy()
    })
  })

  it('renders degraded status', async () => {
    mockPublicStatus.mockResolvedValue(degradedResponse)
    const { StatusPage } = await import('./status-page')
    render(
      <Wrapper>
        <StatusPage />
      </Wrapper>,
    )

    await waitFor(() => {
      expect(screen.getByText(/partial system outage/i)).toBeTruthy()
    })
  })

  it('renders an unconfigured install as incomplete, never as an outage', async () => {
    mockPublicStatus.mockResolvedValue(notConfiguredResponse)
    const { StatusPage } = await import('./status-page')
    render(
      <Wrapper>
        <StatusPage />
      </Wrapper>,
    )

    await waitFor(() => {
      expect(
        screen.getByText(/optional capabilities not configured/i),
      ).toBeTruthy()
    })
    // Not an outage, and not a bare "all systems operational" either.
    expect(screen.queryByText(/system outage/i)).toBeNull()
    expect(screen.queryByText('All Systems Operational')).toBeNull()
    // The unprovisioned capability is named, with its own neutral state.
    expect(screen.getByText(/knowledge retrieval/i)).toBeTruthy()
    const label = screen.getByText('Not configured')
    expect(label.className).toContain('text-muted-foreground')
    // …and it is painted neutral, not amber: an install nobody finished
    // configuring must not wear the colour that means "something is wrong".
    const banner = screen
      .getByText(/optional capabilities not configured/i)
      .closest('div')?.parentElement
    expect(banner?.className).not.toContain('warning')
    expect(banner?.className).not.toContain('danger')
  })

  it('claims nothing when the engine does not answer', async () => {
    mockPublicStatus.mockRejectedValue(new Error('unreachable'))
    const { StatusPage } = await import('./status-page')
    render(
      <Wrapper>
        <StatusPage />
      </Wrapper>,
    )

    await waitFor(() => {
      expect(screen.getAllByText(/currently unavailable/i).length).toBeGreaterThan(0)
    })
    // A green verdict with no evidence behind it is the one thing a status page
    // must never print.
    expect(screen.queryByText(/all systems operational/i)).toBeNull()
  })

  it('renders component rows', async () => {
    mockPublicStatus.mockResolvedValue(operationalResponse)
    const { StatusPage } = await import('./status-page')
    render(
      <Wrapper>
        <StatusPage />
      </Wrapper>,
    )

    await waitFor(() => {
      expect(screen.getByText('API')).toBeTruthy()
    })
    expect(screen.getByText(/database/i)).toBeTruthy()
    expect(screen.getByText(/data ingestion/i)).toBeTruthy()
  })
})
