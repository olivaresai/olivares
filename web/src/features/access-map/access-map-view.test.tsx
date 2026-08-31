// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { DiffResponse, GraphResponse } from './types'

// Permissions are toggled per test to exercise the RBAC boundary. `admin` gates the
// Remediation link (tenant:admin is the permission of the /console route it goes
// to — can() is membership of the effective set, not verb arithmetic).
const perm = { drift: true, admin: true, tenant: 't1' as string | null }
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({
    activeTenant: perm.tenant,
    can: (p: string) => {
      if (p === 'accessmap:drift:read') return perm.drift
      if (p === 'tenant:admin') return perm.admin
      return true
    },
  }),
}))

const navigate = vi.fn()
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
}))

vi.mock('./api', () => ({
  accessMapApi: { graph: vi.fn(), drift: vi.fn(), neighbors: vi.fn() },
  accessMapKeys: {
    all: (t: string | null) => ['am', t],
    graph: (t: string | null, p?: unknown) => ['am', t, 'g', p ?? null],
    drift: (t: string | null, p?: unknown) => ['am', t, 'd', p ?? null],
    neighbors: (t: string | null, id: string, d: string) => [
      'am',
      t,
      'n',
      id,
      d,
    ],
  },
}))

// The React Flow canvas is heavy and not the unit under test here — stub it but keep
// rendering its overlay children (legend) so the view wiring is exercised. The stub also
// exposes ONE button per edge that calls onEdgeClick: without it the drift list is the only
// selectable path in this suite, and the graph path — where an edge that was never drift
// gets selected — could not be exercised at all.
vi.mock('@/features/shared', async (orig) => {
  const actual = await orig<typeof import('@/features/shared')>()
  return {
    ...actual,
    GraphCanvas: ({
      children,
      nodes,
      edges,
      onNodeClick,
      onEdgeClick,
      fitMinZoom,
    }: {
      children?: ReactNode
      nodes?: { id: string; data?: unknown }[]
      edges?: { id: string }[]
      onNodeClick?: (n: { id: string; data?: unknown }) => void
      onEdgeClick?: (e: { id: string }) => void
      fitMinZoom?: number
    }) => (
      <div data-testid="graph-canvas" data-fit-min-zoom={fitMinZoom ?? ''}>
        {/* NODE buttons exist so onExpand is reachable at all. Without them the neighbours
            path — the one that WRITES graph state across a tenant switch — could not be
            exercised, and a case written against it would pass vacuously. It did: the first
            version of the tenant-expansion case below survived its own mutant. */}
        {(nodes ?? []).map((n) => (
          <button key={n.id} type="button" onClick={() => onNodeClick?.(n)}>
            {`graph-node-${n.id}`}
          </button>
        ))}
        {(edges ?? []).map((e) => (
          <button key={e.id} type="button" onClick={() => onEdgeClick?.(e)}>
            {`graph-edge-${e.id}`}
          </button>
        ))}
        {children}
      </div>
    ),
  }
})

import { ApiError } from '@/lib/api/errors'
import { accessMapApi } from './api'
import { AccessMapView } from './access-map-view'
import { buildGraph, emptyFilter } from './graph-model'

const graph: GraphResponse = {
  nodes: [
    { id: 'A1', kind: 'agent', ref: 'agent-1' },
    { id: 'R2', kind: 'postgres.table', ref: 'appdb.public.secrets' },
  ],
  edges: [
    {
      id: 'e2',
      origin_kind: 'agent',
      origin_id: 'A1',
      origin_ref: 'agent-1',
      resource_id: 'R2',
      resource_kind: 'postgres.table',
      resource_ref: 'appdb.public.secrets',
      mode: 'readwrite',
      signal_source: 'otel',
      signal_sources: 'otel,pg_audit',
      confidence: 'attributed',
      bridged: true,
      observed: true,
      permitted: false,
      occurrence_count: 12,
      first_seen: '2026-06-03T12:00:00Z',
      last_seen: '2026-06-03T12:30:00Z',
    },
    {
      id: 'e-clean',
      origin_kind: 'agent',
      origin_id: 'A1',
      origin_ref: 'agent-1',
      resource_id: 'R3',
      resource_kind: 'postgres.table',
      resource_ref: 'appdb.public.clean',
      mode: 'read',
      signal_source: 'policy',
      signal_sources: 'otel,policy',
      confidence: 'attributed',
      bridged: true,
      // Observed AND permitted: a healthy edge a complete diff correctly omits.
      observed: true,
      permitted: true,
      occurrence_count: 3,
      first_seen: '2026-06-03T12:00:00Z',
      last_seen: '2026-06-03T12:30:00Z',
    },
  ],
  has_more: false,
}

/** A SECOND finding, permitted by a scoped grant — used to prove a re-check verdict
 * never leaks from the edge it was measured on to the next one selected. */
