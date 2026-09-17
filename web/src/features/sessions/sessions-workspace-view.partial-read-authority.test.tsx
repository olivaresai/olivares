// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
/**
 * CURRENT READ ADMISSION IN THE SESSIONS LIST — what may still be painted after the
 * authority a half was read under has changed.
 *
 * ⛔ WHAT THIS FILE EXISTS FOR. The independent review of 2026-09-07 reproduced, on a
 *    real `createQueryClient` with only the two API seams doubled, that a SUCCESS
 *    followed by a one-half refusal left that half's rows, its stream overrides, its
 *    share of the summary tiles and its clickable row in the grid, under a warning
 *    strip saying the half could not be read. `enabled` only stops a query being
 *    ISSUED; React Query keeps the last `data` through a later error and holds it for
 *    `gcTime` when the permission leaves. **A notice beside the table is not
 *    exclusion**, and this file asserts the exclusion itself: row text, origin chips,
 *    derived counts, the action column and the open card — never the banner alone.
 *
 * It is the review's disposable fixture made permanent, with the expectations root
 * adjudication corrected: on a principal or credential change BOTH halves become old
 * (an old cached run is not a positive control for a new principal), and a count over a
 * half nobody read is reported as not read rather than as zero.
 *
 * Deliberately NOT proven here: engine or HTTP behaviour, the app shell, the real
 * SessionCard, or anything outside this view. Only `sessionsApi.live`,
 * `agentOpsApi.listRuns`, the SSE hook and the heavy panels are doubled; the merge, the
 * counts, the error formula, DataTable and the row click are the production ones, and
 * both key factories are real.
 */
import { QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactNode } from 'react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import { ApiError } from '@/lib/api/errors'
import { createQueryClient } from '@/lib/api/query'
import { useSessionStore } from '@/stores/session'
import { useWorkspaceStore } from '@/stores/workspace'
import type { LiveDTO } from './types'

const harness = vi.hoisted(() => {
  const perms = new Set<string>()
  return {
    live: vi.fn(),
    listRuns: vi.fn(),
    perms,
    auth: {
      activeTenant: 'tenant-a' as string | null,
      principal: { user_id: 'user-a' } as { user_id: string } | null,
      can: (p: string) => perms.has(p),
    },
    stream: {
      onSnapshot: undefined as
        ((snap: LiveDTO, event: string) => void) | undefined,
      enabled: false,
      contextKey: undefined as string | number | undefined,
      /** Every distinct (contextKey, enabled) the hook was asked for, in order: a
       * retirement or a reconnect shows up here instead of having to be inferred. */
      asked: [] as {
        contextKey: string | number | undefined
        enabled: boolean
      }[],
    },
  }
})

vi.mock('@/lib/auth/context', () => ({ useAuth: () => harness.auth }))

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
  useRouter: () => undefined,
}))

// The stream is a live connection; what is under test is which frames the list is
// still entitled to paint, so the hook is doubled and its callback captured.
vi.mock('@/features/shared', async () => {
  const actual =
    await vi.importActual<typeof import('@/features/shared')>(
      '@/features/shared',
    )
  return {
    ...actual,
    useLiveStream: (opts: {
      onSnapshot: (snap: LiveDTO, event: string) => void
      enabled?: boolean
      contextKey?: string | number
    }) => {
      harness.stream.onSnapshot = opts.onSnapshot
      harness.stream.enabled = !!opts.enabled
      harness.stream.contextKey = opts.contextKey
      const last = harness.stream.asked[harness.stream.asked.length - 1]
      if (
        !last ||
        last.contextKey !== opts.contextKey ||
        last.enabled !== !!opts.enabled
      ) {
        harness.stream.asked.push({
          contextKey: opts.contextKey,
          enabled: !!opts.enabled,
        })
      }
      return { status: opts.enabled ? 'open' : 'idle' }
    },
  }
})

vi.mock('@/features/agentops/profiles-panel', () => ({
  ProfilesPanel: () => <div>profiles-panel</div>,
}))
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
    target: { sessionRef?: string; runRef?: string; liveRef?: string } | null
  }) =>
    target ? (
      <div data-testid="spr-card">
        live:{target.liveRef ?? ''}|sess:{target.sessionRef ?? ''}|run:
        {target.runRef ?? ''}
      </div>
    ) : null,
}))

// `importOriginal` keeps the REAL key factories: the boundary partition under test is
// built by them, and a hand-written copy would only prove the view agrees with itself.
vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  sessionsApi: { live: harness.live },
}))

vi.mock('@/features/agentops/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/agentops/api')>()),
  agentOpsApi: { listRuns: harness.listRuns },
}))

import { SessionsWorkspaceView } from './sessions-workspace-view'

// Distinct origins, so a row can be attributed to the half it came from by TEXT.
const LIVE_ONLY: LiveDTO = {
  session_ref: 'spr-live-only',
  live_ref: 'spr-lr-live',
  attribution: 'legacy',
  cc_state: 'active',
  current_action: 'spr-live-action',
  model_ref: 'claude-opus-4-8',
  input_tokens: 11,
  output_tokens: 7,
  cost_micro_usd: 1500,
  event_count: 1,
  tool_call_count: 0,
  first_event_at: '2026-09-07T10:00:00Z',
  last_event_at: '2026-09-07T10:05:00Z',
  duration_seconds: 300,
}

/**
 * A frame carrying a session that the next admitted GET does NOT return. Under the
 * ratified contract it must change nothing on screen: it is a hint that the tenant
 * inventory may have moved, not a row, a count, an origin or a click target.
 */
const SENTINEL_FRAME: LiveDTO = {
  ...LIVE_ONLY,
  session_ref: 'spr-frame-only',
  live_ref: 'spr-lr-frame',
  current_action: 'spr-frame-action',
  last_event_at: '2026-09-07T10:06:00Z',
}

const RUN_ONLY: RunDTO = {
  run_ref: 'spr-run-only',
  name: 'spr-run-name',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 2,
  pep_provisioned: true,
  record_io: false,
  critical: false,
  created_at: '2026-09-07T10:01:00Z',
  last_activity_at: '2026-09-07T10:06:00Z',
}

// ONE session both halves know about: the join whose origin stripping has to be exact.
const JOINED_LIVE: LiveDTO = {
  ...LIVE_ONLY,
  session_ref: 'spr-joined',
  live_ref: 'spr-lr-joined',
  current_action: 'spr-joined-action',
  input_tokens: 4321,
}
const JOINED_RUN: RunDTO = {
  ...RUN_ONLY,
  run_ref: 'spr-joined-run-ref',
  name: 'spr-joined-run',
  claude_session_id: 'spr-joined',
}

const LIVE_OK = { items: [LIVE_ONLY], has_more: false }
const RUNS_OK = { items: [RUN_ONLY], has_more: false }
const JOINED_LIVE_OK = { items: [JOINED_LIVE], has_more: false }
const JOINED_RUNS_OK = { items: [JOINED_RUN], has_more: false }

const FORBIDDEN = new ApiError(
  403,
  'forbidden',
  'live list refused',
  'spr-req-live-403',
)
const STEP_UP = new ApiError(
  403,
  'step_up_required',
  'run list needs step-up',
  'spr-req-run-stepup',
)
const LIVE_5XX = new ApiError(
  500,
  'internal',
  'live lookup 5xx',
  'spr-req-live-500',
)

const BOTH_READ_AND_RUN_WRITE = [
  'sessions:live:read',
  'sessions:run:read',
  'sessions:run:write',
] as const

function grant(...perms: string[]) {
  harness.perms.clear()
  for (const p of perms) harness.perms.add(p)
}

function makeClient() {
  // The PRODUCTION client (its gcTime and staleTime are exactly what let a refused or
  // unpermitted half keep painting), with retries off so a 5xx does not idle the test
  // and focus refetching off so a stray jsdom focus cannot stand in for a real answer.
  const qc = createQueryClient()
  const prev = qc.getDefaultOptions()
  qc.setDefaultOptions({
    queries: { ...prev.queries, retry: false, refetchOnWindowFocus: false },
    mutations: prev.mutations,
  })
  return qc
}

function view() {
  return <SessionsWorkspaceView entrance="observe" />
}

function renderView(qc = makeClient()) {
  const result = render(
    <QueryClientProvider client={qc}>{view()}</QueryClientProvider>,
  )
  const again = () =>
    result.rerender(
      <QueryClientProvider client={qc}>{view()}</QueryClientProvider>,
    )
  return { qc, again, ...result }
}

/** The number a summary tile is showing (`—` when it reports "not read"). */
function tile(label: string): HTMLElement | undefined {
  const el = screen.getAllByText(label).find((node) => {
    const cls = typeof node.className === 'string' ? node.className : ''
    return cls.includes('text-muted-foreground') && cls.includes('text-xs')
  })
  return (el?.parentElement?.querySelector('.tabular-nums') ?? undefined) as
    HTMLElement | undefined
}

function tileValue(label: string): string | undefined {
  return tile(label)?.textContent ?? undefined
}

async function rowFor(label: string, setupName: string) {
  const cell = await screen.findByText(label)
  const tr = cell.closest('tr')
  expect(tr, setupName).toBeTruthy()
  return tr as HTMLElement
}

function refreshButton() {
  return screen.getByRole('button', { name: /refresh/i })
}

/** The scope object a call to the live/runs API was pinned to. */
function scopeOf(mock: typeof harness.live, call: number) {
  return mock.mock.calls[call]?.[1] as
    { tenant: string | null; signal?: AbortSignal } | undefined
}

