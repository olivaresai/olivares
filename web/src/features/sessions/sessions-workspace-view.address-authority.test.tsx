// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// The address contract under AUTHORITY: guarding the address→Selection adjustment on the
// ADDRESS alone must leave the one-way retirement intact — under A → B → A, and under a
// RETIRED address that returns through Back. Written from an independent review of the
// work surface (2026-09-18), which measured the second case painting a half read under
// an admission that had already left.
import { QueryClientProvider } from '@tanstack/react-query'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fakeRouter } from '@/test/fake-router'
import {
  createSessionRowLocator,
  sessAddress,
  type SessionRowLocator,
} from '@/test/session-row-locator'
import { createQueryClient } from '@/lib/api/query'
import type { LiveDTO } from './types'

const harness = vi.hoisted(() => {
  const perms = new Set<string>()
  return {
    live: vi.fn(),
    liveOne: vi.fn(),
    liveById: vi.fn(),
    timeline: vi.fn(),
    timelineById: vi.fn(),
    listRuns: vi.fn(),
    getRun: vi.fn(),
    listProfiles: vi.fn(),
    perms,
    auth: {
      activeTenant: 'tenant-a' as string | null,
      principal: { user_id: 'user-a' } as { user_id: string } | null,
      can: (p: string) => perms.has(p),
    },
  }
})

vi.mock('@/lib/auth/context', () => ({ useAuth: () => harness.auth }))

vi.mock('@tanstack/react-router', async () => {
  const { fakeRouterModule } = await import('@/test/fake-router')
  return fakeRouterModule()
})

vi.mock('@/features/shared', async () => {
  const actual =
    await vi.importActual<typeof import('@/features/shared')>(
      '@/features/shared',
    )
  return { ...actual, useLiveStream: () => ({ status: 'idle' }) }
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

// The card reports THREE things: which target the view opened, what observed half the
// resolution is handing the panes, and whether it admits the half was not read.
vi.mock('./session-card', () => ({
  SessionCard: ({
    resolution,
  }: {
    resolution: {
      target: { sessionRef?: string; runRef?: string; liveRef?: string } | null
      live?: { current_action?: string }
      observeUnknown: boolean
    }
  }) =>
    resolution.target ? (
      <div data-testid="rev-card">
        sess:{resolution.target.sessionRef ?? ''}|paint:
        {resolution.live?.current_action ?? 'none'}|unknown:
        {String(resolution.observeUnknown)}
      </div>
    ) : null,
}))

vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  sessionsApi: {
    live: harness.live,
    liveOne: harness.liveOne,
    liveById: harness.liveById,
    timeline: harness.timeline,
    timelineById: harness.timelineById,
  },
}))

// `listProfiles` is answered EXPLICITLY even though these cases never grant
// `sessions:profile:read`: the view added that read in `8e8aafd19f`, and a seam left
// undefined turns "the query stayed disabled" and "the query ran and exploded" into the
// same green. The profile half of the screen is the partial-read file's subject.
vi.mock('@/features/agentops/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/features/agentops/api')>()),
  agentOpsApi: {
    listRuns: harness.listRuns,
    getRun: harness.getRun,
    listProfiles: harness.listProfiles,
  },
}))

import { SessionsWorkspaceView } from './sessions-workspace-view'

const LIVE_A: LiveDTO = {
  session_ref: 'rev-a',
  live_ref: 'rev-lr-a',
  attribution: 'legacy',
  cc_state: 'active',
  current_action: 'rev-a-list-action',
  input_tokens: 1,
  output_tokens: 1,
  cost_micro_usd: 1,
  event_count: 1,
  tool_call_count: 0,
  first_event_at: '2026-09-18T09:00:00Z',
  last_event_at: '2026-09-18T09:01:00Z',
  duration_seconds: 60,
}
const LIVE_B: LiveDTO = {
  ...LIVE_A,
  session_ref: 'rev-b',
  live_ref: 'rev-lr-b',
  current_action: 'rev-b-list-action',
}
/** What the RESOLUTION (not the list) answers for A — a distinct string, so a paint
 * can be attributed to the per-session read the card does. */
const RESOLVED_A: LiveDTO = { ...LIVE_A, current_action: 'rev-a-resolved' }

function grant(...perms: string[]) {
  harness.perms.clear()
  for (const p of perms) harness.perms.add(p)
}

