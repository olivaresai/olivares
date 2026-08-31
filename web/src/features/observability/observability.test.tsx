// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { act } from 'react'
import { useEffect as reactUseEffect, useState as reactUseState } from 'react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, renderIntel, screen, waitFor, within } from '@/test/intel'
import '@/features/_intel' // register the shared `intel` namespace for badges/notices
import {
  IngestionHealthTable,
  IngestionSourcesTable,
  SpanDetailPanel,
  SpanStatusBadge,
  StandardMaturityBadge,
  TraceList,
  TraceWaterfall,
} from './components'
import {
  ingestionHealthFixture,
  otelTraceDetailFixture,
  traceDetailFixture,
  traceListFixture,
} from './fixtures'
import './i18n'

describe('StandardMaturityBadge', () => {
  it('pins the version next to the upstream-declared maturity', () => {
    renderIntel(
      <StandardMaturityBadge maturity="development" version="1.41.1" />,
    )
    expect(screen.getByText(/Development/i)).toBeInTheDocument()
    // The version pin must be present and verbatim (no "stable" inflation).
    expect(screen.getByText(/v1\.41\.1/)).toBeInTheDocument()
  })

  it('renders pre-1.0 schemas honestly', () => {
    renderIntel(<StandardMaturityBadge maturity="pre_1_0" version="0.1.0" />)
    expect(screen.getByText(/Pre-1\.0/i)).toBeInTheDocument()
    expect(screen.getByText(/v0\.1\.0/)).toBeInTheDocument()
  })
})

describe('IngestionHealthTable', () => {
  it('lists the standards with verified versions and the opt-in gate state', () => {
    renderIntel(
      <IngestionHealthTable standards={ingestionHealthFixture.standards} />,
    )
    const grid = screen.getByRole('grid')
    // OTel GenAI is Development, version-pinned, and its gate is off by default.
    expect(within(grid).getByText(/OpenTelemetry GenAI/i)).toBeInTheDocument()
    expect(within(grid).getByText(/v1\.41\.1/)).toBeInTheDocument()
    expect(
      within(grid).getByText(/semconv_opt_in=gen_ai_latest_experimental/),
    ).toBeInTheDocument()
    expect(
      within(grid).getByText(
        /open-telemetry\/semantic-conventions-genai main@c321d7e, verified 2026-07-05/,
      ),
    ).toBeInTheDocument()
    expect(within(grid).getAllByText(/main@c321d7e/)).toHaveLength(1)
    // The gate state is the honest tri-state: the engine cannot read connector
    // config, so an absent opt_in_active reads "no evidence yet", never "off".
    expect(within(grid).getByText(/no evidence yet/i)).toBeInTheDocument()
    // OCSF GA + Prometheus text 0.0.4 are present and version-pinned.
    expect(within(grid).getByText(/v1\.8\.0/)).toBeInTheDocument()
    expect(within(grid).getByText(/v0\.0\.4/)).toBeInTheDocument()
    // The W3C Trace Context correlation row (new in) is present and active.
    expect(within(grid).getByText(/W3C Trace Context/i)).toBeInTheDocument()
  })

  it('does not relabel Prometheus text as OpenMetrics, and shows no fabricated throughput', () => {
    renderIntel(
      <IngestionHealthTable standards={ingestionHealthFixture.standards} />,
    )
    // Honesty: no "OpenMetrics" relabelling anywhere.
    expect(screen.queryByText(/OpenMetrics/i)).not.toBeInTheDocument()
    // records_total is absent where unattributable → an em-dash, never a number.
    const records = screen.getAllByText('—')
    expect(records.length).toBeGreaterThan(0)
  })

  it('renders no credential', () => {
    renderIntel(
      <IngestionHealthTable standards={ingestionHealthFixture.standards} />,
    )
    expect(screen.queryByText(/sk-ant/)).not.toBeInTheDocument()
  })
})