/** Deliver one `session` frame through the doubled hook, as the transport would. */
async function sendHint(snapshot: LiveDTO = SENTINEL_FRAME) {
  await act(async () => {
    harness.stream.onSnapshot?.(snapshot, 'session')
  })
}

/** Wait until the view has actually subscribed. It only does so over a live half that
 * is admitted AND already has an answer of its own. */
async function streamOpen(setupName: string) {
  await waitFor(() => {
    expect(harness.stream.onSnapshot, setupName).toEqual(expect.any(Function))
    expect(harness.stream.enabled, `${setupName}-enabled`).toBe(true)
  })
}

/** A read the test holds open. `reject` is what lets a case say HOW the read ended —
 * refused, or merely broken — which is the whole difference the queue has to see. */
function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

/** Let every already-scheduled microtask and macrotask settle, so "nothing else
 * happened" is measured after the queue had its chance to act, not before it. */
function quiesce(ms = 30) {
  return new Promise((r) => setTimeout(r, ms))
}

beforeEach(() => {
  vi.clearAllMocks()
  harness.stream.onSnapshot = undefined
  harness.stream.enabled = false
  harness.stream.contextKey = undefined
  harness.stream.asked = []
  harness.auth.activeTenant = 'tenant-a'
  harness.auth.principal = { user_id: 'user-a' }
  grant(...BOTH_READ_AND_RUN_WRITE)
  harness.live.mockResolvedValue(LIVE_OK)
  harness.listRuns.mockResolvedValue(RUNS_OK)
  useWorkspaceStore.setState({
    activeWorkspace: null,
    activeWorkspaceName: null,
  })
})

describe('SessionsWorkspaceView — both halves admitted (controls)', () => {
  it('CONTROL: distinct live and run origins, their counts and their row actions', async () => {
    const user = userEvent.setup()
    renderView()
    const liveRow = await rowFor(
      'spr-live-only',
      'spr-auth/healthy/setup-live-row',
    )
    const runRow = await rowFor(
      'spr-run-name',
      'spr-auth/healthy/setup-run-row',
    )
    expect
      .soft(
        within(liveRow).getByText('Discovered'),
        'spr-auth/healthy/live-origin-discovered',
      )
      .toBeInTheDocument()
    expect
      .soft(
        within(runRow).getByText('Launched'),
        'spr-auth/healthy/run-origin-launched',
      )
      .toBeInTheDocument()
    expect.soft(tileValue('Sessions'), 'spr-auth/healthy/total-count').toBe('2')
    expect
      .soft(tileValue('Launched'), 'spr-auth/healthy/launched-count')
      .toBe('1')
    expect
      .soft(tileValue('Discovered'), 'spr-auth/healthy/discovered-count')
      .toBe('1')
    expect
      .soft(
        screen.queryByText(/could not be read/i),
        'spr-auth/healthy/no-error-notice',
      )
      .not.toBeInTheDocument()
    expect
      .soft(
        screen.getByRole('button', { name: /new session/i }),
        'spr-auth/healthy/run-write-launch',
      )
      .toBeInTheDocument()

    await user.click(liveRow)
    expect
      .soft(
        await screen.findByTestId('spr-card'),
        'spr-auth/healthy/live-row-action',
      )
      .toHaveTextContent('sess:spr-live-only')
    await user.click(runRow)
    expect
      .soft(screen.getByTestId('spr-card'), 'spr-auth/healthy/run-row-action')
      .toHaveTextContent('run:spr-run-only')
  })

  it('CONTROL: each read is pinned to the tenant its cache key names, and is cancellable', async () => {
    renderView()
    await rowFor('spr-live-only', 'spr-auth/scope/setup-live-row')
    await rowFor('spr-run-name', 'spr-auth/scope/setup-run-row')
    expect
      .soft(scopeOf(harness.live, 0)?.tenant, 'spr-auth/scope/live-tenant')
      .toBe('tenant-a')
    expect
      .soft(scopeOf(harness.listRuns, 0)?.tenant, 'spr-auth/scope/runs-tenant')
      .toBe('tenant-a')
    expect
      .soft(scopeOf(harness.live, 0)?.signal, 'spr-auth/scope/live-signal')
      .toBeInstanceOf(AbortSignal)
    expect
      .soft(scopeOf(harness.listRuns, 0)?.signal, 'spr-auth/scope/runs-signal')
      .toBeInstanceOf(AbortSignal)
  })
})

describe('SessionsWorkspaceView — one half refused, the other still admitted', () => {
  it('success then live 403: refused rows, overrides, counts and open card leave; runs stay', async () => {
    const user = userEvent.setup()
    renderView()
    const liveRow = await rowFor(
      'spr-live-only',
      'spr-auth/live-403/setup-live-row',
    )
    await rowFor('spr-run-name', 'spr-auth/live-403/setup-run-row')
    await streamOpen('spr-auth/live-403/setup-stream-open')
    await sendHint()
    // A frame is a hint, not a row: the count is still what the admitted GET returned.
    expect
      .soft(
        tileValue('Sessions'),
        'spr-auth/live-403/setup-total-from-the-answer',
      )
      .toBe('2')
    // The card is opened ON the live row, so it is that half's act too.
    await user.click(liveRow)
    expect(
      await screen.findByTestId('spr-card'),
      'spr-auth/live-403/setup-card-open',
    ).toHaveTextContent('sess:spr-live-only')

    harness.live.mockRejectedValue(FORBIDDEN)
    await user.click(refreshButton())
    await waitFor(() => {
      expect(
        screen.getByText(/observed half could not be read/i),
        'spr-auth/live-403/setup-live-error-notice',
      ).toBeInTheDocument()
    })

    expect
      .soft(
        screen.queryByText('spr-live-only'),
        'spr-auth/live-403/refused-live-row-excluded',
      )
      .not.toBeInTheDocument()
    expect
      .soft(
        screen.queryByText('spr-frame-only'),
        'spr-auth/live-403/frame-session-never-painted',
      )
      .not.toBeInTheDocument()
    expect
      .soft(
        harness.stream.enabled,
        'spr-auth/live-403/stream-retired-with-the-refusal',
      )
      .toBe(false)
    expect
      .soft(
        screen.queryByText('spr-live-action'),
        'spr-auth/live-403/refused-live-action-excluded',
      )
      .not.toBeInTheDocument()
    expect
      .soft(
        screen.queryByTestId('spr-card'),
        'spr-auth/live-403/refused-row-target-closed',
      )
      .not.toBeInTheDocument()
    expect
      .soft(tileValue('Sessions'), 'spr-auth/live-403/total-count-healthy-only')
      .toBe('1')
    expect
      .soft(
        tileValue('Launched'),
        'spr-auth/live-403/launched-count-healthy-only',
      )
      .toBe('1')
    expect
      .soft(tileValue('Discovered'), 'spr-auth/live-403/discovered-count-zero')
      .toBe('0')

    // …and the half that was never refused is untouched, down to its row action.
    const runRow = await rowFor(
      'spr-run-name',
      'spr-auth/live-403/healthy-run-row-kept',
    )
    expect
      .soft(
        within(runRow).getByText('Launched'),
        'spr-auth/live-403/healthy-run-origin',
      )
      .toBeInTheDocument()
    // NOT the whole-screen replacement: `role="grid"` survives it (the table keeps the
    // role and swaps its body — data-table.tsx:722), so the discriminator is the
    // ForbiddenState the replacement would render.
    expect
      .soft(
        screen.queryByText('Not authorized'),
        'spr-auth/live-403/table-not-replaced',
      )
      .not.toBeInTheDocument()
    expect
      .soft(
        screen.getByRole('button', { name: /new session/i }),
        'spr-auth/live-403/independent-run-write-kept',
      )
      .toBeInTheDocument()
    // A refusal of one half is not the permission notice of the other, and not a
    // global Forbidden either.
    expect
      .soft(
        screen.queryByText(/Launched sessions are not shown/i),
        'spr-auth/live-403/no-run-permission-claim',
      )
      .not.toBeInTheDocument()
    await user.click(runRow)
    expect
      .soft(
        await screen.findByTestId('spr-card'),
        'spr-auth/live-403/healthy-run-row-action',
      )
      .toHaveTextContent('run:spr-run-only')
  })

  it('success then run step-up: refused run leaves, live stays, and origin counts say NOT READ', async () => {
    const user = userEvent.setup()
    renderView()
    await rowFor('spr-live-only', 'spr-auth/run-stepup/setup-live-row')
    await rowFor('spr-run-name', 'spr-auth/run-stepup/setup-run-row')

    harness.listRuns.mockRejectedValue(STEP_UP)
    await user.click(refreshButton())
    await waitFor(() => {
      expect(
        screen.getByText(/launched half could not be read/i),
        'spr-auth/run-stepup/setup-run-error-notice',
      ).toBeInTheDocument()
    })

    expect
      .soft(
        screen.queryByText('spr-run-name'),
        'spr-auth/run-stepup/refused-run-row-excluded',
      )
      .not.toBeInTheDocument()
    const liveRow = await rowFor(
      'spr-live-only',
      'spr-auth/run-stepup/healthy-live-row-kept',
    )
    expect
      .soft(
        within(liveRow).queryByText('Launched'),
        'spr-auth/run-stepup/cached-run-must-not-rebrand-live',
      )
      .not.toBeInTheDocument()
    expect
      .soft(
        within(liveRow).getByText('Origin not read'),
        'spr-auth/run-stepup/live-origin-is-not-read',
      )
      .toBeInTheDocument()
    // ⛔ MISSING METADATA IS NOT ZERO INVENTORY (root adjudication). The review's
    //    fixture expected `0` here; `0` would report an empty inventory of launches
    //    when the truth is that the half nobody read cannot be counted.
    expect
      .soft(
        tileValue('Launched'),
        'spr-auth/run-stepup/launched-count-not-read',
      )
      .toBe('—')
    expect
      .soft(
        tileValue('Discovered'),
        'spr-auth/run-stepup/discovered-count-not-read',
      )
      .toBe('—')
    expect
      .soft(
        tile('Launched')?.getAttribute('title'),
        'spr-auth/run-stepup/launched-count-explains-itself',
      )
      .toMatch(/could not be checked/i)
    // The healthy half's own count remains.
    expect
      .soft(
        tileValue('Sessions'),
        'spr-auth/run-stepup/total-count-healthy-only',
      )
      .toBe('1')
    expect
      .soft(
        screen.queryByText('Not authorized'),
        'spr-auth/run-stepup/table-not-replaced',
      )
      .not.toBeInTheDocument()
    await user.click(liveRow)
    expect
      .soft(
        await screen.findByTestId('spr-card'),
        'spr-auth/run-stepup/healthy-live-row-action',
      )
      .toHaveTextContent('sess:spr-live-only')
  })

  it('CONTROL: a live 5xx is not a refusal — its last answer stays and the run half is not revoked', async () => {
    const user = userEvent.setup()
    renderView()
    await rowFor('spr-run-name', 'spr-auth/live-5xx/setup-run-row')
    await rowFor('spr-live-only', 'spr-auth/live-5xx/setup-live-row')

    harness.live.mockRejectedValue(LIVE_5XX)
    await user.click(refreshButton())
    await waitFor(() => {
      expect(
        screen.getByText(/observed half could not be read/i),
        'spr-auth/live-5xx/setup-live-error-notice',
      ).toBeInTheDocument()
    })

    // Admission was never withdrawn: the engine broke, it did not refuse. Hiding the
    // half here would be the same error in the other direction.
    expect
      .soft(
        screen.queryByText('spr-live-only'),
        'spr-auth/live-5xx/broken-half-keeps-its-last-answer',
      )
      .toBeInTheDocument()
    expect
      .soft(
        screen.queryByText(/Observed sessions are not shown/i),
        'spr-auth/live-5xx/not-reported-as-a-permission-denial',
      )
      .not.toBeInTheDocument()
    const runRow = await rowFor(
      'spr-run-name',
      'spr-auth/live-5xx/run-row-not-revoked',
    )
    expect
      .soft(
        within(runRow).getByText('Launched'),
        'spr-auth/live-5xx/run-origin-kept',
      )
      .toBeInTheDocument()
    expect
      .soft(
        screen.getByRole('button', { name: /new session/i }),
        'spr-auth/live-5xx/run-write-not-revoked',
      )
      .toBeInTheDocument()
    expect
      .soft(
        screen.queryByText('Not authorized'),
        'spr-auth/live-5xx/table-not-replaced',
      )
      .not.toBeInTheDocument()
    expect
      .soft(tileValue('Launched'), 'spr-auth/live-5xx/launched-count-still-one')
      .toBe('1')
    await user.click(runRow)
    expect
      .soft(
        await screen.findByTestId('spr-card'),
        'spr-auth/live-5xx/run-row-action-kept',
      )
      .toHaveTextContent('run:spr-run-only')
  })

  it('CONTROL: both halves out still replaces the grid, and names the refusal it got', async () => {
    const user = userEvent.setup()
    renderView()
    await rowFor('spr-live-only', 'spr-auth/dual/setup-live-row')
    harness.live.mockRejectedValue(FORBIDDEN)
    harness.listRuns.mockRejectedValue(FORBIDDEN)
    await user.click(refreshButton())
    // The body becomes a STATE (the table element and its grid role stay).
    await waitFor(() => {
      expect(
        screen.getByText('Not authorized'),
        'spr-auth/dual/grid-replaced-by-a-state',
      ).toBeInTheDocument()
    })
    expect
      .soft(screen.queryByText('spr-live-only'), 'spr-auth/dual/no-rows-left')
      .not.toBeInTheDocument()
    expect
      .soft(screen.queryByText('spr-run-name'), 'spr-auth/dual/no-run-row-left')
      .not.toBeInTheDocument()
  })
})