const grantEdge = {
  ...graph.edges[0]!,
  id: 'e9',
  origin_ref: 'etl_role',
  resource_ref: 'appdb.public.billing',
  signal_source: 'scoped_grant',
  signal_sources: 'scoped_grant',
  observed: false,
  permitted: true,
}

const diff: DiffResponse = {
  unexpected_accesses: [
    {
      kind: 'unexpected_access',
      reconciliation_pending: false,
      edge: graph.edges[0]!,
    },
  ],
  unused_grants: [{ kind: 'unused_grant', edge: grantEdge }],
  unexpected_count: 1,
  unused_count: 1,
}

function renderView() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <AccessMapView />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  perm.drift = true
  perm.admin = true
  perm.tenant = 't1'
  navigate.mockClear()
  vi.mocked(accessMapApi.graph).mockResolvedValue(graph)
  vi.mocked(accessMapApi.drift).mockResolvedValue(diff)
  vi.mocked(accessMapApi.neighbors).mockReset()
  vi.mocked(accessMapApi.neighbors).mockResolvedValue({
    nodes: [],
    edges: [],
    has_more: false,
  })
})

/** Open the overlay and select the single unexpected-access finding. */
async function openFinding(user: ReturnType<typeof userEvent.setup>) {
  const driftBtn = await screen.findByRole('button', {
    name: /permitted vs observed/i,
  })
  await user.click(driftBtn)
  const row = await screen.findByRole('button', {
    name: /appdb\.public\.secrets/i,
  })
  await user.click(row)
}

describe('AccessMapView', () => {
  it('offers the ceremony, not the accusation, when the graph is refused for ASSURANCE', async () => {
    // ⛔ Los dos 403 satisfacen `isForbidden` (errors.ts:59 es sólo el status).
    // Leyéndolo primero, esta pantalla borraba su cuerpo entero y dejaba como
    // único control un refresh que devuelve el mismo 403 para siempre: un
    // callejón sin salida sobre un permiso que el operador SÍ tiene.
    vi.mocked(accessMapApi.graph).mockRejectedValue(
      new ApiError(403, 'step_up_required', 'assurance level too low'),
    )
    renderView()

    // Ancla POSITIVA primero: la ceremonia está en pantalla. Sin ella, la
    // ausencia de abajo se cumpliría en el primer tick, con el defecto puesto.
    expect(
      await screen.findByText(/step-up|verification|verificación/i),
    ).toBeInTheDocument()
    expect(
      screen.queryByText(/access map not authorized/i),
    ).not.toBeInTheDocument()
  })

  it('keeps the role refusal when the graph is refused for ROLE', async () => {
    // El control negativo del anterior: un 403 SIN código de ceremonia sigue
    // pintando la negativa de rol, que es cierta y no se toca.
    vi.mocked(accessMapApi.graph).mockRejectedValue(
      new ApiError(403, 'forbidden', 'no'),
    )
    renderView()
    expect(
      await screen.findByText(/access map not authorized/i),
    ).toBeInTheDocument()
  })

  it('renders the map header and graph once data loads', async () => {
    renderView()
    expect(await screen.findByText('Access map')).toBeInTheDocument()
    // The privileged/audited copy must be present (docs/SECURITY-HARDENING.md,§8).
    expect(screen.getByText(/privileged, audited action/i)).toBeInTheDocument()
    expect(await screen.findByTestId('graph-canvas')).toBeInTheDocument()
  })

  it('keeps a failed neighbors read visible and lets the operator retry it', async () => {
    const user = userEvent.setup()
    vi.mocked(accessMapApi.neighbors).mockReset()
    vi.mocked(accessMapApi.neighbors).mockRejectedValueOnce(
      new Error('neighbors unavailable'),
    )
    vi.mocked(accessMapApi.neighbors).mockResolvedValueOnce({
      nodes: [{ id: 'N2', kind: 'agent', ref: 'agent-neighbor' }],
      edges: [],
      has_more: false,
    })
    renderView()

    await user.click(
      await screen.findByRole('button', { name: 'graph-node-A1' }),
    )
    await user.click(screen.getByRole('button', { name: /expand/i }))
    await waitFor(() => expect(accessMapApi.neighbors).toHaveBeenCalledTimes(1))
    const dialog = screen.getByRole('dialog')
    expect(
      await within(dialog).findByText('Something went wrong'),
    ).toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(accessMapApi.neighbors).toHaveBeenCalledTimes(2))
    await waitFor(() =>
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
    )
  })

  it('reveals permitted-vs-observed findings when the overlay is toggled on', async () => {
    const user = userEvent.setup()
    renderView()
    const driftBtn = await screen.findByRole('button', {
      name: /permitted vs observed/i,
    })
    await user.click(driftBtn)
    // The drift side panel and the unexpected finding (observed, not permitted).
    expect(await screen.findByText('Least-privilege drift')).toBeInTheDocument()
    // The unexpected-access row surfaces the touched resource ref (unique to it).
    expect(await screen.findByText('appdb.public.secrets')).toBeInTheDocument()
    await waitFor(() => expect(accessMapApi.drift).toHaveBeenCalled())
  })

  it('disables the drift overlay when the role lacks accessmap:drift:read', async () => {
    perm.drift = false
    renderView()
    const driftBtn = await screen.findByRole('button', {
      name: /permitted vs observed/i,
    })
    expect(driftBtn).toBeDisabled()
  })
})

