// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { clippingAncestors } from '@/test/clipping'
import { fakeRouter } from '@/test/fake-router'
import { stubViewportWidth } from '@/test/viewport'
import type { RunDTO } from '@/features/agentops/types'
import type { LiveDTO } from './types'

const perms = new Set<string>()
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: (p: string) => perms.has(p) }),
}))

// THE VIEW'S SELECTION IS NOW THE URL, so the router stub had to become a
// LOCATION. The old two-line stub answered the empty string to every question, which
// now cannot represent "this session is open": a click would write an address
// nothing stored, the view would read back the default, and the card assertions below
// would have measured the default instead of the choice. `fake-router` is one
// in-memory location with a real entry stack and `navigate()` semantics that match the
// ones `useUrlState` relies on. No RouterProvider is mounted: the shared Tabs strip
// consults useRouter, and the double answers undefined there, exactly as the real hook
// did here (console-tab-scroll-restoration R2, 2026-09-06).
vi.mock('@tanstack/react-router', async () => {
  const { fakeRouterModule } = await import('@/test/fake-router')
  return fakeRouterModule()
})

// The SSE stream is a live connection; the list under test is the merge, not the wire.
vi.mock('@/features/shared', async () => {
  const actual =
    await vi.importActual<typeof import('@/features/shared')>(
      '@/features/shared',
    )
  return { ...actual, useLiveStream: () => ({ status: 'open' }) }
})

// The heavy operate panels are proven by their own tests; mounting them here would
// drag CodeMirror and the attach EventSource into a table test.
vi.mock('@/features/agentops/profiles-panel', () => ({
  ProfilesPanel: () => <div>profiles-panel</div>,
}))
vi.mock('@/features/agentops/workspaces-panel', () => ({
  WorkspacesPanel: () => <div>workspaces-panel</div>,
}))
vi.mock('@/features/agentops/run-create-dialog', () => ({
  RunCreateDialog: () => null,
}))
// The card is handed an already-resolved session; the target it was resolved FOR
// is what these cases are about, and it rides on the resolution.
vi.mock('./session-card', () => ({
  SessionCard: ({
    resolution,
  }: {
    resolution: {
      target: { sessionRef?: string; runRef?: string; liveRef?: string } | null
    }
  }) =>
    resolution.target ? (
      <div data-testid="card">
        card:{resolution.target.sessionRef ?? ''}|
        {resolution.target.runRef ?? ''}|{resolution.target.liveRef ?? ''}
      </div>
    ) : null,
}))

// ⛔ ONLY THE API FUNCTIONS ARE DOUBLED; THE KEY FACTORIES ARE THE REAL ONES.
//    They used to be hand-written here, and a hand-written key factory is a copy that
//    stops being the thing it stands for the day production adds a key: the doubles said
//    `['s', t, 'live', …]` while the view had moved on, so this file could only ever
//    prove that the view agreed with the copy. `importOriginal` keeps `sessionsKeys` /
//    `agentOpsKeys` real, which is also what the boundary partition needs — the view's
//    cache scope is built by those factories.
vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  sessionsApi: { live: vi.fn(), liveOne: vi.fn(), timeline: vi.fn() },
}))

vi.mock('@/features/agentops/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/agentops/api')>()),
  agentOpsApi: { listRuns: vi.fn(), getRun: vi.fn(), listProfiles: vi.fn() },
}))

import { agentOpsApi } from '@/features/agentops/api'
import { sessionsApi } from './api'
import { SessionsWorkspaceView } from './sessions-workspace-view'

const observed: LiveDTO = {
  session_ref: 'sess-found',
  live_ref: 'lr-found',
  attribution: 'legacy',
  cc_state: 'active',
  current_action: 'reading appdb',
  model_ref: 'claude-opus-4-8',
  input_tokens: 1200,
  output_tokens: 800,
  cost_micro_usd: 42000,
  event_count: 3,
  tool_call_count: 2,
  first_event_at: '2026-08-10T10:00:00Z',
  last_event_at: '2026-08-10T10:05:00Z',
  duration_seconds: 300,
}