describe('SessionsWorkspaceView — a read bit that leaves, and comes back', () => {
  it('live:read lost: prior rows and overrides leave; run half and run:write stay', async () => {
    const user = userEvent.setup()
    const { again } = renderView()
    await rowFor('spr-live-only', 'spr-auth/live-bit/setup-live-row')
    await rowFor('spr-run-name', 'spr-auth/live-bit/setup-run-row')
    await streamOpen('spr-auth/live-bit/setup-stream-open')
    await sendHint()

    grant('sessions:run:read', 'sessions:run:write')
    again()

    await waitFor(() => {
      expect(
        screen.getByText(/Observed sessions are not shown/i),
        'spr-auth/live-bit/setup-no-live-read-notice',
      ).toBeInTheDocument()
    })
    expect
      .soft(
        screen.queryByText('spr-live-only'),
        'spr-auth/live-bit/prior-live-row-excluded',
      )
      .not.toBeInTheDocument()
    expect
      .soft(
        screen.queryByText('spr-frame-only'),
        'spr-auth/live-bit/frame-session-never-painted',
      )
      .not.toBeInTheDocument()
    expect
      .soft(
        harness.stream.enabled,
        'spr-auth/live-bit/stream-retired-with-the-read-bit',
      )
      .toBe(false)
    const runRow = await rowFor(
      'spr-run-name',
      'spr-auth/live-bit/run-row-kept',
    )
    expect
      .soft(
        within(runRow).getByText('Launched'),
        'spr-auth/live-bit/run-origin-kept',
      )
      .toBeInTheDocument()
    expect
      .soft(tileValue('Sessions'), 'spr-auth/live-bit/total-count-run-only')
      .toBe('1')
    expect
      .soft(tileValue('Launched'), 'spr-auth/live-bit/launched-count-one')
      .toBe('1')
    expect
      .soft(
        screen.getByRole('button', { name: /new session/i }),
        'spr-auth/live-bit/independent-run-write-kept',
      )
      .toBeInTheDocument()
    await user.click(runRow)
    expect
      .soft(
        await screen.findByTestId('spr-card'),
        'spr-auth/live-bit/run-row-action-kept',
      )
      .toHaveTextContent('run:spr-run-only')
  })

  it('live:read regained: the list ASKS again — the old page is not resurrected from cache', async () => {
    const { again } = renderView()
    await rowFor('spr-live-only', 'spr-auth/live-readmit/setup-live-row')
    expect(
      harness.live,
      'spr-auth/live-readmit/setup-one-read',
    ).toHaveBeenCalledTimes(1)

    grant('sessions:run:read', 'sessions:run:write')
    again()
    await waitFor(() => {
      expect(
        screen.queryByText('spr-live-only'),
        'spr-auth/live-readmit/setup-excluded-while-unpermitted',
      ).not.toBeInTheDocument()
    })

    // What the engine would answer NOW is not what it answered then.
    harness.live.mockResolvedValue({
      items: [{ ...LIVE_ONLY, session_ref: 'spr-live-readmitted' }],
      has_more: false,
    })
    grant(...BOTH_READ_AND_RUN_WRITE)
    again()

    await rowFor(
      'spr-live-readmitted',
      'spr-auth/live-readmit/current-answer-painted',
    )
    expect
      .soft(harness.live, 'spr-auth/live-readmit/asked-again')
      .toHaveBeenCalledTimes(2)
    expect
      .soft(
        screen.queryByText('spr-live-only'),
        'spr-auth/live-readmit/old-page-not-resurrected',
      )
      .not.toBeInTheDocument()
  })

  it('run:read lost and regained: prior runs leave, live stays unbranded, write is not inferred lost', async () => {
    const user = userEvent.setup()
    const { again } = renderView()
    await rowFor('spr-live-only', 'spr-auth/run-bit/setup-live-row')
    await rowFor('spr-run-name', 'spr-auth/run-bit/setup-run-row')

    grant('sessions:live:read', 'sessions:run:write')
    again()

    await waitFor(() => {
      expect(
        screen.getByText(/Launched sessions are not shown/i),
        'spr-auth/run-bit/setup-no-run-read-notice',
      ).toBeInTheDocument()
    })
    expect
      .soft(
        screen.queryByText('spr-run-name'),
        'spr-auth/run-bit/prior-run-row-excluded',
      )
      .not.toBeInTheDocument()
    const liveRow = await rowFor(
      'spr-live-only',
      'spr-auth/run-bit/live-row-kept',
    )
    expect
      .soft(
        within(liveRow).queryByText('Launched'),
        'spr-auth/run-bit/cached-run-must-not-rebrand-live',
      )
      .not.toBeInTheDocument()
    expect
      .soft(tileValue('Launched'), 'spr-auth/run-bit/launched-count-not-read')
      .toBe('—')
    expect
      .soft(tileValue('Sessions'), 'spr-auth/run-bit/total-count-live-only')
      .toBe('1')
    expect
      .soft(
        screen.getByRole('button', { name: /new session/i }),
        'spr-auth/run-bit/independent-run-write-kept',
      )
      .toBeInTheDocument()
    await user.click(liveRow)
    expect
      .soft(
        await screen.findByTestId('spr-card'),
        'spr-auth/run-bit/live-row-action-kept',
      )
      .toHaveTextContent('sess:spr-live-only')

    harness.listRuns.mockResolvedValue({
      items: [
        { ...RUN_ONLY, run_ref: 'spr-run-readmitted', name: 'spr-run-2' },
      ],
      has_more: false,
    })
    grant(...BOTH_READ_AND_RUN_WRITE)
    again()
    await rowFor('spr-run-2', 'spr-auth/run-bit/current-answer-painted')
    expect
      .soft(harness.listRuns, 'spr-auth/run-bit/asked-again')
      .toHaveBeenCalledTimes(2)
    expect
      .soft(
        screen.queryByText('spr-run-name'),
        'spr-auth/run-bit/old-page-not-resurrected',
      )
      .not.toBeInTheDocument()
  })
})

