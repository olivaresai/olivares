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
  // No RouterProvider in this test: the shared Tabs strip consults useRouter, and the real
  // hook answers undefined here (console-tab-scroll-restoration R2, 2026-09-06).
  useRouter: () => undefined,
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
  sessionsApi: {
    live: vi.fn(),
    liveOne: vi.fn(),
    liveById: vi.fn(),
    timeline: vi.fn(),
    timelineById: vi.fn(),
  },
  sessionsKeys: {
    all: (t: string | null) => ['s', t],
    live: (t: string | null, p?: unknown) => ['s', t, 'live', p ?? null],
    liveOne: (t: string | null, ref: string) => ['s', t, 'one', ref],
    liveById: (t: string | null, ref: string) => ['s', t, 'by-id', ref],
    timeline: (t: string | null, ref: string, p?: unknown) => [
      's',
      t,
      'tl',
      ref,
      p ?? null,
    ],
    timelineById: (t: string | null, ref: string, p?: unknown) => [
      's',
      t,
      'tl-id',
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
import type { SessionTarget } from './session-target'
import { useSessionResolution } from './use-session-resolution'

const live: LiveDTO = {
  session_ref: 'sess-ours',
  live_ref: 'lr-ours',
  attribution: 'legacy',
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

/**
 * The card no longer resolves the session itself: the surface that owns it does,
 * once, and hands the answer down (the resolution carries an SSE subscription, and two
 * callers would mean two connections to one row). These tests are about the CARD, so
 * they keep addressing it by target and this wrapper does the one call the work surface
 * makes. It is deliberately the real hook: a double here would let the card and the
 * surface disagree about what "resolved" means, which is the thing the extraction
 * exists to prevent.
 */
function CardForTarget({
  target,
  onClose,
  onNavigate,
}: {
  target: SessionTarget | null
  onClose: () => void
  onNavigate?: (t: SessionTarget) => void
}) {
  const resolution = useSessionResolution(target)
  return (
    <SessionCard
      open
      resolution={resolution}
      onClose={onClose}
      onNavigate={onNavigate}
    />
  )
}

function renderCard(target: {
  sessionRef?: string
  runRef?: string
  liveRef?: string
}) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <CardForTarget target={target} onClose={() => {}} />
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

  /**
   * THE CARD IS THE FOURTH CALLER OF THE ONE LADDER, and it was the last surface still
   * painting a raw reference as a name: `sessionLabel`'s second rung was `sessionRef`,
   * so a discovered session with no run, no summary, no goal and no reported action
   * titled its own detail sheet `sess-found`, in monospace, while the table two panes
   * left called the same row "Untitled session". The reference is not lost — it is in
   * the identifiers this sheet exists to show, and on the title's own hover.
   */
  it('titles itself with a NAME, never with the session reference', async () => {
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [],
      has_more: false,
    })
    vi.mocked(sessionsApi.liveOne).mockResolvedValue({
      ...live,
      session_ref: 'sess-found',
      live_ref: 'lr-found',
    })
    renderCard({ sessionRef: 'sess-found' })
    const title = await screen.findByTestId('session-card-title')
    expect(title).toHaveTextContent('Untitled session')
    expect(title.textContent).not.toMatch(/^sess-found/)
    // The distinguishing tail stays beside the word, so two untitled sheets are still
    // told apart, and the WHOLE reference is on the hover.
    expect(title).toHaveTextContent('found')
    expect(title.getAttribute('title')).toContain('sess-found')
  })

  it('titles itself with the run name when the operator typed one', async () => {
    renderCard({ sessionRef: 'sess-ours' })
    const title = await screen.findByTestId('session-card-title')
    expect(title).toHaveTextContent('nightly-indexer')
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

  it('does not name a run by its raw reference', async () => {
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [run, { ...run, run_ref: 'run-anon', name: '' }],
      has_more: false,
    })
    renderCard({ sessionRef: 'sess-ours' })
    expect(
      await screen.findByText(/2 runs drive this session/i),
    ).toBeInTheDocument()
    expect(screen.getByText('Untitled session')).toBeInTheDocument()
    const unnamed = screen.getByText('Untitled session').closest('button')
    expect(unnamed).not.toBeNull()
    expect(unnamed).toHaveTextContent('run-anon')
    expect(
      within(unnamed as HTMLElement).getByText('Untitled session').className,
    ).not.toMatch(/font-mono/)
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

// A row is named by its live_ref. Two homes may announce one provider session
// id, so the card resolves a scoped row by its own id, asks the engine for the run
// it PROVED owns that row, and never falls back to the bare id.
const managedLive: LiveDTO = {
  ...live,
  session_ref: 'sess-dup',
  live_ref: 'lr-managed-a',
  attribution: 'managed',
  provider_profile_ref: 'ppf_a',
  provider: 'claude',
  environment_ref: 'xenv_1',
  canonical_sid: 'osn_a',
  run_ref: 'run-a',
}
const observedLive: LiveDTO = {
  ...live,
  session_ref: 'sess-dup',
  live_ref: 'lr-observed-a',
  attribution: 'observed',
  provider_profile_ref: 'ppf_a',
  provider: 'claude',
  environment_ref: 'xenv_1',
  source_binding_ref: 'psb_1',
  input_tokens: 77,
  output_tokens: 11,
}
const profiledRun: RunDTO = {
  ...run,
  run_ref: 'run-a',
  name: 'home-a',
  claude_session_id: 'sess-dup',
  provider_profile_ref: 'ppf_a',
  provider_driver: 'claude',
  provider_environment_ref: 'xenv_1',
  live_ref: 'lr-managed-a',
}

describe('SessionCard — a scoped row is named by its live_ref', () => {
  beforeEach(() => {
    vi.mocked(sessionsApi.live).mockResolvedValue({
      items: [],
      has_more: false,
    })
  })

  it('opened by live_ref, reads the row by id and asks for the run the plane PROVED', async () => {
    vi.mocked(sessionsApi.liveById).mockResolvedValue(managedLive)
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [profiledRun],
      has_more: false,
    })
    renderCard({ liveRef: 'lr-managed-a' })
    await waitFor(() =>
      expect(vi.mocked(sessionsApi.liveById)).toHaveBeenCalledWith(
        'lr-managed-a',
      ),
    )
    await waitFor(() =>
      expect(vi.mocked(agentOpsApi.listRuns)).toHaveBeenCalledWith({
        live_ref: 'lr-managed-a',
      }),
    )
    expect(await screen.findByText('Launched')).toBeInTheDocument()
    expect(screen.getAllByText('Managed by Olivares').length).toBeGreaterThan(0)
    expect(vi.mocked(sessionsApi.liveOne)).not.toHaveBeenCalled()
    expect(vi.mocked(agentOpsApi.listRuns)).not.toHaveBeenCalledWith({
      claude_session_id: 'sess-dup',
    })
  })

  it('opened from a PROFILED run, resolves its managed row by live_ref — never by the bare id', async () => {
    vi.mocked(agentOpsApi.getRun).mockResolvedValue(profiledRun)
    vi.mocked(sessionsApi.liveById).mockResolvedValue(managedLive)
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [profiledRun],
      has_more: false,
    })
    renderCard({ runRef: 'run-a' })
    await waitFor(() =>
      expect(vi.mocked(sessionsApi.liveById)).toHaveBeenCalledWith(
        'lr-managed-a',
      ),
    )
    expect(await screen.findByText('Full control')).toBeInTheDocument()
    expect(vi.mocked(sessionsApi.liveOne)).not.toHaveBeenCalled()
    expect(vi.mocked(agentOpsApi.listRuns)).not.toHaveBeenCalledWith({
      claude_session_id: 'sess-dup',
    })
  })

  it('says DISCOVERED for an observed row that copies a run id: no process is proven for it', async () => {
    vi.mocked(sessionsApi.liveById).mockResolvedValue(observedLive)
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [],
      has_more: false,
    })
    renderCard({ liveRef: 'lr-observed-a' })
    expect(await screen.findByText('Discovered')).toBeInTheDocument()
    expect(
      screen.getByText(
        /Arrived through a source dedicated to this provider profile/i,
      ),
    ).toBeInTheDocument()
    expect(screen.getByText('Observe only')).toBeInTheDocument()
    expect(vi.mocked(agentOpsApi.listRuns)).toHaveBeenCalledWith({
      live_ref: 'lr-observed-a',
    })
  })

  it('lists the observation rows of the same profile and id BESIDE a managed row, opened by their own live_ref', async () => {
    const user = userEvent.setup()
    vi.mocked(sessionsApi.liveById).mockResolvedValue(managedLive)
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [profiledRun],
      has_more: false,
    })
    vi.mocked(sessionsApi.live).mockResolvedValue({
      items: [managedLive, observedLive],
      has_more: false,
    })
    const onNavigate = vi.fn()
    const qc = new QueryClient({
      defaultOptions: { queries: { retry: false, gcTime: 0 } },
    })
    render(
      <QueryClientProvider client={qc}>
        <CardForTarget
          target={{ liveRef: 'lr-managed-a' }}
          onClose={() => {}}
          onNavigate={onNavigate}
        />
      </QueryClientProvider>,
    )
    const related = await screen.findByTestId('related-observations')
    await waitFor(() =>
      expect(vi.mocked(sessionsApi.live)).toHaveBeenCalledWith({
        provider_profile_ref: 'ppf_a',
        session_ref: 'sess-dup',
        limit: 20,
      }),
    )
    expect(within(related).getByText('psb_1')).toBeInTheDocument()
    // The managed row itself is not listed as its own relative.
    expect(within(related).getAllByRole('listitem')).toHaveLength(1)
    await user.click(within(related).getByRole('button', { name: 'Open' }))
    expect(onNavigate).toHaveBeenCalledWith({ liveRef: 'lr-observed-a' })
  })

  it('a profiled run whose id is not proven yet has no observed half — and borrows none', async () => {
    vi.mocked(agentOpsApi.getRun).mockResolvedValue({
      ...profiledRun,
      claude_session_id: undefined,
      live_ref: undefined,
    })
    renderCard({ runRef: 'run-a' })
    // Twice on purpose: the overview says it and the capability list gives it as
    // the reason nothing can be watched.
    expect(
      (await screen.findAllByText(/Nothing observed for this session yet/i))
        .length,
    ).toBeGreaterThan(0)
    expect(vi.mocked(sessionsApi.liveOne)).not.toHaveBeenCalled()
    expect(vi.mocked(sessionsApi.liveById)).not.toHaveBeenCalled()
    expect(vi.mocked(agentOpsApi.listRuns)).not.toHaveBeenCalled()
  })

  it('subscribes a legacy target to its bare id and ignores a scoped frame sharing it', async () => {
    // The stream hook is mocked to a status here; the matching rule is what the
    // card applies to every frame, and it is the same rule the server applies.
    vi.mocked(sessionsApi.liveOne).mockResolvedValue({
      ...live,
      session_ref: 'sess-dup',
      live_ref: 'lr-legacy',
    })
    vi.mocked(agentOpsApi.listRuns).mockResolvedValue({
      items: [],
      has_more: false,
    })
    renderCard({ sessionRef: 'sess-dup' })
    expect(await screen.findByText('Discovered')).toBeInTheDocument()
    expect(vi.mocked(sessionsApi.liveOne)).toHaveBeenCalledWith('sess-dup')
    expect(vi.mocked(sessionsApi.liveById)).not.toHaveBeenCalled()
  })
})
