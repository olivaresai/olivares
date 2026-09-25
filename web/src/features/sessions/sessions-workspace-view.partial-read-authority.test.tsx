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
 * It is that disposable fixture made permanent, with the expectations root
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
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fakeRouter } from '@/test/fake-router'
import {
  createSessionRowLocator,
  liveAddress,
  runAddress,
  sessAddress,
  type SessionRowLocator,
} from '@/test/session-row-locator'
import type { ProviderProfileDTO, RunDTO } from '@/features/agentops/types'
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
    listProfiles: vi.fn(),
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
      // THERE ARE TWO STREAMS ON THIS SCREEN NOW, and this file is about ONE of
      // them. The LIST's subscription is the tenant-wide hint channel, and it is the
      // only one that declares a `contextKey`: the authority episode it may act in,
      // which is exactly what every case below drives. The other belongs to the OPEN
      // SESSION (`use-session-resolution`), is scoped to one row and carries no
      // episode. Recording both would have let a row subscription overwrite the
      // list's capture — and the assertions would then have graded the wrong
      // connection while still reading green.
      if (!('contextKey' in opts)) return { status: 'idle' }
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
// The card is handed an already-resolved session (the surface owns the one read
// and its SSE subscription). What this file asserts is unchanged: WHICH target the view
// decided to open, which is what the resolution carries.
vi.mock('./session-card', () => ({
  SessionCard: ({
    resolution,
  }: {
    resolution: {
      target: { sessionRef?: string; runRef?: string; liveRef?: string } | null
    }
  }) =>
    resolution.target ? (
      <div data-testid="spr-card">
        live:{resolution.target.liveRef ?? ''}|sess:
        {resolution.target.sessionRef ?? ''}|run:
        {resolution.target.runRef ?? ''}
      </div>
    ) : null,
}))

/**
 * WHAT THE CARD IS SHOWING, AS TEXT.
 *
 * ⛔ THIS HELPER CHANGED MEANING, AND SO DID THE ASSERTIONS THAT USE IT. Until
 *    2026-09-18 the work surface arrived with NOTHING selected, so
 *    `queryByTestId('spr-card')` being absent meant "the target the operator chose was
 *    dropped", and that is what several cases below asserted. The surface now opens on
 *    the top row of the rail's own order when the URL names none, so after a refusal
 *    the card is present again — showing a DIFFERENT, currently admitted session.
 *
 * ⇒ The invariant those cases guard is untouched and is now stated directly: the card
 *   the operator had open does not come back. Each of them captures the text while it
 *   IS open and asserts the later text is not that one. Nothing is weakened — the old
 *   form could be satisfied by an empty screen, and this one cannot.
 */
function openCardText(): string {
  return screen.queryByTestId('spr-card')?.textContent ?? ''
}

// `importOriginal` keeps the REAL key factories: the boundary partition under test is
// built by them, and a hand-written copy would only prove the view agrees with itself.
vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  sessionsApi: { live: harness.live },
}))

// ⛔ THE THIRD READ IS ANSWERED, NOT LEFT UNDEFINED. `8e8aafd19f` added
//    `agentOpsApi.listProfiles` to this view so the Instance column can paint a profile
//    NAME. A seam that is simply absent makes "the query never ran" and "the query ran
//    and threw" the same green, and the profile half of the authority question — what a
//    row shows when the profile read is refused — could not be posed at all.
vi.mock('@/features/agentops/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/agentops/api')>()),
  agentOpsApi: {
    listRuns: harness.listRuns,
    listProfiles: harness.listProfiles,
  },
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

/**
 * ⛔ THESE CASES LIVE ON THE TABLE TAB. The screen's default presentation is the
 *    three-pane work surface; the table — with the columns, the origin chips and the
 *    row-backed open target every case below reads — is the other tab, unchanged and
 *    one click away. Opening it here is what keeps this file measuring the same thing
 *    it always measured, instead of quietly re-grading a different surface.
 *
 *    The estate tiles and the "not read" notices are NOT on a tab: they describe the
 *    reads, not the presentation, so they are above both and every assertion about
 *    them works from either.
 */
function openTable() {
  const trigger = screen.getByRole('tab', { name: 'Table' })
  // MOUSEDOWN, not click: a Radix tab trigger selects on mouse-down, and a bare
  // `.click()` leaves the strip exactly where it was — which reads as "the table is
  // empty" instead of "the tab never changed".
  act(() => {
    fireEvent.mouseDown(trigger)
  })
}

function renderView(qc = makeClient()) {
  const result = render(
    <QueryClientProvider client={qc}>{view()}</QueryClientProvider>,
  )
  openTable()
  const again = () => {
    result.rerender(
      <QueryClientProvider client={qc}>{view()}</QueryClientProvider>,
    )
    openTable()
  }
  return { qc, again, ...result }
}

/**
 * The figure the summary reports for one count (`—` when it reports "not read").
 *
 * ⛔ THE TILES ARE GONE AND THE FOUR FACTS ARE NOT. Until the candidate folded the
 *    counts onto the title line, each count was a `.tabular-nums` figure beside a muted
 *    caption, and this helper found it by that SHAPE. The surface now paints one
 *    sentence inside `sessions-summary` —
 *    `2 Sessions · 1 Launched · 0 Discovered · 0 Needs attention` — with the same `—`
 *    for a half nobody read. The READER moved; not one assertion below changed its
 *    claim, which is the point: these cases are about which counts the view is entitled
 *    to state, not about the element that states them.
 *
 * ⚠ Undefined means the summary does not report that count AT ALL, which is a different
 *   failure from reporting the wrong one — and the label is matched on a whole segment
 *   so a count cannot be read out of the scope note that shares the line.
 */
function summaryText(): string {
  return screen.queryByTestId('sessions-summary')?.textContent ?? ''
}

function tileValue(label: string): string | undefined {
  for (const segment of summaryText().split('·')) {
    const trimmed = segment.trim()
    if (trimmed.endsWith(` ${label}`))
      return trimmed.slice(0, -label.length).trim()
  }
  return undefined
}

/**
 * ⛔ A ROW IS FOUND BY ITS ADDRESS, NEVER BY ITS TEXT — and that is the whole repair.
 *
 *    `8e8aafd19f` made the session cell paint a NAME. These fixtures carry no run name,
 *    no summary and no goal on their observed rows, so every one of them paints the
 *    SAME `Untitled session`: `findByText('spr-live-only')` finds nothing (the 36 reds)
 *    and — far worse — `queryByText('spr-live-only') → null`, which most of this file
 *    used to mean *"the row left"*, became true for every possible view.
 *
 *    `rows` locates by `data-address`, the address the view paints on the row
 *    (`sess:`/`live:`/`run:` + the reference), and REFUSES a "the row left" assertion
 *    unless this same locator saw that row painted earlier in the same case. Every
 *    absence also names something true at that instant, so an unmounted or empty screen
 *    can never stand in for an exclusion.
 */
let rows: SessionRowLocator

/** The addresses of the three standing fixtures, derived the way the address contract
 *  spells them rather than by importing the view's own key function. */
const LIVE_ONLY_AT = sessAddress('spr-live-only')
const RUN_ONLY_AT = runAddress('spr-run-only')
const JOINED_AT = sessAddress('spr-joined')
/** The frame's payload, which must never become a row. */
const FRAME_ONLY_AT = sessAddress('spr-frame-only')

