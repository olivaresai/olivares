// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TooltipProvider } from '@/components/ui/tooltip'
import { ApiError } from '@/lib/api/errors'
import type { TeamSummaryResponse } from './types'

// ---- Hoist mock objects (must appear before vi.mock calls) ----

vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: () => true }),
}))

const api = vi.hoisted(() => ({
  summary: vi.fn(),
}))
// El doble permite afirmar EN POSITIVO que la ceremonia está en pantalla: negar la
// acusación a secas se cumple antes de que la consulta rechace.
// ⛔ EL DOBLE EXPONE `onElevated`. Sin él la celda sólo CLASIFICA —comprueba que aparece la
// ceremonia— y no ve si lleva a alguna parte: el contraste `sol max` lo señaló, y en esta
// misma campaña ya me costó cuatro celdas que pasaban con `onElevated={undefined}` puesto.
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

vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, teamCostsApi: api }
})

const navigate = vi.hoisted(() => vi.fn())
vi.mock('@tanstack/react-router', () => ({
  // useUrlState follows the location, so the mock answers with the REAL URL.
  // A constant '' passed while the effect read window.location; since
  // f9426990b the effect reads this value, and a constant would erase the
  // state the URL just seeded.
  useRouterState: () => window.location.search,
  useNavigate: () => navigate,
}))

// Stand-in for the saved-views menu: exposes what the view hands it and two
// applies — one storing a usable period, one storing a value the view must
// refuse (saved params come back from the server, so they are untrusted too).
vi.mock('@/features/saved-views', () => ({
  SavedViewsMenu: ({
    featureId,
    params,
    onApply,
  }: {
    featureId: string
    params: Record<string, string | undefined>
    onApply: (params: Record<string, string>) => void
  }) => (
    <div>
      <span data-testid="saved-views-feature-id">{featureId}</span>
      <span data-testid="saved-views-params">{JSON.stringify(params)}</span>
      <button type="button" onClick={() => onApply({ period: '7d' })}>
        Apply stored team view
      </button>
      <button
        type="button"
        onClick={() => onApply({ period: '365d', team: 'payments' })}
      >
        Apply broken stored team view
      </button>
    </div>
  ),
}))

// Components imported AFTER vi.mock declarations.
import { TeamCostsView } from './team-costs-view'

// ---- Wrap helper ----

function wrap(ui: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider delayDuration={0}>{ui}</TooltipProvider>
    </QueryClientProvider>,
  )
}

// ---- Mock data ----

const teamSummaryFixture: TeamSummaryResponse = {
  period: '2026-05-27/2026-06-26',
  teams: [
    {
      team: 'payments',
      sessions: 42,
      input_tokens: 1_200_000,
      output_tokens: 300_000,
      cost_micro_usd: 48_000_000_000, // $48,000
      trend: [0, 1000, 2000, 3000, 4000],
      projects: [
        {
          project: 'ledger',
          sessions: 30,
          input_tokens: 900_000,
          output_tokens: 200_000,
          cost_micro_usd: 36_000_000_000,
        },
      ],
      models: [
        { model: 'claude-opus-4-8', cost_micro_usd: 40_000_000_000 },
        { model: 'claude-haiku-4', cost_micro_usd: 8_000_000_000 },
      ],
    },
    {
      team: 'platform',
      sessions: 18,
      input_tokens: 500_000,
      output_tokens: 120_000,
      cost_micro_usd: 15_000_000_000, // $15,000
      trend: [500, 200, 800, 100, 400],
      projects: [],
      models: [{ model: 'claude-opus-4-8', cost_micro_usd: 15_000_000_000 }],
    },
  ],
}

// ---- Lifecycle ----

beforeEach(() => {
  api.summary.mockReset()
  api.summary.mockResolvedValue(teamSummaryFixture)
  navigate.mockReset()
  window.history.replaceState(null, '', '/team-costs')
})
afterEach(() => {
  vi.clearAllMocks()
  window.history.replaceState(null, '', '/')
})