const launchedLive: LiveDTO = {
  ...observed,
  session_ref: 'sess-ours',
  live_ref: 'lr-ours',
}

const launchedRun: RunDTO = {
  run_ref: 'run-1',
  name: 'nightly-indexer',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  claude_session_id: 'sess-ours',
  last_event_seq: 4,
  pep_provisioned: true,
  record_io: false,
  critical: false,
  created_at: '2026-08-10T10:01:00Z',
  last_activity_at: '2026-08-10T10:06:00Z',
}

/**
 * ⛔ THIS FILE MEASURES THE TABLE. The screen's default presentation is the
 *    three-pane work surface; the table — the columns, the origin chips, the facets and
 *    the row-backed open target these cases read — is the other tab, unchanged. Opening
 *    it keeps every case measuring what it was written to measure.
 *
 *    MOUSEDOWN, not click: a Radix tab trigger selects on mouse-down, and a bare
 *    `.click()` leaves the strip where it was — which reads as "the table is empty"
 *    rather than "the tab never changed".
 */
function openTable() {
  // The LAST strip: a few cases render the view twice in one test, and the newest
  // mount is the one they go on to assert against.
  const triggers = screen.getAllByRole('tab', { name: 'Table' })
  act(() => {
    fireEvent.mouseDown(triggers[triggers.length - 1])
  })
}

function renderView(entrance: 'observe' | 'operate' = 'observe') {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  const result = render(
    <QueryClientProvider client={qc}>
      <SessionsWorkspaceView entrance={entrance} />
    </QueryClientProvider>,
  )
  openTable()
  return result
}

const rowFor = async (label: string) => {
  const cell = await screen.findByText(label)
  return cell.closest('tr') as HTMLElement
}

beforeEach(() => {
  vi.clearAllMocks()
  fakeRouter.reset('/sessions')
  perms.clear()
  perms.add('sessions:live:read')
  perms.add('sessions:run:read')
  vi.mocked(sessionsApi.live).mockResolvedValue({
    items: [observed, launchedLive],
    has_more: false,
  })
  vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
    items: [launchedRun],
    has_more: false,
  })
  vi.mocked(agentOpsApi.listProfiles).mockResolvedValue({
    items: [],
    has_more: false,
  })
})

describe('SessionsWorkspaceView — one destination, both origins', () => {
  it('lists discovered and launched sessions in ONE table, each labelled', async () => {
    renderView()
    const found = await rowFor('Untitled session')
    expect(within(found).getByText('Discovered')).toBeInTheDocument()
    // The launched one is titled by the name its operator typed at launch.
    const ours = await rowFor('nightly-indexer')
    expect(within(ours).getByText('Launched')).toBeInTheDocument()
    // …and it is ONE row, not two: the observed and operate halves folded together.
    // The session id stays ON that row so it remains searchable — naming the row after
    // the run must not hide the id the ledger, the API and any saved deep link use.
    expect(screen.getAllByRole('row')).toHaveLength(3) // header + 2 sessions
    // The id is no longer a SECOND LINE — that stack, with the
    // INSTANCE column's own, made every row 110 px. It is on the cell's `title` and it
    // is still what the column searches, which is the property that matters here and
    // the one the search test below exercises.
    expect(
      within(ours).getByTitle('nightly-indexer · sess-ours'),
    ).toBeInTheDocument()
  })

  it('shows the control level per row, from the plane and not from the caller', async () => {
    renderView()
    const ours = await rowFor('nightly-indexer')
    expect(within(ours).getByText('Full control')).toBeInTheDocument()
    const found = await rowFor('Untitled session')
    expect(within(found).getByText('Observe only')).toBeInTheDocument()
  })

  it('carries the launched session its observed telemetry (the halves are joined)', async () => {
    renderView()
    const ours = await rowFor('nightly-indexer')
    expect(within(ours).getByText(/1,200/)).toBeInTheDocument()
    expect(within(ours).getByText('$0.042')).toBeInTheDocument()
  })

  it('filters by origin without making the operator guess a section', async () => {
    const user = userEvent.setup()
    renderView()
    await screen.findByText('Untitled session')
    await user.click(screen.getByLabelText('All sources'))
    await user.click(await screen.findByRole('option', { name: 'Launched' }))
    await waitFor(() =>
      expect(screen.queryByText('Untitled session')).not.toBeInTheDocument(),
    )
    expect(screen.getByText('nightly-indexer')).toBeInTheDocument()
  })

  it('opens the SAME card from either kind of row', async () => {
    const user = userEvent.setup()
    renderView()
    await user.click(await rowFor('Untitled session'))
    expect(await screen.findByTestId('card')).toHaveTextContent(
      'card:sess-found|',
    )
  })

  it('opens the card by RUN ref for a launched session with no observation yet', async () => {
    vi.mocked(sessionsApi.live).mockResolvedValue({
      items: [],
      has_more: false,
    })
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [{ ...launchedRun, claude_session_id: undefined }],
      has_more: false,
    })
    const user = userEvent.setup()
    renderView()
    await user.click(await rowFor('nightly-indexer'))
    expect(await screen.findByTestId('card')).toHaveTextContent('card:|run-1')
  })
})