describe('SessionsWorkspaceView — a JOINED row loses exactly the half that left', () => {
  beforeEach(() => {
    harness.live.mockResolvedValue(JOINED_LIVE_OK)
    harness.listRuns.mockResolvedValue(JOINED_RUNS_OK)
  })

  it('run step-up: the joined row keeps its observed half and loses the launch brand', async () => {
    const user = userEvent.setup()
    renderView()
    const before = await rowFor(
      'spr-joined-run',
      'spr-auth/joined-run-out/setup-joined-row',
    )
    expect
      .soft(
        within(before).getByText('Launched'),
        'spr-auth/joined-run-out/setup-launched',
      )
      .toBeInTheDocument()

    harness.listRuns.mockRejectedValue(STEP_UP)
    await user.click(refreshButton())
    await waitFor(() => {
      expect(
        screen.getByText(/launched half could not be read/i),
        'spr-auth/joined-run-out/setup-run-error-notice',
      ).toBeInTheDocument()
    })

    expect
      .soft(
        screen.queryByText('spr-joined-run'),
        'spr-auth/joined-run-out/run-name-excluded',
      )
      .not.toBeInTheDocument()
    const row = await rowFor(
      'spr-joined',
      'spr-auth/joined-run-out/observed-half-kept',
    )
    expect
      .soft(
        within(row).queryByText('Launched'),
        'spr-auth/joined-run-out/launch-brand-excluded',
      )
      .not.toBeInTheDocument()
    expect
      .soft(
        within(row).getByText('Origin not read'),
        'spr-auth/joined-run-out/origin-is-not-read',
      )
      .toBeInTheDocument()
    expect
      .soft(
        within(row).getByText('spr-joined-action'),
        'spr-auth/joined-run-out/observed-action-kept',
      )
      .toBeInTheDocument()
    expect
      .soft(
        within(row).getByText(/4,321/),
        'spr-auth/joined-run-out/observed-telemetry-kept',
      )
      .toBeInTheDocument()
    expect
      .soft(tileValue('Sessions'), 'spr-auth/joined-run-out/total-still-one')
      .toBe('1')
    expect
      .soft(
        tileValue('Launched'),
        'spr-auth/joined-run-out/launched-count-not-read',
      )
      .toBe('—')
    await user.click(row)
    expect
      .soft(
        await screen.findByTestId('spr-card'),
        'spr-auth/joined-run-out/observed-target',
      )
      .toHaveTextContent('sess:spr-joined')
  })

  it('live 403: the joined row keeps its run half and loses the observed telemetry', async () => {
    const user = userEvent.setup()
    renderView()
    await rowFor('spr-joined-run', 'spr-auth/joined-live-out/setup-joined-row')

    harness.live.mockRejectedValue(FORBIDDEN)
    await user.click(refreshButton())
    await waitFor(() => {
      expect(
        screen.getByText(/observed half could not be read/i),
        'spr-auth/joined-live-out/setup-live-error-notice',
      ).toBeInTheDocument()
    })

    const row = await rowFor(
      'spr-joined-run',
      'spr-auth/joined-live-out/run-half-kept',
    )
    expect
      .soft(
        within(row).getByText('Launched'),
        'spr-auth/joined-live-out/launch-brand-kept',
      )
      .toBeInTheDocument()
    expect
      .soft(
        screen.queryByText('spr-joined-action'),
        'spr-auth/joined-live-out/observed-action-excluded',
      )
      .not.toBeInTheDocument()
    expect
      .soft(
        screen.queryByText(/4,321/),
        'spr-auth/joined-live-out/observed-telemetry-excluded',
      )
      .not.toBeInTheDocument()
    expect
      .soft(tileValue('Sessions'), 'spr-auth/joined-live-out/total-still-one')
      .toBe('1')
    expect
      .soft(
        tileValue('Launched'),
        'spr-auth/joined-live-out/launched-count-one',
      )
      .toBe('1')
    await user.click(row)
    // Opened by the id the RUN captured (`claude_session_id`), which is the run half's
    // own name for the session — not the observed row, which is not admitted here.
    expect
      .soft(
        await screen.findByTestId('spr-card'),
        'spr-auth/joined-live-out/run-target',
      )
      .toHaveTextContent('sess:spr-joined')
    expect
      .soft(
        screen.getByTestId('spr-card'),
        'spr-auth/joined-live-out/target-carries-no-observed-row',
      )
      .not.toHaveTextContent('spr-lr-joined')
  })
})

describe('SessionsWorkspaceView — the boundary moves under the same tenant', () => {
  it('principal switch: BOTH halves become old, and both are asked again', async () => {
    const user = userEvent.setup()
    const { again } = renderView()
    const liveRow = await rowFor(
      'spr-live-only',
      'spr-auth/principal/setup-live-row',
    )
    await rowFor('spr-run-name', 'spr-auth/principal/setup-run-row')
    await user.click(liveRow)
    expect(
      await screen.findByTestId('spr-card'),
      'spr-auth/principal/setup-card-open',
    ).toBeInTheDocument()

    // What the NEW principal is entitled to is its own answer, not this one.
    harness.live.mockResolvedValue({
      items: [{ ...LIVE_ONLY, session_ref: 'spr-live-user-b' }],
      has_more: false,
    })
    harness.listRuns.mockResolvedValue({
      items: [
        { ...RUN_ONLY, run_ref: 'spr-run-b-ref', name: 'spr-run-user-b' },
      ],
      has_more: false,
    })
    harness.auth.principal = { user_id: 'user-b' }
    again()

    // Synchronously, before any new answer can have arrived.
    expect
      .soft(
        screen.queryByText('spr-live-only'),
        'spr-auth/principal/prior-live-row-excluded',
      )
      .not.toBeInTheDocument()
    expect
      .soft(
        screen.queryByText('spr-run-name'),
        'spr-auth/principal/prior-run-row-excluded',
      )
      .not.toBeInTheDocument()
    expect
      .soft(
        screen.queryByTestId('spr-card'),
        'spr-auth/principal/prior-open-card-closed',
      )
      .not.toBeInTheDocument()

    await rowFor('spr-live-user-b', 'spr-auth/principal/new-live-answer')
    await rowFor('spr-run-user-b', 'spr-auth/principal/new-run-answer')
    expect
      .soft(harness.live, 'spr-auth/principal/live-asked-again')
      .toHaveBeenCalledTimes(2)
    expect
      .soft(harness.listRuns, 'spr-auth/principal/runs-asked-again')
      .toHaveBeenCalledTimes(2)
  })

  it('credential change: the same principal under a new credential asks again too', async () => {
    const { again } = renderView()
    await rowFor('spr-live-only', 'spr-auth/credential/setup-live-row')
    await rowFor('spr-run-name', 'spr-auth/credential/setup-run-row')

    harness.live.mockResolvedValue({
      items: [{ ...LIVE_ONLY, session_ref: 'spr-live-renewed' }],
      has_more: false,
    })
    harness.listRuns.mockResolvedValue({
      items: [{ ...RUN_ONLY, run_ref: 'spr-run-renewed', name: 'spr-run-new' }],
      has_more: false,
    })
    // A RENEWAL: the engine rotates the bearer and answers with the SAME session id,
    // so only the store's generation moves (stores/session.ts).
    act(() => {
      useSessionStore.getState().setSession({
        token: `tok-${Date.now()}`,
        sessionId: 'session-same',
        expiresAt: '2026-09-08T00:00:00Z',
      })
    })
    again()

    expect
      .soft(
        screen.queryByText('spr-live-only'),
        'spr-auth/credential/prior-live-row-excluded',
      )
      .not.toBeInTheDocument()
    expect
      .soft(
        screen.queryByText('spr-run-name'),
        'spr-auth/credential/prior-run-row-excluded',
      )
      .not.toBeInTheDocument()
    await rowFor('spr-live-renewed', 'spr-auth/credential/new-live-answer')
    await rowFor('spr-run-new', 'spr-auth/credential/new-run-answer')
  })

  /**
   * ⛔ THIS CASE USED TO ASSERT THE OPPOSITE, and it was wrong about the product, not
   *    about the code. It was written against a fixture whose live double returned a
   *    different page per selected workspace — a scoped endpoint the engine does not
   *    implement. The ratified contract (2026-09-08) records what the three reads
   *    actually are: `GET /live`, `GET /runs` and the stream are all TENANT-WIDE and
   *    take no core-workspace selector, and neither DTO carries one. So the topbar
   *    selector is not this inventory's filter, and treating a change of it as a
   *    departure destroyed a valid global view for nothing.
   */
  it('R3/1 workspace selector: the global inventory and its open card survive it untouched', async () => {
    const user = userEvent.setup()
    const { again } = renderView()
    const liveRow = await rowFor('spr-live-only', 'R3/1/setup-live-row')
    await rowFor('spr-run-name', 'R3/1/setup-run-row')
    await user.click(liveRow)
    expect(
      screen.getByTestId('spr-card'),
      'R3/1/setup-card-open',
    ).toBeInTheDocument()
    const readsBefore = harness.live.mock.calls.length
    const runReadsBefore = harness.listRuns.mock.calls.length
    const episodeBefore = harness.stream.contextKey

    // The scope is stated, not implied — the already translated nav string.
    expect(
      screen.getByTestId('sessions-scope-note').textContent,
      'R3/1/scope-is-declared',
    ).toMatch(/not filtered by workspace/i)
    // …and no workspace ever went out with the read.
    expect
      .soft(harness.live.mock.calls[0]?.[0], 'R3/1/no-workspace-parameter-sent')
      .not.toHaveProperty('workspace_id')

    act(() => {
      useWorkspaceStore.getState().setActiveWorkspace('ws-2')
    })
    again()

    expect
      .soft(screen.queryByText('spr-live-only'), 'R3/1/global-row-preserved')
      .toBeInTheDocument()
    expect
      .soft(screen.queryByText('spr-run-name'), 'R3/1/run-half-preserved')
      .toBeInTheDocument()
    expect
      .soft(screen.queryByTestId('spr-card'), 'R3/1/valid-card-not-closed')
      .toBeInTheDocument()
    expect.soft(tileValue('Sessions'), 'R3/1/counts-unchanged').toBe('2')
    expect
      .soft(harness.live.mock.calls.length, 'R3/1/no-forced-live-read')
      .toBe(readsBefore)
    expect
      .soft(harness.listRuns.mock.calls.length, 'R3/1/no-forced-run-read')
      .toBe(runReadsBefore)
    expect
      .soft(harness.stream.contextKey, 'R3/1/no-manufactured-stream-departure')
      .toBe(episodeBefore)
    expect.soft(harness.stream.enabled, 'R3/1/stream-stays-open').toBe(true)

    // Clearing the selection ("all workspaces") is equally honest: still nothing.
    act(() => {
      useWorkspaceStore.getState().setActiveWorkspace(null)
    })
    again()
    expect
      .soft(
        screen.queryByText('spr-live-only'),
        'R3/1/null-selection-equally-global',
      )
      .toBeInTheDocument()
    expect
      .soft(
        harness.live.mock.calls.length,
        'R3/1/null-selection-forces-no-read',
      )
      .toBe(readsBefore)
  })
})