async function rowFor(address: string, setupName: string) {
  return rows.find(address, setupName)
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

/**
 * THE LIST'S OWN RUN READS.
 *
 * `agentOpsApi.listRuns` has two callers on this screen now. The list asks for a PAGE
 * (`{limit: 200}`); the open session's resolution asks for the runs of ONE row
 * (`{live_ref}` / `{claude_session_id}`). "The list was asked again" is a claim about
 * the first, and counting the second as well graded a read these cases are not about —
 * which is how a correct screen fails a test and an incorrect one could pass it.
 */
function listRunReads() {
  return harness.listRuns.mock.calls.filter(
    ([params]) =>
      (params as { limit?: number } | undefined)?.limit !== undefined,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  rows = createSessionRowLocator()
  fakeRouter.reset('/sessions')
  harness.stream.onSnapshot = undefined
  harness.stream.enabled = false
  harness.stream.contextKey = undefined
  harness.stream.asked = []
  harness.auth.activeTenant = 'tenant-a'
  harness.auth.principal = { user_id: 'user-a' }
  grant(...BOTH_READ_AND_RUN_WRITE)
  harness.live.mockResolvedValue(LIVE_OK)
  harness.listRuns.mockResolvedValue(RUNS_OK)
  harness.listProfiles.mockResolvedValue({ items: [], has_more: false })
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
      LIVE_ONLY_AT,
      'spr-auth/healthy/setup-live-row',
    )
    const runRow = await rowFor(RUN_ONLY_AT, 'spr-auth/healthy/setup-run-row')
    // THE INSTRUMENT'S OWN CONTROL, and it belongs in the healthy case: the table
    // paints exactly these two addresses and no others, so every `gone()` elsewhere in
    // this file is measured against a locator that is known to see what is there.
    expect
      .soft(rows.painted().sort(), 'spr-auth/healthy/locator-sees-both-rows')
      .toEqual([LIVE_ONLY_AT, RUN_ONLY_AT].sort())
    // The observed row's CURRENT ACTION is painted text. Several cases below assert an
    // action string is gone; this is the sighting that makes those mean something.
    expect
      .soft(
        within(liveRow).getByText('spr-live-action'),
        'spr-auth/healthy/live-action-is-painted-text',
      )
      .toBeInTheDocument()
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
    await rowFor(LIVE_ONLY_AT, 'spr-auth/scope/setup-live-row')
    await rowFor(RUN_ONLY_AT, 'spr-auth/scope/setup-run-row')
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
      LIVE_ONLY_AT,
      'spr-auth/live-403/setup-live-row',
    )
    await rowFor(RUN_ONLY_AT, 'spr-auth/live-403/setup-run-row')
    // POSITIVE CONTROL for the action assertion further down: the refused half's
    // current action IS painted while the half is admitted.
    expect(
      within(liveRow).getByText('spr-live-action'),
      'spr-auth/live-403/setup-live-action-painted',
    ).toBeInTheDocument()
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
    const refusedCard = openCardText()

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
        rows.gone(LIVE_ONLY_AT, 'spr-auth/live-403/refused-live-row-excluded', {
          alsoPainted: RUN_ONLY_AT,
        }),
        'spr-auth/live-403/refused-live-row-excluded',
      )
      .toBeNull()
    expect
      .soft(
        rows.neverPainted(
          FRAME_ONLY_AT,
          'spr-auth/live-403/frame-session-never-painted',
          { alsoPainted: RUN_ONLY_AT },
        ),
        'spr-auth/live-403/frame-session-never-painted',
      )
      .toBeNull()
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
      .soft(openCardText(), 'spr-auth/live-403/refused-row-target-closed')
      .not.toBe(refusedCard)
    expect
      .soft(openCardText(), 'spr-auth/live-403/refused-row-not-named')
      .not.toContain('spr-live-only')
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
      RUN_ONLY_AT,
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
    await rowFor(LIVE_ONLY_AT, 'spr-auth/run-stepup/setup-live-row')
    const runRowBefore = await rowFor(
      RUN_ONLY_AT,
      'spr-auth/run-stepup/setup-run-row',
    )
    // POSITIVE CONTROL: the run row's NAME is the text it paints while admitted, which
    // is what makes its later absence a fact about the view.
    expect(
      within(runRowBefore).getByText('spr-run-name'),
      'spr-auth/run-stepup/setup-run-name-painted',
    ).toBeInTheDocument()

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
        rows.gone(RUN_ONLY_AT, 'spr-auth/run-stepup/refused-run-row-excluded', {
          alsoPainted: LIVE_ONLY_AT,
        }),
        'spr-auth/run-stepup/refused-run-row-excluded',
      )
      .toBeNull()
    expect
      .soft(
        screen.queryByText('spr-run-name'),
        'spr-auth/run-stepup/refused-run-name-not-painted-anywhere',
      )
      .not.toBeInTheDocument()
    const liveRow = await rowFor(
      LIVE_ONLY_AT,
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
    // ⛔ MISSING METADATA IS NOT ZERO INVENTORY (a design rule). An earlier
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
    // ⛔ THE DASH STILL EXPLAINS ITSELF, AND THE READER MOVED WITH IT. The explanation
    //    used to be a `title=` on the tile's figure; the counts are plain text on the
    //    title line now and text carries no tooltip. What says why is the partial notice
    //    above the table — the same sentence, painted permanently instead of on hover —
    //    so the pairing is asserted HERE, at the same moment as the dash, rather than
    //    left to the setup that waited for it.
    expect
      .soft(
        screen.getByText(/launched half could not be read/i),
        'spr-auth/run-stepup/launched-count-explains-itself',
      )
      .toBeInTheDocument()
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
    await rowFor(RUN_ONLY_AT, 'spr-auth/live-5xx/setup-run-row')
    await rowFor(LIVE_ONLY_AT, 'spr-auth/live-5xx/setup-live-row')

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
        rows.query(LIVE_ONLY_AT),
        'spr-auth/live-5xx/broken-half-keeps-its-last-answer',
      )
      .not.toBeNull()
    expect
      .soft(
        screen.queryByText(/Observed sessions are not shown/i),
        'spr-auth/live-5xx/not-reported-as-a-permission-denial',
      )
      .not.toBeInTheDocument()
    const runRow = await rowFor(
      RUN_ONLY_AT,
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
    await rowFor(LIVE_ONLY_AT, 'spr-auth/dual/setup-live-row')
    // Sighted too, so the claim that its row leaves is a claim this case can make.
    await rowFor(RUN_ONLY_AT, 'spr-auth/dual/setup-run-row')
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
    // The grid paints NOTHING here, and that is the control: the case has no sibling
    // half to name because both were refused, so the assertion is paired with the
    // state that replaced them (`Not authorized`, asserted above) and with an empty
    // row census rather than with an unmounted screen.
    expect
      .soft(
        rows.gone(LIVE_ONLY_AT, 'spr-auth/dual/no-rows-left', {
          andNoRowAtAll: true,
        }),
        'spr-auth/dual/no-rows-left',
      )
      .toBeNull()
    expect
      .soft(
        rows.gone(RUN_ONLY_AT, 'spr-auth/dual/no-run-row-left', {
          andNoRowAtAll: true,
        }),
        'spr-auth/dual/no-run-row-left',
      )
      .toBeNull()
  })
})