/**
 * — the four-step loop. Before this, selecting a finding opened a sheet that ended
 * at "only metadata is shown" with NO action: the map explained the drift and led
 * nowhere. These cases pin each step, including the two failure directions that would
 * otherwise report a fix nobody measured.
 */
describe('AccessMapView — from a finding to where it is fixed', () => {
  it('explains the authority of a finding and offers the surface that owns it', async () => {
    const user = userEvent.setup()
    renderView()
    await openFinding(user)

    // Step 2 — the honest explanation for an observed, non-permitted edge: nothing
    // authorises it. The sheet must NOT claim some grant produced it.
    expect(
      await screen.findByText(/No permit signal is present in this map/i),
    ).toBeVisible()

    // Step 3 — the way out. Before the feature had zero Link/navigate targets.
    const action = await screen.findByRole('button', {
      name: /open source bindings/i,
    })
    await user.click(action)
    expect(navigate).toHaveBeenCalledWith(
      expect.objectContaining({
        to: '/console',
        search: { tab: 'bindings' },
      }),
    )
  })

  it('re-asks the engine and reports CLEAR only when the fresh drift no longer has the edge', async () => {
    const user = userEvent.setup()
    renderView()
    await openFinding(user)

    // Step 4 — verify by re-consulting. The second response is the fixed estate.
    vi.mocked(accessMapApi.drift).mockResolvedValue({
      unexpected_accesses: [],
      unused_grants: [],
      unexpected_count: 0,
      unused_count: 0,
    })
    await user.click(
      screen.getByRole('button', { name: /re-check the drift/i }),
    )

    expect(await screen.findByText(/no longer in the drift set/i)).toBeVisible()
  })

  it('reports STILL PRESENT when the re-check finds the edge again', async () => {
    // The non-firing direction: a re-check that always said "clear" would pass the
    // case above while telling every operator their finding was fixed.
    const user = userEvent.setup()
    renderView()
    await openFinding(user)

    await user.click(
      screen.getByRole('button', { name: /re-check the drift/i }),
    )
    expect(await screen.findByText(/still in the drift set/i)).toBeVisible()
  })

  it('says COULD NOT CHECK — never clear — when the re-fetch fails', async () => {
    const user = userEvent.setup()
    renderView()
    await openFinding(user)

    vi.mocked(accessMapApi.drift).mockRejectedValue(new Error('engine down'))
    await user.click(
      screen.getByRole('button', { name: /re-check the drift/i }),
    )

    expect(await screen.findByText(/Could not confirm/i)).toBeVisible()
    // The distinction is the whole point: a failed read must not read as a fix.
    expect(screen.queryByText(/no longer in the drift set/i)).toBeNull()
  })

  it('never carries a verdict from one edge over to the next edge selected', async () => {
    // A verdict belongs to the edge it was measured on. Without the edgeId guard,
    // opening a second finding would inherit the first one's "clear" — a fix reported
    // for an edge that was never checked.
    const user = userEvent.setup()
    renderView()
    await openFinding(user)

    vi.mocked(accessMapApi.drift).mockResolvedValue({
      unexpected_accesses: [],
      unused_grants: [{ kind: 'unused_grant', edge: grantEdge }],
      unexpected_count: 0,
      unused_count: 1,
    })
    await user.click(
      screen.getByRole('button', { name: /re-check the drift/i }),
    )
    expect(await screen.findByText(/no longer in the drift set/i)).toBeVisible()

    // Now open the OTHER finding: it has never been re-checked. The sheet is modal, so
    // the list behind it is aria-hidden until it closes.
    await user.click(screen.getByRole('button', { name: /^close$/i }))
    await user.click(
      await screen.findByRole('button', { name: /appdb\.public\.billing/i }),
    )
    await waitFor(() =>
      expect(screen.queryByText(/no longer in the drift set/i)).toBeNull(),
    )
    expect(screen.queryByText(/still in the drift set/i)).toBeNull()
  })

  it('sends a scoped-grant edge to the source-binding plane that owns it', async () => {
    const user = userEvent.setup()
    renderView()
    const driftBtn = await screen.findByRole('button', {
      name: /permitted vs observed/i,
    })
    await user.click(driftBtn)
    await user.click(
      await screen.findByRole('button', { name: /appdb\.public\.billing/i }),
    )

    expect(
      await screen.findByText(/A source-to-scope binding recorded the permit/i),
    ).toBeVisible()
    expect(
      screen.getByRole('button', { name: /open source bindings/i }),
    ).toBeVisible()
  })

  it('will not call a finding fixed when the fresh drift window was truncated', async () => {
    // End-to-end companion to the unit case: the engine answered, the edge is gone from
    // the arrays, but the diff was reconciled over a PARTIAL window — so the honest
    // answer is "could not check", not "fixed".
    const user = userEvent.setup()
    renderView()
    await openFinding(user)

    vi.mocked(accessMapApi.drift).mockResolvedValue({
      unexpected_accesses: [],
      unused_grants: [],
      unexpected_count: 0,
      unused_count: 0,
      truncated: true,
    })
    await user.click(
      screen.getByRole('button', { name: /re-check the drift/i }),
    )

    expect(await screen.findByText(/Could not confirm/i)).toBeVisible()
    expect(screen.queryByText(/no longer in the drift set/i)).toBeNull()
  })

  it('labels a truncated drift panel as partial rather than showing it as the estate', async () => {
    vi.mocked(accessMapApi.drift).mockResolvedValue({
      ...diff,
      truncated: true,
    })
    const user = userEvent.setup()
    renderView()
    await user.click(
      await screen.findByRole('button', { name: /permitted vs observed/i }),
    )
    expect(await screen.findByText(/Partial result/i)).toBeVisible()
  })

  it('does NOT label the panel partial for a complete window', async () => {
    // Non-firing direction: a banner shown always would be noise and would teach
    // operators to ignore it on the one run where it matters.
    const user = userEvent.setup()
    renderView()
    await user.click(
      await screen.findByRole('button', { name: /permitted vs observed/i }),
    )
    await screen.findByText('Least-privilege drift')
    expect(screen.queryByText(/Partial result/i)).toBeNull()
  })

  it('offers no re-check on an edge that was never drift', async () => {
    // A complete diff correctly omits a normal permitted+observed edge, so a re-check would
    // have answered "no longer in the drift set" about an edge that never was in it.
    const user = userEvent.setup()
    renderView()
    await user.click(
      await screen.findByRole('button', { name: /permitted vs observed/i }),
    )
    await user.click(
      await screen.findByRole('button', { name: 'graph-edge-e-clean' }),
    )
    expect(
      screen.queryByRole('button', { name: /re-check the drift/i }),
    ).toBeNull()
  })

  it('drops the selection and any verdict when the active tenant changes', async () => {
    // The queries are keyed by tenant but selection and verdict were tenantless local state,
    // and the tenant switcher does not remount this route. Re-checking after a switch would
    // read tenant B's diff, not find tenant A's edge, and report A's finding CLEARED —
    // graded against an estate it never looked at. Found by the the model contrast.
    const user = userEvent.setup()
    const { rerender } = renderView()
    await openFinding(user)
    expect(
      await screen.findByText(/No permit signal is present in this map/i),
    ).toBeVisible()

    perm.tenant = 't2'
    rerender(
      <QueryClientProvider client={new QueryClient()}>
        <AccessMapView />
      </QueryClientProvider>,
    )
    await waitFor(() =>
      expect(
        screen.queryByText(/No permit signal is present in this map/i),
      ).toBeNull(),
    )
  })

  it('never calls a truncated empty window a clean estate', async () => {
    // Two empty arrays out of a PARTIAL window mean "nothing in the scanned window", not
    // "every observed access is permitted". The page cap is 50k raw drift rows and
    // permitted-only inventory grants can fill it while a real violation sits past it.
    vi.mocked(accessMapApi.drift).mockResolvedValue({
      unexpected_accesses: [],
      unused_grants: [],
      unexpected_count: 0,
      unused_count: 0,
      truncated: true,
    })
    const user = userEvent.setup()
    renderView()
    await user.click(
      await screen.findByRole('button', { name: /permitted vs observed/i }),
    )
    expect(
      await screen.findByText(/No findings in the scanned window/i),
    ).toBeVisible()
    expect(screen.queryByText(/Every observed access is permitted/i)).toBeNull()
  })

  it('does call a COMPLETE empty window clean', async () => {
    // Non-firing direction: the clean state must still be reachable, or the product would
    // never be able to say a tenant is in order.
    vi.mocked(accessMapApi.drift).mockResolvedValue({
      unexpected_accesses: [],
      unused_grants: [],
      unexpected_count: 0,
      unused_count: 0,
    })
    const user = userEvent.setup()
    renderView()
    await user.click(
      await screen.findByRole('button', { name: /permitted vs observed/i }),
    )
    expect(
      await screen.findByText(/Every observed access is permitted/i),
    ).toBeVisible()
  })

  it('names the missing permission instead of offering a link that lands on Forbidden', async () => {
    perm.admin = false
    const user = userEvent.setup()
    renderView()
    await openFinding(user)

    expect(
      screen.queryByRole('button', { name: /open source bindings/i }),
    ).toBeNull()
    expect(await screen.findByText(/tenant:admin/)).toBeVisible()
  })
})