describe('SessionsWorkspaceView — the third answer', () => {
  it('says the operate half was NOT READ rather than calling everything discovered', async () => {
    perms.delete('sessions:run:read')
    renderView()
    await screen.findAllByText('Untitled session')
    expect(vi.mocked(agentOpsApi.listRuns)).not.toHaveBeenCalled()
    expect(
      screen.getByText(/Launched sessions are not shown/i),
    ).toBeInTheDocument()
    // And the COLUMN says it too. A notice beside the table does not unsay a chip that
    // reads "Discovered" — the row would still be stating a fact nothing checked.
    const table = screen.getByRole('grid')
    expect(within(table).queryAllByText('Discovered')).toHaveLength(0)
    expect(
      within(table).getAllByText('Origin not read').length,
    ).toBeGreaterThan(0)
  })

  it('says origin not read when the run lookup FAILS, not just when it is forbidden', async () => {
    vi.mocked(agentOpsApi.listRuns).mockRejectedValue(new Error('boom'))
    renderView()
    // The observed rows that DID arrive are still shown — a failed run lookup must not
    // throw away data the other half returned.
    await screen.findAllByText('Untitled session')
    expect(screen.getByText(/run lookup failed/i)).toBeInTheDocument()
    const table = screen.getByRole('grid')
    expect(within(table).queryAllByText('Discovered')).toHaveLength(0)
    expect(
      within(table).getAllByText('Origin not read').length,
    ).toBeGreaterThan(0)
  })

  it('still says LAUNCHED for a row whose run it did read', async () => {
    // A linked run outranks "did not look": the answer is in hand for that row.
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [launchedRun],
      has_more: true, // page truncated ⇒ the notice fires, but this row is known
    })
    renderView()
    expect(await screen.findByText('Launched')).toBeInTheDocument()
  })

  it('can isolate runs by their LIFECYCLE state, which /agentops could', async () => {
    const user = userEvent.setup()
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [
        launchedRun,
        {
          ...launchedRun,
          run_ref: 'run-2',
          name: 'broken',
          state: 'failed',
          claude_session_id: undefined,
        },
      ],
      has_more: false,
    })
    renderView()
    await screen.findByText('broken')
    await user.click(screen.getByLabelText('All states'))
    await user.click(await screen.findByRole('option', { name: 'Run: Failed' }))
    await waitFor(() =>
      expect(screen.queryByText('Untitled session')).not.toBeInTheDocument(),
    )
    expect(screen.getByText('broken')).toBeInTheDocument()
  })

  it('says the observed half was not read when live-read is missing', async () => {
    perms.delete('sessions:live:read')
    renderView()
    await screen.findByText('nightly-indexer')
    expect(vi.mocked(sessionsApi.live)).not.toHaveBeenCalled()
    expect(
      screen.getByText(/Observed sessions are not shown/i),
    ).toBeInTheDocument()
  })

  it('declares that origin is joined over a PAGE when either page is truncated', async () => {
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [launchedRun],
      has_more: true,
    })
    renderView()
    await screen.findByText('Untitled session')
    expect(
      screen.getByText(/most recent page, not the whole estate/i),
    ).toBeInTheDocument()
  })

  it('says nothing about truncation when both pages are complete', async () => {
    renderView()
    await screen.findByText('Untitled session')
    expect(
      screen.queryByText(/most recent page, not the whole estate/i),
    ).not.toBeInTheDocument()
  })
})