describe('IngestionSourcesTable', () => {
  it('renders the per-source counters with kinds and signals', () => {
    renderIntel(
      <IngestionSourcesTable
        sources={ingestionHealthFixture.sources}
        since={ingestionHealthFixture.since}
      />,
    )
    const grid = screen.getByRole('grid')
    expect(within(grid).getByText('olivares.claude')).toBeInTheDocument()
    expect(within(grid).getByText('42')).toBeInTheDocument()
    // Kind badges + per-signal breakdown render verbatim from the response.
    expect(within(grid).getAllByText(/Edge/).length).toBeGreaterThan(0)
    expect(within(grid).getByText(/otel/)).toBeInTheDocument()
    // The process-global / reset-on-restart caveat is attached to the table.
    expect(
      screen.getByText(/process-global and accumulate since engine start/i),
    ).toBeInTheDocument()
  })

  it('renders an honest empty state when nothing flowed since engine start', () => {
    renderIntel(
      <IngestionSourcesTable
        sources={[]}
        since={ingestionHealthFixture.since}
      />,
    )
    expect(
      screen.getByText(/No records observed on the bus since engine start/i),
    ).toBeInTheDocument()
    expect(screen.queryByRole('grid')).not.toBeInTheDocument()
  })
})

describe('SpanStatusBadge', () => {
  it('reads error loud and ok calm', () => {
    const { rerender } = renderIntel(<SpanStatusBadge status="error" />)
    expect(screen.getByText(/Error/i)).toBeInTheDocument()
    rerender(<SpanStatusBadge status="ok" />)
    expect(screen.getByText(/OK/i)).toBeInTheDocument()
  })
})