/**
 * — THE HELPER ABOVE ALWAYS OPENS THE OVERLAY FIRST, and that is precisely why the
 * defect below survived a suite that looked thorough. `openFinding` toggles the drift panel
 * on before it clicks, so every case in this file had a successful /drift read in hand; the
 * DEFAULT path an operator takes — land on the map, click an edge — was never exercised with
 * the panel closed. These cases select from the graph instead.
 */
describe('AccessMapView — the sheet does not claim a finding it never read', () => {
  it('says the drift was NOT READ, and offers the read rather than a remedy', async () => {
    const user = userEvent.setup()
    renderView()
    // Overlay OFF (the default). e2 is observed-and-not-permitted, so before this change
    // the sheet called it a confirmed finding and handed out remediation links — while the
    // engine may hold it reconciliation_pending, which only /drift ever says.
    await user.click(
      await screen.findByRole('button', { name: 'graph-edge-e2' }),
    )
    expect(
      await screen.findByText(/drift state has not been established/i),
    ).toBeVisible()
    expect(
      screen.queryByText(/No permit signal is present in this map/i),
    ).toBeNull()
    expect(
      screen.queryByRole('button', { name: /open source bindings/i }),
    ).toBeNull()

    // The offered action is the one that RESOLVES the uncertainty, not one that acts on it.
    await user.click(
      screen.getByRole('button', { name: /read the drift for this edge/i }),
    )
    await waitFor(() => expect(accessMapApi.drift).toHaveBeenCalled())
    // ...and once read, the same edge IS the finding, with its remedy. Without this half the
    // case above would pass on a sheet that had simply stopped working.
    expect(
      await screen.findByText(/No permit signal is present in this map/i),
    ).toBeVisible()
    expect(
      screen.getByRole('button', { name: /open source bindings/i }),
    ).toBeVisible()
  })

  it('names accessmap:drift:read for a graph reader who can never make that read', async () => {
    // The overlay is disabled for this role, so there is no action to offer — but the sheet
    // still must not present an unread edge as a confirmed violation.
    perm.drift = false
    const user = userEvent.setup()
    renderView()
    await user.click(
      await screen.findByRole('button', { name: 'graph-edge-e2' }),
    )
    expect(
      await screen.findByText(/drift state has not been established/i),
    ).toBeVisible()
    expect(
      screen.queryByRole('button', { name: /read the drift for this edge/i }),
    ).toBeNull()
    expect(await screen.findByText(/accessmap:drift:read/)).toBeVisible()
    expect(
      screen.queryByRole('button', { name: /open source bindings/i }),
    ).toBeNull()
  })

  it('does not tell an operator whose read FAILED that the diff was never requested', async () => {
    // The regression this copy kept re-acquiring: every attempt to explain WHY the state
    // arises left a case out. DRIFT_UNREAD covers not-requested, not-permitted, in-flight and
    // FAILED alike, so any enumeration on screen is a claim about which one happened. Here
    // the read genuinely failed; the sheet must describe the state and name no cause.
    const user = userEvent.setup()
    renderView()
    await user.click(
      await screen.findByRole('button', { name: 'graph-edge-e2' }),
    )
    // Reject BEFORE the read is issued, so the failure IS this edge's drift state.
    vi.mocked(accessMapApi.drift).mockRejectedValue(new Error('engine down'))
    await user.click(
      screen.getByRole('button', { name: /read the drift for this edge/i }),
    )
    await waitFor(() => expect(accessMapApi.drift).toHaveBeenCalled())

    const explanation = await screen.findByText(
      /drift state has not been established/i,
    )
    expect(explanation).toBeVisible()
    // The two causes the copy used to enumerate, neither of which happened here.
    expect(explanation.textContent).not.toMatch(
      /never requested|not requested/i,
    )
    expect(explanation.textContent).not.toMatch(/part of the estate|truncat/i)
  })

  it('NON-FIRING: a permitted edge still explains itself with the overlay closed', async () => {
    // The unread guard belongs to the observed-without-permit branch alone. `signal_sources`
    // rides on the graph DTO, so suppressing the whole section would be the opposite
    // over-correction — and a sheet that explained nothing would pass both cases above.
    const user = userEvent.setup()
    renderView()
    await user.click(
      await screen.findByRole('button', { name: 'graph-edge-e-clean' }),
    )
    expect(
      await screen.findByText(/A declared permit signal was recorded/i),
    ).toBeVisible()
    expect(screen.queryByText(/has not been read/i)).toBeNull()
  })
})