describe('SessionsWorkspaceView — two doors, one room', () => {
  it('keeps the operate framing on the /agentops door', async () => {
    renderView('operate')
    expect(
      await screen.findByRole('heading', { name: 'Claude Code' }),
    ).toBeInTheDocument()
    // …and still lists the sessions Olivares only discovered.
    expect(await screen.findByText('Untitled session')).toBeInTheDocument()
  })

  it('keeps the observe framing on the /sessions door', async () => {
    renderView('observe')
    expect(
      await screen.findByRole('heading', { name: 'Sessions' }),
    ).toBeInTheDocument()
    // …and still lists what Olivares launched.
    expect(await screen.findByText('nightly-indexer')).toBeInTheDocument()
  })

  it('offers the provider-profile plane only to a principal who can read profiles', async () => {
    perms.add('sessions:profile:read')
    renderView()
    expect(
      await screen.findByRole('tab', { name: 'Provider profiles' }),
    ).toBeInTheDocument()
    perms.delete('sessions:profile:read')
    renderView()
    await waitFor(() =>
      expect(
        screen.queryAllByRole('tab', { name: 'Provider profiles' }),
      ).toHaveLength(1),
    )
  })

  it('offers the workspace plane only to a principal who can read runs', async () => {
    renderView()
    expect(
      await screen.findByRole('tab', { name: 'Workspaces' }),
    ).toBeInTheDocument()
    perms.delete('sessions:run:read')
    renderView()
    await waitFor(() =>
      expect(screen.queryAllByRole('tab', { name: 'Workspaces' })).toHaveLength(
        1,
      ),
    )
  })

  it('offers the launch action only to a principal who can write runs', async () => {
    renderView()
    await screen.findByText('Untitled session')
    expect(
      screen.queryByRole('button', { name: /New session/i }),
    ).not.toBeInTheDocument()
    perms.add('sessions:run:write')
    renderView()
    expect(
      await screen.findAllByRole('button', { name: /New session/i }),
    ).not.toHaveLength(0)
  })
})