describe('SessionsWorkspaceView — a read bit that leaves, and comes back', () => {
  it('live:read lost: prior rows and overrides leave; run half and run:write stay', async () => {
    const user = userEvent.setup()
    const { again } = renderView()
    await rowFor(LIVE_ONLY_AT, 'spr-auth/live-bit/setup-live-row')
    await rowFor(RUN_ONLY_AT, 'spr-auth/live-bit/setup-run-row')
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
        rows.gone(LIVE_ONLY_AT, 'spr-auth/live-bit/prior-live-row-excluded', {
          alsoPainted: RUN_ONLY_AT,
        }),
        'spr-auth/live-bit/prior-live-row-excluded',
      )
      .toBeNull()
    expect
      .soft(
        rows.neverPainted(
          FRAME_ONLY_AT,
          'spr-auth/live-bit/frame-session-never-painted',
          { alsoPainted: RUN_ONLY_AT },
        ),
        'spr-auth/live-bit/frame-session-never-painted',
      )
      .toBeNull()
    expect
      .soft(
        harness.stream.enabled,
        'spr-auth/live-bit/stream-retired-with-the-read-bit',
      )
      .toBe(false)
    const runRow = await rowFor(RUN_ONLY_AT, 'spr-auth/live-bit/run-row-kept')
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
    await rowFor(LIVE_ONLY_AT, 'spr-auth/live-readmit/setup-live-row')
    // The run half is sighted so it can serve as the live control while the observed
    // half is out — and so the exclusion below is one this case is allowed to claim.
    await rowFor(RUN_ONLY_AT, 'spr-auth/live-readmit/setup-run-row')
    expect(
      harness.live,
      'spr-auth/live-readmit/setup-one-read',
    ).toHaveBeenCalledTimes(1)

    grant('sessions:run:read', 'sessions:run:write')
    again()
    await waitFor(() => {
      expect(
        rows.gone(
          LIVE_ONLY_AT,
          'spr-auth/live-readmit/setup-excluded-while-unpermitted',
          { alsoPainted: RUN_ONLY_AT },
        ),
        'spr-auth/live-readmit/setup-excluded-while-unpermitted',
      ).toBeNull()
    })

    // What the engine would answer NOW is not what it answered then.
    harness.live.mockResolvedValue({
      items: [{ ...LIVE_ONLY, session_ref: 'spr-live-readmitted' }],
      has_more: false,
    })
    grant(...BOTH_READ_AND_RUN_WRITE)
    again()

    await rowFor(
      sessAddress('spr-live-readmitted'),
      'spr-auth/live-readmit/current-answer-painted',
    )
    expect
      .soft(harness.live, 'spr-auth/live-readmit/asked-again')
      .toHaveBeenCalledTimes(2)
    expect
      .soft(
        rows.gone(
          LIVE_ONLY_AT,
          'spr-auth/live-readmit/old-page-not-resurrected',
          { alsoPainted: sessAddress('spr-live-readmitted') },
        ),
        'spr-auth/live-readmit/old-page-not-resurrected',
      )
      .toBeNull()
  })

  it('run:read lost and regained: prior runs leave, live stays unbranded, write is not inferred lost', async () => {
    const user = userEvent.setup()
    const { again } = renderView()
    await rowFor(LIVE_ONLY_AT, 'spr-auth/run-bit/setup-live-row')
    await rowFor(RUN_ONLY_AT, 'spr-auth/run-bit/setup-run-row')

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
        rows.gone(RUN_ONLY_AT, 'spr-auth/run-bit/prior-run-row-excluded', {
          alsoPainted: LIVE_ONLY_AT,
        }),
        'spr-auth/run-bit/prior-run-row-excluded',
      )
      .toBeNull()
    const liveRow = await rowFor(LIVE_ONLY_AT, 'spr-auth/run-bit/live-row-kept')
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
    await rowFor(
      runAddress('spr-run-readmitted'),
      'spr-auth/run-bit/current-answer-painted',
    )
    expect.soft(listRunReads(), 'spr-auth/run-bit/asked-again').toHaveLength(2)
    expect
      .soft(
        rows.gone(RUN_ONLY_AT, 'spr-auth/run-bit/old-page-not-resurrected', {
          alsoPainted: runAddress('spr-run-readmitted'),
        }),
        'spr-auth/run-bit/old-page-not-resurrected',
      )
      .toBeNull()
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
      JOINED_AT,
      'spr-auth/joined-run-out/setup-joined-row',
    )
    expect
      .soft(
        within(before).getByText('Launched'),
        'spr-auth/joined-run-out/setup-launched',
      )
      .toBeInTheDocument()
    // POSITIVE CONTROLS for the three text absences below: while both halves are
    // admitted the row paints the RUN's name as its label, the observed action and the
    // observed token figure. Each is sighted here, in this case, before it is claimed
    // to have left.
    expect
      .soft(
        within(before).getByText('spr-joined-run'),
        'spr-auth/joined-run-out/setup-run-name-is-the-label',
      )
      .toBeInTheDocument()
    expect
      .soft(
        within(before).getByText('spr-joined-action'),
        'spr-auth/joined-run-out/setup-observed-action-painted',
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

    // The ROW stays — it is the run HALF that leaves it — so the assertion here is
    // about the name the run half contributed, and the row itself is the control.
    const row = await rowFor(
      JOINED_AT,
      'spr-auth/joined-run-out/observed-half-kept',
    )
    expect
      .soft(
        screen.queryByText('spr-joined-run'),
        'spr-auth/joined-run-out/run-name-excluded',
      )
      .not.toBeInTheDocument()
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
    const joinedBefore = await rowFor(
      JOINED_AT,
      'spr-auth/joined-live-out/setup-joined-row',
    )
    // POSITIVE CONTROLS, in this case: the observed half's action and its token figure
    // ARE painted on the row while the half is admitted.
    expect(
      within(joinedBefore).getByText('spr-joined-action'),
      'spr-auth/joined-live-out/setup-observed-action-painted',
    ).toBeInTheDocument()
    expect(
      within(joinedBefore).getByText(/4,321/),
      'spr-auth/joined-live-out/setup-observed-telemetry-painted',
    ).toBeInTheDocument()

    harness.live.mockRejectedValue(FORBIDDEN)
    await user.click(refreshButton())
    await waitFor(() => {
      expect(
        screen.getByText(/observed half could not be read/i),
        'spr-auth/joined-live-out/setup-live-error-notice',
      ).toBeInTheDocument()
    })

    const row = await rowFor(
      JOINED_AT,
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
      LIVE_ONLY_AT,
      'spr-auth/principal/setup-live-row',
    )
    await rowFor(RUN_ONLY_AT, 'spr-auth/principal/setup-run-row')
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
    // Both halves became old at once, so there is no sibling row to name: the control
    // is that the grid is mounted and paints NOTHING, which is a different screen from
    // an unmounted one.
    expect
      .soft(
        rows.gone(LIVE_ONLY_AT, 'spr-auth/principal/prior-live-row-excluded', {
          andNoRowAtAll: true,
        }),
        'spr-auth/principal/prior-live-row-excluded',
      )
      .toBeNull()
    expect
      .soft(
        rows.gone(RUN_ONLY_AT, 'spr-auth/principal/prior-run-row-excluded', {
          andNoRowAtAll: true,
        }),
        'spr-auth/principal/prior-run-row-excluded',
      )
      .toBeNull()
    expect
      .soft(
        screen.queryByTestId('spr-card'),
        'spr-auth/principal/prior-open-card-closed',
      )
      .not.toBeInTheDocument()

    await rowFor(
      sessAddress('spr-live-user-b'),
      'spr-auth/principal/new-live-answer',
    )
    await rowFor(
      runAddress('spr-run-b-ref'),
      'spr-auth/principal/new-run-answer',
    )
    expect
      .soft(harness.live, 'spr-auth/principal/live-asked-again')
      .toHaveBeenCalledTimes(2)
    expect
      .soft(listRunReads(), 'spr-auth/principal/runs-asked-again')
      .toHaveLength(2)
  })

  it('credential change: the same principal under a new credential asks again too', async () => {
    const { again } = renderView()
    await rowFor(LIVE_ONLY_AT, 'spr-auth/credential/setup-live-row')
    await rowFor(RUN_ONLY_AT, 'spr-auth/credential/setup-run-row')

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
        rows.gone(LIVE_ONLY_AT, 'spr-auth/credential/prior-live-row-excluded', {
          andNoRowAtAll: true,
        }),
        'spr-auth/credential/prior-live-row-excluded',
      )
      .toBeNull()
    expect
      .soft(
        rows.gone(RUN_ONLY_AT, 'spr-auth/credential/prior-run-row-excluded', {
          andNoRowAtAll: true,
        }),
        'spr-auth/credential/prior-run-row-excluded',
      )
      .toBeNull()
    await rowFor(
      sessAddress('spr-live-renewed'),
      'spr-auth/credential/new-live-answer',
    )
    await rowFor(
      runAddress('spr-run-renewed'),
      'spr-auth/credential/new-run-answer',
    )
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
    const liveRow = await rowFor(LIVE_ONLY_AT, 'R3/1/setup-live-row')
    await rowFor(RUN_ONLY_AT, 'R3/1/setup-run-row')
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
      .soft(rows.query(LIVE_ONLY_AT), 'R3/1/global-row-preserved')
      .not.toBeNull()
    expect
      .soft(rows.query(RUN_ONLY_AT), 'R3/1/run-half-preserved')
      .not.toBeNull()
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
      .soft(rows.query(LIVE_ONLY_AT), 'R3/1/null-selection-equally-global')
      .not.toBeNull()
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
    const liveRow = await rowFor(
      LIVE_ONLY_AT,
      'spr-auth/inflight-tenant/setup-live-row',
    )
    // POSITIVE CONTROL for the stale-action absence below: an admitted answer's
    // `current_action` IS painted in this table, so a stale one would have been too.
    expect(
      within(liveRow).getByText('spr-live-action'),
      'spr-auth/inflight-tenant/setup-action-is-painted',
    ).toBeInTheDocument()
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
    // The weakest control of the three, and it is the only one available HERE on
    // purpose: the assertion is made in the instant after the stale answer resolved,
    // when the new tenant's two reads are still outstanding, so no sibling row can be
    // named. It still rules out an unmounted screen, and the sighting requirement still
    // rules out a locator that never saw the row.
    expect
      .soft(
        rows.gone(
          LIVE_ONLY_AT,
          'spr-auth/inflight-tenant/old-tenant-row-excluded',
          { andTheGridIsMounted: true },
        ),
        'spr-auth/inflight-tenant/old-tenant-row-excluded',
      )
      .toBeNull()
    await rowFor(
      sessAddress('spr-live-tenant-b'),
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
    const liveRow = await rowFor(
      LIVE_ONLY_AT,
      'spr-auth/inflight-principal/setup-live-row',
    )
    expect(
      within(liveRow).getByText('spr-live-action'),
      'spr-auth/inflight-principal/setup-action-is-painted',
    ).toBeInTheDocument()
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
    // Same instant, same reason as the tenant case above: the replacement reads are
    // still out, so the mounted grid is the only live control there is.
    expect
      .soft(
        rows.gone(
          LIVE_ONLY_AT,
          'spr-auth/inflight-principal/prior-live-row-excluded',
          { andTheGridIsMounted: true },
        ),
        'spr-auth/inflight-principal/prior-live-row-excluded',
      )
      .toBeNull()
    await rowFor(
      sessAddress('spr-live-user-b'),
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
      const oldAt = half === 'live' ? LIVE_ONLY_AT : RUN_ONLY_AT
      const healthyAt = half === 'live' ? RUN_ONLY_AT : LIVE_ONLY_AT
      const failing = half === 'live' ? harness.live : harness.listRuns
      const qroot = half === 'live' ? 'sessions' : 'agentops'
      // ⛔ THE LIST'S OWN QUERY, NAMED. `findAll({queryKey:[root]})[0]` was the first
      //    entry under a root that now holds several: the view added a `profiles` read
      //    under `agentops` (`8e8aafd19f`) and the open session's resolution holds
      //    `live/one` and `live/by-id` under `sessions`. Grading whichever landed first
      //    measures a read these cases are not about — and it did: R1 runs read the
      //    disabled profiles entry and saw a null error where the refusal was.
      //    Both list keys are `[root, tenant, 'b', epoch, <leaf>, params]`.
      const leaf = half === 'live' ? 'live' : 'runs'
      const listQuery = () =>
        qc
          .getQueryCache()
          .findAll({ queryKey: [qroot] })
          .find((q) => q.queryKey[2] === 'b' && q.queryKey[4] === leaf)
      const errorOf = () => listQuery()?.state.error

      await rowFor(oldAt, 'R1/setup-old-half')
      await rowFor(healthyAt, 'R1/setup-healthy-half')
      await user.click(await rowFor(oldAt, 'R1/setup-selected-row'))
      expect(
        screen.getByTestId('spr-card'),
        'R1/setup-card-open',
      ).toBeInTheDocument()
      const retiredCard = openCardText()

      // CONTROL, and it must survive the correction: an ordinary 500 with no refusal
      // behind it withdraws nothing. The half keeps its last answer and its card.
      failing.mockRejectedValue(LIVE_5XX)
      await user.click(refreshButton())
      await waitFor(() => expect(errorOf()).toBe(LIVE_5XX))
      expect
        .soft(rows.query(oldAt), 'R1/ordinary-500-keeps-its-last-answer')
        .not.toBeNull()
      expect
        .soft(
          screen.queryByTestId('spr-card'),
          'R1/ordinary-500-keeps-the-open-card',
        )
        .toBeInTheDocument()
      rows.get(healthyAt, 'R1/ordinary-500-healthy-half')

      // The refusal itself: admission ends here, and so does the selection made under it.
      const denial = half === 'live' ? FORBIDDEN : STEP_UP
      failing.mockRejectedValue(denial)
      await user.click(refreshButton())
      await waitFor(() => expect(errorOf()).toBe(denial))
      await waitFor(() =>
        expect(
          rows.gone(oldAt, 'R1/refusal-excludes-the-refused-row', {
            alsoPainted: healthyAt,
          }),
        ).toBeNull(),
      )
      expect(openCardText(), 'R1/refusal-retires-the-card').not.toBe(
        retiredCard,
      )
      rows.get(healthyAt, 'R1/refusal-spares-the-healthy-half')

      // No success follows the refusal — only another outage, which answers nothing.
      failing.mockRejectedValue(LIVE_5XX)
      await user.click(refreshButton())
      await waitFor(() => expect(errorOf()).toBe(LIVE_5XX))
      await waitFor(() => expect(qc.isFetching()).toBe(0))

      expect
        .soft(
          rows.gone(oldAt, 'R1/refused-row-does-not-return-on-outage', {
            alsoPainted: healthyAt,
          }),
          'R1/refused-row-does-not-return-on-outage',
        )
        .toBeNull()
      expect
        .soft(openCardText(), 'R1/refused-selection-does-not-return-on-outage')
        .not.toBe(retiredCard)
      expect
        .soft(tileValue('Sessions'), 'R1/only-healthy-half-counted')
        .toBe('1')
      rows.get(healthyAt, 'R1/healthy-half-still-usable')
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
      const freshAt =
        half === 'live'
          ? sessAddress('spr-live-readmitted')
          : runAddress('spr-run-re')
      const freshRow = await rowFor(
        freshAt,
        'R1/successful-read-lifts-the-refusal',
      )
      expect
        .soft(
          openCardText(),
          'R1/re-admission-does-not-reopen-the-retired-card',
        )
        .not.toBe(retiredCard)
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
      const oldAt = half === 'live' ? LIVE_ONLY_AT : RUN_ONLY_AT
      const healthyAt = half === 'live' ? RUN_ONLY_AT : LIVE_ONLY_AT
      const failing = half === 'live' ? harness.live : harness.listRuns

      const row = await rowFor(oldAt, 'R2/setup-selected-row')
      await rowFor(healthyAt, 'R2/setup-healthy-row')
      await user.click(row)
      expect(
        screen.getByTestId('spr-card'),
        'R2/setup-card-open',
      ).toBeInTheDocument()
      const retiredCard = openCardText()

      grant(
        half === 'live' ? 'sessions:run:read' : 'sessions:live:read',
        'sessions:run:write',
      )
      again()
      expect(openCardText(), 'R2/bit-loss-closes-the-card').not.toBe(
        retiredCard,
      )
      expect(
        rows.gone(oldAt, 'R2/bit-loss-excludes-the-row', {
          alsoPainted: healthyAt,
        }),
        'R2/bit-loss-excludes-the-row',
      ).toBeNull()
      rows.get(healthyAt, 'R2/bit-loss-spares-the-other-half')

      // The bit comes back and the read is deliberately still in flight.
      //
      // ⚠ THE SYNCHRONISATION POINT, NOT AN ECONOMY CLAIM: this waits for the
      //   re-admitted half to be ASKED again, so the assertion below is made while its
      //   answer is genuinely outstanding.
      //
      // ⛔ AND THE COUNT IS TWO FOR BOTH HALVES AGAIN, WHICH IS THE LATCH SPEAKING. It
      //   was `half === 'live' ? 2 : 3` for one day: the work-first pass made the
      //   surface open on a
      //   session, so the run half was read a second time by `useSessionResolution` for
      //   that default. The default now waits until BOTH halves have answered — a
      //   browser walk showed it otherwise resolving on the first half to land and then
      //   tearing down the stream it had just opened — and in this case the re-admitted
      //   half is deliberately left in flight, so there is no settled list, no default
      //   and no second read. Two is the number of reads the LIST itself makes.
      failing.mockImplementation(() => next.promise)
      grant(...BOTH_READ_AND_RUN_WRITE)
      again()
      await waitFor(() => expect(failing).toHaveBeenCalledTimes(2))
      expect
        .soft(
          openCardText(),
          'R2/restored-bit-awaits-current-answer-no-old-target',
        )
        .not.toBe(retiredCard)

      // A new valid answer says the old row is not there. It cannot authorize opening
      // that old row-backed target either.
      await act(async () => {
        next.resolve({ items: [], has_more: false })
        await next.promise
      })
      await waitFor(() => expect(qc.isFetching()).toBe(0))
      await rowFor(healthyAt, 'R2/healthy-after-empty-current-answer')
      expect
        .soft(openCardText(), 'R2/fresh-empty-does-not-reopen-retired-target')
        .not.toBe(retiredCard)
      expect(
        rows.gone(oldAt, 'R2/old-row-absent-after-empty-answer', {
          alsoPainted: healthyAt,
        }),
        'R2/old-row-absent-after-empty-answer',
      ).toBeNull()

      // CONTROL: the half is genuinely usable again — the retirement is of the INTENT,
      // not of the ability to choose. A row that is currently there still opens.
      await user.click(await rowFor(healthyAt, 'R2/fresh-selection-target-row'))
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
      const oldAt = half === 'live' ? LIVE_ONLY_AT : RUN_ONLY_AT
      const failing = half === 'live' ? harness.live : harness.listRuns

      const row = await rowFor(oldAt, 'R2/aba/setup-row')
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
      await rowFor(sessAddress('spr-live-b'), 'R2/aba/B-has-its-own-answer')

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
      // Both halves answered with NO rows, so the grid is mounted and empty: the
      // control names that, rather than a sibling there is none of.
      expect
        .soft(
          rows.gone(oldAt, 'R2/aba/old-row-absent-on-return', {
            andNoRowAtAll: true,
          }),
          'R2/aba/old-row-absent-on-return',
        )
        .toBeNull()

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
      const freshAt =
        half === 'live'
          ? sessAddress('spr-live-fresh-a')
          : runAddress('spr-run-fresh')
      await user.click(await rowFor(freshAt, 'R2/aba/fresh-row-after-return'))
      expect(
        await screen.findByTestId('spr-card'),
        'R2/aba/fresh-click-after-return-works',
      ).toBeInTheDocument()
    },
  )

  it('a new mount cannot bootstrap itself from the page an earlier reader was refused', async () => {
    const user = userEvent.setup()
    const { qc, unmount } = renderView()
    await rowFor(LIVE_ONLY_AT, 'R1/remount/setup-row')
    // Sighted so the run half can act as the live control through the whole case.
    await rowFor(RUN_ONLY_AT, 'R1/remount/setup-run-row')

    harness.live.mockRejectedValue(FORBIDDEN)
    await user.click(refreshButton())
    await waitFor(() =>
      expect(
        rows.gone(LIVE_ONLY_AT, 'R1/remount/refusal-excludes-the-row', {
          alsoPainted: RUN_ONLY_AT,
        }),
      ).toBeNull(),
    )
    // …then an ordinary outage replaces the refusal as the query's ONLY error. The
    // cache still holds the pre-refusal page.
    harness.live.mockRejectedValue(LIVE_5XX)
    await user.click(refreshButton())
    // The LIST's entry, named rather than taken by position: the open session's
    // resolution keeps its own entries under the same `sessions` root.
    const liveList = () =>
      qc
        .getQueryCache()
        .findAll({ queryKey: ['sessions'] })
        .find((q) => q.queryKey[2] === 'b' && q.queryKey[4] === 'live')
    await waitFor(() => expect(liveList()?.state.error).toBe(LIVE_5XX))
    expect(
      liveList()?.state.data,
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
    // This case mounts the view itself rather than through `renderView`, so it opens
    // the table the same way — the row assertions below read the table's grid.
    openTable()
    await waitFor(() => expect(harness.live).toHaveBeenCalled())
    expect
      .soft(
        rows.gone(
          LIVE_ONLY_AT,
          'R1/remount/inherited-failed-cache-is-not-admitted',
          { alsoPainted: RUN_ONLY_AT },
        ),
        'R1/remount/inherited-failed-cache-is-not-admitted',
      )
      .toBeNull()
    // The healthy run half is untouched by the other half's history.
    expect
      .soft(rows.query(RUN_ONLY_AT), 'R1/remount/healthy-half-unaffected')
      .not.toBeNull()

    // Its own accepted answer is what admits it — and only what that answer contains.
    await act(async () => {
      pending.resolve({
        items: [{ ...LIVE_ONLY, session_ref: 'spr-live-new-owner' }],
        has_more: false,
      })
      await pending.promise
    })
    await rowFor(
      sessAddress('spr-live-new-owner'),
      'R1/remount/own-accepted-answer-paints',
    )
    expect
      .soft(
        rows.gone(LIVE_ONLY_AT, 'R1/remount/pre-refusal-page-never-returns', {
          alsoPainted: sessAddress('spr-live-new-owner'),
        }),
        'R1/remount/pre-refusal-page-never-returns',
      )
      .toBeNull()
  })
})