describe('SessionsWorkspaceView — an answer that arrives after its context is over', () => {
  /**
   * The review could not stage this: it switched tenant while the FIRST live read was
   * still pending, and the whole table is a skeleton until a first answer arrives, so
   * nothing could be observed. The staging that does work — and is the state an
   * operator is actually in — is a SUCCESS followed by a refetch that is still in
   * flight when the context changes.
   */
  it('tenant switch mid-refetch: the old answer is cancelled and cannot paint', async () => {
    const user = userEvent.setup()
    const inflight = deferred<typeof LIVE_OK>()
    let liveCalls = 0
    harness.live.mockImplementation(() => {
      liveCalls += 1
      if (liveCalls === 1) return Promise.resolve(LIVE_OK)
      if (liveCalls === 2) return inflight.promise
      return Promise.resolve({
        items: [{ ...LIVE_ONLY, session_ref: 'spr-live-tenant-b' }],
        has_more: false,
      })
    })
    const { again } = renderView()
    await rowFor('spr-live-only', 'spr-auth/inflight-tenant/setup-live-row')
    await user.click(refreshButton())
    await waitFor(() => {
      expect(
        liveCalls,
        'spr-auth/inflight-tenant/setup-refetch-in-flight',
      ).toBe(2)
    })

    harness.listRuns.mockResolvedValue({
      items: [{ ...RUN_ONLY, run_ref: 'spr-run-b', name: 'spr-run-tenant-b' }],
      has_more: false,
    })
    harness.auth.activeTenant = 'tenant-b'
    again()

    // The read that belonged to the previous context is aborted through its own
    // signal, so its answer has nowhere to land.
    expect
      .soft(
        scopeOf(harness.live, 1)?.signal?.aborted,
        'spr-auth/inflight-tenant/old-read-cancelled',
      )
      .toBe(true)

    inflight.resolve({
      items: [{ ...LIVE_ONLY, current_action: 'spr-stale-inflight' }],
      has_more: false,
    })
    await act(async () => {
      await inflight.promise
    })

    expect
      .soft(
        screen.queryByText('spr-stale-inflight'),
        'spr-auth/inflight-tenant/stale-action-excluded',
      )
      .not.toBeInTheDocument()
    expect
      .soft(
        screen.queryByText('spr-live-only'),
        'spr-auth/inflight-tenant/old-tenant-row-excluded',
      )
      .not.toBeInTheDocument()
    await rowFor(
      'spr-live-tenant-b',
      'spr-auth/inflight-tenant/new-tenant-answer',
    )
    expect
      .soft(
        scopeOf(harness.live, 2)?.tenant,
        'spr-auth/inflight-tenant/new-read-pinned-to-new-tenant',
      )
      .toBe('tenant-b')
  })

  it('principal switch mid-refetch: the previous principal’s answer cannot paint', async () => {
    const user = userEvent.setup()
    const inflight = deferred<typeof LIVE_OK>()
    let liveCalls = 0
    harness.live.mockImplementation(() => {
      liveCalls += 1
      if (liveCalls === 1) return Promise.resolve(LIVE_OK)
      if (liveCalls === 2) return inflight.promise
      return Promise.resolve({
        items: [{ ...LIVE_ONLY, session_ref: 'spr-live-user-b' }],
        has_more: false,
      })
    })
    const { again } = renderView()
    await rowFor('spr-live-only', 'spr-auth/inflight-principal/setup-live-row')
    await user.click(refreshButton())
    await waitFor(() => {
      expect(
        liveCalls,
        'spr-auth/inflight-principal/setup-refetch-in-flight',
      ).toBe(2)
    })

    harness.auth.principal = { user_id: 'user-b' }
    again()
    expect
      .soft(
        scopeOf(harness.live, 1)?.signal?.aborted,
        'spr-auth/inflight-principal/old-read-cancelled',
      )
      .toBe(true)

    inflight.resolve({
      items: [{ ...LIVE_ONLY, current_action: 'spr-stale-inflight' }],
      has_more: false,
    })
    await act(async () => {
      await inflight.promise
    })

    expect
      .soft(
        screen.queryByText('spr-stale-inflight'),
        'spr-auth/inflight-principal/stale-action-excluded',
      )
      .not.toBeInTheDocument()
    expect
      .soft(
        screen.queryByText('spr-live-only'),
        'spr-auth/inflight-principal/prior-live-row-excluded',
      )
      .not.toBeInTheDocument()
    await rowFor(
      'spr-live-user-b',
      'spr-auth/inflight-principal/new-principal-answer',
    )
  })
})

/**
 * The independent review of 2026-09-08 returned two mechanisms this file did not
 * exercise. Its four causal cases are reproduced here — same shape, same assertion
 * names — with the controls that keep the correction honest in the other direction:
 * an ordinary outage must still keep its half's last answer, and a half that is
 * properly re-admitted must become usable again.
 */