// Two homes of one provider may announce ONE session id. The list keys rows by
// their own live_ref, joins a profiled run only to the row the plane proved for it,
// and opens a scoped row by live_ref.
describe('SessionsWorkspaceView — two homes, one provider session id', () => {
  const managedA: LiveDTO = {
    ...observed,
    session_ref: 'sess-dup',
    live_ref: 'lr-a',
    attribution: 'managed',
    provider_profile_ref: 'ppf_a',
    provider: 'claude',
    canonical_sid: 'osn_a',
    run_ref: 'run-a',
  }
  const managedB: LiveDTO = {
    ...managedA,
    live_ref: 'lr-b',
    provider_profile_ref: 'ppf_b',
    canonical_sid: 'osn_b',
    run_ref: 'run-b',
    cost_micro_usd: 1000,
  }
  const runA: RunDTO = {
    ...launchedRun,
    run_ref: 'run-a',
    name: 'home-a',
    claude_session_id: 'sess-dup',
    provider_profile_ref: 'ppf_a',
    provider_driver: 'claude',
    live_ref: 'lr-a',
  }
  const runB: RunDTO = {
    ...runA,
    run_ref: 'run-b',
    name: 'home-b',
    provider_profile_ref: 'ppf_b',
    live_ref: 'lr-b',
  }

  it('keeps two rows sharing a provider session id, each joined to ITS run', async () => {
    vi.mocked(sessionsApi.live).mockResolvedValue({
      items: [managedA, managedB],
      has_more: false,
    })
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [runA, runB],
      has_more: false,
    })
    renderView()
    const a = await rowFor('home-a')
    const b = await rowFor('home-b')
    expect(screen.getAllByRole('row')).toHaveLength(3) // header + 2 sessions
    expect(within(a).queryByText('ppf_a')).toBeNull()
    expect(within(b).queryByText('ppf_b')).toBeNull()
    expect(within(a).getByText('Managed by Olivares')).toBeInTheDocument()
    expect(within(a).getByText('Launched')).toBeInTheDocument()
    expect(within(b).getByText('$0.001')).toBeInTheDocument()
    expect(within(a).getByText('$0.042')).toBeInTheDocument()
  })

  it('paints zero table cells that start with a raw ppf_ or sess- identifier', async () => {
    perms.add('sessions:profile:read')
    vi.mocked(sessionsApi.live).mockResolvedValue({
      items: [managedA, managedB],
      has_more: false,
    })
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [runA, runB],
      has_more: false,
    })
    vi.mocked(agentOpsApi.listProfiles).mockResolvedValue({
      items: [
        {
          profile_ref: 'ppf_a',
          driver: 'claude',
          environment_ref: 'xenv_1',
          display_name: 'Home A',
          state: 'active',
          local_environment: true,
          operable: true,
        },
        {
          profile_ref: 'ppf_b',
          driver: 'claude',
          environment_ref: 'xenv_1',
          display_name: 'Home B',
          state: 'active',
          local_environment: true,
          operable: true,
        },
      ],
      has_more: false,
    })
    renderView()
    const a = await rowFor('home-a')
    await waitFor(() =>
      expect(within(a).getByText('Home A')).toBeInTheDocument(),
    )
    expect(within(a).getByTitle('ppf_a')).toBeInTheDocument()
    expect(
      within(await rowFor('home-b')).getByText('Home B'),
    ).toBeInTheDocument()
    for (const cell of screen.getAllByRole('gridcell')) {
      expect(cell.textContent?.trim() ?? '').not.toMatch(/^(ppf_|sess-)/)
    }
  })

  it('opens the card by live_ref for a scoped row', async () => {
    vi.mocked(sessionsApi.live).mockResolvedValue({
      items: [managedA, managedB],
      has_more: false,
    })
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [runA, runB],
      has_more: false,
    })
    const user = userEvent.setup()
    renderView()
    await user.click(await rowFor('home-b'))
    expect(await screen.findByTestId('card')).toHaveTextContent('|lr-b')
  })

  it('never folds a profiled run onto a legacy row that shares its id', async () => {
    vi.mocked(sessionsApi.live).mockResolvedValue({
      items: [{ ...observed, session_ref: 'sess-dup', live_ref: 'lr-legacy' }],
      has_more: false,
    })
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [runA],
      has_more: false,
    })
    renderView()
    const own = await rowFor('home-a')
    // The legacy row has no run name, so it paints the observed action, not the
    // bare session id. The profiled row is titled by the name the operator typed.
    const legacy = (await screen.findAllByText('Untitled session'))
      .map((el) => el.closest('tr') as HTMLElement)
      .find((tr) => tr !== own) as HTMLElement
    expect(screen.getAllByRole('row')).toHaveLength(3)
    expect(within(legacy).getByText('Discovered')).toBeInTheDocument()
    expect(within(own).getByText('Launched')).toBeInTheDocument()
  })
})

/**
 * THE SESSION IS ADDRESSABLE. A review named this the single largest gap the front door
 * left: a work row there could open the room and not the card, because the selection
 * lived in `useState`.
 *
 * Every case below drives the REAL location double (`@/test/fake-router`), so what is
 * asserted is the address bar an operator would copy, and not a prop.
 */