describe('AccessMapView — a verdict never outlives the context it was measured in', () => {
  it('never merges a tenant A expansion into tenant B', async () => {
    // The re-check guard did not cover this, and this failure is worse than a lost verdict:
    // it WRITES graph state. A neighbours call started in A closes over A's `merged`; the
    // switch clears `extra`, so the late continuation finds `prev === null` and merges A's
    // whole captured graph plus A's response into B. Found by the second the model
    // contrast — "closed for settled state and for recheck" was not closed for every async
    // write of that state.
    const user = userEvent.setup()
    let release!: (g: GraphResponse) => void
    vi.mocked(accessMapApi.neighbors).mockImplementation(
      () => new Promise<GraphResponse>((res) => (release = res)),
    )
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const view = () => (
      <QueryClientProvider client={qc}>
        <AccessMapView />
      </QueryClientProvider>
    )
    const { rerender } = render(view())

    // Select a NODE and expand it: that is the only path that calls onExpand.
    await user.click(
      await screen.findByRole('button', { name: 'graph-node-A1' }),
    )
    await user.click(await screen.findByRole('button', { name: /expand/i }))
    await waitFor(() => expect(accessMapApi.neighbors).toHaveBeenCalled())

    // Away, while the expansion is genuinely in flight.
    perm.tenant = 't2'
    rerender(view())

    // Tenant A's subgraph now answers. It must not appear in tenant B's canvas.
    release({
      nodes: [{ id: 'A-ONLY', kind: 'agent', ref: 'tenant-a-only' }],
      edges: [{ ...graph.edges[0]!, id: 'edge-from-tenant-a' }],
      has_more: false,
    })

    await waitFor(() =>
      expect(
        screen.getByRole('button', { name: 'graph-edge-e2' }),
      ).toBeVisible(),
    )
    expect(
      screen.queryByRole('button', { name: 'graph-edge-edge-from-tenant-a' }),
    ).toBeNull()
    expect(
      screen.queryByRole('button', { name: 'graph-node-A-ONLY' }),
    ).toBeNull()
  })

  it('discards a re-check that completes after the operator left and came back', async () => {
    // Dropping the state on a tenant switch cannot reach a promise that is ALREADY awaiting.
    // Its continuation runs later and writes a verdict for the edge it closed over.
    //
    // This is the A→B→A shape, and it is why the guard counts CONTEXTS rather than comparing
    // tenant ids: on return the tenant matches again, so a tenant comparison would let the
    // stale verdict through. Nothing here is fabricated — the edge is t1's own e2, selected
    // in t1, and the answer applied to it was resolved while the sheet was showing t2.
    const user = userEvent.setup()
    // Every /drift call parks here until the case chooses to answer it, so the switch can
    // happen with a re-check genuinely in flight rather than simulated after the fact.
    const parked: ((d: DiffResponse) => void)[] = []
    vi.mocked(accessMapApi.drift).mockImplementation(
      () =>
        new Promise<DiffResponse>((res) => {
          parked.push(res)
        }),
    )
    const flush = (d: DiffResponse) => {
      const waiting = parked.splice(0, parked.length)
      for (const res of waiting) res(d)
    }
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    })
    const view = () => (
      <QueryClientProvider client={qc}>
        <AccessMapView />
      </QueryClientProvider>
    )
    const { rerender } = render(view())

    // Land the FIRST drift read so the finding is selectable.
    await user.click(
      await screen.findByRole('button', { name: /permitted vs observed/i }),
    )
    await waitFor(() => expect(parked.length).toBeGreaterThan(0))
    flush(diff)
    await user.click(
      await screen.findByRole('button', { name: /appdb\.public\.secrets/i }),
    )
    // The re-check now parks: it is in flight for the rest of this case.
    await user.click(
      screen.getByRole('button', { name: /re-check the drift/i }),
    )
    await waitFor(() => expect(parked.length).toBeGreaterThan(0))

    // Away and back while it is still in flight.
    perm.tenant = 't2'
    rerender(view())
    perm.tenant = 't1'
    rerender(view())

    // Everything still outstanding now answers with an estate that has no such finding.
    flush({
      unexpected_accesses: [],
      unused_grants: [],
      unexpected_count: 0,
      unused_count: 0,
    })

    // Re-select the very same edge back in t1. It must carry NO verdict: the only answer
    // this session ever got for it was measured somewhere else.
    await user.click(
      await screen.findByRole('button', { name: 'graph-edge-e2' }),
    )
    await waitFor(() =>
      expect(screen.queryByText(/no longer in the drift set/i)).toBeNull(),
    )
    expect(screen.queryByText(/still in the drift set/i)).toBeNull()
  })
})