describe('TraceWaterfall', () => {
  // The pure waterfall renders ANY TraceSpan shape; the OTel-shaped fixture
  // exercises parent-child indentation the ledger read-model never produces.
  it('renders the span tree and the accessible summary', () => {
    renderIntel(<TraceWaterfall trace={otelTraceDetailFixture} />)
    // Spans appear by name in the waterfall.
    expect(
      screen.getByText(/POST \/v1\/agents\/support-triage:invoke/),
    ).toBeInTheDocument()
    expect(screen.getByText(/chat anthropic/)).toBeInTheDocument()
    // The AccessibleChart summary (1.1.1) is present as readable text.
    expect(screen.getByText(/Span waterfall: 6 spans/i)).toBeInTheDocument()
    // No prompt / completion / credential is leaked from attributes.
    expect(screen.queryByText(/sk-ant/)).not.toBeInTheDocument()
  })

  it('renders the ledger-derived shape (flat spans, kind ledger, status unset)', () => {
    renderIntel(<TraceWaterfall trace={traceDetailFixture} />)
    // Grouped-span name carries the honest "+N events" suffix.
    expect(
      screen.getByText(/session\.start \(\+2 events\)/),
    ).toBeInTheDocument()
    expect(screen.getByText(/Span waterfall: 2 spans/i)).toBeInTheDocument()
    // No fabricated ok/error verdict: the ledger stores no OTel status.
    expect(screen.queryByText(/Error/i)).not.toBeInTheDocument()
  })

  //selecting a row used to paint it solid `bg-accent`, which put its own
  // `text-foreground` label at 2.58:1 and its `text-muted-foreground` one at
  // 1.17:1. The AT gate derives that pairing now and would catch a regression,
  // but the gate is a MANUAL per-release step; this pins the contract on every
  // `pnpm test` run. The assertion is deliberately about the two classes that
  // carry the state — the fill and the ring — plus the programmatic state,
  // because the fill alone is ~1.1:1 against the card and carries no luminance.
  it('marks a selected span with the accent FILL, its own ink, and aria-pressed', async () => {
    const { userEvent } = await import('@/test/intel')
    renderIntel(<TraceWaterfall trace={traceDetailFixture} />)
    // Reach the ROW, not the chart's show-as-table toggle — that one also
    // carries aria-pressed, and picking it by role alone tests nothing.
    const row = screen
      .getByText(/session\.start/)
      .closest('[role="button"]') as HTMLElement
    expect(row).toHaveAttribute('aria-pressed', 'false')
    await userEvent.click(row)
    expect(row).toHaveAttribute('aria-pressed', 'true')
    // ⛔ ESTA CELDA EXIGÍA `bg-accent-soft` Y PROHIBÍA `bg-accent`, y con eso puso `main` ROJO.
    // Su intención era buena y su premisa estaba caducada: citaba 2.58:1 y 1.17:1, que son las
    // cifras del defecto que `01824cf47` YA ARREGLÓ — y lo arregló en la dirección CONTRARIA.
    // El remedio aceptado no fue ablandar el RELLENO, fue cambiar la TINTA:
    //
    //   --accent #f08000 sobre --accent-foreground #1a1206  ->  6.88:1  (AA con holgura)
    //
    // Así que prohibir `bg-accent` prohibía el arreglo embarcado. Lo que hay que guardar es la
    // propiedad que el remedio estableció: una fila seleccionada lleva SU tinta, no la del lienzo.
    // Encontrado por the orchestrator midiendo lo suyo.
    expect(row.className).toContain('bg-accent')
    expect(row.className).toContain('text-accent-foreground')
    // La regresión que esto guarda, y es la real: que una fila seleccionada vuelva a heredar las
    // tintas del lienzo, que sobre el accent son ilegibles (2.97:1 y 1.45:1 en claro).
    expect(row.className).not.toMatch(/(^|\s)text-(foreground|muted-foreground)(\s|$)/)
  })

  it('shows a legend when multiple actors are present', () => {
    renderIntel(<TraceWaterfall trace={traceDetailFixture} />)
    // The fixture has two actors: user:admin and agent:bot-1.
    expect(screen.getByText('user:admin')).toBeInTheDocument()
    expect(screen.getByText('agent:bot-1')).toBeInTheDocument()
  })

  it('shows actor labels on spans', () => {
    renderIntel(<TraceWaterfall trace={traceDetailFixture} />)
    // Actor labels stripped of kind prefix: "admin" from "user:admin".
    expect(screen.getByText('admin')).toBeInTheDocument()
    expect(screen.getByText('bot-1')).toBeInTheDocument()
  })

  //a SELECTED row is an `accent` FILL, and a fill carries its own ink. This row
  // used to keep the canvas inks, which are tuned for the canvas: measured on the accent,
  // the span name was 2.97:1 in light / 2.58:1 in dark and the actor 1.45:1 / 1.17:1, all
  // below the 4.5:1 of 1.4.3. It was NOT introduced by moving the light fill to the brand
  // orange — that move improved both numbers — which is exactly why nothing caught it:
  // every value-level token assert stayed green while the PAIR was never anyone's property.
  it('a selected row wears the ink that belongs on the accent fill (WCAG 1.4.3)', async () => {
    renderIntel(<TraceWaterfall trace={traceDetailFixture} />)
    // Scoped to the waterfall list: the chart chrome has its own aria-pressed toggle,
    // and an unscoped query picks it up instead of a row.
    const list = screen.getByRole('list', { name: /waterfall/i })
    const rows = within(list).getAllByRole('button', { pressed: false })
    await userEvent.click(rows[0])
    const selected = within(list).getByRole('button', { pressed: true })
    expect(selected.className).toContain('bg-accent')
    // accent-foreground is #1a1206 in BOTH themes, so one value serves both: 6.88:1.
    expect(selected.className).toContain('text-accent-foreground')
    // The secondary tone stays secondary without falling off the fill: /80 is 5.00:1.
    const actor = within(selected).getByText('admin')
    expect(actor.className).not.toContain('text-muted-foreground')
    expect(actor.className).toContain('text-accent-foreground/80')
    // And an UNselected row keeps the canvas inks, which is where they are correct.
    const other = within(list).getAllByRole('button', { pressed: false })[0]
    expect(other.className).not.toContain('text-accent-foreground')
    expect(within(other).getByText('bot-1').className).toContain(
      'text-muted-foreground',
    )
  })

  it('offers the equivalent data table (WCAG 1.4.1)', async () => {
    const { userEvent } = await import('@/test/intel')
    renderIntel(<TraceWaterfall trace={otelTraceDetailFixture} />)
    await userEvent.click(
      screen.getByRole('button', { name: /show as table/i }),
    )
    expect(screen.getByRole('grid')).toBeInTheDocument()
  })
})