describe('SessionsWorkspaceView — a stream frame is a hint, never a row', () => {
  it('R3/3 the frame triggers exactly one current read and never paints its payload', async () => {
    renderView()
    const liveRow = await rowFor(LIVE_ONLY_AT, 'R3/3/setup-row')
    // POSITIVE CONTROL for the two absences below: an admitted answer's session DOES
    // get a row and its action IS painted, so a frame's payload would have shown.
    expect(
      within(liveRow).getByText('spr-live-action'),
      'R3/3/setup-an-admitted-action-is-painted',
    ).toBeInTheDocument()
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
        rows.neverPainted(FRAME_ONLY_AT, 'R3/3/sentinel-row-never-painted', {
          alsoPainted: LIVE_ONLY_AT,
        }),
        'R3/3/sentinel-row-never-painted',
      )
      .toBeNull()
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
    await rowFor(
      sessAddress('spr-live-newly-active'),
      'R3/3/answer-paints-the-new-row',
    )
    expect
      .soft(tileValue('Sessions'), 'R3/3/count-follows-the-answer')
      .toBe('3')
  })

  it('R3/5 a burst produces one read in flight and one trailing read, not a read per frame', async () => {
    renderView()
    await rowFor(LIVE_ONLY_AT, 'R3/5/setup-row')
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
    await rowFor(LIVE_ONLY_AT, 'R3/5b/setup-row')
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
    await rowFor(LIVE_ONLY_AT, 'R3/4/setup-row')
    await rowFor(RUN_ONLY_AT, 'R3/4/setup-run-row')
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
      .soft(
        rows.gone(LIVE_ONLY_AT, 'R3/4/hint-cannot-re-admit', {
          alsoPainted: RUN_ONLY_AT,
        }),
        'R3/4/hint-cannot-re-admit',
      )
      .toBeNull()
    expect
      .soft(
        rows.neverPainted(FRAME_ONLY_AT, 'R3/4/hint-payload-never-painted', {
          alsoPainted: RUN_ONLY_AT,
        }),
        'R3/4/hint-payload-never-painted',
      )
      .toBeNull()
    // The independent healthy half and its write affordance are untouched throughout.
    expect
      .soft(rows.query(RUN_ONLY_AT), 'R3/4/healthy-half-kept')
      .not.toBeNull()
    expect(
      screen.getByRole('button', { name: /new session/i }),
      'R3/4/run-write-kept',
    ).toBeInTheDocument()
  })

  it('R3/6 the scope note does not displace the page disclosure or any global action', async () => {
    const user = userEvent.setup()
    harness.live.mockResolvedValue({ items: [LIVE_ONLY], has_more: true })
    renderView()
    await rowFor(LIVE_ONLY_AT, 'R3/6/setup-row')
    await rowFor(RUN_ONLY_AT, 'R3/6/setup-run-row')
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
      expect(
        rows.gone(LIVE_ONLY_AT, 'R3/6/facet-narrows-away-the-observed-row', {
          alsoPainted: RUN_ONLY_AT,
        }),
      ).toBeNull(),
    )
    rows.get(RUN_ONLY_AT, 'R3/6/facet-still-filters')
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
    await rowFor(LIVE_ONLY_AT, 'R3/7a/setup-row')
    await rowFor(RUN_ONLY_AT, 'R3/7a/setup-run-row')
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
        rows.gone(LIVE_ONLY_AT, 'R3/7a/pre-refusal-rows-excluded', {
          alsoPainted: RUN_ONLY_AT,
        }),
        'R3/7a/pre-refusal-rows-excluded',
      )
      .toBeNull()
    expect
      .soft(rows.query(RUN_ONLY_AT), 'R3/7a/healthy-half-kept')
      .not.toBeNull()
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
    await rowFor(
      sessAddress('spr-after-recovery'),
      'R3/7a/manual-recovery-paints',
    )
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
    await rowFor(LIVE_ONLY_AT, 'R3/7b/setup-row')
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
    await rowFor(sessAddress('spr-after-outage'), 'R3/7b/trailing-read-paints')
    await quiesce()
    expect(
      harness.live.mock.calls.length,
      'R3/7b/and-exactly-one-trailing-read',
    ).toBe(enVuelo + 1)
    expect(harness.stream.enabled, 'R3/7b/half-was-never-withdrawn').toBe(true)
  })

  it('R3/7 the view unmounts with work queued: nothing is asked for a screen that is gone', async () => {
    const { unmount } = renderView()
    await rowFor(LIVE_ONLY_AT, 'R3/7c/setup-row')
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
    await rowFor(LIVE_ONLY_AT, 'R3/7d/setup-row')
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
    await rowFor(
      sessAddress('spr-new-principal'),
      'R3/7d/new-episode-asks-for-itself',
    )
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
    await rowFor(LIVE_ONLY_AT, 'R3/7e/setup-row')
    await rowFor(RUN_ONLY_AT, 'R3/7e/setup-run-row')
    await streamOpen('R3/7e/setup-stream-open')
    const { pendiente } = await conTrabajoEnCola('R3/7e')

    grant('sessions:run:read', 'sessions:run:write')
    again()
    await waitFor(() =>
      expect(
        rows.gone(LIVE_ONLY_AT, 'R3/7e/read-bit-gone-excludes-the-half', {
          alsoPainted: RUN_ONLY_AT,
        }),
        'R3/7e/read-bit-gone-excludes-the-half',
      ).toBeNull(),
    )

    harness.live.mockImplementation(() =>
      Promise.resolve({
        items: [{ ...LIVE_ONLY, session_ref: 'spr-after-restore' }],
        has_more: false,
      }),
    )
    grant(...BOTH_READ_AND_RUN_WRITE)
    again()
    await rowFor(
      sessAddress('spr-after-restore'),
      'R3/7e/restored-bit-asks-again',
    )
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
    await rowFor(LIVE_ONLY_AT, 'R3/7f/setup-row')
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
    await rowFor(
      sessAddress('spr-manual-answer'),
      'R3/7f/the-joined-answer-paints',
    )
    await quiesce()
    expect(
      harness.live.mock.calls.length,
      'R3/7f/joining-buys-no-extra-read',
    ).toBe(antes + 1)
  })
})