function makeClient() {
  const qc = createQueryClient()
  const prev = qc.getDefaultOptions()
  qc.setDefaultOptions({
    queries: { ...prev.queries, retry: false, refetchOnWindowFocus: false },
    mutations: prev.mutations,
  })
  return qc
}

function openTable() {
  const trigger = screen.getByRole('tab', { name: 'Table' })
  act(() => {
    fireEvent.mouseDown(trigger)
  })
}

function renderView(qc = makeClient(), table = true) {
  const view = () => <SessionsWorkspaceView entrance="observe" />
  const result = render(
    <QueryClientProvider client={qc}>{view()}</QueryClientProvider>,
  )
  if (table) openTable()
  const again = () => {
    result.rerender(
      <QueryClientProvider client={qc}>{view()}</QueryClientProvider>,
    )
    if (table) openTable()
  }
  return { qc, again, ...result }
}

/**
 * ⛔ ROWS ARE FOUND BY ADDRESS, NOT BY TEXT. The cell paints a NAME now
 *    (`8e8aafd19f`), and these fixtures carry no run name, no summary and no goal — so
 *    every one of them paints the same `Untitled session` and `findByText('rev-a')`
 *    finds nothing at all. `data-address` carries the row's address, which is the
 *    same string the URL below is asserted to hold.
 */
let rows: SessionRowLocator
const A_ADDRESS = sessAddress('rev-a')
const B_ADDRESS = sessAddress('rev-b')

async function rowFor(address: string, setupName: string) {
  return rows.find(address, setupName)
}

const card = () => screen.queryByTestId('rev-card')

beforeEach(() => {
  vi.clearAllMocks()
  rows = createSessionRowLocator()
  fakeRouter.reset('/sessions')
  harness.auth.activeTenant = 'tenant-a'
  grant('sessions:live:read', 'sessions:run:read')
  harness.live.mockResolvedValue({ items: [LIVE_A, LIVE_B], has_more: false })
  harness.listRuns.mockResolvedValue({ items: [], has_more: false })
  harness.listProfiles.mockResolvedValue({ items: [], has_more: false })
  harness.liveOne.mockResolvedValue(RESOLVED_A)
  harness.liveById.mockResolvedValue(RESOLVED_A)
  harness.timeline.mockResolvedValue({ items: [], has_more: false })
  harness.timelineById.mockResolvedValue({ items: [], has_more: false })
})

describe('the address contract keeps the one-way retirement — A → B → A', () => {
  it('walks A → B → A by clicks and by Back, and the card follows the address', async () => {
    const user = userEvent.setup()
    renderView()
    await user.click(await rowFor(A_ADDRESS, 'rev/aba/row-a'))
    expect(await screen.findByTestId('rev-card')).toHaveTextContent(
      'sess:rev-a',
    )
    expect(fakeRouter.url()).toBe('/sessions?session=sess%3Arev-a')

    await user.click(await rowFor(B_ADDRESS, 'rev/aba/row-b'))
    expect(await screen.findByTestId('rev-card')).toHaveTextContent(
      'sess:rev-b',
    )

    await user.click(await rowFor(A_ADDRESS, 'rev/aba/row-a-again'))
    expect(await screen.findByTestId('rev-card')).toHaveTextContent(
      'sess:rev-a',
    )
    expect(fakeRouter.depth()).toBe(4)

    await user.click(await rowFor(B_ADDRESS, 'rev/aba/row-b-again'))
    await waitFor(() => expect(card()).toHaveTextContent('sess:rev-b'))
    act(() => fakeRouter.back())
    await waitFor(() => expect(card()).toHaveTextContent('sess:rev-a'))
    expect(fakeRouter.url()).toBe('/sessions?session=sess%3Arev-a')
  })
})