describe('SessionsWorkspaceView — a refusal outlives the error that reported it', () => {
  it.each(['live', 'runs'] as const)(
    'R1 %s: a refusal followed by a 500 must not reauthorize pre-refusal data',
    async (half) => {
      const user = userEvent.setup()
      const { qc } = renderView()
      const oldLabel = half === 'live' ? 'spr-live-only' : 'spr-run-name'
      const healthyLabel = half === 'live' ? 'spr-run-name' : 'spr-live-only'
      const failing = half === 'live' ? harness.live : harness.listRuns
      const qroot = half === 'live' ? 'sessions' : 'agentops'
      const errorOf = () =>
        qc.getQueryCache().findAll({ queryKey: [qroot] })[0]?.state.error

      await rowFor(oldLabel, 'R1/setup-old-half')
      await rowFor(healthyLabel, 'R1/setup-healthy-half')
      await user.click(await rowFor(oldLabel, 'R1/setup-selected-row'))
      expect(
        screen.getByTestId('spr-card'),
        'R1/setup-card-open',
      ).toBeInTheDocument()

      // CONTROL, and it must survive the correction: an ordinary 500 with no refusal
      // behind it withdraws nothing. The half keeps its last answer and its card.
      failing.mockRejectedValue(LIVE_5XX)
      await user.click(refreshButton())
      await waitFor(() => expect(errorOf()).toBe(LIVE_5XX))
      expect
        .soft(
          screen.queryByText(oldLabel),
          'R1/ordinary-500-keeps-its-last-answer',
        )
        .toBeInTheDocument()
      expect
        .soft(
          screen.queryByTestId('spr-card'),
          'R1/ordinary-500-keeps-the-open-card',
        )
        .toBeInTheDocument()
      expect(
        screen.getByText(healthyLabel),
        'R1/ordinary-500-healthy-half',
      ).toBeInTheDocument()

      // The refusal itself: admission ends here, and so does the selection made under it.
      const denial = half === 'live' ? FORBIDDEN : STEP_UP
      failing.mockRejectedValue(denial)
      await user.click(refreshButton())
      await waitFor(() => expect(errorOf()).toBe(denial))
      await waitFor(() =>
        expect(screen.queryByText(oldLabel)).not.toBeInTheDocument(),
      )
      expect(
        screen.queryByTestId('spr-card'),
        'R1/refusal-retires-the-card',
      ).not.toBeInTheDocument()
      expect(
        screen.getByText(healthyLabel),
        'R1/refusal-spares-the-healthy-half',
      ).toBeInTheDocument()

      // No success follows the refusal — only another outage, which answers nothing.
      failing.mockRejectedValue(LIVE_5XX)
      await user.click(refreshButton())
      await waitFor(() => expect(errorOf()).toBe(LIVE_5XX))
      await waitFor(() => expect(qc.isFetching()).toBe(0))

      expect
        .soft(
          screen.queryByText(oldLabel),
          'R1/refused-row-does-not-return-on-outage',
        )
        .not.toBeInTheDocument()
      expect
        .soft(
          screen.queryByTestId('spr-card'),
          'R1/refused-selection-does-not-return-on-outage',
        )
        .not.toBeInTheDocument()
      expect
        .soft(tileValue('Sessions'), 'R1/only-healthy-half-counted')
        .toBe('1')
      expect(
        screen.getByText(healthyLabel),
        'R1/healthy-half-still-usable',
      ).toBeInTheDocument()
      expect(
        screen.getByRole('button', { name: /new session/i }),
        'R1/run-write-still-offered',
      ).toBeInTheDocument()

      // CONTROL in the other direction: the mark is a standing refusal, not a permanent
      // one. A read that SUCCEEDS in this context lifts it and the half is usable again
      // — while the intent chosen before the refusal stays retired.
      const readmitted =
        half === 'live'
          ? {
              items: [{ ...LIVE_ONLY, session_ref: 'spr-live-readmitted' }],
              has_more: false,
            }
          : {
              items: [
                {
                  ...RUN_ONLY,
                  run_ref: 'spr-run-re',
                  name: 'spr-run-readmitted',
                },
              ],
              has_more: false,
            }
      failing.mockResolvedValue(readmitted)
      await user.click(refreshButton())
      const freshLabel =
        half === 'live' ? 'spr-live-readmitted' : 'spr-run-readmitted'
      const freshRow = await rowFor(
        freshLabel,
        'R1/successful-read-lifts-the-refusal',
      )
      expect
        .soft(
          screen.queryByTestId('spr-card'),
          'R1/re-admission-does-not-reopen-the-retired-card',
        )
        .not.toBeInTheDocument()
      // …and a NEW explicit selection of a currently admitted row still works.
      await user.click(freshRow)
      expect(
        await screen.findByTestId('spr-card'),
        'R1/fresh-selection-after-re-admission',
      ).toBeInTheDocument()
    },
  )
})

describe('SessionsWorkspaceView — a retired selection is retired for good', () => {
  it.each(['live', 'runs'] as const)(
    'R2 %s: restoring a read bit cannot revive a selected target before or after a fresh empty answer',
    async (half) => {
      const user = userEvent.setup()
      const { qc, again } = renderView()
      const next = deferred<{ items: never[]; has_more: boolean }>()
      const oldLabel = half === 'live' ? 'spr-live-only' : 'spr-run-name'
      const healthyLabel = half === 'live' ? 'spr-run-name' : 'spr-live-only'
      const failing = half === 'live' ? harness.live : harness.listRuns

      const row = await rowFor(oldLabel, 'R2/setup-selected-row')
      await rowFor(healthyLabel, 'R2/setup-healthy-row')
      await user.click(row)
      expect(
        screen.getByTestId('spr-card'),
        'R2/setup-card-open',
      ).toBeInTheDocument()

      grant(
        half === 'live' ? 'sessions:run:read' : 'sessions:live:read',
        'sessions:run:write',
      )
      again()
      expect(
        screen.queryByTestId('spr-card'),
        'R2/bit-loss-closes-the-card',
      ).not.toBeInTheDocument()
      expect(
        screen.queryByText(oldLabel),
        'R2/bit-loss-excludes-the-row',
      ).not.toBeInTheDocument()
      expect(
        screen.getByText(healthyLabel),
        'R2/bit-loss-spares-the-other-half',
      ).toBeInTheDocument()

      // The bit comes back and the read is deliberately still in flight.
      failing.mockImplementation(() => next.promise)
      grant(...BOTH_READ_AND_RUN_WRITE)
      again()
      await waitFor(() => expect(failing).toHaveBeenCalledTimes(2))
      expect
        .soft(
          screen.queryByTestId('spr-card'),
          'R2/restored-bit-awaits-current-answer-no-old-target',
        )
        .not.toBeInTheDocument()

      // A new valid answer says the old row is not there. It cannot authorize opening
      // that old row-backed target either.
      await act(async () => {
        next.resolve({ items: [], has_more: false })
        await next.promise
      })
      await waitFor(() => expect(qc.isFetching()).toBe(0))
      await rowFor(healthyLabel, 'R2/healthy-after-empty-current-answer')
      expect
        .soft(
          screen.queryByTestId('spr-card'),
          'R2/fresh-empty-does-not-reopen-retired-target',
        )
        .not.toBeInTheDocument()
      expect(
        screen.queryByText(oldLabel),
        'R2/old-row-absent-after-empty-answer',
      ).not.toBeInTheDocument()

      // CONTROL: the half is genuinely usable again — the retirement is of the INTENT,
      // not of the ability to choose. A row that is currently there still opens.
      await user.click(
        await rowFor(healthyLabel, 'R2/fresh-selection-target-row'),
      )
      expect(
        await screen.findByTestId('spr-card'),
        'R2/fresh-selection-after-re-admission',
      ).toBeInTheDocument()
    },
  )
})

/**
 * The independent review of 2026-09-08 accepted the refusal history and the per-half
 * withdrawal, and returned one residual: a context that is LEFT and RETURNED TO. The
 * boundary epoch is memoised per key, so principal A → B → A hands back A's original
 * epoch; with both read bits still effective nothing else moved either, and A's old
 * card matched again — through two pending reads and then an answer with no rows.
 */
describe('SessionsWorkspaceView — leaving a context and coming back to it', () => {
  it.each(['live', 'runs'] as const)(
    'R2 %s: A → B → A cannot revive the intent A chose, pending or empty',
    async (half) => {
      const user = userEvent.setup()
      const { qc, again } = renderView()
      const oldLabel = half === 'live' ? 'spr-live-only' : 'spr-run-name'
      const failing = half === 'live' ? harness.live : harness.listRuns

      const row = await rowFor(oldLabel, 'R2/aba/setup-row')
      await user.click(row)
      expect(
        screen.getByTestId('spr-card'),
        'R2/aba/setup-card-open',
      ).toBeInTheDocument()
      const episodeA1 = harness.stream.contextKey

      // → B. Same tenant, same credential, same permissions: only the principal moves.
      harness.auth.principal = { user_id: 'user-b' }
      harness.live.mockResolvedValue({
        items: [{ ...LIVE_ONLY, session_ref: 'spr-live-b' }],
        has_more: false,
      })
      harness.listRuns.mockResolvedValue({
        items: [{ ...RUN_ONLY, run_ref: 'spr-run-b-ref', name: 'spr-run-b' }],
        has_more: false,
      })
      again()
      expect(
        screen.queryByTestId('spr-card'),
        'R2/aba/B-does-not-see-As-card',
      ).not.toBeInTheDocument()
      await rowFor('spr-live-b', 'R2/aba/B-has-its-own-answer')

      // → back to A, with A's replacement reads deliberately still in flight.
      const pendingLive = deferred<{ items: never[]; has_more: boolean }>()
      const pendingRuns = deferred<{ items: never[]; has_more: boolean }>()
      harness.live.mockImplementation(() => pendingLive.promise)
      harness.listRuns.mockImplementation(() => pendingRuns.promise)
      harness.auth.principal = { user_id: 'user-a' }
      again()
      await waitFor(() => expect(qc.isFetching()).toBeGreaterThan(0))

      expect
        .soft(
          screen.queryByTestId('spr-card'),
          'R2/return-to-A-pending-cannot-reopen-intent',
        )
        .not.toBeInTheDocument()
      // The reused boundary key is exactly what used to make this look current; the
      // episode is what actually owns the lifetime, and it never repeats.
      expect
        .soft(harness.stream.contextKey, 'R2/aba/episode-is-not-reused')
        .not.toBe(episodeA1)

      await act(async () => {
        pendingLive.resolve({ items: [], has_more: false })
        pendingRuns.resolve({ items: [], has_more: false })
        await Promise.all([pendingLive.promise, pendingRuns.promise])
      })
      await waitFor(() => expect(qc.isFetching()).toBe(0))
      expect
        .soft(
          screen.queryByTestId('spr-card'),
          'R2/return-to-A-empty-cannot-reopen-intent',
        )
        .not.toBeInTheDocument()
      expect
        .soft(screen.queryByText(oldLabel), 'R2/aba/old-row-absent-on-return')
        .not.toBeInTheDocument()

      // CONTROL: A is genuinely usable again — a current row still opens on a click.
      const fresh =
        half === 'live'
          ? {
              items: [{ ...LIVE_ONLY, session_ref: 'spr-live-fresh-a' }],
              has_more: false,
            }
          : {
              items: [
                {
                  ...RUN_ONLY,
                  run_ref: 'spr-run-fresh',
                  name: 'spr-run-fresh-a',
                },
              ],
              has_more: false,
            }
      failing.mockResolvedValue(fresh)
      await user.click(refreshButton())
      const freshLabel =
        half === 'live' ? 'spr-live-fresh-a' : 'spr-run-fresh-a'
      await user.click(
        await rowFor(freshLabel, 'R2/aba/fresh-row-after-return'),
      )
      expect(
        await screen.findByTestId('spr-card'),
        'R2/aba/fresh-click-after-return-works',
      ).toBeInTheDocument()
    },
  )

  it('a new mount cannot bootstrap itself from the page an earlier reader was refused', async () => {
    const user = userEvent.setup()
    const { qc, unmount } = renderView()
    await rowFor('spr-live-only', 'R1/remount/setup-row')

    harness.live.mockRejectedValue(FORBIDDEN)
    await user.click(refreshButton())
    await waitFor(() =>
      expect(screen.queryByText('spr-live-only')).not.toBeInTheDocument(),
    )
    // …then an ordinary outage replaces the refusal as the query's ONLY error. The
    // cache still holds the pre-refusal page.
    harness.live.mockRejectedValue(LIVE_5XX)
    await user.click(refreshButton())
    await waitFor(() =>
      expect(
        qc.getQueryCache().findAll({ queryKey: ['sessions'] })[0]?.state.error,
      ).toBe(LIVE_5XX),
    )
    expect(
      qc.getQueryCache().findAll({ queryKey: ['sessions'] })[0]?.state.data,
      'R1/remount/cache-still-holds-the-pre-refusal-page',
    ).toBeDefined()

    // A NEW owner arrives over that cache. Its marks did not survive the unmount, and
    // the query's last error is a plain 500 — nothing in the cache says "refused".
    unmount()
    const pending = deferred<typeof LIVE_OK>()
    harness.live.mockImplementation(() => pending.promise)
    render(
      <QueryClientProvider client={qc}>
        <SessionsWorkspaceView entrance="observe" />
      </QueryClientProvider>,
    )
    await waitFor(() => expect(harness.live).toHaveBeenCalled())
    expect
      .soft(
        screen.queryByText('spr-live-only'),
        'R1/remount/inherited-failed-cache-is-not-admitted',
      )
      .not.toBeInTheDocument()
    // The healthy run half is untouched by the other half's history.
    expect
      .soft(
        screen.queryByText('spr-run-name'),
        'R1/remount/healthy-half-unaffected',
      )
      .toBeInTheDocument()

    // Its own accepted answer is what admits it — and only what that answer contains.
    await act(async () => {
      pending.resolve({
        items: [{ ...LIVE_ONLY, session_ref: 'spr-live-new-owner' }],
        has_more: false,
      })
      await pending.promise
    })
    await rowFor('spr-live-new-owner', 'R1/remount/own-accepted-answer-paints')
    expect
      .soft(
        screen.queryByText('spr-live-only'),
        'R1/remount/pre-refusal-page-never-returns',
      )
      .not.toBeInTheDocument()
  })
})