/**
 * THE THIRD READ ON THIS SCREEN, AND ITS OWN AUTHORITY.
 *
 * `8e8aafd19f` added `agentOpsApi.listProfiles` so the Instance column can paint the
 * profile's NAME instead of its `ppf_` reference. That is a read like the other two:
 * it can be granted, it can be refused, it can fail — and the answer to "what does the
 * row show then" has to be a fallback the plane can prove, never a name nobody
 * returned.
 *
 * ⛔ THE ROW IS NOT THE PROFILE'S TO WITHDRAW. A refused profile read says nothing
 *    about whether the session may be shown: the observed half answered, so the row,
 *    its action, its telemetry and its click target all stay. The only thing that
 *    changes is which of the three names the Instance cell can honestly paint.
 */
const PROFILED_LIVE: LiveDTO = {
  ...LIVE_ONLY,
  session_ref: 'spr-profiled-sid',
  live_ref: 'spr-lr-profiled',
  // A scoped row: `liveRowKey` addresses it by its own id, not by the external one.
  attribution: 'managed',
  provider_profile_ref: 'ppf_spr',
  /** The driver the LIVE row itself declares — the fallback the view may use. */
  provider: 'spr-driver-from-the-row',
  current_action: 'spr-profiled-action',
}
const PROFILED_AT = liveAddress('spr-lr-profiled')

