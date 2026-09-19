// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE TWO ADMINISTRATIVE READS DO NOT LEAVE WITHOUT A CURRENT, EXACT ADMISSION.
//
// The independent review of `c04cb75de1` measured, against an immutable copy of the
// pinned source, that the collection's query callback handed the shared client only
// `{ tenant }`: with the surface permit ALREADY expired, clicking the real Refresh put a
// SECOND `GET /channels/administration` on the wire — two dispatches where one was
// admitted. The grant-sheet read had the visibly equivalent omission. A rendered
// `enabled` had decided the read at a render, and nothing looked again between that
// render and the bytes.
//
// This file measures the closure of that gap where it actually has to hold: through the
// REAL api wrappers and the REAL shared client (`lib/api/client.ts`), whose dispatch
// guard runs after the awaited proactive credential refresh and again before the single
// 401 replay — the two intervals a check at the callback alone cannot see. The permits
// are the product's own (`createCapabilityPermit`), so `isCurrent()` is the real rule and
// not a double that always agrees; only the OBSERVATION seam (`useCapability`) and the
// local monotonic clock are controlled, and every claim keeps its positive control
// beside it: the same read DOES leave under a current exact permit, and a NEW exact
// admission resumes it with the filter and the continuation it already had.
//
// The two reads share one mechanism (`admittedRead` in api.ts), so the seam table is
// walked once for both rather than transcribed twice.
import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { __resetRefreshState, configureApiClient } from '@/lib/api/client'

const caps = vi.hoisted(() => ({
  /** What `useCapability` answers, by mounted route pattern. */
  answers: new Map<string, unknown>(),
}))
vi.mock('@/lib/auth/capabilities', async (orig) => {
  const real = (await orig()) as Record<string, unknown>
  return {
    ...real,
    // Only the OBSERVATION is controlled. `createCapabilityPermit`, `sameQuestion`,
    // `CapabilityLostError` and everything the seam actually decides with stay real.
    useCapability: (q: { operation: string } | null) =>
      (q && caps.answers.get(q.operation)) ?? {
        access: 'unknown',
        permit: null,
        question: q,
      },
    // No mutation is confirmed here; the preflight is inert and never returns a permit.
    useCapabilityPreflight: () => ({
      context: null,
      live: () => null,
      request: async () => null,
    }),
  }
})

import {
  createCapabilityPermit,
  type CapabilityContext,
  type CapabilityPermit,
  type NormalizedCapabilityQuestion,
} from '@/lib/auth/capabilities'
import {
  listAdministrableChannels,
  listChannelGrants,
  UnadmittedReadError,
} from './api'
import {
  administrationSurfaceQuestion,
  CHANNEL_ADMINISTRATION_SURFACE,
  CHANNEL_GRANTS_OPERATION,
  grantSheetQuestion,
} from './capabilities'
import { ChannelAdminContinuity } from './channel-admin-continuity'
import { ChannelAdminSheet } from './channel-admin-sheet'
import { ChannelAdministration } from './channel-administration'
import {
  adminItemOf,
  CHANNEL_ID,
  grantsPageOf,
  renderWithQuery,
  scopeOf,
  USER_A,
  WS,
  WS2,
} from './test-harness'
import './i18n'

/* ── the wire, counted by path the way a server counts it ────────────────────── */

let sent: string[] = []
let statuses: number[] = []
let bodies: unknown[] = []
const collection = () =>
  sent.filter((u) => u.includes('/channels/administration'))
const grants = () => sent.filter((u) => u.includes('/grants'))

function stubFetch() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      if (init?.signal?.aborted) throw new DOMException('aborted', 'AbortError')
      sent.push(url)
      const status = statuses.shift() ?? 200
      const body =
        status === 401
          ? { error: { code: 'unauthenticated', message: 'expired' } }
          : (bodies.shift() ?? { items: [], has_more: false })
      return new Response(JSON.stringify(body), {
        status,
        headers: { 'Content-Type': 'application/json' },
      })
    }),
  )
}

/** A local monotonic clock: the permit's deadline is on `performance.now()`, and a
 *  deadline that a wall clock has to reach would make these cases timing-dependent. */