describe('a RETIRED address that comes back through Back', () => {
  it('hands the panes NOTHING read under the admission that left, and says so', async () => {
    const user = userEvent.setup()
    const { again } = renderView()

    // A is opened and its per-session read answers: the resolution's cache entry for A
    // exists under the admission that was granted.
    await user.click(await rowFor(A_ADDRESS, 'rev/retired/row-a'))
    await waitFor(() =>
      expect(card()).toHaveTextContent('paint:rev-a-resolved'),
    )
    expect(harness.liveOne).toHaveBeenCalledTimes(1)

    // B is opened: a second history entry, so A's entry survives the cleanup below.
    await user.click(await rowFor(B_ADDRESS, 'rev/retired/row-b'))
    await waitFor(() => expect(card()).toHaveTextContent('sess:rev-b'))

    // THE READ BIT LEAVES. B is retired, the notice is shown and the bar is cleaned.
    //
    // POSITIVE CONTROL for the absences below: both rows were located by this locator
    // a moment ago, and they are asserted gone by the SAME locator — so "the row left"
    // is a statement about the view rather than about a string nobody paints.
    grant('sessions:run:read')
    again()
    await waitFor(() => expect(card()).toBeNull())
    expect(
      rows.gone(A_ADDRESS, 'rev/retired/observed-half-excluded', {
        andNoRowAtAll: true,
      }),
      'rev/retired/observed-half-excluded',
    ).toBeNull()
    expect(
      rows.gone(B_ADDRESS, 'rev/retired/other-observed-row-excluded', {
        andNoRowAtAll: true,
      }),
      'rev/retired/other-observed-row-excluded',
    ).toBeNull()
    expect(screen.getByTestId('sessions-address-retired')).toBeVisible()
    await waitFor(() => expect(fakeRouter.url()).toBe('/sessions'))

    // BACK — onto the entry that still names A, with live:read still withdrawn. A
    // disabled query keeps its last data for gcTime; the resolution must not hand it
    // to the panes: nothing painted, the half declared unread, no new request.
    act(() => fakeRouter.back())
    await act(async () => {
      await new Promise((r) => setTimeout(r, 30))
    })
    expect(card()).toHaveTextContent('sess:rev-a')
    expect(harness.liveOne).toHaveBeenCalledTimes(2)
    expect(card()).toHaveTextContent('paint:none')
    expect(card()).toHaveTextContent('unknown:true')
  })

  it('a COLD deep link arriving with the live bit already gone paints nothing', async () => {
    grant('sessions:run:read')
    fakeRouter.reset('/sessions?session=sess%3Arev-a')
    const { again } = renderView()
    await act(async () => {
      await new Promise((r) => setTimeout(r, 30))
    })
    expect(card()).toHaveTextContent('sess:rev-a')
    expect(card()).toHaveTextContent('paint:none')
    expect(card()).toHaveTextContent('unknown:true')
    expect(harness.liveOne).not.toHaveBeenCalled()

    // POSITIVE CONTROL, in this case rather than by appeal to another one: "no read
    // was made" only means something if a read WOULD have been made here. Grant the
    // bit on the same address and the same mount, and the read happens and paints.
    grant('sessions:live:read', 'sessions:run:read')
    again()
    await waitFor(() =>
      expect(
        harness.liveOne,
        'rev/cold/control-the-read-does-happen',
      ).toHaveBeenCalledTimes(1),
    )
    await waitFor(() =>
      expect(card()).toHaveTextContent('paint:rev-a-resolved'),
    )
    expect(card()).toHaveTextContent('unknown:false')
  })

  it('the NARRATIVE pane paints no stale half either', async () => {
    const user = userEvent.setup()
    const { again } = renderView(makeClient(), false)
    const railRow = async (address: string) => {
      let el: HTMLElement | null = null
      await waitFor(() => {
        el = screen
          .getByTestId('work-rail')
          .querySelector(`[data-address="${address}"]`) as HTMLElement | null
        expect(el).not.toBeNull()
      })
      return el as unknown as HTMLElement
    }
    await user.click(await railRow('sess:rev-a'))
    await act(async () => {
      await new Promise((r) => setTimeout(r, 60))
    })
    // POSITIVE CONTROL: the pane DOES paint the per-session read while the admission
    // holds. Without this line the assertion at the end of the case — that it paints
    // no stale half — would hold for a pane that never painted anything.
    await waitFor(() =>
      expect(
        screen.getByTestId('session-narrative'),
        'rev/narrative/control-the-admitted-half-is-painted',
      ).toHaveTextContent('rev-a-resolved'),
    )
    await user.click(await railRow('sess:rev-b'))
    await waitFor(() => expect(fakeRouter.url()).toContain('sess%3Arev-b'))

    grant('sessions:run:read')
    again()
    await waitFor(() => expect(fakeRouter.url()).toBe('/sessions'))

    act(() => fakeRouter.back())
    await act(async () => {
      await new Promise((r) => setTimeout(r, 30))
    })
    const narrative = screen.getByTestId('session-narrative')
    expect(narrative).not.toHaveTextContent('rev-a-resolved')
  })
})