const PROFILE: ProviderProfileDTO = {
  profile_ref: 'ppf_spr',
  driver: 'spr-driver-from-the-profile',
  environment_ref: 'env-spr',
  display_name: 'spr-profile-display-name',
  state: 'active',
  local_environment: true,
  operable: true,
}
/** A profile the row is NOT attributed to. Its name must never reach this row. */
const OTHER_PROFILE: ProviderProfileDTO = {
  ...PROFILE,
  profile_ref: 'ppf_other',
  display_name: 'spr-other-profile-name',
}

const PROFILE_READ = 'sessions:profile:read'

/** The Instance cell of the profiled row: what the column is entitled to paint. */
function instanceCell(row: HTMLElement): HTMLElement {
  const el = row.querySelector<HTMLElement>(`[title="${PROFILE.profile_ref}"]`)
  return el as HTMLElement
}

describe('SessionsWorkspaceView — the PROFILE name is a read like any other', () => {
  beforeEach(() => {
    harness.live.mockResolvedValue({
      items: [PROFILED_LIVE],
      has_more: false,
    })
    harness.listRuns.mockResolvedValue({ items: [], has_more: false })
  })

  it('CONTROL granted: the answer names the profile, and the raw ppf_ reference is not painted', async () => {
    grant(...BOTH_READ_AND_RUN_WRITE, PROFILE_READ)
    harness.listProfiles.mockResolvedValue({
      items: [PROFILE, OTHER_PROFILE],
      has_more: false,
    })
    renderView()
    const row = await rowFor(PROFILED_AT, 'spr-auth/profile-ok/setup-row')

    await waitFor(() =>
      expect(
        instanceCell(row)?.textContent,
        'spr-auth/profile-ok/name-from-the-answer',
      ).toBe(PROFILE.display_name),
    )
    // The whole point of the change this file had to catch up with: the reference is
    // carried, not read out.
    expect
      .soft(
        screen.queryByText(PROFILE.profile_ref),
        'spr-auth/profile-ok/raw-reference-not-painted',
      )
      .not.toBeInTheDocument()
    expect
      .soft(
        instanceCell(row)?.getAttribute('title'),
        'spr-auth/profile-ok/reference-is-on-the-title',
      )
      .toBe(PROFILE.profile_ref)
    expect
      .soft(
        screen.queryByText(OTHER_PROFILE.display_name as string),
        'spr-auth/profile-ok/a-stranger-profile-is-not-borrowed',
      )
      .not.toBeInTheDocument()
    // ⚠ CALLED, not called ONCE. Two components on this screen ask for the same
    //   profile page under the same key — the list's Instance column and the open
    //   session's context pane — and the observed count is 2. Which of them asks, and
    //   how often, is not what this case is about; pinning a number here would grade a
    //   read these cases do not own and would break on a mount order nothing promises.
    expect
      .soft(harness.listProfiles, 'spr-auth/profile-ok/the-read-was-made')
      .toHaveBeenCalled()
  })

  it.each([
    ['refused', FORBIDDEN],
    ['step-up', STEP_UP],
    ['failed', LIVE_5XX],
  ] as const)(
    'profiles %s: the row keeps everything and the cell falls back — it does not invent a name',
    async (_how, failure) => {
      grant(...BOTH_READ_AND_RUN_WRITE, PROFILE_READ)
      harness.listProfiles.mockRejectedValue(failure)
      renderView()
      const row = await rowFor(PROFILED_AT, 'spr-auth/profile-out/setup-row')
      await waitFor(() => expect(harness.listProfiles).toHaveBeenCalled())

      // THE FALLBACK IS A FACT THE ROW ITSELF CARRIES: the driver the live row
      // declares. Not the display name (nobody answered), not the reference (the
      // console stopped painting those), not a blank cell.
      await waitFor(() =>
        expect(
          instanceCell(row)?.textContent,
          'spr-auth/profile-out/falls-back-to-the-rows-own-driver',
        ).toBe(PROFILED_LIVE.provider),
      )
      expect
        .soft(
          screen.queryByText(PROFILE.display_name as string),
          'spr-auth/profile-out/no-name-from-an-unread-half',
        )
        .not.toBeInTheDocument()
      expect
        .soft(
          screen.queryByText(PROFILE.profile_ref),
          'spr-auth/profile-out/still-no-raw-reference',
        )
        .not.toBeInTheDocument()

      // …and the session half is untouched: a profile read that failed is not a
      // refusal of the session. The row, its action and its target all stay.
      expect
        .soft(
          within(row).getByText('spr-profiled-action'),
          'spr-auth/profile-out/observed-half-untouched',
        )
        .toBeInTheDocument()
      expect
        .soft(tileValue('Sessions'), 'spr-auth/profile-out/count-untouched')
        .toBe('1')
      expect
        .soft(
          screen.queryByText(/could not be read/i),
          'spr-auth/profile-out/not-reported-as-a-half-that-could-not-be-read',
        )
        .not.toBeInTheDocument()
    },
  )

  it('no sessions:profile:read: the read is never issued, and the same fallback is painted', async () => {
    // This is the state every other case in this file runs in, stated once here so the
    // rest of the file is measuring a screen whose profile half is known, not assumed.
    grant(...BOTH_READ_AND_RUN_WRITE)
    harness.listProfiles.mockResolvedValue({
      items: [PROFILE],
      has_more: false,
    })
    renderView()
    const row = await rowFor(PROFILED_AT, 'spr-auth/profile-unpermitted/row')
    await quiesce()
    expect(
      harness.listProfiles,
      'spr-auth/profile-unpermitted/no-read-issued',
    ).not.toHaveBeenCalled()
    expect(
      instanceCell(row)?.textContent,
      'spr-auth/profile-unpermitted/same-fallback',
    ).toBe(PROFILED_LIVE.provider)
    // CONTROL in the other direction, in this case: the name IS available and the only
    // reason it is not painted is the missing read bit.
    expect(
      PROFILE.display_name,
      'spr-auth/profile-unpermitted/the-name-existed-all-along',
    ).toBe('spr-profile-display-name')
  })

  it('granted but the answer does not name THIS profile: the cell still falls back', async () => {
    grant(...BOTH_READ_AND_RUN_WRITE, PROFILE_READ)
    harness.listProfiles.mockResolvedValue({
      items: [OTHER_PROFILE],
      has_more: false,
    })
    renderView()
    const row = await rowFor(PROFILED_AT, 'spr-auth/profile-absent/row')
    await waitFor(() => expect(harness.listProfiles).toHaveBeenCalled())
    await quiesce()
    expect(
      instanceCell(row)?.textContent,
      'spr-auth/profile-absent/falls-back-rather-than-borrowing',
    ).toBe(PROFILED_LIVE.provider)
    expect(
      screen.queryByText(OTHER_PROFILE.display_name as string),
      'spr-auth/profile-absent/the-other-name-is-not-painted-here',
    ).not.toBeInTheDocument()
  })
})