describe('SpanDetailPanel', () => {
  it('renders the span attributes and actor', () => {
    const span = traceDetailFixture.spans[0]
    const onClose = vi.fn()
    renderIntel(<SpanDetailPanel span={span} onClose={onClose} />)
    expect(screen.getByText(span.span_id)).toBeInTheDocument()
    // Actor appears in the detail (and again in attributes as ledger.actor).
    expect(screen.getAllByText('user:admin').length).toBeGreaterThanOrEqual(1)
    expect(screen.getByText('ledger.events')).toBeInTheDocument()
  })
})

describe('TraceList', () => {
  it('lists ledger-derived traces with their windows and the honest unset status', () => {
    renderIntel(<TraceList traces={traceListFixture} />)
    const grid = screen.getByRole('grid')
    expect(
      within(grid).getByText(/4bf92f3577b34da6a3ce929d0e0e4736/),
    ).toBeInTheDocument()
    // The ledger read-model never claims ok/error — status is always Unset.
    expect(within(grid).getAllByText(/Unset/i).length).toBeGreaterThan(0)
    expect(within(grid).queryByText(/Error/i)).not.toBeInTheDocument()
  })

  it('shows the agent count column header', () => {
    renderIntel(<TraceList traces={traceListFixture} />)
    const grid = screen.getByRole('grid')
    expect(within(grid).getByText(/Agents/i)).toBeInTheDocument()
  })
})

// --- live container + honesty -------------------------------------------------
//
// The view's queries hit the LIVE modules/observability routes. The default
// mocks resolve, the view renders the read-model, and the honesty markers (engine-
// wide scope, Development maturity, ledger-window semantics) stay asserted.