/** Apply the last navigate() search updater to `current`, the way the router
 *  would, so a test can assert the merged search instead of a function. */
function lastSearch(
  current: Record<string, unknown> = {},
): Record<string, unknown> {
  const call = navigate.mock.calls.at(-1)?.[0] as
    | { search: (cur: Record<string, unknown>) => Record<string, unknown> }
    | undefined
  if (!call) throw new Error('navigate was never called')
  return call.search(current)
}

// ---- Tests ----

describe('TeamCostsView', () => {
  it('renders team table with sparklines', async () => {
    wrap(<TeamCostsView />)

    // Both team names must appear in the table.
    expect(await screen.findByText('payments')).toBeInTheDocument()
    expect(screen.getByText('platform')).toBeInTheDocument()

    // Costs appear in compact form ($48K, $15K).
    // Sparkline renders no geometry under jsdom (Recharts), but the wrapping
    // element is still present (aria-hidden decorative trend column).
    expect(screen.getByRole('grid', { name: 'Team Costs' })).toBeInTheDocument()

    // Export button is enabled when data is present.
    const exportBtn = screen.getByRole('button', { name: 'Export CSV' })
    expect(exportBtn).not.toBeDisabled()
  })

  it('period selector changes period query parameter', async () => {
    wrap(<TeamCostsView />)

    // Wait for initial load with default 30d period.
    await waitFor(() => expect(api.summary).toHaveBeenCalledWith('30d'))

    // Click the "Last 7 days" button.
    await userEvent.click(screen.getByRole('button', { name: 'Last 7 days' }))

    // The API must be called again with '7d'.
    await waitFor(() => expect(api.summary).toHaveBeenCalledWith('7d'))
  })

  it('expands team row to show project and model breakdowns', async () => {
    wrap(<TeamCostsView />)

    // Wait for the table to appear.
    expect(await screen.findByText('payments')).toBeInTheDocument()

    // Projects panel should not be visible yet.
    expect(screen.queryByText('Projects')).not.toBeInTheDocument()

    // Click the payments row to expand it.
    await userEvent.click(screen.getByRole('row', { name: /payments/i }))

    // The breakdown section headers and project name must now appear.
    expect(screen.getByText('Projects')).toBeInTheDocument()
    expect(screen.getByText('Models')).toBeInTheDocument()
    expect(screen.getByText('ledger')).toBeInTheDocument()
    expect(screen.getByText('claude-opus-4-8')).toBeInTheDocument()

    // Clicking again collapses the row.
    await userEvent.click(screen.getByRole('row', { name: /payments/i }))
    await waitFor(() =>
      expect(screen.queryByText('Projects')).not.toBeInTheDocument(),
    )
  })
})