describe('AccessMapView — one claim, painted the same everywhere', () => {
  it('does not ring the resource in danger when its only finding is PENDING', async () => {
    // Fourth independent derivation of "this is a finding", found by the second contrast:
    // the resource node folded `pending` in with `unexpected`, so it got the danger ring
    // while its OWN edge was drawn amber two pixels away. Amber that can be overruled by a
    // red halo on the thing it points at is not amber.
    vi.mocked(accessMapApi.drift).mockResolvedValue({
      ...diff,
      unexpected_accesses: [
        {
          kind: 'unexpected_access',
          reconciliation_pending: true,
          edge: graph.edges[0]!,
        },
      ],
    })
    const user = userEvent.setup()
    renderView()
    await user.click(
      await screen.findByRole('button', { name: /permitted vs observed/i }),
    )
    await screen.findByText('Least-privilege drift')
    expect(
      buildGraph({
        graph,
        diff: {
          ...diff,
          unexpected_accesses: [
            {
              kind: 'unexpected_access',
              reconciliation_pending: true,
              edge: graph.edges[0]!,
            },
          ],
        },
        overlay: true,
        filter: emptyFilter(),
      }).nodes.find((n) => n.id === 'R2')?.data,
    ).toMatchObject({ hasUnexpected: false })
  })

  it('NON-FIRING: a FIRM unexpected access still rings its resource in danger', async () => {
    // The direction that stops the fix from disarming the product's whole visual "aha".
    expect(
      buildGraph({
        graph,
        diff,
        overlay: true,
        filter: emptyFilter(),
      }).nodes.find((n) => n.id === 'R2')?.data,
    ).toMatchObject({ hasUnexpected: true })
  })
})

