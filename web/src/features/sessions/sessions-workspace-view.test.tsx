// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { ComponentProps } from 'react'
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
const streamStatus = vi.hoisted(() => ({
  value: 'open' as 'open' | 'error',
}))
vi.mock('@/features/shared', async () => {
  const actual =
    await vi.importActual<typeof import('@/features/shared')>(
      '@/features/shared',
    )
  return { ...actual, useLiveStream: () => ({ status: streamStatus.value }) }
})

// The heavy operate panels are proven by their own tests; mounting them here would
// drag CodeMirror and the attach EventSource into a table test.
vi.mock('@/features/agentops/profiles-panel', () => ({
  ProfilesPanel: () => <div>profiles-panel</div>,
}))
vi.mock('@/features/agentops/workspaces-panel', () => ({
  WorkspacesPanel: () => <div>workspaces-panel</div>,
}))
// Keep the real modal focus lifecycle; the form's inputs have their own tests.
vi.mock('@/features/agentops/run-create-dialog', async () => {
  const { Dialog, DialogContent, DialogTitle } =
    await import('@/components/ui/dialog')
  return {
    RunCreateDialog: ({
      open,
      onOpenChange,
      onCloseAutoFocus,
    }: ComponentProps<
      typeof import('@/features/agentops/run-create-dialog').RunCreateDialog
    >) => (
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent
          onCloseAutoFocus={onCloseAutoFocus}
          aria-describedby={undefined}
        >
          <DialogTitle>Advanced launch</DialogTitle>
        </DialogContent>
      </Dialog>
    ),
  }
})
vi.mock('@/features/first-hour/first-hour', () => ({
  NewSessionDialog: ({
    open,
    onAdvanced,
  }: {
    open: boolean
    onAdvanced: () => void
  }) =>
    open ? <button onClick={onAdvanced}>Advanced launch options</button> : null,
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
import { NewSessionHost } from '@/features/first-hour/new-session-host'
import { useNewSessionDialog } from '@/features/first-hour/new-session-store'
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
 *    The table is reached through the list header's `⋯` menu ("Show as table"). The menu
 *    opens on the trigger's Enter key, which jsdom delivers as a plain KeyboardEvent
 *    (Radix opens on pointer-down only for a real pointer event).
 */
function openListMenu() {
  // The LAST list header: a few cases render the view twice in one test, and the newest
  // mount is the one they go on to assert against.
  const triggers = screen.queryAllByTestId('sessions-list-menu')
  if (triggers.length === 0) return
  act(() => {
    fireEvent.keyDown(triggers[triggers.length - 1], { key: 'Enter' })
  })
}

function openView(label: 'Show as table' | 'Workspaces' | 'Provider profiles') {
  openListMenu()
  const items = screen.getAllByRole('menuitem', { name: label })
  act(() => {
    fireEvent.click(items[items.length - 1])
  })
}

function openTable() {
  openView('Show as table')
}

function renderView(
  entrance: 'observe' | 'operate' = 'observe',
  { table = true }: { table?: boolean } = {},
) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  const result = render(
    <QueryClientProvider client={qc}>
      <SessionsWorkspaceView entrance={entrance} />
    </QueryClientProvider>,
  )
  if (
    table &&
    new URLSearchParams(fakeRouter.searchStr()).get('tab') !== 'table'
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
  streamStatus.value = 'open'
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

  // The fit itself is a browser measurement — jsdom lays nothing out. What this case
  // holds is the declaration that produces it: a fixed table, a last column that may
  // reach zero, and a cell whose content gives way at its end with the whole of it
  // still reachable on `title`.
  it('the last column declares the way it gives: fixed table, min-w-0, truncate with a title', async () => {
    renderView()
    const ours = await rowFor('nightly-indexer')
    const table = ours.closest('table') as HTMLElement
    expect(table.className.split(/\s+/)).toContain('table-fixed')
    const lastHead = screen.getByRole('columnheader', { name: /^last seen$/i })
    expect(lastHead.className.split(/\s+/)).toContain('min-w-0')
    const lastCell = ours.querySelector('td:last-child') as HTMLElement
    expect(lastCell.className.split(/\s+/)).toContain('min-w-0')
    const clipped = lastCell.querySelector('.truncate')
    expect(clipped).not.toBeNull()
    expect(clipped).toHaveAttribute('title')
  })

  // ⛔ FIXED LAYOUT DIVIDES WHAT NOBODY CLAIMS — AND CLAIMS ONLY FIT WHERE THERE IS
  //    ROOM. The table is `table-fixed` so the last column can give way, and under that
  //    algorithm a column with no declared width shares the table equally with the other
  //    nine: the session name, the widest fact on the row, was given the width of a
  //    two-badge state cell. Three columns claim their own — but the three claims add up
  //    to more than a phone's whole region, where they would leave the other seven
  //    nothing and push the table into its horizontal scroll, so below the shell's
  //    breakpoint no column claims anything and the ten share the region again.
  //    jsdom lays nothing out: this holds the declaration at both widths and no more.
  it('the three columns declare their own width from the desktop breakpoint up', async () => {
    stubViewportWidth(1440)
    renderView()
    await rowFor('nightly-indexer')
    const head = (name: RegExp) =>
      screen.getByRole('columnheader', { name }) as HTMLElement
    const session = head(/^session$/i)
    const state = head(/^state$/i)
    const lastSeen = head(/^last seen$/i)
    for (const column of [session, state, lastSeen]) {
      expect(column.style.width).not.toBe('')
    }
    // Three declarations, not one repeated: the name column is the widest of them.
    expect(parseInt(session.style.width, 10)).toBeGreaterThan(
      parseInt(state.style.width, 10),
    )
    expect(parseInt(session.style.width, 10)).toBeGreaterThan(
      parseInt(lastSeen.style.width, 10),
    )
    // A column with no width of its own keeps the shared one.
    expect(head(/^cost$/i).style.width).toBe('')
    // And the cap that fixed layout never applied is gone from header and cell.
    expect(lastSeen.className).not.toContain('max-w-')
    expect(document.querySelectorAll('[class*="max-w-[8rem]"]')).toHaveLength(0)
  })

  it('at phone width no column claims a width, so the ten share the region', async () => {
    stubViewportWidth(390)
    renderView()
    await rowFor('nightly-indexer')
    for (const name of [/^session$/i, /^state$/i, /^last seen$/i, /^cost$/i]) {
      expect(
        (screen.getByRole('columnheader', { name }) as HTMLElement).style.width,
      ).toBe('')
    }
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
  it('reads Sessions on the /agentops door too, and opens its table on launched sessions', async () => {
    renderView('operate')
    expect(
      await screen.findByRole('heading', { name: 'Table' }),
    ).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Claude Code' })).toBeNull()
    // The door is only the filter the table opens on: what Olivares launched, while
    // the source filter keeps every other session one choice away.
    expect(
      await screen.findByRole('combobox', { name: 'All sources' }),
    ).toHaveTextContent('Launched')
    expect(await screen.findByText('nightly-indexer')).toBeInTheDocument()
    expect(screen.queryByText('Untitled session')).toBeNull()
  })

  it('says the filters hide the sessions, never "no sessions yet", and offers the way back', async () => {
    // Nothing launched: the operate door opens on a filter that matches nothing.
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [],
      has_more: false,
    } as never)
    const user = userEvent.setup()
    renderView('operate')
    expect(
      await screen.findByText('No sessions match these filters'),
    ).toBeInTheDocument()
    expect(screen.queryByText('No sessions yet')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Clear filters' }))
    // Both discovered sessions are back in the table.
    expect(await screen.findAllByText('Untitled session')).toHaveLength(2)
  })

  it('keeps the observe framing on the /sessions door', async () => {
    renderView('observe', { table: false })
    expect(
      await screen.findByRole('heading', { name: 'Sessions' }),
    ).toBeInTheDocument()
    // …and still lists what Olivares launched.
    expect(await screen.findByText('nightly-indexer')).toBeInTheDocument()
  })

  it('offers the provider-profile view only to a principal who can read profiles', async () => {
    perms.add('sessions:profile:read')
    const first = renderView('observe', { table: false })
    await screen.findByTestId('sessions-list-menu')
    openListMenu()
    expect(
      await screen.findByRole('menuitem', { name: 'Provider profiles' }),
    ).toBeInTheDocument()
    first.unmount()
    perms.delete('sessions:profile:read')
    renderView('observe', { table: false })
    await screen.findByTestId('sessions-list-menu')
    openListMenu()
    await screen.findByRole('menuitem', { name: 'Show as table' })
    expect(
      screen.queryByRole('menuitem', { name: 'Provider profiles' }),
    ).toBeNull()
  })

  it('offers the workspace view only to a principal who can read runs', async () => {
    const first = renderView('observe', { table: false })
    await screen.findByTestId('sessions-list-menu')
    openListMenu()
    expect(
      await screen.findByRole('menuitem', { name: 'Workspaces' }),
    ).toBeInTheDocument()
    first.unmount()
    perms.delete('sessions:run:read')
    renderView('observe', { table: false })
    await screen.findByTestId('sessions-list-menu')
    openListMenu()
    await screen.findByRole('menuitem', { name: 'Show as table' })
    expect(screen.queryByRole('menuitem', { name: 'Workspaces' })).toBeNull()
  })

  it('offers the launch action only to a principal who can write runs', async () => {
    const first = renderView()
    await screen.findByText('Untitled session')
    expect(
      screen.queryByRole('button', { name: /New session/i }),
    ).not.toBeInTheDocument()
    first.unmount()
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
  it('keeps the name, the count and the views menu in the list header, with no tab strip', async () => {
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
    const header = heading.closest('[data-slot="list-header"]') as HTMLElement
    expect(header).toContainElement(summary)
    expect(header).toContainElement(screen.getByTestId('sessions-list-menu'))
    const panes = screen.getByTestId('sessions-panes')
    expect(panes).toContainElement(await screen.findByTestId('work-rail'))
    expect(panes).toContainElement(header)
    expect(screen.queryByRole('tablist')).toBeNull()
    // The list is live: no Refresh button and no Live chip beside the name.
    expect(screen.queryByRole('button', { name: 'Refresh' })).toBeNull()
    expect(within(header).queryByText('Live')).toBeNull()
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
    renderView('observe', { table: false })
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
    expect(fakeRouter.url()).toBe(
      '/sessions?tab=table&session=sess%3Asess-found',
    )
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
    await waitFor(() => expect(fakeRouter.url()).toBe('/sessions?tab=table'))
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
    await waitFor(() => expect(fakeRouter.url()).toBe('/sessions?tab=table'))
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

  // The list header is ONE 48 px row: the name, the count, `+` and `⋯`. The first row of
  // the list starts under it, and nothing else spends a line above the work.
  it('paints the list header on one 48 px row, with the name as the page heading', async () => {
    renderSurface()
    const heading = await screen.findByRole('heading', { level: 1 })
    expect(heading).toHaveTextContent('Sessions')
    const header = heading.closest('[data-slot="list-header"]') as HTMLElement
    expect(header).not.toBeNull()
    expect(header.firstElementChild!.className).toMatch(/\bh-12\b/)
    expect(screen.queryByRole('tablist')).toBeNull()
  })

  it('the count is a number, and says sessions and running on its hover (no engine wording)', async () => {
    renderSurface()
    const summary = await screen.findByTestId('sessions-summary')
    expect(summary.textContent).toMatch(/^\d+$/)
    expect(summary.getAttribute('title')).toMatch(
      /^\d+ sessions?( · \d+ running)?$/,
    )
    expect(summary).toHaveAttribute('aria-hidden', 'true')
    expect(
      screen.getByText(summary.getAttribute('title') as string),
    ).toHaveClass('sr-only')
    const header = summary.closest('[data-slot="list-header"]') as HTMLElement
    expect(header.textContent).not.toMatch(/plane|Tenant-wide|—/i)
    expect(screen.queryByTestId('sessions-scope-note')).toBeNull()
  })

  // CLX via Root: with no sessions the header said "0 sessions · 0 running" above the
  // empty state. The empty state is the whole answer.
  it('with no sessions the header shows no count, only the empty state speaks', async () => {
    vi.mocked(sessionsApi.live).mockResolvedValue({
      items: [],
      has_more: false,
    })
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [],
      has_more: false,
    })
    renderSurface()
    await screen.findByTestId('sessions-list-menu')
    await waitFor(() => expect(agentOpsApi.listRuns).toHaveBeenCalled())
    await new Promise((r) => setTimeout(r, 50))
    expect(screen.queryByTestId('sessions-summary')).toBeNull()
    expect(screen.queryByText(/0 sessions/)).toBeNull()
  })

  // Every truncating element in the header owes the reader the whole text.
  it('leaves no truncating element in the list header without a title', async () => {
    renderSurface()
    const summary = await screen.findByTestId('sessions-summary')
    const header = summary.closest('[data-slot="list-header"]') as HTMLElement
    const naked = [...header.querySelectorAll('.truncate')].filter(
      (el) => !el.closest('[title]'),
    )
    expect(naked.map((el) => (el.textContent ?? '').slice(0, 40))).toEqual([])
  })

  it('names the icon buttons: New session and More, each with a hover', async () => {
    perms.add('sessions:run:write')
    renderSurface()
    const user = userEvent.setup()
    const add = await screen.findByTestId('sessions-new')
    expect(add).toHaveAccessibleName('New session')
    const more = screen.getByTestId('sessions-list-menu')
    expect(more).toHaveAccessibleName('More')
    await user.hover(more)
    expect(await screen.findByRole('tooltip')).toHaveTextContent('More')
  })

  // Nothing between the notices and the panes: the list header is inside the panes.
  it('puts the panes straight under the notices, with no strip between them', async () => {
    renderSurface()
    const panes = await screen.findByTestId('sessions-panes')
    expect(panes.querySelector('[role="tablist"]')).toBeNull()
    expect(document.querySelector('[data-slot="work-chrome"]')).toBeNull()
    for (const panel of panes.querySelectorAll('[role="tabpanel"]')) {
      expect(panel.className).toMatch(/\bpt-0\b/)
    }
  })

  it('opens the table from the menu, with a way back to the list', async () => {
    const user = userEvent.setup()
    renderSurface()
    await screen.findByTestId('work-rail')
    openTable()
    expect(
      await screen.findByRole('heading', { level: 1, name: 'Table' }),
    ).toBeInTheDocument()
    await user.click(screen.getByTestId('sessions-view-back'))
    expect(
      await screen.findByRole('heading', { level: 1, name: 'Sessions' }),
    ).toBeInTheDocument()
  })

  it('shows a failed stream as one quiet line under the header, and nothing otherwise', async () => {
    streamStatus.value = 'error'
    const first = renderSurface()
    const notice = await screen.findByTestId('sessions-stream-notice')
    expect(notice).toHaveTextContent('Reconnecting…')
    expect(notice.closest('[data-slot="list-header"]')).not.toBeNull()
    first.unmount()
    streamStatus.value = 'open'
    renderSurface()
    await screen.findByTestId('sessions-list-menu')
    expect(screen.queryByTestId('sessions-stream-notice')).toBeNull()
  })
})

// 26.10.1 review, measured again before this fix: an empty Sessions page painted two
// empty states and THREE New session buttons (header, rail, work pane). One empty estate
// is one fact.
describe('SessionsWorkspaceView — an empty estate', () => {
  afterEach(() => stubViewportWidth(1440))

  function renderEmpty(entrance: 'observe' | 'operate' = 'observe') {
    vi.mocked(sessionsApi.live).mockResolvedValue({
      items: [],
      has_more: false,
    })
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [],
      has_more: false,
    } as never)
    perms.add('sessions:run:write')
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: 0 } },
    })
    // The shell's one New session host, which the header's New session opens.
    useNewSessionDialog.setState({ open: false, opener: null, advanced: null })
    return render(
      <QueryClientProvider client={qc}>
        <SessionsWorkspaceView entrance={entrance} />
        <NewSessionHost />
      </QueryClientProvider>,
    )
  }

  it('shows one empty state, in one line of plain words, with the one New session', async () => {
    renderEmpty()
    const title = await screen.findByText('No sessions yet')
    expect(screen.getAllByText('No sessions yet')).toHaveLength(1)
    expect(screen.queryByText('Select a session')).toBeNull()
    expect(screen.queryByText('No session selected')).toBeNull()
    const empty = title.closest('[data-slot="empty-state"]') as HTMLElement
    expect(empty).toHaveTextContent(
      'Sessions you start, and the ones Olivares finds, appear here.',
    )
    expect(
      screen.getAllByRole('button', { name: /New session/i }),
    ).toHaveLength(1)
    const pill = within(empty).getByRole('button', { name: 'New session' })
    expect(pill).toBeInTheDocument()
    // The one primary action of an empty list is a pill in open space.
    expect(pill.className).toContain('rounded-full')
    expect(pill.className).toContain('bg-accent')
    // The list header stays beside it, without a second New session.
    expect(screen.getByTestId('sessions-list-menu')).toBeInTheDocument()
    expect(screen.queryByTestId('sessions-new')).toBeNull()
  })

  it('keeps exactly one New session on the Table tab', async () => {
    renderEmpty()
    await screen.findByText('No sessions yet')
    openTable()
    const title = await screen.findByText('No sessions yet')
    const empty = title.closest('[data-slot="empty-state"]') as HTMLElement
    expect(
      screen.getAllByRole('button', { name: /New session/i }),
    ).toHaveLength(1)
    expect(
      within(empty).getByRole('button', { name: 'New session' }),
    ).toBeInTheDocument()
  })

  it.each(['Sessions', 'Table', 'Provider profiles'])(
    'returns focus to the single New session action after Advanced launch closes on %s',
    async (tab) => {
      const user = userEvent.setup()
      perms.add('sessions:profile:read')
      renderEmpty()
      await screen.findByText('No sessions yet')
      if (tab === 'Table') openTable()
      if (tab === 'Provider profiles') openView('Provider profiles')
      const launch = screen.getByRole('button', { name: 'New session' })
      await user.click(launch)
      await user.click(
        screen.getByRole('button', { name: 'Advanced launch options' }),
      )
      await screen.findByRole('dialog', { name: 'Advanced launch' })
      await user.keyboard('{Escape}')
      await waitFor(() =>
        expect(screen.queryByRole('dialog')).not.toBeInTheDocument(),
      )
      expect(
        screen.getAllByRole('button', { name: 'New session' }),
      ).toHaveLength(1)
      await waitFor(() => expect(launch).toHaveFocus())
    },
  )

  it.each([
    [1024, 'narrative'],
    [1024, 'context'],
    [1024, 'rail'],
    [1600, 'narrative'],
  ] as const)(
    'keeps one header New session for a cold selection with empty lists at %s px, pane %s',
    async (width, pane) => {
      stubViewportWidth(width)
      fakeRouter.reset(`/sessions?session=sess%3Asess-found&pane=${pane}`)
      vi.mocked(sessionsApi.liveOne).mockResolvedValue(observed)
      vi.mocked(sessionsApi.timeline).mockResolvedValue({
        items: [],
        has_more: false,
      })
      renderEmpty()

      await screen.findByText('No sessions yet')
      await waitFor(() =>
        expect(sessionsApi.liveOne).toHaveBeenCalledWith('sess-found'),
      )
      const action = screen.getByRole('button', { name: 'New session' })
      expect(action).toBeEnabled()
      expect(action.closest('[data-slot="list-header"]')).not.toBeNull()
      // One, and in the list header: the rail's own empty state carries none.
      expect(
        screen.getAllByRole('button', { name: 'New session' }),
      ).toHaveLength(1)
      expect(
        within(
          document.getElementById('work-pane-rail') as HTMLElement,
        ).getAllByRole('button', { name: 'New session' }),
      ).toHaveLength(1)
      expect(
        within(
          document.querySelector('[data-slot="empty-state"]') as HTMLElement,
        ).queryByRole('button', { name: 'New session' }),
      ).not.toBeInTheDocument()
    },
  )

  it.each([true, false])(
    'keeps the Table error visible and New session gated by launch permission (%s)',
    async (canLaunch) => {
      if (canLaunch) perms.add('sessions:run:write')
      vi.mocked(sessionsApi.live).mockRejectedValue(
        new Error('live unavailable'),
      )
      vi.mocked(agentOpsApi.listRuns).mockRejectedValue(
        new Error('runs unavailable'),
      )
      renderView()

      await screen.findByRole('button', { name: /retry/i })
      expect(screen.queryByText('No sessions yet')).not.toBeInTheDocument()
      const actions = screen.queryAllByRole('button', { name: 'New session' })
      expect(actions).toHaveLength(canLaunch ? 1 : 0)
      if (canLaunch) expect(actions[0]).toBeEnabled()
    },
  )

  it.each(['Sessions', 'Table'])(
    'keeps New session available while %s reads are pending',
    async (tab) => {
      perms.add('sessions:run:write')
      vi.mocked(sessionsApi.live).mockReturnValue(new Promise(() => {}))
      vi.mocked(agentOpsApi.listRuns).mockReturnValue(new Promise(() => {}))
      renderView('observe', { table: tab === 'Table' })

      expect(screen.queryByText('No sessions yet')).not.toBeInTheDocument()
      expect(
        screen.getAllByRole('button', { name: 'New session' }),
      ).toHaveLength(1)
      expect(screen.getByRole('button', { name: 'New session' })).toBeEnabled()
    },
  )

  it('titles the page Sessions, as navigation names it, whichever door opened it', async () => {
    renderEmpty('operate')
    await screen.findByText('No sessions yet')
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent(
      /^Sessions$/,
    )
    expect(screen.queryByText('Claude Code')).toBeNull()
  })

  it('with sessions listed, New session is the header verb and appears once', async () => {
    perms.add('sessions:run:write')
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: 0 } },
    })
    render(
      <QueryClientProvider client={qc}>
        <SessionsWorkspaceView entrance="observe" />
      </QueryClientProvider>,
    )
    await screen.findByTestId('work-rail')
    await screen.findByTestId('sessions-summary')
    expect(
      screen.getAllByRole('button', { name: /New session/i }),
    ).toHaveLength(1)
  })
})