describe('SessionsWorkspaceView — chrome above the work', () => {
  it('folds the counts AND the tab strip onto the title line', async () => {
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: 0 } },
    })
    render(
      <QueryClientProvider client={qc}>
        <SessionsWorkspaceView entrance="observe" />
      </QueryClientProvider>,
    )
    const heading = await screen.findByRole('heading', { name: 'Sessions' })
    const summary = await screen.findByTestId('sessions-summary')
    expect(heading.parentElement).toContainElement(summary)
    const panes = screen.getByTestId('sessions-panes')
    // The strip used to be the pane header, one 36 px band under the title. It is on
    // the title line now, so the panes begin with the work itself.
    expect(panes).not.toContainElement(screen.getByRole('tablist'))
    expect(heading.closest('[data-slot="work-chrome"]')).toContainElement(
      screen.getByRole('tablist'),
    )
    expect(panes).toContainElement(await screen.findByTestId('work-rail'))
    expect(panes).not.toContainElement(summary)
  })
})

describe('SessionsWorkspaceView — the surface opens on work, not on nothing', () => {
  it('opens on the top row of the rail when the address names no session', async () => {
    // The flagship work screen of this cycle arrived with nothing
    // selected
    // — the narrative pane saying `Select a session`, the context pane saying `No
    // session selected`, and 60 % of the screen blank. With no session in the
    // address the surface opens on the session the RAIL puts first, which is its own
    // order — needs attention, then working, then settled, most recent inside each.
    fakeRouter.reset('/sessions')
    renderView()
    const card = await screen.findByTestId('card')
    expect(card.textContent).not.toBe('')
  })

  it('does not write the default into the URL, so a link that named nothing still does', async () => {
    // A default is not a selection. A surface that rewrote the address under its reader
    // would hand a recipient a different link than its author copied.
    fakeRouter.reset('/sessions')
    renderView()
    await screen.findByTestId('card')
    expect(fakeRouter.url()).toBe('/sessions')
  })
})