describe('AccessMapView — the canvas stops claiming what it can no longer confirm', () => {
  it('drops the finding colouring when a refetch fails over retained cache', async () => {
    // react-query keeps the last good data after a failed refetch, so without the isSuccess
    // gate the canvas keeps painting findings it can no longer confirm — while the sheet,
    // which does check, has already degraded to "not read". Two surfaces disagreeing about
    // whether the estate is in drift is the defect, whichever one happens to be right.
    const user = userEvent.setup()
    renderView()
    await user.click(
      await screen.findByRole('button', { name: /permitted vs observed/i }),
    )
    // The unexpected-count chip is the canvas's own claim, drawn from the same built model.
    expect(await screen.findByText('Unexpected')).toBeVisible()

    vi.mocked(accessMapApi.drift).mockRejectedValue(new Error('engine down'))
    await user.click(screen.getByRole('button', { name: /refresh/i }))

    await waitFor(() => expect(screen.queryByText('Unexpected')).toBeNull())
  })

  it('does not report an EMPTY MAP when the diff that populated it failed to revalidate', async () => {
    // The stale-empty my own isSuccess gate introduced. Unused grants enter the model only
    // from the diff, so when they were the only thing on screen a failed refetch emptied the
    // canvas — and the empty branch replaces the whole body, hiding even the drift panel's
    // retry. "There is nothing here" is not what "I could not revalidate" means.
    vi.mocked(accessMapApi.graph).mockResolvedValue({
      nodes: [],
      edges: [],
      has_more: false,
    })
    const user = userEvent.setup()
    renderView()
    await user.click(
      await screen.findByRole('button', { name: /permitted vs observed/i }),
    )
    await screen.findByText('Least-privilege drift')

    vi.mocked(accessMapApi.drift).mockRejectedValue(new Error('engine down'))
    await user.click(screen.getByRole('button', { name: /refresh/i }))

    // The estate-empty claim must not appear while the read it rests on is failing...
    await waitFor(() =>
      expect(screen.queryByText(/No access observed yet/i)).toBeNull(),
    )
    // ...and the point of not making it is that the retry stays reachable. Asserting only
    // the absence would pass on a blank screen, which is not what "keep the body" means.
    expect(
      await screen.findByRole('button', { name: /retry|try again/i }),
    ).toBeVisible()
  })

  it('NON-FIRING: a genuinely empty graph still reports itself empty', async () => {
    // Otherwise the guard above would suppress the honest empty state forever.
    vi.mocked(accessMapApi.graph).mockResolvedValue({
      nodes: [],
      edges: [],
      has_more: false,
    })
    renderView()
    expect(await screen.findByText(/No access observed yet/i)).toBeVisible()
  })

  it('NON-FIRING: a successful read still colours the canvas', async () => {
    // Otherwise a gate that dropped the diff unconditionally would pass the case above and
    // make the overlay useless.
    const user = userEvent.setup()
    renderView()
    await user.click(
      await screen.findByRole('button', { name: /permitted vs observed/i }),
    )
    expect(await screen.findByText('Unexpected')).toBeVisible()
  })
})

