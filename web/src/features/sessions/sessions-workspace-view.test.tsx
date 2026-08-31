// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import type { LiveDTO } from './types'

const perms = new Set<string>()
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: (p: string) => perms.has(p) }),
}))

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

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
vi.mock('@/features/agentops/workspaces-panel', () => ({
  WorkspacesPanel: () => <div>workspaces-panel</div>,
}))
vi.mock('@/features/agentops/run-create-dialog', () => ({
  RunCreateDialog: () => null,
}))
vi.mock('./session-card', () => ({
  SessionCard: ({
    target,
  }: {
    target: { sessionRef?: string; runRef?: string } | null
  }) =>
    target ? (
      <div data-testid="card">
        card:{target.sessionRef ?? ''}|{target.runRef ?? ''}
      </div>
    ) : null,
}))

vi.mock('./api', () => ({
  sessionsApi: { live: vi.fn(), liveOne: vi.fn(), timeline: vi.fn() },
  sessionsKeys: {
    all: (t: string | null) => ['s', t],
    live: (t: string | null, p?: unknown) => ['s', t, 'live', p ?? null],
    liveOne: (t: string | null, ref: string) => ['s', t, 'one', ref],
    timeline: (t: string | null, ref: string, p?: unknown) => [
      's',
      t,
      'tl',
      ref,
      p ?? null,
    ],
  },
}))

vi.mock('@/features/agentops/api', () => ({
  agentOpsApi: { listRuns: vi.fn(), getRun: vi.fn() },
  agentOpsKeys: {
    all: (t: string | null) => ['a', t],
    runs: (t: string | null, p?: unknown) => ['a', t, 'runs', p ?? null],
    run: (t: string | null, r: string) => ['a', t, 'run', r],
  },
}))

import { agentOpsApi } from '@/features/agentops/api'
import { sessionsApi } from './api'
import { SessionsWorkspaceView } from './sessions-workspace-view'

const observed: LiveDTO = {
  session_ref: 'sess-found',
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

const launchedLive: LiveDTO = { ...observed, session_ref: 'sess-ours' }

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

function renderView(entrance: 'observe' | 'operate' = 'observe') {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <SessionsWorkspaceView entrance={entrance} />
    </QueryClientProvider>,
  )
}

const rowFor = async (label: string) => {
  const cell = await screen.findByText(label)
  return cell.closest('tr') as HTMLElement
}

beforeEach(() => {
  vi.clearAllMocks()
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
})

describe('SessionsWorkspaceView — one destination, both origins', () => {
  it('lists discovered and launched sessions in ONE table, each labelled', async () => {
    renderView()
    const found = await rowFor('sess-found')
    expect(within(found).getByText('Discovered')).toBeInTheDocument()
    // The launched one is titled by the name its operator typed at launch.
    const ours = await rowFor('nightly-indexer')
    expect(within(ours).getByText('Launched')).toBeInTheDocument()
    // …and it is ONE row, not two: the observed and operate halves folded together.
    // The session id stays ON that row (as the secondary line) so it remains
    // searchable — naming the row after the run must not hide the id the ledger,
    // the API and any saved deep link use.
    expect(screen.getAllByRole('row')).toHaveLength(3) // header + 2 sessions
    expect(within(ours).getByText('sess-ours')).toBeInTheDocument()
  })

  it('shows the control level per row, from the plane and not from the caller', async () => {
    renderView()
    const ours = await rowFor('nightly-indexer')
    expect(within(ours).getByText('Full control')).toBeInTheDocument()
    const found = await rowFor('sess-found')
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
    await screen.findByText('sess-found')
    await user.click(screen.getByLabelText('All sources'))
    await user.click(await screen.findByRole('option', { name: 'Launched' }))
    await waitFor(() =>
      expect(screen.queryByText('sess-found')).not.toBeInTheDocument(),
    )
    expect(screen.getByText('nightly-indexer')).toBeInTheDocument()
  })

  it('opens the SAME card from either kind of row', async () => {
    const user = userEvent.setup()
    renderView()
    await user.click(await rowFor('sess-found'))
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
    await screen.findByText('sess-found')
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
    await screen.findByText('sess-found')
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
      expect(screen.queryByText('sess-found')).not.toBeInTheDocument(),
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
    await screen.findByText('sess-found')
    expect(
      screen.getByText(/most recent page, not the whole estate/i),
    ).toBeInTheDocument()
  })

  it('says nothing about truncation when both pages are complete', async () => {
    renderView()
    await screen.findByText('sess-found')
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
    expect(await screen.findByText('sess-found')).toBeInTheDocument()
  })

  it('keeps the observe framing on the /sessions door', async () => {
    renderView('observe')
    expect(
      await screen.findByRole('heading', { name: 'Sessions' }),
    ).toBeInTheDocument()
    // …and still lists what Olivares launched.
    expect(await screen.findByText('nightly-indexer')).toBeInTheDocument()
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
    await screen.findByText('sess-found')
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