describe('SessionsWorkspaceView — the address bar holds the session', () => {
  it('opens a session COLD from a deep link, with no click and no list behind it', async () => {
    // The whole point: nothing is loaded yet when the address is read, so the card has
    // to be resolvable from the URL alone.
    fakeRouter.reset('/sessions?session=sess%3Asess-found')
    renderView()
    expect(await screen.findByTestId('card')).toHaveTextContent(
      'card:sess-found|',
    )
  })

  it('opens a scoped row cold by its own opaque id', async () => {
    fakeRouter.reset('/sessions?session=live%3Alr-ours')
    renderView()
    expect(await screen.findByTestId('card')).toHaveTextContent('|lr-ours')
  })

  it('writes the address when a row is clicked — the URL is the only writer', async () => {
    const user = userEvent.setup()
    renderView()
    await user.click(await rowFor('Untitled session'))
    await screen.findByTestId('card')
    expect(fakeRouter.url()).toBe('/sessions?session=sess%3Asess-found')
  })

  it('Back returns to the session read a moment ago, and Forward goes on again', async () => {
    const user = userEvent.setup()
    renderView()
    await user.click(await rowFor('Untitled session'))
    await screen.findByTestId('card')
    await user.click(await rowFor('nightly-indexer'))
    expect(await screen.findByTestId('card')).toHaveTextContent(
      'card:sess-ours|',
    )

    // A session is a PLACE, so each one is its own history entry.
    act(() => fakeRouter.back())
    expect(await screen.findByTestId('card')).toHaveTextContent(
      'card:sess-found|',
    )
    act(() => fakeRouter.forward())
    expect(await screen.findByTestId('card')).toHaveTextContent(
      'card:sess-ours|',
    )
  })

  it('closing the card takes the session out of the address, and the surface falls back to the rail', async () => {
    // ⛔ THIS USED TO ASSERT THAT THE SURFACE WENT EMPTY, and that was the defect the
    //    measurement recorded: the flagship work screen arriving with nothing
    //    selected, two empty states side by side saying the same thing. Since the
    //    work-first pass
    //    an address that names no session means the surface shows the top row of the
    //    rail's OWN order, so what this case must say is that the session the URL named
    //    is no longer the one open — not that nothing is.
    fakeRouter.reset('/sessions?session=sess%3Asess-found')
    renderView()
    const closed = (await screen.findByTestId('card')).textContent
    expect(closed).toContain('sess-found')
    act(() => fakeRouter.go('/sessions'))
    await waitFor(() =>
      expect(screen.getByTestId('card').textContent).not.toBe(closed),
    )
  })

  it('refuses an address that names nothing, says which key, and cleans the bar', async () => {
    // A URL is typed, pasted and edited. A deep link that silently ignored half of
    // what it was handed would show the recipient a different screen than the author.
    fakeRouter.reset('/sessions?session=nonsense&pane=diff')
    renderView()
    expect(await screen.findByTestId('url-state-notice')).toHaveTextContent(
      /session/,
    )
    expect(screen.queryByTestId('card')).not.toBeInTheDocument()
    await waitFor(() => expect(fakeRouter.url()).toBe('/sessions'))
  })

  it('a withdrawn read retires the open session, clears the bar and SAYS SO', async () => {
    // The authority model is unchanged and this proves the address did not weaken it:
    // the selection was made ON a row the observed half answered, so losing that half
    // takes the card with it — and now the URL follows, because a link naming a
    // session the screen is not showing is the one state a shareable surface must not
    // sit in quietly.
    const user = userEvent.setup()
    const { rerender } = renderView()
    await user.click(await rowFor('Untitled session'))
    await screen.findByTestId('card')

    perms.delete('sessions:live:read')
    rerender(
      <QueryClientProvider
        client={
          new QueryClient({
            defaultOptions: { queries: { retry: false, gcTime: 0 } },
          })
        }
      >
        <SessionsWorkspaceView entrance="observe" />
      </QueryClientProvider>,
    )
    // The RETIRED session is not what is open — the surface falls back to the top row
    // that survived the withdrawal, which is a different, currently admitted one
    // `railDefault` excludes the retired address by construction, so the
    // R2 invariant holds here too: a read bit coming back cannot revive it.
    await waitFor(() =>
      expect(screen.queryByTestId('card')?.textContent ?? '').not.toContain(
        'sess-found',
      ),
    )
    expect(await screen.findByTestId('sessions-address-retired')).toBeVisible()
    await waitFor(() => expect(fakeRouter.url()).toBe('/sessions'))
  })
})