describe('AccessMapView — the headline never contradicts the explanation', () => {
  it('does not shout a red finding over an edge whose drift state was never read', async () => {
    // The banner used to be a raw `observed && !permitted` computed independently of the
    // section below it, so the sheet said "Observed without a matching grant" in danger red
    // while its own explanation said the diff had not been read. Two derivations of the same
    // question always disagree eventually, and the louder one wins with the operator.
    const user = userEvent.setup()
    renderView()
    await user.click(
      await screen.findByRole('button', { name: 'graph-edge-e2' }),
    )
    expect(
      await screen.findByText(/drift state has not been established/i),
    ).toBeVisible()
    expect(screen.queryByText('Observed without a matching grant')).toBeNull()
  })

  it('keeps a PENDING finding amber, never red — the mirror contract says so', async () => {
    vi.mocked(accessMapApi.drift).mockResolvedValue({
      ...diff,
      unexpected_accesses: [
        {
          kind: 'unexpected_access',
          reconciliation_pending: true,
          edge: graph.edges[0]!,
        },
      ],
    })
    const user = userEvent.setup()
    renderView()
    await openFinding(user)
    expect(
      await screen.findByText(/cannot be decided yet for this edge/i),
    ).toBeVisible()
    expect(screen.queryByText('Observed without a matching grant')).toBeNull()
  })

  it('NON-FIRING: a CONFIRMED unexpected access is still headlined in red', async () => {
    // The direction that stops the fix from silencing the product's whole "aha".
    const user = userEvent.setup()
    renderView()
    await openFinding(user)
    expect(
      await screen.findByText('Observed without a matching grant'),
    ).toBeVisible()
  })

  it('does not blame the identity link for every pending finding', async () => {
    // The engine marks pending for THREE conditions and does not say which
    // (modules/access-map/query.go:216-225, :290-329). The sheet was corrected for this and
    // the side list was not, so the same false cause survived in the panel read first.
    vi.mocked(accessMapApi.drift).mockResolvedValue({
      ...diff,
      unexpected_accesses: [
        {
          kind: 'unexpected_access',
          reconciliation_pending: true,
          edge: graph.edges[0]!,
        },
      ],
    })
    const user = userEvent.setup()
    renderView()
    await user.click(
      await screen.findByRole('button', { name: /permitted vs observed/i }),
    )
    expect(await screen.findByText(/unknown grant mode/i)).toBeVisible()
  })
})

/**
 * — WHAT THE RE-CHECK CAN EVER SAY. The store OR-merges observed and permitted forever
 * (core/internal/store/sqlstore/accessgraph.go:162-163) and drift is `permitted <> observed`
 * (accessgraph.go:82), so the two halves of the drift set behave oppositely under the same
 * remediation. Step 4 was offered identically on both.
 *
 * ⚠ The claim is about the OPERATION, not the future of /drift — narrowed after the second
 * the model contrast, which was right that the first wording over-reached in BOTH
 * directions. An unused grant CAN still leave the diff for an unrelated reason (`observed`
 * also OR-merges, so the access finally being seen removes it), and recording a permit is
 * not guaranteed to reach this edge (source-scope projection is best-effort, and group,
 * role and folder bindings enforce while projecting nothing — sourcescope/accessmap.go:47-81).
 * What holds is the narrow statement: the deletion does not retract the bit.
 */
describe('AccessMapView — the two halves answer step 4 differently', () => {
  it('warns that deleting an UNUSED GRANT does not clear the permitted flag', async () => {
    const user = userEvent.setup()
    renderView()
    await user.click(
      await screen.findByRole('button', { name: /permitted vs observed/i }),
    )
    await user.click(
      await screen.findByRole('button', { name: /appdb\.public\.billing/i }),
    )
    expect(
      await screen.findByText(
        /will not clear the permitted flag on this edge/i,
      ),
    ).toBeVisible()
  })

  it('tells an UNEXPECTED ACCESS the opposite, because for it the loop does close', async () => {
    // Non-firing direction, and the reason the warning has to be per-half: a screen that
    // showed "cannot close" everywhere would pass the case above and talk an operator out of
    // the one remediation that works.
    const user = userEvent.setup()
    renderView()
    await openFinding(user)
    expect(
      await screen.findByText(
        /Recording a permit is what clears this finding/i,
      ),
    ).toBeVisible()
    expect(
      screen.queryByText(/will not clear the permitted flag on this edge/i),
    ).toBeNull()
  })
})

describe('P1 — el mapa PIDE el suelo de zoom legible', () => {
  /**
   * ⛔ ESTE CASO VIGILA AL LLAMANTE, y existe porque su ausencia SOBREVIVIO a mi
   * propia bateria: quitar `fitMinZoom` de la vista dejaba los 110 casos del modulo
   * en verde. Es la misma clase que el F-01 del contraste `sol max` — la maquinaria
   * estaba bien y nadie comprobaba que se usara.
   *
   * Desde F-02 el suelo es OPT-IN (a health le recorta el overview con 15 nodos), asi
   * que «existe la opcion» y «esta vista la pide» son dos afirmaciones distintas y
   * hacen falta las dos.
   */
  it('la vista pasa un suelo legible a su lienzo', async () => {
    renderView()
    const canvas = await screen.findByTestId('graph-canvas')
    const suelo = Number(canvas.getAttribute('data-fit-min-zoom'))
    expect(
      suelo,
      'el access map dejo de pedir suelo: `fitView` volveria a encoger hasta ~0,12',
    ).toBeGreaterThanOrEqual(0.45)
    expect(suelo).toBeLessThanOrEqual(1)
  })
})
