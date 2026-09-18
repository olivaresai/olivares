// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import type { LiveDTO } from './types'

/**
 * CLT1 — the lifecycle controls proved at the TRANSPORT boundary.
 *
 * session-card.test.tsx cuts at the MODULE boundary: it replaces
 * `@/features/agentops/api` with `stop: vi.fn(), resume: vi.fn()` and asserts the spy
 * was called with a run_ref. That proves the button is wired to a function. It cannot
 * prove the function issues the right METHOD, the right PATH or the tenant header,
 * because the code that builds those never runs. Point `resume` at the stop path and
 * that file stays green.
 *
 * So this file keeps the real `agentOpsApi` and the real `@/lib/api/client`, and stubs
 * only `fetch`. The chain under test is
 *
 *   SessionCard → RunActions → agentOpsApi.stop/.resume → http.post → apiFetch → fetch
 *
 * against the EXISTING Community routes (`modules/sessions/runtime_api.go`:
 * POST /runs/{ref}/stop, POST /runs/{ref}/resume). It closes nothing else: no private
 * namespace, no lifecycle design, no new route.
 */

const perms = new Set<string>()
vi.mock('@/lib/auth/context', () => ({
  useAuth: () => ({ activeTenant: 't1', can: (p: string) => perms.has(p) }),
}))

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
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

// The observed half is not the subject: it stays a module stub so the only bytes on
// the wire in this file belong to the operate plane.
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

/** The display seam RunActions.onErr actually uses. Captured verbatim: an assertion on
 * the exact typed text is the proof, a re-worded one would not be. */
const shown: string[] = []
vi.mock('@/components/ui/toaster', () => ({
  toast: {
    error: (m: string) => {
      shown.push(m)
    },
    success: vi.fn(),
    info: vi.fn(),
    warning: vi.fn(),
    message: vi.fn(),
  },
}))

import { configureApiClient, __resetRefreshState } from '@/lib/api/client'
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

interface Sent {
  method: string
  path: string
  tenant: string | null
}

const sent: Sent[] = []

/** One JSON answer, recorded. `hold` lets a case keep a mutation in flight. */
function stubFetch(
  answer: (method: string, path: string) => Promise<Response> | Response,
): void {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const raw = typeof input === 'string' ? input : String(input)
      const path = new URL(raw, 'http://console.test').pathname
      const method = (init?.method ?? 'GET').toUpperCase()
      sent.push({
        method,
        path,
        tenant: new Headers(init?.headers).get('X-Olivares-Tenant'),
      })
      return answer(method, path)
    }),
  )
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

/** The list read the card performs before any button exists. */
function listAnswer(state: RunDTO['state'], overrides: Partial<RunDTO> = {}) {
  return json({ items: [{ ...run, state, ...overrides }], has_more: false })
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

function renderCard() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  })
  return render(
    <QueryClientProvider client={qc}>
      <CardForTarget target={{ sessionRef: 'sess-ours' }} onClose={() => {}} />
    </QueryClientProvider>,
  )
}

const lifecycle = (p: Sent) => p.path.startsWith('/v1/m/sessions/runs/run-1/')

