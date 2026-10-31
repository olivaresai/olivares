// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
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

const recording = { enabled: false }
vi.mock('@/lib/hooks/use-server-info', () => ({
  useServerInfo: () => ({
    isSuccess: true,
    data: {
      edition: 'enterprise',
      modules_not_enabled: recording.enabled ? [] : ['recording'],
    },
  }),
}))
vi.mock('@/features/recordings/api', async (orig) => ({
  ...(await orig<typeof import('@/features/recordings/api')>()),
  recordingApi: { notice: vi.fn(), acknowledge: vi.fn() },
}))

// The view's state IS the address, so the router is the in-memory location double, not a
// stub that answers the empty string. `navigate` still records every call.
const navigate = vi.fn()
vi.mock('@tanstack/react-router', async () => {
  const fake = (await import('@/test/fake-router')).fakeRouterModule()
  const real = fake.useNavigate()
  return {
    ...fake,
    useNavigate: () => (args: unknown) => {
      navigate(args)
      return real(args as never)
    },
  }
})

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
        {/* React Flow's own wrapper is a focusable element with the node id on it: the
            view's arrow keys read both from the focused element. */}
        {(nodes ?? []).map((n) => (
          <div
            key={n.id}
            className="react-flow__node"
            data-id={n.id}
            data-testid={`rf-node-${n.id}`}
            tabIndex={0}
          >
            <button type="button" onClick={() => onNodeClick?.(n)}>
              {`graph-node-${n.id}`}
            </button>
          </div>
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

import { recordingApi } from '@/features/recordings/api'
import { ApiError } from '@/lib/api/errors'
import { fakeRouter } from '@/test/fake-router'
import { accessMapApi } from './api'
import { AccessMapView } from './access-map-view'
import { buildGraph, emptyFilter } from './graph-model'
import { connectedNeighbor } from './neighbor-nav'

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
  recording.enabled = false
  vi.mocked(recordingApi.notice).mockReset()
  perm.drift = true
  perm.admin = true
  perm.tenant = 't1'
  navigate.mockClear()
  fakeRouter.reset('/access-map')
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
  it('shows one privileged notice when access-map recording is enabled', async () => {
    recording.enabled = true
    vi.mocked(recordingApi.notice).mockResolvedValue({
      recorded_namespaces: ['accessmap'],
      breakglass_always: true,
      consent_mode: 'notice',
      consent_required: false,
      acknowledged: false,
      schema: 'olivares.recording/v1',
      semconv: '',
    })
    renderView()
    expect(
      await screen.findByText(/recorded privileged surface/i),
    ).toBeVisible()
    expect(screen.queryByText(/privileged, audited action/i)).toBeNull()
  })

  it.each(['unrecorded', 'loading', 'failed'])(
    'keeps one audit notice when the recording notice is %s',
    async (state) => {
      recording.enabled = true
      if (state === 'loading') {
        vi.mocked(recordingApi.notice).mockReturnValue(new Promise(() => {}))
      } else if (state === 'failed') {
        vi.mocked(recordingApi.notice).mockRejectedValue(
          new Error('Unavailable'),
        )
      } else {
        vi.mocked(recordingApi.notice).mockResolvedValue({
          recorded_namespaces: [],
          breakglass_always: true,
          consent_mode: 'notice',
          consent_required: false,
          acknowledged: false,
          schema: 'olivares.recording/v1',
          semconv: '',
        })
      }
      renderView()
      await screen.findByTestId('graph-canvas')
      expect(screen.getAllByText(/privileged, audited action/i)).toHaveLength(1)
      expect(screen.queryByText(/recorded privileged surface/i)).toBeNull()
    },
  )

  it('keeps recording consent in the single notice slot', async () => {
    recording.enabled = true
    vi.mocked(recordingApi.notice).mockResolvedValue({
      recorded_namespaces: ['accessmap'],
      breakglass_always: true,
      consent_mode: 'required',
      consent_required: true,
      acknowledged: false,
      schema: 'olivares.recording/v1',
      semconv: '',
    })
    renderView()
    expect(await screen.findByRole('dialog')).toBeVisible()
    expect(screen.getAllByText(/recorded privileged surface/i)).toHaveLength(1)
    expect(screen.queryByText(/privileged, audited action/i)).toBeNull()
    expect(
      screen.getByRole('button', { name: /acknowledge and continue/i }),
    ).toBeVisible()
  })

  it('offers the ceremony, not the accusation, when the graph is refused for ASSURANCE', async () => {
    // ⛔ Both 403s satisfy `isForbidden` (errors.ts:59 is only the status).
    // Reading it first, this screen erased its whole body and left a refresh that
    // returns the same 403 forever as the only control: a dead end over a
    // permission the operator DOES have.
    vi.mocked(accessMapApi.graph).mockRejectedValue(
      new ApiError(403, 'step_up_required', 'assurance level too low'),
    )
    renderView()

    // POSITIVE anchor first: the ceremony is on screen. Without it, the absence
    // below would hold on the first tick, with the defect in place.
    expect(await screen.findByText(/step-up|verification/i)).toBeInTheDocument()
    expect(
      screen.queryByText(/access map not authorized/i),
    ).not.toBeInTheDocument()
  })

  it('keeps the role refusal when the graph is refused for ROLE', async () => {
    // The negative control of the previous case: a 403 WITHOUT a ceremony code still
    // draws the role refusal, which is true and is left alone.
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

  it('offers a keyboard-reachable list of every node on the map', async () => {
    const user = userEvent.setup()
    renderView()
    const summary = await screen.findByText('Map as a list', {
      selector: 'summary',
    })
    await user.click(summary)
    const row = await screen.findByRole('button', { name: 'agent-1' })
    expect(row).toBeInTheDocument()
    // The row SELECTS; it does not navigate. Assert the EFFECT, not the tag:
    // a link to `#access-node-<id>` passed a role check while pointing at a
    // fragment that exists nowhere in the tree.
    await user.click(row)
    expect(
      await screen.findByText(
        /A graph node — expand to see what it connects to/i,
      ),
    ).toBeInTheDocument()
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
    expect(
      await screen.findByText(
        /needs admin access for this; this role does not hold it/,
      ),
    ).toBeVisible()
    expect(screen.queryByText(/tenant:admin/)).toBeNull()
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
    expect(
      await screen.findByText(
        /needs read access for this; this role does not hold it/,
      ),
    ).toBeVisible()
    expect(screen.queryByText(/accessmap:drift:read/)).toBeNull()
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

describe('P1 — the map ASKS FOR the legible zoom floor', () => {
  /**
   * ⛔ THIS CASE WATCHES THE CALLER, and it exists because its absence SURVIVED my
   * own battery: removing `fitMinZoom` from the view left the module's 110 cases
   * green. It is the same class as F-01 of the `sol max` contrast — the machinery
   * was fine and nobody checked that it was used.
   *
   * Since F-02 the floor is OPT-IN (it clips the health overview with 15 nodes), so
   * "the option exists" and "this view asks for it" are two different claims and
   * both are needed.
   */
  it('the view passes a legible floor to its canvas', async () => {
    renderView()
    const canvas = await screen.findByTestId('graph-canvas')
    const floor = Number(canvas.getAttribute('data-fit-min-zoom'))
    expect(
      floor,
      'the access map stopped asking for a floor: `fitView` would shrink to about 0.12 again',
    ).toBeGreaterThanOrEqual(0.45)
    expect(floor).toBeLessThanOrEqual(1)
  })
})

/**
 * #2914 — the view IS the address. Selection, filters, the drift overlay and the
 * expanded neighbors round-trip through the URL: a pasted link restores the same map,
 * Back leaves it, and expanding a node keeps the previous map one Back away.
 */
describe('AccessMapView — the view is the address', () => {
  const extraEdge = {
    ...graph.edges[0]!,
    id: 'e-extra',
    resource_id: 'R9',
    resource_ref: 'appdb.public.extra',
  }
  const neighborsOfA1 = {
    nodes: [{ id: 'R9', kind: 'postgres.table', ref: 'appdb.public.extra' }],
    edges: [extraEdge],
    has_more: false,
  }

  it('writes the selected node to ?node= and keeps history flat', async () => {
    const user = userEvent.setup()
    renderView()
    await user.click(
      await screen.findByRole('button', { name: 'graph-node-A1' }),
    )
    expect(fakeRouter.searchStr()).toBe('?node=A1')
    expect(fakeRouter.depth()).toBe(1)
    await user.keyboard('{Escape}')
    await waitFor(() => expect(fakeRouter.searchStr()).toBe(''))
  })

  it('restores a selected node from a pasted link', async () => {
    fakeRouter.reset('/access-map?node=A1')
    renderView()
    expect(
      await screen.findByText(
        /A graph node — expand to see what it connects to/i,
      ),
    ).toBeInTheDocument()
  })

  it('restores a selected edge from a pasted link, and writes ?edge= on a click', async () => {
    fakeRouter.reset('/access-map?edge=e2')
    const user = userEvent.setup()
    renderView()
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Access edge')).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(fakeRouter.searchStr()).toBe(''))
    await user.click(
      await screen.findByRole('button', { name: 'graph-edge-e-clean' }),
    )
    expect(fakeRouter.searchStr()).toBe('?edge=e-clean')
  })

  it('restores the overlay and a drift-only edge (an unused grant) from a link', async () => {
    fakeRouter.reset('/access-map?drift=1&edge=e9')
    renderView()
    const dialog = await screen.findByRole('dialog')
    expect(
      await within(dialog).findByText('appdb.public.billing'),
    ).toBeInTheDocument()
    expect(fakeRouter.searchStr()).toContain('edge=e9')
  })

  it('round-trips the search and the mode filter, and reads the legacy ?focus=', async () => {
    fakeRouter.reset('/access-map?q=secrets&mode=write,readwrite')
    const user = userEvent.setup()
    const { unmount } = renderView()
    const search = await screen.findByRole('textbox', {
      name: /search origin/i,
    })
    expect(search).toHaveValue('secrets')
    expect(screen.getByRole('button', { name: 'Write' })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
    unmount()

    fakeRouter.reset('/access-map?focus=agent-1&tab=keep')
    renderView()
    const seeded = await screen.findByRole('textbox', {
      name: /search origin/i,
    })
    expect(seeded).toHaveValue('agent-1')
    await user.type(seeded, 'x')
    // `q` replaces `focus`; a key this view does not own survives; history stays flat.
    await waitFor(() => expect(fakeRouter.searchStr()).toContain('q=agent-1x'))
    expect(fakeRouter.searchStr()).not.toContain('focus=')
    expect(fakeRouter.searchStr()).toContain('tab=keep')
    expect(fakeRouter.depth()).toBe(1)
  })

  it('writes the mode filter, the attributed filter and the overlay to the address', async () => {
    const user = userEvent.setup()
    renderView()
    await user.click(await screen.findByRole('button', { name: 'Read' }))
    await user.click(screen.getByRole('button', { name: 'Only attributed' }))
    await user.click(
      screen.getByRole('button', { name: /permitted vs observed/i }),
    )
    await waitFor(() =>
      expect(fakeRouter.searchStr()).toBe('?mode=read&conf=attributed&drift=1'),
    )
    expect(fakeRouter.depth()).toBe(1)
  })

  it('says so and drops a refused value instead of showing a different map', async () => {
    fakeRouter.reset('/access-map?mode=read,root&drift=yes')
    renderView()
    expect(await screen.findByTestId('url-state-notice')).toHaveTextContent(
      /mode, drift/,
    )
    await waitFor(() => expect(fakeRouter.searchStr()).toBe(''))
  })

  it('says so and clears a node or edge that is not on this map', async () => {
    fakeRouter.reset('/access-map?node=not-here')
    renderView()
    expect(await screen.findByTestId('url-state-notice')).toHaveTextContent(
      /node/,
    )
    await waitFor(() => expect(fakeRouter.searchStr()).toBe(''))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('keeps the previous map one Back away after expanding neighbors', async () => {
    const user = userEvent.setup()
    vi.mocked(accessMapApi.neighbors).mockResolvedValue(neighborsOfA1)
    fakeRouter.reset('/access-map?node=A1')
    renderView()
    await user.click(await screen.findByRole('button', { name: /expand/i }))

    expect(
      await screen.findByRole('button', { name: 'graph-node-R9' }),
    ).toBeInTheDocument()
    expect(fakeRouter.depth()).toBe(2)
    expect(fakeRouter.searchStr()).toBe('?expand=agent%3AA1')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    // ONE read: the click fetched it and the address then read the same cache.
    expect(accessMapApi.neighbors).toHaveBeenCalledTimes(1)
    expect(accessMapApi.neighbors).toHaveBeenCalledWith('A1', 'both', 'agent')

    act(() => fakeRouter.back())
    await waitFor(() =>
      expect(
        screen.queryByRole('button', { name: 'graph-node-R9' }),
      ).not.toBeInTheDocument(),
    )
    expect(
      await screen.findByText(
        /A graph node — expand to see what it connects to/i,
      ),
    ).toBeInTheDocument()
  })

  it('reads the expansions a pasted link names', async () => {
    vi.mocked(accessMapApi.neighbors).mockResolvedValue(neighborsOfA1)
    fakeRouter.reset('/access-map?expand=agent:A1')
    renderView()
    expect(
      await screen.findByRole('button', { name: 'graph-node-R9' }),
    ).toBeInTheDocument()
    expect(accessMapApi.neighbors).toHaveBeenCalledWith('A1', 'both', 'agent')
  })

  it('says an expansion could not be read, and retries it', async () => {
    const user = userEvent.setup()
    vi.mocked(accessMapApi.neighbors)
      .mockRejectedValueOnce(new Error('neighbors unavailable'))
      .mockResolvedValueOnce(neighborsOfA1)
    fakeRouter.reset('/access-map?expand=agent:A1')
    renderView()
    expect(
      await screen.findByText(/could not be read, so the map is incomplete/i),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(
      await screen.findByRole('button', { name: 'graph-node-R9' }),
    ).toBeInTheDocument()
  })

  it('moves between connected nodes with the arrow keys, and opens one with Enter', async () => {
    const user = userEvent.setup()
    renderView()
    const origin = await screen.findByTestId('rf-node-A1')
    const built = buildGraph({ graph, overlay: false, filter: emptyFilter() })
    const expected = connectedNeighbor(
      built.nodes,
      built.edges,
      'A1',
      'ArrowRight',
    )
    expect(expected).not.toBeNull()
    act(() => origin.focus())
    await user.keyboard('{ArrowRight}')
    expect(screen.getByTestId(`rf-node-${expected}`)).toHaveFocus()

    await user.keyboard('{Enter}')
    expect(fakeRouter.searchStr()).toBe(`?node=${expected}`)
  })

  it('leaves a click outside the nodes, and an arrow with nowhere to go, alone', async () => {
    const user = userEvent.setup()
    renderView()
    const origin = await screen.findByTestId('rf-node-A1')
    act(() => origin.focus())
    await user.keyboard('{ArrowLeft}')
    expect(origin).toHaveFocus()
    expect(fakeRouter.searchStr()).toBe('')
  })

  it("leaves the browser's Back and Forward keys alone", async () => {
    // Alt+Arrow is how the keyboard reaches Back, the path this issue relies on.
    const user = userEvent.setup()
    renderView()
    const origin = await screen.findByTestId('rf-node-A1')
    act(() => origin.focus())
    await user.keyboard('{Alt>}{ArrowRight}{/Alt}')
    expect(origin).toHaveFocus()
  })

  it('shows only the nodes a restored filter leaves', async () => {
    fakeRouter.reset('/access-map?q=no-such-origin')
    renderView()
    await screen.findByRole('textbox', { name: /search origin/i })
    expect(
      screen.queryByRole('button', { name: 'graph-node-A1' }),
    ).not.toBeInTheDocument()
  })

  it('reads no expansion for a node this map does not show, and says so', async () => {
    // A read is audited under whoever opens the link: ids that are not on the map, or a
    // kind the map does not give the id, are refused before any request leaves.
    vi.mocked(accessMapApi.neighbors).mockResolvedValue(neighborsOfA1)
    fakeRouter.reset(
      '/access-map?expand=agent:FORGED,postgres.table:A1,agent:A1',
    )
    renderView()
    expect(
      await screen.findByRole('button', { name: 'graph-node-R9' }),
    ).toBeInTheDocument()
    expect(await screen.findByTestId('url-state-notice')).toHaveTextContent(
      /expand/,
    )
    expect(accessMapApi.neighbors).toHaveBeenCalledTimes(1)
    expect(accessMapApi.neighbors).toHaveBeenCalledWith('A1', 'both', 'agent')
    await waitFor(() =>
      expect(fakeRouter.searchStr()).toBe('?expand=agent%3AA1'),
    )
  })

  it('reads an expansion that sits on an earlier expansion, in order', async () => {
    vi.mocked(accessMapApi.neighbors).mockImplementation(async (id: string) =>
      id === 'A1' ? neighborsOfA1 : { nodes: [], edges: [], has_more: false },
    )
    fakeRouter.reset('/access-map?expand=agent:A1,postgres.table:R9')
    renderView()
    await waitFor(() =>
      expect(accessMapApi.neighbors).toHaveBeenCalledWith(
        'R9',
        'both',
        'postgres.table',
      ),
    )
    expect(screen.queryByTestId('url-state-notice')).not.toBeInTheDocument()
  })

  it('keeps a selected unused grant in the address when the re-check fails', async () => {
    // A failed read is not an answer: the link still names the edge, and the sheet says
    // "could not look" about it instead of closing and cleaning the address.
    vi.mocked(accessMapApi.drift)
      .mockReset()
      .mockResolvedValueOnce(diff)
      .mockRejectedValueOnce(new Error('drift unavailable'))
    fakeRouter.reset('/access-map?drift=1&edge=e9')
    const user = userEvent.setup()
    renderView()
    const dialog = await screen.findByRole('dialog')
    await user.click(
      await within(dialog).findByRole('button', {
        name: /re-check the drift/i,
      }),
    )
    await waitFor(() => expect(accessMapApi.drift).toHaveBeenCalledTimes(2))
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
    expect(fakeRouter.searchStr()).toContain('edge=e9')
  })

  it('does not read the old tenant’s expansions in the new one, and clears the address', async () => {
    vi.mocked(accessMapApi.neighbors).mockResolvedValue(neighborsOfA1)
    fakeRouter.reset('/access-map?expand=agent:A1')
    const { rerender } = renderView()
    expect(
      await screen.findByRole('button', { name: 'graph-node-R9' }),
    ).toBeInTheDocument()
    expect(accessMapApi.neighbors).toHaveBeenCalledTimes(1)

    perm.tenant = 't2'
    rerender(
      <QueryClientProvider client={new QueryClient()}>
        <AccessMapView />
      </QueryClientProvider>,
    )
    await waitFor(() => expect(fakeRouter.searchStr()).toBe(''))
    expect(accessMapApi.neighbors).toHaveBeenCalledTimes(1)
  })
})

/**
 * The expand gate and the notice share one rule with the canvas: a node is on this map
 * when the canvas can draw it. The graph read stops at its limit while the drift read is
 * drained, so the overlay draws endpoints the base graph does not hold.
 */
describe('AccessMapView — what the canvas draws, the address may name', () => {
  const overlayOnly = (
    origin_kind: string,
    origin_id: string,
    origin_ref: string,
  ) => ({
    ...diff,
    unused_grants: [
      ...diff.unused_grants,
      {
        kind: 'unused_grant' as const,
        edge: {
          ...grantEdge,
          id: `e-${origin_id}`,
          origin_kind,
          origin_id,
          origin_ref,
          resource_id: 'R8',
          resource_kind: 'postgres.table',
          resource_ref: 'appdb.public.only_in_drift',
        },
      },
    ],
  })
  const extraEdge = {
    ...graph.edges[0]!,
    id: 'e-extra',
    resource_id: 'R9',
    resource_ref: 'appdb.public.extra',
  }
  const neighborsOfA1 = {
    nodes: [{ id: 'R9', kind: 'postgres.table', ref: 'appdb.public.extra' }],
    edges: [extraEdge],
    has_more: false,
  }

  it('expands a node that only the drift overlay draws, and keeps it in the address', async () => {
    const user = userEvent.setup()
    vi.mocked(accessMapApi.drift).mockResolvedValue(
      overlayOnly('db_role', 'U7', 'etl_role_2'),
    )
    fakeRouter.reset('/access-map?drift=1&node=U7')
    renderView()
    await user.click(await screen.findByRole('button', { name: /expand/i }))
    await waitFor(() =>
      expect(accessMapApi.neighbors).toHaveBeenCalledWith(
        'U7',
        'both',
        'db_role',
      ),
    )
    await waitFor(() =>
      expect(fakeRouter.searchStr()).toContain('expand=db_role%3AU7'),
    )
    await waitFor(() => expect(fakeRouter.depth()).toBe(2))
    expect(screen.queryByTestId('url-state-notice')).not.toBeInTheDocument()
    expect(fakeRouter.searchStr()).toContain('expand=db_role%3AU7')
  })

  it('drops the expansion of an overlay-only node with the overlay, without blaming the link', async () => {
    const user = userEvent.setup()
    vi.mocked(accessMapApi.drift).mockResolvedValue(
      overlayOnly('db_role', 'U7', 'etl_role_2'),
    )
    fakeRouter.reset('/access-map?drift=1&node=U7')
    renderView()
    await user.click(await screen.findByRole('button', { name: /expand/i }))
    await waitFor(() =>
      expect(fakeRouter.searchStr()).toContain('expand=db_role%3AU7'),
    )
    await user.click(
      await screen.findByRole('button', { name: /permitted vs observed/i }),
    )
    // One patch turns the overlay off and takes out what only the overlay drew: it was
    // the operator's own expansion, not a value a link asked for.
    await waitFor(() => expect(fakeRouter.searchStr()).not.toContain('drift='))
    expect(fakeRouter.searchStr()).not.toContain('expand=')
    expect(screen.queryByTestId('url-state-notice')).not.toBeInTheDocument()
  })

  it('drops a whole chain that stands on an overlay-only node, and only that chain', async () => {
    const user = userEvent.setup()
    vi.mocked(accessMapApi.drift).mockResolvedValue(
      overlayOnly('db_role', 'U7', 'etl_role_2'),
    )
    vi.mocked(accessMapApi.neighbors).mockImplementation(async (id: string) =>
      id === 'U7'
        ? {
            nodes: [
              { id: 'R9', kind: 'postgres.table', ref: 'appdb.public.extra' },
            ],
            edges: [extraEdge],
            has_more: false,
          }
        : { nodes: [], edges: [], has_more: false },
    )
    // R9 is listed BEFORE U7, and is known only through U7's read; A1 is the graph's own.
    fakeRouter.reset(
      '/access-map?drift=1&expand=postgres.table:R9,agent:A1,db_role:U7',
    )
    renderView()
    await screen.findByRole('button', { name: 'graph-node-R9' })
    await user.click(
      await screen.findByRole('button', { name: /permitted vs observed/i }),
    )
    await waitFor(() => expect(fakeRouter.searchStr()).not.toContain('drift='))
    expect(decodeURIComponent(fakeRouter.searchStr())).toBe('?expand=agent:A1')
    expect(screen.queryByTestId('url-state-notice')).not.toBeInTheDocument()
  })

  it('NON-FIRING: turning the overlay off keeps the expansion of a node the graph draws', async () => {
    const user = userEvent.setup()
    vi.mocked(accessMapApi.neighbors).mockImplementation(async (id: string) =>
      id === 'A1' ? neighborsOfA1 : { nodes: [], edges: [], has_more: false },
    )
    fakeRouter.reset('/access-map?drift=1&expand=agent:A1')
    renderView()
    await screen.findByRole('button', { name: 'graph-node-R9' })
    await user.click(
      await screen.findByRole('button', { name: /permitted vs observed/i }),
    )
    await waitFor(() => expect(fakeRouter.searchStr()).not.toContain('drift='))
    expect(decodeURIComponent(fakeRouter.searchStr())).toBe('?expand=agent:A1')
    expect(screen.queryByTestId('url-state-notice')).not.toBeInTheDocument()
  })

  it('opens the session of a node that only the drift overlay draws', async () => {
    const user = userEvent.setup()
    vi.mocked(accessMapApi.drift).mockResolvedValue(
      overlayOnly('session', 'S9', 'ext-session-9'),
    )
    fakeRouter.reset('/access-map?drift=1&node=S9')
    renderView()
    await user.click(
      await screen.findByRole('button', { name: /open session/i }),
    )
    expect(navigate).toHaveBeenCalledWith(
      expect.objectContaining({
        to: '/sessions',
        search: { session: 'sess:ext-session-9' },
      }),
    )
  })

  it('keeps a node that sits on an earlier expansion when that node is expanded again', async () => {
    const user = userEvent.setup()
    vi.mocked(accessMapApi.neighbors).mockImplementation(async (id: string) =>
      id === 'A1' ? neighborsOfA1 : { nodes: [], edges: [], has_more: false },
    )
    fakeRouter.reset('/access-map?expand=agent:A1,postgres.table:R9&node=A1')
    renderView()
    await user.click(await screen.findByRole('button', { name: /expand/i }))
    await waitFor(() => expect(fakeRouter.searchStr()).not.toContain('node='))
    expect(decodeURIComponent(fakeRouter.searchStr())).toBe(
      '?expand=agent:A1,postgres.table:R9',
    )
    expect(screen.queryByTestId('url-state-notice')).not.toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'graph-node-R9' }),
    ).toBeInTheDocument()
  })

  it('keeps the "not on this map" notice dismissed once the operator dismisses it', async () => {
    const user = userEvent.setup()
    fakeRouter.reset('/access-map?node=GHOST')
    renderView()
    expect(await screen.findByTestId('url-state-notice')).toHaveTextContent(
      /node/,
    )
    await user.click(screen.getByTestId('url-state-notice-dismiss'))
    expect(screen.queryByTestId('url-state-notice')).not.toBeInTheDocument()
    // A render that is no patch of the address: opening the list. It must not bring
    // the notice back.
    await user.click(
      await screen.findByText('Map as a list', { selector: 'summary' }),
    )
    expect(
      await screen.findByRole('button', { name: 'agent-1' }),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('url-state-notice')).not.toBeInTheDocument()
  })

  it('clears the "not on this map" notice on the operator’s next deliberate change', async () => {
    const user = userEvent.setup()
    fakeRouter.reset('/access-map?node=GHOST')
    renderView()
    expect(await screen.findByTestId('url-state-notice')).toBeInTheDocument()
    await user.click(
      await screen.findByRole('button', { name: 'graph-node-A1' }),
    )
    await user.keyboard('{Escape}')
    await waitFor(() => expect(fakeRouter.searchStr()).toBe(''))
    expect(screen.queryByTestId('url-state-notice')).not.toBeInTheDocument()
  })

  it('reads a chain of expansions whatever order the address lists them in', async () => {
    const hop = (from: string, to: string) => ({
      nodes: [{ id: to, kind: 'postgres.table', ref: `appdb.public.${to}` }],
      edges: [
        {
          ...extraEdge,
          id: `e-${from}-${to}`,
          origin_id: from,
          origin_kind: from === 'A1' ? 'agent' : 'postgres.table',
          resource_id: to,
          resource_ref: `appdb.public.${to}`,
        },
      ],
      has_more: false,
    })
    vi.mocked(accessMapApi.neighbors).mockImplementation(async (id: string) =>
      id === 'A1'
        ? hop('A1', 'R9')
        : id === 'R9'
          ? hop('R9', 'R10')
          : { nodes: [], edges: [], has_more: false },
    )
    fakeRouter.reset(
      '/access-map?expand=postgres.table:R10,postgres.table:R9,agent:A1',
    )
    renderView()
    expect(
      await screen.findByRole('button', { name: 'graph-node-R10' }),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('url-state-notice')).not.toBeInTheDocument()
    expect(decodeURIComponent(fakeRouter.searchStr())).toBe(
      '?expand=postgres.table:R10,postgres.table:R9,agent:A1',
    )
  })

  it('NON-FIRING: reads nothing for a node only the overlay draws when the overlay is off', async () => {
    vi.mocked(accessMapApi.drift).mockResolvedValue(
      overlayOnly('db_role', 'U7', 'etl_role_2'),
    )
    vi.mocked(accessMapApi.drift).mockClear()
    fakeRouter.reset('/access-map?expand=db_role:U7')
    renderView()
    expect(await screen.findByTestId('url-state-notice')).toHaveTextContent(
      /expand/,
    )
    expect(accessMapApi.neighbors).not.toHaveBeenCalled()
    expect(accessMapApi.drift).not.toHaveBeenCalled()
    await waitFor(() => expect(fakeRouter.searchStr()).toBe(''))
  })

  it('reads nothing for the endpoint of an unexpected access the canvas does not draw', async () => {
    // Unexpected accesses are the graph's own edges; only unused grants come from /drift.
    vi.mocked(accessMapApi.drift).mockResolvedValue({
      ...diff,
      unexpected_accesses: [
        {
          kind: 'unexpected_access',
          reconciliation_pending: false,
          edge: {
            ...graph.edges[0]!,
            id: 'e-U8',
            origin_kind: 'db_role',
            origin_id: 'U8',
          },
        },
      ],
    })
    fakeRouter.reset('/access-map?drift=1&expand=db_role:U8')
    renderView()
    expect(await screen.findByTestId('url-state-notice')).toHaveTextContent(
      /expand/,
    )
    expect(accessMapApi.neighbors).not.toHaveBeenCalled()
  })

  describe('at the expansion cap', () => {
    const wide = {
      nodes: Array.from({ length: 11 }, (_, i) => ({
        id: `X${i + 1}`,
        kind: 'postgres.table',
        ref: `appdb.public.x${i + 1}`,
      })),
      edges: Array.from({ length: 11 }, (_, i) => ({
        ...extraEdge,
        id: `e-x${i + 1}`,
        resource_id: `X${i + 1}`,
        resource_ref: `appdb.public.x${i + 1}`,
      })),
      has_more: false,
    }
    const tenExpanded = `agent:A1,${Array.from(
      { length: 9 },
      (_, i) => `postgres.table:X${i + 1}`,
    ).join(',')}`
    beforeEach(() => {
      vi.mocked(accessMapApi.neighbors).mockImplementation(
        async (id: string) =>
          id === 'A1' ? wide : { nodes: [], edges: [], has_more: false },
      )
    })

    it('lets the operator expand a node again that is already one of the ten', async () => {
      const user = userEvent.setup()
      fakeRouter.reset(`/access-map?expand=${tenExpanded}&node=A1`)
      renderView()
      await user.click(await screen.findByRole('button', { name: /expand/i }))
      await waitFor(() => expect(fakeRouter.searchStr()).not.toContain('node='))
      expect(
        screen.queryByText(/already shows 10 expanded nodes/i),
      ).not.toBeInTheDocument()
    })

    it('refuses an eleventh node and says so', async () => {
      const user = userEvent.setup()
      fakeRouter.reset(`/access-map?expand=${tenExpanded}&node=X10`)
      renderView()
      await user.click(await screen.findByRole('button', { name: /expand/i }))
      // The sheet is a modal dialog: everything outside it is aria-hidden, so the sentence
      // has to be IN it to reach a screen reader (and a phone, where it is full width).
      expect(
        await within(await screen.findByRole('dialog')).findByText(
          /already shows 10 expanded nodes/i,
        ),
      ).toBeInTheDocument()
      expect(accessMapApi.neighbors).not.toHaveBeenCalledWith(
        'X10',
        'both',
        'postgres.table',
      )
    })
  })

  it('keeps the notice for a refused value when a node is dropped as well', async () => {
    fakeRouter.reset('/access-map?mode=bogus&node=GHOST')
    renderView()
    await waitFor(() =>
      expect(screen.getByTestId('url-state-notice')).toHaveTextContent(
        /mode, node/,
      ),
    )
    await waitFor(() => expect(fakeRouter.searchStr()).toBe(''))
  })

  it('NON-FIRING: a refused value still clears on the next change', async () => {
    const user = userEvent.setup()
    fakeRouter.reset('/access-map?mode=bogus')
    renderView()
    expect(await screen.findByTestId('url-state-notice')).toBeInTheDocument()
    await user.click(
      await screen.findByRole('button', { name: 'graph-node-A1' }),
    )
    expect(screen.queryByTestId('url-state-notice')).not.toBeInTheDocument()
  })
})

describe('AccessMapView — the detail panel leads to the thing it names', () => {
  const sessionGraph: GraphResponse = {
    nodes: [
      { id: 'S1', kind: 'session', ref: 'ext-session-1' },
      { id: 'R2', kind: 'postgres.table', ref: 'appdb.public.secrets' },
    ],
    edges: [
      {
        ...graph.edges[0]!,
        id: 'es',
        origin_kind: 'session',
        origin_id: 'S1',
        origin_ref: 'ext-session-1',
      },
    ],
    has_more: false,
  }

  it('opens the session on the work surface from its node', async () => {
    const user = userEvent.setup()
    vi.mocked(accessMapApi.graph).mockResolvedValue(sessionGraph)
    fakeRouter.reset('/access-map?node=S1')
    renderView()
    await user.click(
      await screen.findByRole('button', { name: /open session/i }),
    )
    expect(navigate).toHaveBeenCalledWith(
      expect.objectContaining({
        to: '/sessions',
        search: { session: 'sess:ext-session-1' },
      }),
    )
  })

  it('opens the session from an edge that names it', async () => {
    const user = userEvent.setup()
    vi.mocked(accessMapApi.graph).mockResolvedValue(sessionGraph)
    fakeRouter.reset('/access-map?edge=es')
    renderView()
    await user.click(
      await screen.findByRole('button', { name: /open session/i }),
    )
    expect(navigate).toHaveBeenCalledWith(
      expect.objectContaining({ to: '/sessions' }),
    )
  })

  it('opens the agents list from an agent node, but only for who may see it', async () => {
    const user = userEvent.setup()
    fakeRouter.reset('/access-map?node=A1')
    const { unmount } = renderView()
    await user.click(
      await screen.findByRole('button', { name: /open agents/i }),
    )
    expect(navigate).toHaveBeenCalledWith(
      expect.objectContaining({ to: '/console', search: { tab: 'agents' } }),
    )
    unmount()

    perm.admin = false
    fakeRouter.reset('/access-map?node=A1')
    renderView()
    await screen.findByText(/A graph node — expand to see what it connects to/i)
    expect(
      screen.queryByRole('button', { name: /open agents/i }),
    ).not.toBeInTheDocument()
  })

  it('offers no page link for a resource', async () => {
    fakeRouter.reset('/access-map?node=R2')
    renderView()
    await screen.findByText(/A graph node — expand to see what it connects to/i)
    expect(
      screen.queryByRole('button', { name: /^open /i }),
    ).not.toBeInTheDocument()
  })
})