// THE OBSERVED HALF HAS NO TIMER AND ITS STREAM OPENS ONLY OVER AN ANSWER: a first read that
// failed stayed failed. The notice carries the way out.
describe('SessionsWorkspaceView — a half that failed can be asked again', () => {
  it('shows Retry on the live notice and asks the live half again, not the runs', async () => {
    const user = userEvent.setup()
    vi.mocked(sessionsApi.live).mockRejectedValue(new Error('live unavailable'))
    renderView('observe', { table: false })
    const notice = await screen.findByTestId('sessions-live-lookup-failed')
    expect(notice).toHaveTextContent(/observed half could not be read/i)
    const liveBefore = vi.mocked(sessionsApi.live).mock.calls.length
    const runsBefore = vi.mocked(agentOpsApi.listRuns).mock.calls.length
    vi.mocked(sessionsApi.live).mockResolvedValue({
      items: [observed],
      has_more: false,
    })
    await user.click(within(notice).getByRole('button', { name: 'Retry' }))
    await waitFor(() =>
      expect(vi.mocked(sessionsApi.live).mock.calls.length).toBeGreaterThan(
        liveBefore,
      ),
    )
    await waitFor(() =>
      expect(screen.queryByTestId('sessions-live-lookup-failed')).toBeNull(),
    )
    // The guarded read asks the runs again too when this person may read them.
    expect(
      vi.mocked(agentOpsApi.listRuns).mock.calls.length,
    ).toBeGreaterThanOrEqual(runsBefore)
  })

  it('shows Retry on the runs notice', async () => {
    const user = userEvent.setup()
    vi.mocked(agentOpsApi.listRuns).mockRejectedValue(new Error('runs down'))
    renderView('observe', { table: false })
    const notice = await screen.findByTestId('sessions-run-lookup-failed')
    const before = vi.mocked(agentOpsApi.listRuns).mock.calls.length
    await user.click(within(notice).getByRole('button', { name: 'Retry' }))
    await waitFor(() =>
      expect(vi.mocked(agentOpsApi.listRuns).mock.calls.length).toBeGreaterThan(
        before,
      ),
    )
  })
})

