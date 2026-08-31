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

vi.mock('@/features/shared', async () => {
  const actual =
    await vi.importActual<typeof import('@/features/shared')>(
      '@/features/shared',
    )
  return { ...actual, useLiveStream: () => ({ status: 'open' }) }
})

vi.mock('@/features/agentops/live-console', () => ({
  LiveConsole: () => <div>live-console</div>,
}))
vi.mock('@/features/agentops/governance-panel', () => ({
  GovernancePanel: () => <div>governance-panel</div>,
}))
vi.mock('@/features/agentops/run-detail', () => ({
  EventsPanel: () => <div>events-panel</div>,
  RunInfo: () => <div>run-info</div>,
}))
vi.mock('./timeline', () => ({ SessionTimeline: () => <div>timeline</div> }))

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
  agentOpsApi: {
    listRuns: vi.fn(),
    getRun: vi.fn(),
    stop: vi.fn(),
    resume: vi.fn(),
    cleanup: vi.fn(),
    deleteRun: vi.fn(),
  },
  agentOpsKeys: {
    all: (t: string | null) => ['a', t],
    runs: (t: string | null, p?: unknown) => ['a', t, 'runs', p ?? null],
    run: (t: string | null, r: string) => ['a', t, 'run', r],
  },
}))

import { ApiError } from '@/lib/api/errors'
import { agentOpsApi } from '@/features/agentops/api'
import { sessionsApi } from './api'
import { SessionCard } from './session-card'

const live: LiveDTO = {
  session_ref: 'sess-ours',
  cc_state: 'active',
  input_tokens: 1200,
  output_tokens: 800,
  cost_micro_usd: 42000,
  event_count: 3,
  tool_call_count: 2,
  first_event_at: '2026-08-10T10:00:00Z',
  last_event_at: '2026-08-10T10:05:00Z',
  duration_seconds: 300,
}

const run: RunDTO = {
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
}

function renderCard(target: { sessionRef?: string; runRef?: string }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <SessionCard target={target} onClose={() => {}} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  perms.clear()
  perms.add('sessions:live:read')
  perms.add('sessions:run:read')
  vi.mocked(sessionsApi.liveOne).mockResolvedValue(live)
  vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
    items: [run],
    has_more: false,
  })
  vi.mocked(agentOpsApi.getRun).mockResolvedValue(run)
})

describe('SessionCard — provenance comes from the engine', () => {
  it('asks the engine which runs drive THIS session, by its id', async () => {
    renderCard({ sessionRef: 'sess-ours' })
    await waitFor(() =>
      expect(vi.mocked(agentOpsApi.listRuns)).toHaveBeenCalledWith({
        claude_session_id: 'sess-ours',
      }),
    )
  })

  it('states LAUNCHED, with the run that drives it', async () => {
    renderCard({ sessionRef: 'sess-ours' })
    expect(await screen.findByText('Launched')).toBeInTheDocument()
    expect(
      screen.getByText(/A launch record links this session to Olivares/i),
    ).toBeInTheDocument()
    // Twice on purpose: the card is TITLED by the run's name and the run is also
    // listed as the evidence for the provenance claim.
    expect(screen.getAllByText('nightly-indexer').length).toBeGreaterThan(0)
  })

  it('states DISCOVERED when the engine links no run — and says why', async () => {
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [],
      has_more: false,
    })
    renderCard({ sessionRef: 'sess-found' })
    expect(await screen.findByText('Discovered')).toBeInTheDocument()
    expect(
      screen.getByText(/No launch record links this session/i),
    ).toBeInTheDocument()
  })

  it('does NOT claim discovered when it could not look (no run:read)', async () => {
    perms.delete('sessions:run:read')
    renderCard({ sessionRef: 'sess-found' })
    expect(
      await screen.findByText(/This is “not read”, not “not launched”/i),
    ).toBeInTheDocument()
    expect(vi.mocked(agentOpsApi.listRuns)).not.toHaveBeenCalled()
  })

  it('does NOT claim discovered when the run lookup FAILED either', async () => {
    // Missing permission was covered; a failed lookup was not, and the mutation round
    // caught the gap by surviving. Both are "could not look", and neither is an answer.
    vi.mocked(agentOpsApi.listRuns).mockRejectedValue(new Error('boom'))
    renderCard({ sessionRef: 'sess-found' })
    expect(
      await screen.findByText(/This is “not read”, not “not launched”/i),
    ).toBeInTheDocument()
    expect(screen.queryByText('Discovered')).not.toBeInTheDocument()
    expect(screen.getByText('Origin not read')).toBeInTheDocument()
  })

  it('does NOT claim discovered when the seed run read FAILED', async () => {
    vi.mocked(agentOpsApi.getRun).mockRejectedValue(new Error('boom'))
    renderCard({ runRef: 'run-1' })
    expect(await screen.findByText('Origin not read')).toBeInTheDocument()
  })

  it('names every run when more than one drives the session', async () => {
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [run, { ...run, run_ref: 'run-2', name: 'resumed' }],
      has_more: false,
    })
    renderCard({ sessionRef: 'sess-ours' })
    expect(
      await screen.findByText(/2 runs drive this session/i),
    ).toBeInTheDocument()
    expect(screen.getByText('resumed')).toBeInTheDocument()
  })

  it('resolves the observed half from a run when opened from the operate side', async () => {
    renderCard({ runRef: 'run-1' })
    await waitFor(() =>
      expect(vi.mocked(sessionsApi.liveOne)).toHaveBeenCalledWith('sess-ours'),
    )
  })
})