describe('SessionsWorkspaceView — a stream frame is a hint, never a row', () => {
  it('R3/3 the frame triggers exactly one current read and never paints its payload', async () => {
    renderView()
    await rowFor('spr-live-only', 'R3/3/setup-row')
    await streamOpen('R3/3/setup-stream-open')
    const before = harness.live.mock.calls.length

    await sendHint()
    await waitFor(() =>
      expect(harness.live.mock.calls.length, 'R3/3/one-current-read').toBe(
        before + 1,
      ),
    )
    expect
      .soft(
        screen.queryByText('spr-frame-only'),
        'R3/3/sentinel-row-never-painted',
      )
      .not.toBeInTheDocument()
    expect
      .soft(
        screen.queryByText('spr-frame-action'),
        'R3/3/sentinel-action-never-painted',
      )
      .not.toBeInTheDocument()
    expect.soft(tileValue('Sessions'), 'R3/3/sentinel-never-counted').toBe('2')

    // A newly active session enters the normal way: through the admitted answer.
    harness.live.mockResolvedValue({
      items: [
        LIVE_ONLY,
        { ...LIVE_ONLY, session_ref: 'spr-live-newly-active' },
      ],
      has_more: false,
    })
    await sendHint()
    await rowFor('spr-live-newly-active', 'R3/3/answer-paints-the-new-row')
    expect
      .soft(tileValue('Sessions'), 'R3/3/count-follows-the-answer')
      .toBe('3')
  })

  it('R3/5 a burst produces one read in flight and one trailing read, not a read per frame', async () => {
    renderView()
    await rowFor('spr-live-only', 'R3/5/setup-row')
    await streamOpen('R3/5/setup-stream-open')
    const before = harness.live.mock.calls.length

    const slow = deferred<typeof LIVE_OK>()
    harness.live.mockImplementation(() => slow.promise)
    await sendHint()
    await waitFor(() => expect(harness.live.mock.calls.length).toBe(before + 1))
    // Eight more frames while that read is still out.
    for (let i = 0; i < 8; i++) await sendHint()
    expect
      .soft(
        harness.live.mock.calls.length,
        'R3/5/burst-does-not-multiply-reads',
      )
      .toBe(before + 1)

    harness.live.mockResolvedValue(LIVE_OK)
    await act(async () => {
      slow.resolve(LIVE_OK)
      await slow.promise
    })
    // Exactly ONE trailing read for everything that happened while it ran.
    await waitFor(() =>
      expect(
        harness.live.mock.calls.length,
        'R3/5/one-coalesced-trailing-read',
      ).toBe(before + 2),
    )
    await new Promise((r) => setTimeout(r, 30))
    expect
      .soft(
        harness.live.mock.calls.length,
        'R3/5/no-further-reads-after-trailing',
      )
      .toBe(before + 2)
  })

  it('R3/5 a same-context rerender does not re-subscribe; a real departure does', async () => {
    const { again } = renderView()
    await rowFor('spr-live-only', 'R3/5b/setup-row')
    await streamOpen('R3/5b/setup-stream-open')
    const asked = harness.stream.asked.length
    again()
    again()
    expect
      .soft(harness.stream.asked.length, 'R3/5b/rerender-does-not-resubscribe')
      .toBe(asked)

    harness.auth.principal = { user_id: 'user-b' }
    again()
    expect
      .soft(
        harness.stream.asked.length,
        'R3/5b/authority-departure-retires-and-resubscribes',
      )
      .toBeGreaterThan(asked)
  })

  it('R3/4 the subscription never opens over a half that has no admitted answer', async () => {
    const user = userEvent.setup()
    renderView()
    await rowFor('spr-live-only', 'R3/4/setup-row')
    await streamOpen('R3/4/setup-stream-open')

    harness.live.mockRejectedValue(FORBIDDEN)
    await user.click(refreshButton())
    await waitFor(() =>
      expect(harness.stream.enabled, 'R3/4/refusal-retires-the-stream').toBe(
        false,
      ),
    )
    const reads = harness.live.mock.calls.length
    // A frame that arrives anyway cannot re-admit the refused half or make a read.
    await sendHint()
    await new Promise((r) => setTimeout(r, 20))
    expect
      .soft(
        harness.live.mock.calls.length,
        'R3/4/hint-cannot-refresh-a-refused-half',
      )
      .toBe(reads)
    expect
      .soft(screen.queryByText('spr-live-only'), 'R3/4/hint-cannot-re-admit')
      .not.toBeInTheDocument()
    expect
      .soft(
        screen.queryByText('spr-frame-only'),
        'R3/4/hint-payload-never-painted',
      )
      .not.toBeInTheDocument()
    // The independent healthy half and its write affordance are untouched throughout.
    expect
      .soft(screen.queryByText('spr-run-name'), 'R3/4/healthy-half-kept')
      .toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: /new session/i }),
      'R3/4/run-write-kept',
    ).toBeInTheDocument()
  })

  it('R3/6 the scope note does not displace the page disclosure or any global action', async () => {
    const user = userEvent.setup()
    harness.live.mockResolvedValue({ items: [LIVE_ONLY], has_more: true })
    renderView()
    await rowFor('spr-live-only', 'R3/6/setup-row')
    expect(
      screen.getByTestId('sessions-scope-note').textContent,
      'R3/6/scope-note-present',
    ).toMatch(/not filtered by workspace/i)
    expect
      .soft(
        screen.queryByText(/most recent page, not the whole estate/i),
        'R3/6/page-disclosure-still-owns-truncation',
      )
      .toBeInTheDocument()
    expect
      .soft(
        screen.getByRole('button', { name: /new session/i }),
        'R3/6/launch-still-offered',
      )
      .toBeInTheDocument()
    expect
      .soft(refreshButton(), 'R3/6/manual-refresh-still-offered')
      .toBeInTheDocument()
    // The origin facet still narrows the same global inventory.
    await user.click(screen.getByLabelText('All sources'))
    await user.click(await screen.findByRole('option', { name: 'Launched' }))
    await waitFor(() =>
      expect(screen.queryByText('spr-live-only')).not.toBeInTheDocument(),
    )
    expect(
      screen.getByText('spr-run-name'),
      'R3/6/facet-still-filters',
    ).toBeInTheDocument()
  })
})

/**
 * THE QUEUE ENDS WITH THE ADMISSION THAT AUTHORISED IT (independent review R3,
 * 2026-09-08).
 *
 * A `session` frame asks the CURRENT admitted read to run again. Between that request
 * and its answer, the thing that authorised it can end: the read can come back 403, the
 * view can unmount, the boundary can move, the live read bit can leave. The queue used
 * to be a record with an episode stamp on it, and an episode does not move for any of
 * those — so a hint queued during a read that was then refused started ANOTHER read,
 * and a hint queued in a view that then unmounted issued a request for a screen nobody
 * was looking at.
 *
 * These cases assert the four ends and the two things that must NOT change: an ordinary
 * outage is still not a withdrawal, and a read already in flight is joined rather than
 * cancelled. Where a case is a control — behaviour the previous implementation already
 * had, pinned so the correction cannot cost it — it says so on the case itself.
 */