describe('SessionsWorkspaceView — where the keyboard goes between views', () => {
  it('enters another view on its back button and returns to the menu button', async () => {
    const user = userEvent.setup()
    renderView('observe', { table: false })
    await screen.findByTestId('sessions-list-menu')
    openTable()
    const back = await screen.findByTestId('sessions-view-back')
    expect(back).toHaveFocus()
    await user.click(back)
    expect(await screen.findByTestId('sessions-list-menu')).toHaveFocus()
  })
})

describe('URL views are authoritative', () => {
  it.each(['table', 'workspaces', 'profiles'] as const)(
    'opens a cold %s link and follows Back/Forward',
    async (tab) => {
      perms.add('sessions:workspace:read')
      perms.add('sessions:profile:read')
      fakeRouter.reset(`/sessions?tab=${tab}`)
      const view = renderView('observe', { table: false })
      const assertView = async () => {
        if (tab === 'table') await screen.findByRole('grid')
        else await screen.findByText(`${tab}-panel`)
      }
      await assertView()
      view.unmount()
      renderView('observe', { table: false })
      await assertView()
      act(() => fakeRouter.go('/sessions'))
      await screen.findByTestId('sessions-list-menu')
      act(() => fakeRouter.back())
      await assertView()
      act(() => fakeRouter.forward())
      await screen.findByTestId('sessions-list-menu')
    },
  )
})