let now = 1_000
const far = () => new Date(Date.now() + 3_600_000).toISOString()
const soon = () => new Date(Date.now() + 10_000).toISOString()
function wireClient(over: {
  refreshSession?: () => Promise<boolean>
  getExpiresAt?: () => string | null
}) {
  configureApiClient({
    getToken: () => 'synthetic-credential',
    getTenant: () => 't1',
    onUnauthorized: () => {},
    refreshSession: over.refreshSession ?? (async () => false),
    getExpiresAt: over.getExpiresAt ?? far,
  })
}
/** A refresh the case releases: the interval the client AWAITS before its fetch. */
function controlledRefresh() {
  let started!: () => void
  let release!: () => void
  const startedP = new Promise<void>((r) => (started = r))
  const gate = new Promise<void>((r) => (release = r))
  return {
    refresh: async () => {
      started()
      await gate
      return true
    },
    started: startedP,
    release,
  }
}

/* ── real permits over a live context this file owns ─────────────────────────── */

const OWNER: CapabilityContext = {
  principalKind: 'user',
  actor: `user:${USER_A}`,
  tenant: 't1',
  credentialGeneration: 0,
  workspace: WS,
  lifetime: 0,
}
/** The live authority `isCurrent()` compares against. `null` is a withdrawn owner: no
 *  principal can be read, which is what a logout or a lost whoami looks like. */
let live: CapabilityContext | null = OWNER
const permitFor = (
  question: NormalizedCapabilityQuestion | null,
  deadline: number,
): CapabilityPermit =>
  createCapabilityPermit(
    OWNER,
    question as NormalizedCapabilityQuestion,
    deadline,
    () => live,
  )
const current = (q: NormalizedCapabilityQuestion | null) =>
  permitFor(q, now + 30_000)
const expired = (q: NormalizedCapabilityQuestion | null) =>
  permitFor(q, now - 1)
const SURFACE = () => administrationSurfaceQuestion(WS)
const ENTITY = () => grantSheetQuestion(WS, CHANNEL_ID)

const outcomeOf = (p: Promise<unknown>) =>
  p.then(
    () => 'resolved' as const,
    (e: unknown) => e,
  )