/**
 * THE RUNG THE CASES ABOVE CANNOT REACH: a row that declares NO driver.
 *
 * ⛔ WHY IT NEEDED A SECOND FIXTURE. Every case in the block above uses `PROFILED_LIVE`,
 *    whose live half carries `provider: 'spr-driver-from-the-row'`, so a refusal, a
 *    failure and an absent profile all stop on the SECOND rung and the third is never
 *    executed. That third rung used to be the field LABEL — "Provider profile" — so the
 *    cases named "it does not invent a name" were green over a cell that invents one.
 *
 * ⛔ AND `provider` IS OPTIONAL BY DECLARATION (`types.ts:94`), not by accident: a
 *    discovered row whose source never reported a driver is a real row of the estate.
 */
const DRIVERLESS_LIVE: LiveDTO = {
  ...PROFILED_LIVE,
  session_ref: 'spr-driverless-sid',
  live_ref: 'spr-lr-driverless',
  provider: undefined,
  current_action: 'spr-driverless-action',
}
const DRIVERLESS_AT = liveAddress('spr-lr-driverless')

/** The label the cell used to borrow. It is the name of the FIELD, and it is still a
 *  correct label elsewhere — the context pane titles the profile row with it — which is
 *  precisely why a cell painting it reads as a value nobody can tell apart from one. */
const FIELD_LABEL = 'Provider profile'