const auth = vi.hoisted(() => ({
  ...({} as Record<string, never>),
  activeTenant: 'demo' as string | null,
  can: (_p: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

const navigate = vi.hoisted(() => vi.fn())
// The canonical URL-state hook follows the location, so the mock has to be able
// to MOVE it: a stub that always answers '' makes every test that depends on a
// navigation pass against an implementation that never navigates.
// ⛔ Este stub es fiel al SEMBRADO INICIAL, no al router. La versión anterior de
// este comentario decía que la URL es la única fuente de verdad «exactamente como
// la del router real» y que un `searchStr` en desacuerdo con `window.location` es
// un estado que ningún navegador produce. **Es al revés, y lo dice el producto**:
// `use-url-state.ts` documenta que leer `window.location` «can observe the previous
// URL for one tick and revert the optimistic state after a filter already fired its
// new request» — `@tanstack/history` notifica el payload nuevo y agenda el volcado
// de la ventana en una microtarea, así que la discrepancia de un tick es un estado
// DELIBERADO de la librería.
//
// La consecuencia para quien lea esta prueba: el stub no cubre esa ventana, y un
// selector roto puede seguir verde aquí porque el mock ignora el `select` y devuelve
// la cadena global. Hallazgo F-01 del contraste de sobre.
const routerState = vi.hoisted(() => ({
  listeners: new Set<() => void>(),
}))
vi.mock('@tanstack/react-router', () => ({
  useRouterState: ({ select }: { select: (s: unknown) => unknown }) => {
    const [, force] = reactUseState(0)
    reactUseEffect(() => {
      const fn = () => force((n) => n + 1)
      routerState.listeners.add(fn)
      return () => {
        routerState.listeners.delete(fn)
      }
    }, [])
    return select({ location: { searchStr: window.location.search } })
  },
  useNavigate: () => navigate,
}))

function setSearchStr(next: string) {
  // A navigation moves the URL; moving only the notification would let a
  // test pass against a view that never reads the location it was given.
  window.history.replaceState(null, '', window.location.pathname + next)
  for (const fn of routerState.listeners) fn()
}

const api = vi.hoisted(() => ({
  ingestionHealth: vi.fn(),
  traces: vi.fn(),
  trace: vi.fn(),
  exportTrace: vi.fn(),
}))
vi.mock('./api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api')>()
  return { ...actual, observabilityApi: api }
})

import { ObservabilityView } from './observability-view'

function resetLive() {
  vi.clearAllMocks()
  window.history.replaceState(null, '', '/observability')
  auth.can = () => true
  api.ingestionHealth.mockResolvedValue(ingestionHealthFixture)
  api.traces.mockResolvedValue({ items: traceListFixture, has_more: false })
  api.trace.mockResolvedValue(traceDetailFixture)
}

describe('ObservabilityView — live + honesty', () => {
  it('renders the live ingestion read-model (standards + per-source counters), not a seam', async () => {
    resetLive()
    renderIntel(<ObservabilityView />)
    await waitFor(() => expect(api.ingestionHealth).toHaveBeenCalled())
    // Standards grid is live…
    expect(await screen.findByText(/OpenTelemetry GenAI/i)).toBeInTheDocument()
    // …and the per-source counters render next to it.
    expect(screen.getByText('olivares.claude')).toBeInTheDocument()
    // No pending-seam copy and no red error.
    expect(
      screen.queryByText(/not wired to the backend/i),
    ).not.toBeInTheDocument()
    expect(screen.queryByText(/Something went wrong/i)).not.toBeInTheDocument()
  })

  it('renders the honest empty sources state when nothing flowed on the bus', async () => {
    resetLive()
    api.ingestionHealth.mockResolvedValue({
      ...ingestionHealthFixture,
      sources: [],
    })
    renderIntel(<ObservabilityView />)
    expect(
      await screen.findByText(
        /No records observed on the bus since engine start/i,
      ),
    ).toBeInTheDocument()
  })

  it('carries the honesty markers: engine-wide scope, sound attribution, Development maturity', async () => {
    resetLive()
    renderIntel(<ObservabilityView />)
    // Process-global vs per-tenant disclosure (in the description AND the caveat).
    expect(
      (
        await screen.findAllByText(
          /engine-wide \(process-global\), not per-tenant/i,
        )
      ).length,
    ).toBeGreaterThan(0)
    // The no-fabrication note: counters only where attribution is sound.
    expect(screen.getByText(/a figure is never invented/i)).toBeInTheDocument()
    // The OTel GenAI convention still reads Development, never inflated.
    expect(await screen.findAllByText(/Development/i)).not.toHaveLength(0)
  })

  it('trace tab explains the ledger-window semantics + keeps the W3C Level-1-only caveat', async () => {
    resetLive()
    const { userEvent } = await import('@/test/intel')
    renderIntel(<ObservabilityView />)
    await userEvent.click(
      screen.getByRole('tab', { name: /Trace drill-down/i }),
    )
    // The honest live note: ledger-event windows, no OTel spans stored.
    expect(
      await screen.findByText(/the engine stores no OTel spans/i),
    ).toBeInTheDocument()
    expect(
      screen.getByText(/validated only at its Stable Level 1/i),
    ).toBeInTheDocument()
    // No Level-2 claim.
    expect(screen.queryByText(/Level 2/i)).not.toBeInTheDocument()
  })

  it('opens the ledger-derived waterfall when a trace is selected', async () => {
    resetLive()
    const { userEvent } = await import('@/test/intel')
    renderIntel(<ObservabilityView />)
    await userEvent.click(
      screen.getByRole('tab', { name: /Trace drill-down/i }),
    )
    const grid = await screen.findByRole('grid')
    await userEvent.click(within(grid).getByText(/session\.start/))
    await waitFor(() =>
      expect(api.trace).toHaveBeenCalledWith(
        '4bf92f3577b34da6a3ce929d0e0e4736',
      ),
    )
    // The grouped span renders with its honest "+N events" suffix.
    expect(
      await screen.findByText(/session\.start \(\+2 events\)/),
    ).toBeInTheDocument()
    expect(screen.getByText(/Span waterfall: 2 spans/i)).toBeInTheDocument()
  })

  it('trace search input filters the list by trace_id prefix', async () => {
    resetLive()
    const { userEvent } = await import('@/test/intel')
    renderIntel(<ObservabilityView />)
    await userEvent.click(
      screen.getByRole('tab', { name: /Trace drill-down/i }),
    )
    const searchInput =
      await screen.findByPlaceholderText(/Search by trace ID/i)
    await userEvent.type(searchInput, '4bf')
    await waitFor(() =>
      expect(api.traces).toHaveBeenCalledWith(
        expect.objectContaining({ q: '4bf' }),
      ),
    )
  })

  it('appends distinct cursor pages when Load more is selected', async () => {
    resetLive()
    api.traces.mockImplementation(({ cursor }: { cursor?: string } = {}) =>
      cursor === 'trace-page-2'
        ? Promise.resolve({
            items: [traceListFixture[1]],
            has_more: false,
          })
        : Promise.resolve({
            items: [traceListFixture[0]],
            cursor: 'trace-page-2',
            has_more: true,
          }),
    )
    const { userEvent } = await import('@/test/intel')
    renderIntel(<ObservabilityView />)
    await userEvent.click(
      screen.getByRole('tab', { name: /Trace drill-down/i }),
    )

    expect(await screen.findByText('session.start')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: /Load more/i }))

    await waitFor(() =>
      expect(api.traces).toHaveBeenCalledWith(
        expect.objectContaining({
          cursor: 'trace-page-2',
          limit: 50,
        }),
      ),
    )
    expect(await screen.findByText('guard.decision')).toBeInTheDocument()
    expect(screen.getByText('session.start')).toBeInTheDocument()
  })

  it('sends every trace filter server-side with local dates converted to RFC3339 UTC', async () => {
    resetLive()
    const { userEvent } = await import('@/test/intel')
    renderIntel(<ObservabilityView />)
    await userEvent.click(
      screen.getByRole('tab', { name: /Trace drill-down/i }),
    )
    await screen.findByRole('grid')

    await userEvent.type(screen.getByLabelText(/Search by trace ID/i), '4bf')
    await userEvent.type(
      screen.getByLabelText(/Filter by service/i),
      'olivares',
    )
    await userEvent.selectOptions(
      screen.getByLabelText(/Filter by status/i),
      'unset',
    )
    fireEvent.change(screen.getByLabelText(/^From$/i), {
      target: { value: '2026-07-24T12:30' },
    })
    fireEvent.change(screen.getByLabelText(/^To$/i), {
      target: { value: '2026-07-24T14:45' },
    })

    await waitFor(() =>
      expect(api.traces).toHaveBeenLastCalledWith({
        limit: 50,
        q: '4bf',
        service: 'olivares',
        status: 'unset',
        from: new Date('2026-07-24T12:30').toISOString(),
        to: new Date('2026-07-24T14:45').toISOString(),
        cursor: undefined,
      }),
    )
  })

  it('seeds trace investigation filters from the URL on mount', async () => {
    resetLive()
    window.history.replaceState(
      null,
      '',
      '/observability?q=8a3&service=olivares&status=unset&from=2026-07-24T10%3A00%3A00Z&to=2026-07-24T11%3A00%3A00Z',
    )
    const { userEvent } = await import('@/test/intel')
    renderIntel(<ObservabilityView />)
    await userEvent.click(
      screen.getByRole('tab', { name: /Trace drill-down/i }),
    )

    expect(screen.getByLabelText(/Search by trace ID/i)).toHaveValue('8a3')
    expect(screen.getByLabelText(/Filter by service/i)).toHaveValue('olivares')
    expect(screen.getByLabelText(/Filter by status/i)).toHaveValue('unset')
    await waitFor(() =>
      expect(api.traces).toHaveBeenCalledWith({
        limit: 50,
        q: '8a3',
        service: 'olivares',
        status: 'unset',
        from: '2026-07-24T10:00:00.000Z',
        to: '2026-07-24T11:00:00.000Z',
        cursor: undefined,
      }),
    )
  })

  it('shows the export button when a trace is selected', async () => {
    resetLive()
    api.exportTrace.mockResolvedValue({ resourceSpans: [] })
    const { userEvent } = await import('@/test/intel')
    renderIntel(<ObservabilityView />)
    await userEvent.click(
      screen.getByRole('tab', { name: /Trace drill-down/i }),
    )
    const grid = await screen.findByRole('grid')
    await userEvent.click(within(grid).getByText(/session\.start/))
    await waitFor(() => expect(api.trace).toHaveBeenCalled())
    expect(await screen.findByText(/Export OTLP/i)).toBeInTheDocument()
  })

  it('RBAC: without observability:traces:read the trace rows are not selectable (no forbidden drill offered)', async () => {
    resetLive()
    // The drill permission is the module's REAL enforced perm.
    auth.can = (p: string) => p !== 'observability:traces:read'
    const { userEvent } = await import('@/test/intel')
    renderIntel(<ObservabilityView />)
    await userEvent.click(
      screen.getByRole('tab', { name: /Trace drill-down/i }),
    )
    const grid = await screen.findByRole('grid')
    // Rows are present (read) but clicking one must NOT open a waterfall.
    const row = within(grid).getByText(/session\.start/)
    await userEvent.click(row)
    // The waterfall stays a prompt — the gated drill is never offered.
    expect(screen.getByText(/Select a trace to see/i)).toBeInTheDocument()
    expect(api.trace).not.toHaveBeenCalled()
    expect(screen.queryByText(/Span waterfall:/i)).not.toBeInTheDocument()
  })
})