beforeEach(() => {
  sent = []
  statuses = []
  bodies = []
  now = 1_000
  live = OWNER
  caps.answers.clear()
  __resetRefreshState()
  stubFetch()
  vi.spyOn(performance, 'now').mockImplementation(() => now)
  wireClient({})
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

/* ── 1. the seam: one mechanism, both reads ──────────────────────────────────── */

/** The two administrative reads, each called with an admission the case chooses. The
 *  question a permit must answer is the read's OWN — the surface of the workspace being
 *  listed, and the `GET …/grants` operation of the channel being read. */
const READS: Array<{
  name: string
  question: () => NormalizedCapabilityQuestion | null
  /** A CURRENT permit that answers ANOTHER question of the same feature. */
  sibling: () => CapabilityPermit
  call: (admission: CapabilityPermit | null) => Promise<unknown>
}> = [
  {
    name: 'collection administration',
    question: SURFACE,
    // The entity permit of a row in the very list being asked for: current, positive,
    // and not a licence to enumerate its neighbours.
    sibling: () => current(ENTITY()),
    call: (admission) =>
      listAdministrableChannels(
        { workspace_id: WS, state: 'all', limit: 50 },
        { tenant: 't1', admission },
      ),
  },
  {
    name: 'entity grants',
    question: ENTITY,
    // The surface permit of the collection this channel is listed in.
    sibling: () => current(SURFACE()),
    call: (admission) =>
      listChannelGrants(
        CHANNEL_ID,
        { workspace_id: WS, state: 'active', limit: 50 },
        { tenant: 't1', admission },
      ),
  },
]

describe('an administrative read travels under its own current, exact admission', () => {
  it.each(READS)(
    '$name: POSITIVE CONTROL — a current exact permit sends exactly one request',
    async ({ question, call }) => {
      await expect(call(current(question()))).resolves.toBeDefined()
      expect(sent).toHaveLength(1)
    },
  )

  it.each(READS)(
    '$name: an EXPIRED permit refuses before the fetch, with zero bytes on the wire',
    async ({ question, call }) => {
      const err = await outcomeOf(call(expired(question())))
      expect(err).toBeInstanceOf(UnadmittedReadError)
      expect((err as UnadmittedReadError).reason).toBe('permit')
      // The permit's own reason is kept, not thrown away for the seam's.
      expect((err as UnadmittedReadError).cause).toMatchObject({
        name: 'CapabilityLostError',
      })
      expect(sent).toHaveLength(0)
    },
  )

  it.each(READS)(
    '$name: a WITHDRAWN owner refuses the same way — the permit is current in time and not in authority',
    async ({ question, call }) => {
      const admission = current(question())
      live = null
      const err = await outcomeOf(call(admission))
      expect(err).toBeInstanceOf(UnadmittedReadError)
      expect((err as UnadmittedReadError).reason).toBe('permit')
      expect(sent).toHaveLength(0)
    },
  )

  it.each(READS)(
    '$name: a MOVED owner — same values, another lifetime — refuses too',
    async ({ question, call }) => {
      const admission = current(question())
      live = { ...OWNER, lifetime: OWNER.lifetime + 1 }
      expect(await outcomeOf(call(admission))).toBeInstanceOf(
        UnadmittedReadError,
      )
      expect(sent).toHaveLength(0)
    },
  )

  it.each(READS)(
    '$name: NO admission at all refuses; the deny-closed decision lives at the seam, not in each caller',
    async ({ call }) => {
      const err = await outcomeOf(call(null))
      expect(err).toBeInstanceOf(UnadmittedReadError)
      expect((err as UnadmittedReadError).reason).toBe('permit')
      expect(sent).toHaveLength(0)
    },
  )

  it.each(READS)(
    '$name: a CURRENT permit for the sibling question is refused as `question` — an answer about one opening is not an answer about the other',
    async ({ sibling, call }) => {
      const err = await outcomeOf(call(sibling()))
      expect(err).toBeInstanceOf(UnadmittedReadError)
      expect((err as UnadmittedReadError).reason).toBe('question')
      expect(sent).toHaveLength(0)
    },
  )

  it('a current SURFACE permit for another workspace does not admit this workspace', async () => {
    const err = await outcomeOf(
      listAdministrableChannels(
        { workspace_id: WS, state: 'all', limit: 50 },
        {
          tenant: 't1',
          admission: current(administrationSurfaceQuestion(WS2)),
        },
      ),
    )
    expect((err as UnadmittedReadError).reason).toBe('question')
    expect(sent).toHaveLength(0)
  })

  it('a current GRANTS permit for another channel does not admit this channel', async () => {
    const other = '0192f2c0-bbbb-7000-8000-0000000000ff'
    const err = await outcomeOf(
      listChannelGrants(
        CHANNEL_ID,
        { workspace_id: WS, state: 'active', limit: 50 },
        { tenant: 't1', admission: current(grantSheetQuestion(WS, other)) },
      ),
    )
    expect((err as UnadmittedReadError).reason).toBe('question')
    expect(sent).toHaveLength(0)
  })
})

/* ── 2. the two intervals a check at the callback cannot see ─────────────────── */

describe('the check is at the transport, so the client’s own awaits are covered', () => {
  it('an expiry DURING the awaited proactive refresh refuses: zero fetches, and the callback had already run', async () => {
    const r = controlledRefresh()
    wireClient({ refreshSession: r.refresh, getExpiresAt: soon })
    const admission = current(SURFACE())
    const settled = outcomeOf(
      listAdministrableChannels(
        { workspace_id: WS, state: 'all', limit: 50 },
        { tenant: 't1', admission },
      ),
    )
    await r.started
    expect(sent).toHaveLength(0) // still inside the refresh
    expect(admission.isCurrent()).toBe(true)
    now += 60_000 // the permit dies while the client waits
    r.release()
    expect(await settled).toBeInstanceOf(UnadmittedReadError)
    expect(sent).toHaveLength(0)
  })

  it('POSITIVE CONTROL: the same read with the permit still current sends once through that refresh', async () => {
    const r = controlledRefresh()
    wireClient({ refreshSession: r.refresh, getExpiresAt: soon })
    const settled = listAdministrableChannels(
      { workspace_id: WS, state: 'all', limit: 50 },
      { tenant: 't1', admission: current(SURFACE()) },
    )
    await r.started
    r.release()
    await expect(settled).resolves.toBeDefined()
    expect(sent).toHaveLength(1)
  })

  it('an expiry before the 401 REPLAY leaves exactly the first send on the wire', async () => {
    statuses = [401, 200]
    const r = controlledRefresh()
    wireClient({ refreshSession: r.refresh })
    const settled = outcomeOf(
      listChannelGrants(
        CHANNEL_ID,
        { workspace_id: WS, state: 'active', limit: 50 },
        { tenant: 't1', admission: current(ENTITY()) },
      ),
    )
    await r.started
    expect(grants()).toHaveLength(1) // the first send happened, and answered 401
    now += 60_000
    r.release()
    expect(await settled).toBeInstanceOf(UnadmittedReadError)
    expect(grants()).toHaveLength(1) // the replay never left
  })

  it('POSITIVE CONTROL: the same 401 replay with a live permit sends twice and resolves', async () => {
    statuses = [401, 200]
    bodies = [{ error: {} }, grantsPageOf()]
    wireClient({ refreshSession: async () => true })
    await expect(
      listChannelGrants(
        CHANNEL_ID,
        { workspace_id: WS, state: 'active', limit: 50 },
        { tenant: 't1', admission: current(ENTITY()) },
      ),
    ).resolves.toBeDefined()
    expect(grants()).toHaveLength(2)
  })
})

/* ── 3. component → API → client, on the surface that was measured ──────────── */

function admit(
  operation: string,
  permit: CapabilityPermit | null,
  access: string,
) {
  caps.answers.set(operation, {
    access,
    permit,
    question: permit ? permit.question : null,
  })
}

describe('ChannelAdministration — the collection that was measured', () => {
  const page = (over: { continuation?: string; id?: string } = {}) => ({
    items: [
      adminItemOf({
        ...(over.id ? { id: over.id } : {}),
        name: over.id ? 'Bravo' : 'Ops',
        slug: over.id ? 'bravo' : 'ops',
      }),
    ],
    has_more: Boolean(over.continuation),
    ...(over.continuation ? { continuation: over.continuation } : {}),
  })

  it('an EXPIRED retained admission shows no result or continuation, and a NEW exact admission resumes the read with its filter and its continuation', async () => {
    bodies = [
      page(),
      page({ continuation: 'c3a1.stale-page-two' }),
      page({ continuation: 'c3a1.page-two' }),
      page({ id: '0192f2c0-bbbb-7000-8000-0000000000b2' }),
    ]
    admit(CHANNEL_ADMINISTRATION_SURFACE, current(SURFACE()), 'reachable')
    const view = renderWithQuery(() => (
      <ChannelAdministration scope={scopeOf()} onOpenChannel={() => {}} />
    ))
    expect(await screen.findByText('Ops')).toBeInTheDocument()
    expect(collection()).toHaveLength(1)
    expect(collection()[0]).toContain('state=all&limit=50')

    // A filter the operator chose, which the recovery below must not lose.
    const user = userEvent.setup()
    await user.click(screen.getByRole('combobox', { name: 'Persisted state' }))
    await user.click(
      await screen.findByRole('option', { name: 'Archived only' }),
    )
    await waitFor(() => expect(collection()).toHaveLength(2))
    expect(collection()[1]).toContain('state=archived')
    expect(
      screen.getByRole('button', { name: 'Load more' }),
    ).toBeInTheDocument()

    // THE MEASURED CASE. The rendered admission is still installed — `enabled` is still
    // true, the Refresh control is still enabled — and the permit behind it is dead.
    now += 60_000
    const before = collection().length
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await waitFor(() =>
      expect(screen.queryByText('Ops')).not.toBeInTheDocument(),
    )
    expect(collection()).toHaveLength(before)
    // A local refusal is not reported as an answer from the engine: no failure notice
    // is rendered for it, and its own sentence never reaches the screen.
    expect(document.querySelector('[data-slot="failure-notice"]')).toBeNull()
    expect(screen.queryByText(/not admitted/)).toBeNull()
    // Zero bytes cannot become a successful empty result, and the continuation of the
    // prior page is no longer an action on this local non-answer.
    expect(screen.queryByText('No administrable channels')).toBeNull()
    expect(screen.queryByText(/No channel in this workspace grants/)).toBeNull()
    expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull()
    expect(
      document.querySelector('[data-slot="capability-unavailable"]'),
    ).not.toBeNull()

    // A NEW exact admission, and the legitimate read comes back — same filter, and the
    // continuation of the page it is given is still followed.
    admit(CHANNEL_ADMINISTRATION_SURFACE, current(SURFACE()), 'reachable')
    view.rerender()
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await waitFor(() => expect(collection()).toHaveLength(before + 1))
    expect(collection()[before]).toContain('state=archived&limit=50')
    expect(await screen.findByText('Ops')).toBeInTheDocument()
    expect(
      document.querySelector('[data-slot="capability-unavailable"]'),
    ).toBeNull()
    await user.click(await screen.findByRole('button', { name: 'Load more' }))
    await waitFor(() => expect(collection()).toHaveLength(before + 2))
    expect(collection()[before + 1]).toContain('continuation=c3a1.page-two')
  })

  it('a WITHDRAWN owner stops the collection the same way, and the local refusal is asked exactly once', async () => {
    bodies = [page()]
    admit(CHANNEL_ADMINISTRATION_SURFACE, current(SURFACE()), 'reachable')
    renderWithQuery(() => (
      <ChannelAdministration scope={scopeOf()} onOpenChannel={() => {}} />
    ))
    expect(await screen.findByText('Ops')).toBeInTheDocument()
    live = null
    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    await waitFor(() =>
      expect(screen.queryByText('Ops')).not.toBeInTheDocument(),
    )
    // ONE attempt: a spent permit is a one-way door, so the refusal is not on a retry
    // cadence — re-running the callback could only refuse again.
    expect(collection()).toHaveLength(1)
  })
})

describe('ChannelAdminSheet — the equivalent entity read', () => {
  function mount() {
    return renderWithQuery(() => (
      <ChannelAdminContinuity admitted access="allowed">
        <ChannelAdminSheet
          open
          onOpenChange={() => {}}
          channelId={CHANNEL_ID}
          scope={scopeOf()}
          canUserRead={false}
          canAgentRead={false}
          me={{ userId: USER_A, label: 'A' }}
          onMutated={() => {}}
        />
      </ChannelAdminContinuity>
    ))
  }

  it('an EXPIRED grant-sheet admission dispatches nothing on Re-read, and a NEW exact admission resumes it', async () => {
    bodies = [grantsPageOf(), grantsPageOf()]
    admit(CHANNEL_GRANTS_OPERATION, current(ENTITY()), 'allowed')
    const view = mount()
    await waitFor(() => expect(grants()).toHaveLength(1))
    const sheet = await screen.findByRole('dialog')
    await waitFor(() =>
      expect(
        sheet.querySelector('[data-slot="admin-etag"]')?.textContent?.trim(),
      ).toBe('"v2"'),
    )

    now += 60_000
    fireEvent.click(screen.getByRole('button', { name: 'Re-read' }))
    await waitFor(() =>
      expect(
        sheet.querySelector('[data-slot="admin-etag"]'),
      ).not.toBeInTheDocument(),
    )
    expect(grants()).toHaveLength(1)
    // Nothing is painted from the page the sheet no longer holds an admission for, and
    // no read failure is reported on the engine's behalf.
    expect(sheet.querySelector('[data-slot="admin-read-failure"]')).toBeNull()

    admit(CHANNEL_GRANTS_OPERATION, current(ENTITY()), 'allowed')
    view.rerender()
    fireEvent.click(screen.getByRole('button', { name: 'Re-read' }))
    await waitFor(() => expect(grants()).toHaveLength(2))
    expect(grants()[1]).toContain('state=active&limit=50')
    await waitFor(() =>
      expect(
        sheet.querySelector('[data-slot="admin-etag"]')?.textContent?.trim(),
      ).toBe('"v2"'),
    )
  })
})