describe('SessionsWorkspaceView — the queue ends with the admission that authorised it', () => {
  /** Setup shared by every case: one admitted answer on screen, the stream open, and a
   * live read the test holds open with two more hints coalesced behind it. */
  async function conTrabajoEnCola(setupName: string) {
    const antes = harness.live.mock.calls.length
    const pendiente = deferred<typeof LIVE_OK>()
    harness.live.mockImplementation(() => pendiente.promise)

    await sendHint()
    await waitFor(() =>
      expect(
        harness.live.mock.calls.length,
        `${setupName}/hint-starts-one-read`,
      ).toBe(antes + 1),
    )
    // Two more frames while that read is still out: coalesced, not multiplied.
    await sendHint()
    await sendHint()
    expect(
      harness.live.mock.calls.length,
      `${setupName}/burst-is-coalesced`,
    ).toBe(antes + 1)
    return { antes, pendiente, enVuelo: antes + 1 }
  }

  it('R3/7 a hint queued during a read that is REFUSED starts no further read', async () => {
    const user = userEvent.setup()
    renderView()
    await rowFor('spr-live-only', 'R3/7a/setup-row')
    await streamOpen('R3/7a/setup-stream-open')
    const { pendiente, enVuelo } = await conTrabajoEnCola('R3/7a')

    // The read the queue is waiting on is refused. The queued hint is work this
    // admission authorised, and this admission has just ended.
    await act(async () => {
      pendiente.reject(FORBIDDEN)
      await pendiente.promise.catch(() => undefined)
    })
    await waitFor(() =>
      expect(harness.stream.enabled, 'R3/7a/refusal-retires-the-stream').toBe(
        false,
      ),
    )
    await quiesce()
    expect(
      harness.live.mock.calls.length,
      'R3/7a/no-third-read-from-the-queued-hint',
    ).toBe(enVuelo)
    expect
      .soft(
        screen.queryByText('spr-live-only'),
        'R3/7a/pre-refusal-rows-excluded',
      )
      .not.toBeInTheDocument()
    expect
      .soft(screen.queryByText('spr-run-name'), 'R3/7a/healthy-half-kept')
      .toBeInTheDocument()
    // A frame that arrives after the refusal finds no queue and no admitted read.
    await sendHint()
    await quiesce()
    expect(
      harness.live.mock.calls.length,
      'R3/7a/a-later-hint-does-not-re-admit',
    ).toBe(enVuelo)

    // RECOVERY IS EXPLICIT, and the queue that comes back with it is a NEW one: empty,
    // and working. The operator's own Refresh is the read that lifts the refusal.
    harness.live.mockResolvedValue({
      items: [{ ...LIVE_ONLY, session_ref: 'spr-after-recovery' }],
      has_more: false,
    })
    await user.click(refreshButton())
    await rowFor('spr-after-recovery', 'R3/7a/manual-recovery-paints')
    await streamOpen('R3/7a/recovered-stream-open')
    const traRecuperar = harness.live.mock.calls.length
    await sendHint()
    await waitFor(() =>
      expect(
        harness.live.mock.calls.length,
        'R3/7a/the-new-owner-serves-one-hint',
      ).toBe(traRecuperar + 1),
    )
    await quiesce()
    expect(harness.live.mock.calls.length, 'R3/7a/and-exactly-one').toBe(
      traRecuperar + 1,
    )
  })

  it('R3/7 CONTROL an ordinary outage is not a withdrawal: the queued hint still runs its one read', async () => {
    renderView()
    await rowFor('spr-live-only', 'R3/7b/setup-row')
    await streamOpen('R3/7b/setup-stream-open')
    const { pendiente, enVuelo } = await conTrabajoEnCola('R3/7b')

    // The trailing read is the one that answers, so it gets its own distinct row.
    harness.live.mockImplementation(() =>
      Promise.resolve({
        items: [{ ...LIVE_ONLY, session_ref: 'spr-after-outage' }],
        has_more: false,
      }),
    )
    await act(async () => {
      pendiente.reject(LIVE_5XX)
      await pendiente.promise.catch(() => undefined)
    })
    await waitFor(() =>
      expect(
        harness.live.mock.calls.length,
        'R3/7b/outage-still-buys-the-trailing-read',
      ).toBe(enVuelo + 1),
    )
    await rowFor('spr-after-outage', 'R3/7b/trailing-read-paints')
    await quiesce()
    expect(
      harness.live.mock.calls.length,
      'R3/7b/and-exactly-one-trailing-read',
    ).toBe(enVuelo + 1)
    expect(harness.stream.enabled, 'R3/7b/half-was-never-withdrawn').toBe(true)
  })

  it('R3/7 the view unmounts with work queued: nothing is asked for a screen that is gone', async () => {
    const { unmount } = renderView()
    await rowFor('spr-live-only', 'R3/7c/setup-row')
    await streamOpen('R3/7c/setup-stream-open')
    const { pendiente, enVuelo } = await conTrabajoEnCola('R3/7c')

    unmount()
    // React Query cancels the read when its last observer leaves, so the queue's
    // completion runs with the view already gone — and the cached entry it would
    // refetch is still there.
    await act(async () => {
      pendiente.resolve(LIVE_OK)
      await pendiente.promise
    })
    await quiesce()
    expect(harness.live.mock.calls.length, 'R3/7c/no-read-after-unmount').toBe(
      enVuelo,
    )
  })

  it('R3/7 CONTROL the boundary moves with work queued: the old queue serves no new principal', async () => {
    const { again } = renderView()
    await rowFor('spr-live-only', 'R3/7d/setup-row')
    await streamOpen('R3/7d/setup-stream-open')
    const { pendiente } = await conTrabajoEnCola('R3/7d')

    harness.live.mockImplementation(() =>
      Promise.resolve({
        items: [{ ...LIVE_ONLY, session_ref: 'spr-new-principal' }],
        has_more: false,
      }),
    )
    harness.auth.principal = { user_id: 'user-b' }
    again()
    await rowFor('spr-new-principal', 'R3/7d/new-episode-asks-for-itself')
    const traCambio = harness.live.mock.calls.length

    await act(async () => {
      pendiente.resolve(LIVE_OK)
      await pendiente.promise
    })
    await quiesce()
    expect(
      harness.live.mock.calls.length,
      'R3/7d/the-departed-queue-launches-nothing',
    ).toBe(traCambio)
  })

  it('R3/7 CONTROL the live read bit leaves with work queued, and comes back to an EMPTY queue', async () => {
    const { again } = renderView()
    await rowFor('spr-live-only', 'R3/7e/setup-row')
    await streamOpen('R3/7e/setup-stream-open')
    const { pendiente } = await conTrabajoEnCola('R3/7e')

    grant('sessions:run:read', 'sessions:run:write')
    again()
    await waitFor(() =>
      expect(
        screen.queryByText('spr-live-only'),
        'R3/7e/read-bit-gone-excludes-the-half',
      ).not.toBeInTheDocument(),
    )

    harness.live.mockImplementation(() =>
      Promise.resolve({
        items: [{ ...LIVE_ONLY, session_ref: 'spr-after-restore' }],
        has_more: false,
      }),
    )
    grant(...BOTH_READ_AND_RUN_WRITE)
    again()
    await rowFor('spr-after-restore', 'R3/7e/restored-bit-asks-again')
    const traRestaurar = harness.live.mock.calls.length

    await act(async () => {
      pendiente.resolve(LIVE_OK)
      await pendiente.promise
    })
    await quiesce()
    expect(
      harness.live.mock.calls.length,
      'R3/7e/restored-bit-inherits-no-queued-work',
    ).toBe(traRestaurar)
  })

  it('R3/7 a read already in flight is JOINED, not cancelled, by a hint', async () => {
    const user = userEvent.setup()
    renderView()
    await rowFor('spr-live-only', 'R3/7f/setup-row')
    await streamOpen('R3/7f/setup-stream-open')
    const antes = harness.live.mock.calls.length

    // The OPERATOR's own read, which the queue never started and does not track.
    const manual = deferred<typeof LIVE_OK>()
    harness.live.mockImplementation(() => manual.promise)
    await user.click(refreshButton())
    await waitFor(() =>
      expect(harness.live.mock.calls.length, 'R3/7f/manual-read-out').toBe(
        antes + 1,
      ),
    )
    const alcance = scopeOf(harness.live, antes)

    await sendHint()
    await quiesce()
    // Both facts are soft so a regression reports the CANCELLATION and the extra read,
    // rather than stopping at whichever of the two is asserted first.
    expect
      .soft(
        alcance?.signal?.aborted,
        'R3/7f/the-operators-read-was-not-cancelled',
      )
      .toBe(false)
    expect
      .soft(harness.live.mock.calls.length, 'R3/7f/hint-joins-the-running-read')
      .toBe(antes + 1)

    // And it is that read's answer — not a replacement the hint forced — that lands.
    await act(async () => {
      manual.resolve({
        items: [{ ...LIVE_ONLY, session_ref: 'spr-manual-answer' }],
        has_more: false,
      })
      await manual.promise
    })
    await rowFor('spr-manual-answer', 'R3/7f/the-joined-answer-paints')
    await quiesce()
    expect(
      harness.live.mock.calls.length,
      'R3/7f/joining-buys-no-extra-read',
    ).toBe(antes + 1)
  })
})