describe('SessionsWorkspaceView — one chrome row above the work', () => {
  afterEach(() => stubViewportWidth(1440))

  /** The surface, not the table: this is the screen an operator arrives on. */
  function renderSurface() {
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: 0 } },
    })
    return render(
      <QueryClientProvider client={qc}>
        <SessionsWorkspaceView entrance="observe" />
      </QueryClientProvider>,
    )
  }

  // Measured on the seeded estate at 1440×900: title line 24, gap 12, tab strip 36 —
  // the first rail row at y=181 against a budget of 136. The strip joins the title
  // line, which is the shape /console and /provider-profiles already carry.
  it('paints the title and the tab strip on the same 36 px row', async () => {
    renderSurface()
    await screen.findAllByRole('tab', { name: 'Table' })
    const chrome = document.querySelector('[data-slot="work-chrome"]')
    expect(chrome).not.toBeNull()
    expect(chrome!.className).toMatch(/\bh-9\b/)
    expect(
      within(chrome as HTMLElement).getByRole('heading', { level: 1 }),
    ).toHaveTextContent('Sessions')
    expect(
      within(chrome as HTMLElement).getByRole('tablist'),
    ).toBeInTheDocument()
  })

  it('keeps the counts and the scope note on that same line', async () => {
    renderSurface()
    const chrome = (await waitFor(() => {
      const el = document.querySelector('[data-slot="work-chrome"]')
      expect(el).not.toBeNull()
      return el
    })) as HTMLElement
    await waitFor(() =>
      expect(
        within(chrome).getByTestId('sessions-summary'),
      ).toBeInTheDocument(),
    )
    expect(
      within(chrome).getByTestId('sessions-scope-note'),
    ).toBeInTheDocument()
  })

  // Measured at 1440: the description was cut from 1454 px to 797 with no `title` at
  // all — the attribute was there a round earlier and a later one lost it, because the
  // description on THIS screen is a node (the counts and the scope note ride in it) and
  // `title` takes a string. The walk is what catches the next one here instead of in a
  // browser: every truncating element in the header owes the reader the whole text.
  it('leaves no truncating element in the header without a title', async () => {
    renderSurface()
    const chrome = (await waitFor(() => {
      const el = document.querySelector('[data-slot="work-chrome"]')
      expect(el).not.toBeNull()
      return el
    })) as HTMLElement
    await waitFor(() =>
      expect(
        within(chrome).getByTestId('sessions-summary'),
      ).toBeInTheDocument(),
    )
    const header = chrome.querySelector(
      '[data-slot="page-header"]',
    ) as HTMLElement
    const naked = [...header.querySelectorAll('.truncate')].filter(
      (el) => !el.closest('[title]'),
    )
    expect(naked.map((el) => (el.textContent ?? '').slice(0, 40))).toEqual([])
    const described = within(chrome).getByTestId('sessions-summary')
      .parentElement as HTMLElement
    expect(described.getAttribute('title')).toContain('Tenant-wide')
  })

  /**
   * ⛔ THE PANEL IS PAINTED AND UNTOUCHABLE WHEN THE ROW CLIPS IT, and no assertion this
   *    round had could see that. The disclosure opens a panel `absolute top-full` INSIDE
   *    the chrome row; the row was `h-9 … overflow-hidden`, so at 390×844 the panel's box
   *    ran y 94→168 inside a row that ended at 96 and `elementFromPoint` at its centre
   *    answered the table underneath — Launch, Refresh, Register and Export unreachable on
   *    all four screens, with `aria-expanded` true and no `hidden` class, which is exactly
   *    what the round-3 oracle asserted.
   *
   *    The walk is over CLASSES because jsdom loads no stylesheet (see `@/test/clipping`);
   *    the computed fact is measured in a browser by `code-r4/probe/panel.mjs`.
   */
  it('phone: nothing between the open panel and the page clips it', async () => {
    const user = userEvent.setup()
    perms.add('sessions:run:write')
    stubViewportWidth(390)
    renderSurface()
    const toggle = await screen.findByTestId('page-actions-toggle')
    await user.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    const panel = document.querySelector(
      '[data-slot="page-actions"]',
    ) as HTMLElement
    // Joined, so a failure NAMES the element that cut the panel on its one line.
    expect(clippingAncestors(panel).join(' | ')).toBe('')
    expect(
      within(panel).getByRole('button', { name: 'New session' }),
    ).toBeInTheDocument()
  })

  // Nothing between the chrome and the panes but the notices, and those only when
  // something has to be said: a tab strip of its own there is what cost the 45 px.
  it('puts the panes straight under the chrome row, with no strip between them', async () => {
    renderSurface()
    const panes = await screen.findByTestId('sessions-panes')
    const chrome = document.querySelector('[data-slot="work-chrome"]')!
    expect(chrome.parentElement).toBe(panes.parentElement)
    expect(panes.previousElementSibling).toBe(chrome)
    expect(panes.querySelector('[role="tablist"]')).toBeNull()
    // The tab body starts flush under the strip; `pt-4` there is a second gap.
    for (const panel of panes.querySelectorAll('[role="tabpanel"]')) {
      expect(panel.className).toMatch(/\bpt-0\b/)
    }
  })
})