beforeEach(() => {
  sent.length = 0
  shown.length = 0
  perms.clear()
  perms.add('sessions:live:read')
  perms.add('sessions:run:read')
  perms.add('sessions:run:write')
  vi.mocked(sessionsApi.liveOne).mockResolvedValue(live)
  __resetRefreshState()
  configureApiClient({
    getToken: () => 'tok-clt1',
    getTenant: () => 't1',
    onUnauthorized: () => {},
  })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('SessionCard lifecycle — real client, intercepted transport', () => {
  it('Stop issues exactly one POST to the run’s stop route with the active tenant', async () => {
    const user = userEvent.setup()
    stubFetch((method) =>
      method === 'GET' ? listAnswer('running') : json({ ...run }),
    )
    renderCard()

    await user.click(await screen.findByRole('button', { name: 'Stop' }))

    await waitFor(() => expect(sent.filter(lifecycle)).toHaveLength(1))
    expect(sent.filter(lifecycle)[0]).toEqual({
      method: 'POST',
      path: '/v1/m/sessions/runs/run-1/stop',
      tenant: 't1',
    })
  })

  it('Resume issues exactly one POST to the run’s resume route with the active tenant', async () => {
    const user = userEvent.setup()
    stubFetch((method) =>
      method === 'GET' ? listAnswer('stopped') : json({ ...run }),
    )
    renderCard()

    await user.click(await screen.findByRole('button', { name: 'Resume' }))

    await waitFor(() => expect(sent.filter(lifecycle)).toHaveLength(1))
    expect(sent.filter(lifecycle)[0]).toEqual({
      method: 'POST',
      path: '/v1/m/sessions/runs/run-1/resume',
      tenant: 't1',
    })
  })

  it.each(['running', 'cleaned'] as const)(
    'offers no Resume for a %s run, and sends nothing while none is pressed',
    async (state) => {
      // Eligibility is a state fact, not a label (provenance.ts RESUMABLE_RUN_STATES =
      // stopped | failed). The same card, the same grants, one field different: the
      // control is absent and no lifecycle bytes leave. Without this, a Resume button
      // rendered for every run would still pass the case above.
      stubFetch((method) =>
        method === 'GET' ? listAnswer(state) : json({ ...run }),
      )
      renderCard()

      expect(
        (await screen.findAllByText('nightly-indexer')).length,
      ).toBeGreaterThan(0)
      await waitFor(() =>
        expect(sent.some((p) => p.method === 'GET')).toBe(true),
      )
      expect(
        screen.queryByRole('button', { name: 'Resume' }),
      ).not.toBeInTheDocument()
      expect(sent.filter(lifecycle)).toHaveLength(0)
    },
  )

  it('offers Stop for a running run and Resume only once it is stopped', async () => {
    stubFetch((method) =>
      method === 'GET' ? listAnswer('stopped') : json({ ...run }),
    )
    renderCard()

    expect(await screen.findByRole('button', { name: 'Resume' })).toBeTruthy()
    expect(
      screen.queryByRole('button', { name: 'Stop' }),
    ).not.toBeInTheDocument()
  })

  it('shows the engine’s typed refusal and does not advance the row', async () => {
    const user = userEvent.setup()
    stubFetch((method) =>
      method === 'GET'
        ? listAnswer('running')
        : json(
            {
              error: {
                code: 'conflict',
                message: 'the run is claimed by another holder',
              },
            },
            409,
          ),
    )
    renderCard()

    await user.click(await screen.findByRole('button', { name: 'Stop' }))

    await waitFor(() =>
      expect(shown).toContain('the run is claimed by another holder'),
    )
    // The row must still be what the engine last said it was: a refused stop leaves a
    // RUNNING run, so Stop is still the offered control and Resume never appears.
    expect(await screen.findByRole('button', { name: 'Stop' })).toBeEnabled()
    expect(
      screen.queryByRole('button', { name: 'Resume' }),
    ).not.toBeInTheDocument()
    expect(sent.filter(lifecycle)).toHaveLength(1)
  })

  it('sends one request while the mutation is pending, however many times it is activated', async () => {
    const user = userEvent.setup()
    let release!: () => void
    const held = new Promise<void>((resolve) => {
      release = resolve
    })
    stubFetch(async (method) => {
      if (method === 'GET') return listAnswer('running')
      await held
      return json({ ...run })
    })
    renderCard()

    const stop = await screen.findByRole('button', { name: 'Stop' })
    await user.click(stop)
    await waitFor(() => expect(stop).toBeDisabled())
    await user.click(stop)
    await user.click(stop)

    expect(sent.filter(lifecycle)).toHaveLength(1)
    release()
    await waitFor(() => expect(stop).toBeEnabled())
    expect(sent.filter(lifecycle)).toHaveLength(1)
  })
})