describe('TeamCostsView — URL state', () => {
  it('round-trips the period through the URL with replace semantics', async () => {
    window.history.replaceState(null, '', '/team-costs?period=90d')
    wrap(<TeamCostsView />)

    // URL -> state: the link's period drives both the request and the selector.
    await waitFor(() => expect(api.summary).toHaveBeenCalledWith('90d'))
    expect(
      screen.getByRole('button', { name: 'Last 90 days' }),
    ).toHaveAttribute('aria-pressed', 'true')

    // state -> URL: a replace (filters must not pile up in the history stack)
    // that merges over params this view does not own.
    await userEvent.click(screen.getByRole('button', { name: 'Last 7 days' }))
    expect(navigate).toHaveBeenLastCalledWith(
      expect.objectContaining({ replace: true }),
    )
    expect(lastSearch({ tab: 'spend' })).toEqual({
      tab: 'spend',
      period: '7d',
    })
    await waitFor(() => expect(api.summary).toHaveBeenCalledWith('7d'))

    // The default lives in code and stays OUT of the URL: going back to it
    // clears the key, so a pristine view has a clean shareable link.
    await userEvent.click(screen.getByRole('button', { name: 'Last 30 days' }))
    expect(lastSearch({ tab: 'spend' })).toEqual({
      tab: 'spend',
      period: undefined,
    })
  })

  it('falls back to the default period for a refused URL value AND says so', async () => {
    window.history.replaceState(null, '', '/team-costs?period=1y')
    wrap(<TeamCostsView />)

    await waitFor(() => expect(api.summary).toHaveBeenCalledWith('30d'))
    expect(api.summary).not.toHaveBeenCalledWith('1y')
    expect(
      screen.getByRole('button', { name: 'Last 30 days' }),
    ).toHaveAttribute('aria-pressed', 'true')

    // Falling back SILENTLY is the defect this unit exists to remove.
    expect(screen.getByTestId('url-state-notice')).toBeInTheDocument()
  })

  it('re-sanitises a saved view before it reaches the URL or the request', async () => {
    wrap(<TeamCostsView />)
    await waitFor(() => expect(api.summary).toHaveBeenCalledWith('30d'))

    // The menu is wired to the registry's savedViewsFeatureId.
    expect(screen.getByTestId('saved-views-feature-id')).toHaveTextContent(
      'team-costs',
    )

    await userEvent.click(
      screen.getByRole('button', { name: 'Apply stored team view' }),
    )
    await waitFor(() => expect(api.summary).toHaveBeenCalledWith('7d'))
    expect(lastSearch()).toEqual({ period: '7d' })
    expect(screen.getByTestId('saved-views-params')).toHaveTextContent(
      '{"period":"7d"}',
    )
    expect(screen.queryByTestId('url-state-notice')).not.toBeInTheDocument()

    // A stored value the view refuses must reach neither the request nor the
    // URL, and the unknown key must not be smuggled in with it.
    await userEvent.click(
      screen.getByRole('button', { name: 'Apply broken stored team view' }),
    )
    await waitFor(() => expect(api.summary).toHaveBeenLastCalledWith('30d'))
    expect(api.summary).not.toHaveBeenCalledWith('365d')
    expect(lastSearch()).toEqual({ period: undefined })
    expect(screen.getByTestId('url-state-notice')).toBeInTheDocument()
  })
})

describe('TeamCostsView — los dos 403 no son el mismo', () => {
  it('un step_up_required pinta la CEREMONIA, no la acusación de rol', async () => {
    // ⛔ Esta vista se sustituía ENTERA por «no tienes autorización» ante un 403 de
    // aseguramiento —`isForbidden` es sólo el status, lib/api/errors.ts:59— cuando lo que
    // hacía falta era la ceremonia de un toque. Ancla POSITIVA: la ceremonia ESTÁ.
    api.summary.mockRejectedValue(
      new ApiError(403, 'step_up_required', 'assurance level too low'),
    )
    wrap(<TeamCostsView />)

    expect(await screen.findByText('step-up ceremony')).toBeInTheDocument()

    // Y la salida: elevar reintenta la lectura refusada. Sin esto, una ceremonia cableada a
    // nada dejaría la celda verde.
    api.summary.mockResolvedValue({ teams: [], total_usd: 0 })
    await userEvent.click(screen.getByRole('button', { name: /elevar/i }))
    await waitFor(() => expect(api.summary).toHaveBeenCalledTimes(2))
  })

  it('y un 403 SIN código de ceremonia conserva la negativa de ROL', async () => {
    // Control negativo: la negativa de rol es CIERTA y se queda. Sin esta celda, mandar los
    // DOS 403 a la ceremonia también pasaría — el defecto simétrico.
    api.summary.mockRejectedValue(new ApiError(403, 'forbidden', 'no'))
    wrap(<TeamCostsView />)

    expect(await screen.findByRole('status')).toBeInTheDocument()
    expect(screen.queryByText('step-up ceremony')).not.toBeInTheDocument()
  })
})