describe('ObservabilityView outer tab', () => {
  // The measured defect this closes: the outer tab was Radix-uncontrolled and
  // Radix unmounts the inactive TabsContent, so /observability?q=…&service=…
  // landed the recipient on Ingestion with the trace filters doing nothing. The
  // one place URL state had shipped was unreachable by the link that carried it.
  it('opens the tab the URL names', async () => {
    resetLive()
    window.history.replaceState(null, '', '/observability?tab=traces')
    renderIntel(<ObservabilityView />)
    expect(
      await screen.findByRole('tab', { name: /trace/i, selected: true }),
    ).toBeInTheDocument()
  })

  it('opens Traces for a link that carries trace filters and NO tab', async () => {
    resetLive()
    // This is the link that actually exists in the wild, and the whole reason
    // the defect mattered: it used to land on Ingestion with the parameters
    // doing nothing. Nothing here clicks a tab.
    window.history.replaceState(
      null,
      '',
      '/observability?q=timeout&service=api',
    )
    renderIntel(<ObservabilityView />)
    expect(
      await screen.findByRole('tab', { name: /trace/i, selected: true }),
    ).toBeInTheDocument()
    await waitFor(() => expect(api.traces).toHaveBeenCalled())
    expect(api.traces.mock.calls.at(-1)?.[0]).toMatchObject({
      q: 'timeout',
      service: 'api',
    })
  })

  it('keeps the operator on Traces after a refused filter is cleaned away', async () => {
    resetLive()
    // The location and the router state have to AGREE, or the sync effect never
    // runs and this test passes against a version that cleans nothing.
    window.history.replaceState(null, '', '/observability?status=nonsense')
    setSearchStr('?status=nonsense')
    renderIntel(<ObservabilityView />)

    expect(
      await screen.findByRole('tab', { name: /trace/i, selected: true }),
    ).toBeInTheDocument()
    expect(await screen.findByTestId('url-state-notice')).toHaveTextContent(
      /status/,
    )

    // The hook cleans the refused key and the view canonicalises the inferred
    // tab. Both go through navigate; assert the INSTRUCTIONS rather than
    // installing the answer by hand.
    const searches = navigate.mock.calls.map((c) =>
      (c[0] as { search: (cur: Record<string, unknown>) => unknown }).search({
        status: 'nonsense',
      }),
    )
    expect(searches).toContainEqual(
      expect.objectContaining({ status: undefined }),
    )
    expect(searches).toContainEqual(expect.objectContaining({ tab: 'traces' }))

    // Now let the router actually apply them. The tab must not move, and the
    // notice must survive the removal of the thing it is about.
    await act(async () => {
      window.history.replaceState(null, '', '/observability?tab=traces')
      setSearchStr('?tab=traces')
    })
    expect(
      screen.getByRole('tab', { name: /trace/i, selected: true }),
    ).toBeInTheDocument()
    expect(screen.getByTestId('url-state-notice')).toBeInTheDocument()
  })

  it('lets the operator leave an inferred Traces for Ingestion', async () => {
    resetLive()
    // The trap this closes: clearing the key on an explicit click resolved
    // straight back to the inferred Traces, so Ingestion could not be selected.
    window.history.replaceState(null, '', '/observability?q=timeout')
    setSearchStr('?q=timeout')
    renderIntel(<ObservabilityView />)
    await screen.findByRole('tab', { name: /trace/i, selected: true })

    navigate.mockClear()
    // Radix tabs answer to a real pointer sequence, not a bare click event.
    const user = userEvent.setup()
    await user.click(screen.getByRole('tab', { name: /ingestion/i }))

    // The assertion is on what the click ASKS FOR. Installing ?tab=ingestion
    // ourselves and then checking the tab would pass with the old handler,
    // which asked for `undefined` and left the operator on Traces.
    const asked = navigate.mock.calls.map((c) =>
      (c[0] as { search: (cur: Record<string, unknown>) => unknown }).search({
        q: 'timeout',
      }),
    )
    expect(asked).toContainEqual(
      expect.objectContaining({ tab: 'ingestion', q: 'timeout' }),
    )
    expect(asked).not.toContainEqual(
      expect.objectContaining({ tab: undefined }),
    )
  })

  it('shows and queries the same instant for a time bound', async () => {
    resetLive()
    window.history.replaceState(
      null,
      '',
      '/observability?tab=traces&from=2026-06-01T10%3A30%3A45.123Z',
    )
    renderIntel(<ObservabilityView />)
    await waitFor(() => expect(api.traces).toHaveBeenCalled())
    // The control renders minutes; a request carrying seconds would make two
    // visually identical links query different windows.
    const sent = api.traces.mock.calls.at(-1)?.[0].from
    expect(sent).toBe('2026-06-01T10:30:00.000Z')
    // Assert the SHOWN value too: checking only the request cannot see the
    // divergence this test exists to catch.
    const shown = (screen.getByLabelText(/from/i) as HTMLInputElement).value
    expect(new Date(shown).toISOString()).toBe(sent)
  })

  it('falls back to ingestion for an unknown tab AND says it did', async () => {
    resetLive()
    window.history.replaceState(null, '', '/observability?tab=nonsense')
    renderIntel(<ObservabilityView />)
    expect(
      await screen.findByRole('tab', { name: /ingestion/i, selected: true }),
    ).toBeInTheDocument()
    // Falling back silently is what made a bad link indistinguishable from a
    // good one.
    expect(screen.getByTestId('url-state-notice')).toBeInTheDocument()
  })

  it('says nothing when no tab is named at all', async () => {
    resetLive()
    window.history.replaceState(null, '', '/observability')
    renderIntel(<ObservabilityView />)
    await screen.findByRole('tab', { name: /ingestion/i, selected: true })
    expect(screen.queryByTestId('url-state-notice')).not.toBeInTheDocument()
  })
})

describe('ObservabilityView refuses an impossible trace bound', () => {
  // The hub's R5-01. This view kept the original opportunistic check while the
  // recordings bound was hardened four times, so `2026-02-30` was accepted here
  // and queried as the 2nd of March — a link showing one window and asking for
  // another, silently. Both views now share one parser.
  it('does not query the 2nd of March for the 30th of February', async () => {
    resetLive()
    window.history.replaceState(
      null,
      '',
      '/observability?tab=traces&from=2026-02-30',
    )
    setSearchStr('?tab=traces&from=2026-02-30')
    renderIntel(<ObservabilityView />)

    await waitFor(() => expect(api.traces).toHaveBeenCalled())
    expect(api.traces.mock.calls.at(-1)?.[0].from).toBeUndefined()
    expect(await screen.findByTestId('url-state-notice')).toHaveTextContent(
      /from/,
    )
  })
})