describe('SessionsWorkspaceView — with no driver on the row, the cell paints no name at all', () => {
  beforeEach(() => {
    harness.live.mockResolvedValue({
      items: [DRIVERLESS_LIVE],
      has_more: false,
    })
    harness.listRuns.mockResolvedValue({ items: [], has_more: false })
  })

  it.each([
    [
      'refused',
      () => {
        grant(...BOTH_READ_AND_RUN_WRITE, PROFILE_READ)
        harness.listProfiles.mockRejectedValue(FORBIDDEN)
      },
    ],
    [
      'failed',
      () => {
        grant(...BOTH_READ_AND_RUN_WRITE, PROFILE_READ)
        harness.listProfiles.mockRejectedValue(LIVE_5XX)
      },
    ],
    [
      'out of the page',
      () => {
        grant(...BOTH_READ_AND_RUN_WRITE, PROFILE_READ)
        harness.listProfiles.mockResolvedValue({
          items: [OTHER_PROFILE],
          has_more: true,
        })
      },
    ],
  ] as const)(
    'profiles %s and the row declares no driver: the quiet dash, the reference on title, never the field label',
    async (_how, arrange) => {
      arrange()
      renderView()
      const row = await rowFor(DRIVERLESS_AT, 'spr-auth/driverless/setup-row')
      await waitFor(() => expect(harness.listProfiles).toHaveBeenCalled())
      await quiesce()

      const cell = instanceCell(row)
      expect(cell, 'spr-auth/driverless/the-cell-is-there').not.toBeNull()
      // ⛔ THE DASH IS WHAT IS SEEN; "not known" IS WHAT IS READ OUT. A glyph alone has
      //    no meaning in the accessibility tree, and the only text this cell used to
      //    carry was the `ppf_…` reference on `title` — which names the profile without
      //    saying that its NAME is the thing missing (WCAG 2.1 AA 4.1.2).
      expect(
        cell?.querySelector('[aria-hidden="true"]')?.textContent,
        'spr-auth/driverless/says-nothing-rather-than-a-label',
      ).toBe('—')
      expect(
        within(cell).getByText('Provider profile name not known'),
        'spr-auth/driverless/the-dash-has-words',
      ).toBeInTheDocument()
      // NOTHING IS LOST: the reference is still reachable from the cell that refuses to
      // read it out, which is the same contract the granted case pins.
      expect
        .soft(
          cell?.getAttribute('title'),
          'spr-auth/driverless/reference-still-on-the-title',
        )
        .toBe(PROFILE.profile_ref)
      // The label is not painted ANYWHERE on this screen — not in this cell and not
      // borrowed into another one.
      expect
        .soft(
          screen.queryByText(FIELD_LABEL),
          'spr-auth/driverless/the-field-label-is-not-a-value',
        )
        .not.toBeInTheDocument()
      // …and the session half is untouched, exactly as in the driver-bearing cases.
      expect
        .soft(
          within(row).getByText('spr-driverless-action'),
          'spr-auth/driverless/observed-half-untouched',
        )
        .toBeInTheDocument()
    },
  )

  /**
   * WHICH OF THE FOUR IT WAS — the half the cell cannot say.
   *
   * ⛔ A ROW'S FALLBACK IS HONEST ABOUT THE VALUE AND SILENT ABOUT THE CAUSE. Refused,
   *    refused by the engine, failed and "older than the page the engine returned" all
   *    reach the same dash, and a reader seeing it has no way to tell "I am not allowed
   *    to see any profile name on this screen" from "this one profile is not in the
   *    page". Only the surface holds that fact, so only the surface can say it — once,
   *    in the shape the findings table already uses.
   *
   * ⛔ AND THE FOURTH CAUSE IS SILENT ON PURPOSE: there the read ANSWERED. A line saying
   *    the names could not be read would be false about a read that succeeded.
   */
  it('profiles refused by the GRANT: the screen says so once, and names the grant', async () => {
    grant(...BOTH_READ_AND_RUN_WRITE)
    renderView()
    await rowFor(DRIVERLESS_AT, 'spr-auth/driverless/refused-row')
    await quiesce()
    const notice = await screen.findByTestId('sessions-profile-names-refused')
    expect(
      notice.textContent,
      'spr-auth/driverless/notice-names-the-grant',
    ).toContain('sessions:profile:read')
    expect(
      screen.queryByTestId('sessions-profile-names-failed'),
      'spr-auth/driverless/one-cause-one-line',
    ).toBeNull()
    // The read that was never permitted was never made, which is what "refused" means
    // here: the query is disabled rather than attempted and caught.
    expect(
      harness.listProfiles,
      'spr-auth/driverless/an-ungranted-read-is-not-attempted',
    ).not.toHaveBeenCalled()
  })

  it.each([
    ['refused by the engine', FORBIDDEN],
    ['step-up', STEP_UP],
    ['failed', LIVE_5XX],
  ] as const)(
    'profiles %s with the grant held: the screen says the read did not answer',
    async (_how, failure) => {
      // A 403 and a 5xx are one line on purpose, and it is the line this screen already
      // uses for its other halves (`partial.runLookupFailed`): with the grant HELD, what
      // the reader can act on is that the answer did not arrive. The grant-absent case
      // above is the one that names a permission, because there is one to name.
      grant(...BOTH_READ_AND_RUN_WRITE, PROFILE_READ)
      harness.listProfiles.mockRejectedValue(failure)
      renderView()
      await rowFor(DRIVERLESS_AT, 'spr-auth/driverless/failed-row')
      await waitFor(() => expect(harness.listProfiles).toHaveBeenCalled())
      expect(
        await screen.findByTestId('sessions-profile-names-failed'),
        'spr-auth/driverless/failed-notice',
      ).toBeInTheDocument()
      expect(
        screen.queryByTestId('sessions-profile-names-refused'),
        'spr-auth/driverless/one-cause-one-line',
      ).toBeNull()
    },
  )

  it('profile out of the page: no notice at all, because the read ANSWERED', async () => {
    grant(...BOTH_READ_AND_RUN_WRITE, PROFILE_READ)
    harness.listProfiles.mockResolvedValue({
      items: [OTHER_PROFILE],
      has_more: true,
    })
    renderView()
    const row = await rowFor(
      DRIVERLESS_AT,
      'spr-auth/driverless/out-of-page-row',
    )
    await waitFor(() => expect(harness.listProfiles).toHaveBeenCalled())
    await quiesce()
    // The dash is there — this is the same cell as the cases above — and the screen
    // still does not claim a read failed.
    expect(
      within(instanceCell(row)).getByText('Provider profile name not known'),
      'spr-auth/driverless/out-of-page-still-dashes',
    ).toBeInTheDocument()
    expect(
      screen.queryByTestId('sessions-profile-names-refused'),
      'spr-auth/driverless/no-refusal-was-claimed',
    ).toBeNull()
    expect(
      screen.queryByTestId('sessions-profile-names-failed'),
      'spr-auth/driverless/no-failure-was-claimed',
    ).toBeNull()
  })

  it('NEGATIVE CONTROL: a refused read whose rows all name themselves says nothing', async () => {
    // Withheld where it changes nothing: every row falls back to the driver it declares,
    // so a line regretting the directory read would be the console apologising for a
    // read it did not need. Without this control, "says so once" would also hold for a
    // notice that is always on.
    grant(...BOTH_READ_AND_RUN_WRITE)
    harness.live.mockResolvedValue({
      items: [{ ...DRIVERLESS_LIVE, provider: 'spr-driver-present' }],
      has_more: false,
    })
    renderView()
    await rowFor(DRIVERLESS_AT, 'spr-auth/driverless/named-row')
    await quiesce()
    expect(
      screen.queryByTestId('sessions-profile-names-refused'),
      'spr-auth/driverless/nothing-to-regret',
    ).toBeNull()
  })

  it('POSITIVE CONTROL: the same row with a driver still paints it, so the dash above is the absence and not a broken cell', async () => {
    grant(...BOTH_READ_AND_RUN_WRITE, PROFILE_READ)
    harness.live.mockResolvedValue({
      items: [{ ...DRIVERLESS_LIVE, provider: 'spr-driver-present' }],
      has_more: false,
    })
    harness.listProfiles.mockRejectedValue(FORBIDDEN)
    renderView()
    const row = await rowFor(DRIVERLESS_AT, 'spr-auth/driverless/control-row')
    await waitFor(() => expect(harness.listProfiles).toHaveBeenCalled())
    await waitFor(() =>
      expect(
        instanceCell(row)?.textContent,
        'spr-auth/driverless/control-paints-the-declared-driver',
      ).toBe('spr-driver-present'),
    )
  })
})