describe('SessionCard — control is what can be done, not what fits', () => {
  it('says FULL CONTROL for a live bridged run and offers Stop', async () => {
    perms.add('sessions:run:write')
    renderCard({ sessionRef: 'sess-ours' })
    expect(await screen.findByText('Full control')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Stop' })).toBeInTheDocument()
  })

  it('says LIFECYCLE ONLY for a relayed run, and why its I/O cannot be shown', async () => {
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [{ ...run, transport: 'remote-control' }],
      has_more: false,
    })
    renderCard({ sessionRef: 'sess-ours' })
    expect(await screen.findByText('Lifecycle only')).toBeInTheDocument()
    expect(
      screen.getAllByText(/relayed to Anthropic’s cloud/i).length,
    ).toBeGreaterThan(0)
  })

  it('says OBSERVE ONLY for a discovered session and offers no lifecycle button', async () => {
    perms.add('sessions:run:write')
    perms.add('sessions:run:admin')
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [],
      has_more: false,
    })
    renderCard({ sessionRef: 'sess-found' })
    expect(await screen.findByText('Observe only')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Stop' }),
    ).not.toBeInTheDocument()
    // …and it explains that this is the PLANE's limit, not the caller's.
    expect(
      screen.getByText(
        /Unavailable: no process: Olivares did not launch this session/i,
      ),
    ).toBeInTheDocument()
  })

  it('blames the ROLE, not the plane, when a writer permission is missing', async () => {
    renderCard({ sessionRef: 'sess-ours' })
    expect(await screen.findByText('Full control')).toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Stop' }),
    ).not.toBeInTheDocument()
    expect(
      screen.getAllByText(/your role does not include it/i).length,
    ).toBeGreaterThan(0)
  })

  it('switches the control level with the run the operator picks', async () => {
    // Two live runs on one session: the card opens on the bridged one (full control)
    // and must FOLLOW the picker — the level and the capabilities always describing
    // the same run is the whole point of splitting plane reach from caller rights.
    const user = userEvent.setup()
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [
        run,
        {
          ...run,
          run_ref: 'run-2',
          name: 'relayed-run',
          transport: 'remote-control',
        },
      ],
      has_more: false,
    })
    renderCard({ sessionRef: 'sess-ours' })
    expect(await screen.findByText('Full control')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /relayed-run/ }))
    expect(await screen.findByText('Lifecycle only')).toBeInTheDocument()
    expect(screen.queryByText('Full control')).not.toBeInTheDocument()
  })

  it('shows the observed telemetry the two screens never showed together', async () => {
    renderCard({ sessionRef: 'sess-ours' })
    expect(await screen.findByText('$0.042')).toBeInTheDocument()
    expect(screen.getByText('timeline')).toBeInTheDocument()
  })

  it('says nothing was observed when the engine ANSWERS 404', async () => {
    vi.mocked(sessionsApi.liveOne).mockRejectedValue(
      new ApiError(404, 'not_found', 'not found'),
    )
    renderCard({ sessionRef: 'sess-ours' })
    expect(
      await screen.findByText(/Nothing observed for this session yet/i),
    ).toBeInTheDocument()
  })

  it('does NOT call a failed observed lookup "nothing observed"', async () => {
    // A 404 is an answer; a 500 or a dropped connection is not. Painting the second
    // as the first is the same conflation this card exists to remove.
    vi.mocked(sessionsApi.liveOne).mockRejectedValue(
      new ApiError(503, 'unavailable', 'engine unavailable'),
    )
    renderCard({ sessionRef: 'sess-ours' })
    expect(
      await screen.findByText(/observed half was not read/i),
    ).toBeInTheDocument()
    expect(
      screen.queryByText(/Nothing observed for this session yet/i),
    ).not.toBeInTheDocument()
  })

  it('fires Stop only for a running run and a run writer', async () => {
    const user = userEvent.setup()
    perms.add('sessions:run:write')
    renderCard({ sessionRef: 'sess-ours' })

    await user.click(await screen.findByRole('button', { name: 'Stop' }))

    await waitFor(() => expect(agentOpsApi.stop).toHaveBeenCalledWith('run-1'))
  })

  it('fires Resume for a resumable stopped run', async () => {
    const user = userEvent.setup()
    perms.add('sessions:run:write')
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [{ ...run, state: 'stopped' }],
      has_more: false,
    })
    renderCard({ sessionRef: 'sess-ours' })

    await user.click(await screen.findByRole('button', { name: 'Resume' }))

    await waitFor(() =>
      expect(agentOpsApi.resume).toHaveBeenCalledWith('run-1'),
    )
  })

  it.each(['stopped', 'failed'] as const)(
    'confirms and fires Clean up for a %s run only for a run admin',
    async (state) => {
      const user = userEvent.setup()
      perms.add('sessions:run:admin')
      vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
        items: [{ ...run, state }],
        has_more: false,
      })
      renderCard({ sessionRef: 'sess-ours' })

      await user.click(await screen.findByRole('button', { name: 'Clean up' }))
      const dialog = await screen.findByRole('dialog', { name: 'Clean up' })
      await user.click(within(dialog).getByRole('button', { name: 'Clean up' }))

      await waitFor(() =>
        expect(agentOpsApi.cleanup).toHaveBeenCalledWith('run-1'),
      )
    },
  )

  it('confirms and fires Delete only for a cleaned run and a run admin', async () => {
    const user = userEvent.setup()
    perms.add('sessions:run:admin')
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [{ ...run, state: 'cleaned' }],
      has_more: false,
    })
    renderCard({ sessionRef: 'sess-ours' })

    await user.click(await screen.findByRole('button', { name: 'Delete' }))
    const dialog = await screen.findByRole('dialog', { name: 'Delete' })
    await user.click(within(dialog).getByRole('button', { name: 'Delete' }))

    await waitFor(() =>
      expect(agentOpsApi.deleteRun).toHaveBeenCalledWith('run-1'),
    )
  })

  it('withholds lifecycle controls when the matching RBAC grant is absent', async () => {
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [{ ...run, state: 'stopped' }],
      has_more: false,
    })
    renderCard({ sessionRef: 'sess-ours' })

    await screen.findByText('Lifecycle only')
    expect(
      screen.queryByRole('button', { name: 'Resume' }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Clean up' }),
    ).not.toBeInTheDocument()
  })
})
