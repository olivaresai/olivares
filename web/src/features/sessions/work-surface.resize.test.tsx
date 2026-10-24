// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
import { usePaneWidthsStore } from '@/stores/pane-widths'
import { mergeSessions } from './provenance'
import type { RunDTO } from '@/features/agentops/types'
import type { LiveDTO } from './types'
import type { SessionResolution } from './use-session-resolution'
import { WorkSurface } from './work-surface'
import './i18n'
import '@/features/agentops/i18n'

const auth = vi.hoisted(() => ({
  activeTenant: 'tnt-demo' as string | null,
  can: (_: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

const api = vi.hoisted(() => ({ timelineById: vi.fn(), timeline: vi.fn() }))
vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  sessionsApi: api,
}))

function live(over: Partial<LiveDTO> = {}): LiveDTO {
  return {
    session_ref: 'sess-a',
    live_ref: 'lr-a',
    attribution: 'managed',
    cc_state: 'active',
    input_tokens: 0,
    output_tokens: 0,
    cost_micro_usd: 0,
    event_count: 0,
    tool_call_count: 0,
    first_event_at: '2026-09-18T09:00:00Z',
    last_event_at: '2026-09-18T09:00:10Z',
    duration_seconds: 10,
    ...over,
  }
}
const RUN: RunDTO = {
  run_ref: 'run-a',
  name: 'nightly',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 0,
  pep_provisioned: true,
  record_io: true,
  critical: false,
  provider_driver: 'claude',
  provider_profile_ref: 'ppf_team',
  live_ref: 'lr-a',
}
const RUN_B: RunDTO = {
  ...RUN,
  run_ref: 'run-b',
  name: 'review',
  live_ref: 'lr-b',
}
const sessions = mergeSessions(
  [live(), live({ session_ref: 'sess-b', live_ref: 'lr-b' })],
  [RUN, RUN_B],
)
const sessionA = sessions.find((s) => s.live?.live_ref === 'lr-a')!
const sessionB = sessions.find((s) => s.live?.live_ref === 'lr-b')!
const resolution: SessionResolution = {
  target: { liveRef: 'lr-a' },
  session: sessionA,
  live: live(),
  runs: [RUN],
  related: [],
  streamStatus: 'open',
  operateUnknown: false,
  observeUnknown: false,
  loading: false,
  grants: { liveRead: true, runRead: true, runWrite: true, runAdmin: false },
}
const besideResolution: SessionResolution = {
  ...resolution,
  target: { liveRef: 'lr-b' },
  session: sessionB,
  live: live({ session_ref: 'sess-b', live_ref: 'lr-b' }),
  runs: [RUN_B],
}

// jsdom has no layout: every pane is 360 px wide.
let paneWidth = 360
beforeEach(() => {
  vi.clearAllMocks()
  window.localStorage.clear()
  paneWidth = 360
  api.timelineById.mockResolvedValue({ items: [], has_more: false, cursor: '' })
  usePaneWidthsStore.setState({
    owner: null,
    loaded: false,
    widths: {},
    touched: new Set(),
    persist: null,
  })
  vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(
    () => ({ width: paneWidth }) as DOMRect,
  )
})
afterEach(() => vi.restoreAllMocks())

function renderSurface(over: Partial<Parameters<typeof WorkSurface>[0]> = {}) {
  return renderIntel(
    <WorkSurface
      sessions={sessions}
      loading={false}
      address={{ address: sessionA.key, pane: 'rail', evidence: 'checks' }}
      resolution={resolution}
      pinned={new Set()}
      onTogglePin={vi.fn()}
      onOpen={vi.fn()}
      onPane={vi.fn()}
      onEvidence={vi.fn()}
      onOpenDetail={vi.fn()}
      {...over}
    />,
  )
}
const split = {
  address: {
    address: sessionA.key,
    beside: sessionB.key,
    pane: 'narrative' as const,
    evidence: 'checks' as const,
  },
  besideResolution,
}
const surfaceGrid = () =>
  document.querySelector('[data-context]') as HTMLElement
const threadsGrid = () =>
  document.getElementById('work-pane-narrative')!.parentElement as HTMLElement
const stored = () => usePaneWidthsStore.getState().widths

describe('WorkSurface resizing', () => {
  it('keeps the stylesheet widths until a person stores one', async () => {
    renderSurface()
    await screen.findByTestId('work-rail')
    expect(surfaceGrid().style.getPropertyValue('--list-w')).toBe('')
    expect(surfaceGrid().style.getPropertyValue('--ctx-w')).toBe('')
  })

  it('applies the stored widths of the list and the context pane', async () => {
    usePaneWidthsStore.setState({
      widths: { 'sessions-list': 420, 'sessions-context': 360 },
    })
    renderSurface({
      address: { address: sessionA.key, pane: 'context', evidence: 'checks' },
    })
    await screen.findByTestId('session-context')
    expect(surfaceGrid().style.getPropertyValue('--list-w')).toBe('420px')
    expect(surfaceGrid().style.getPropertyValue('--ctx-w')).toBe('360px')
  })

  it('applies the stored width of the second thread', async () => {
    usePaneWidthsStore.setState({ widths: { 'sessions-beside': 640 } })
    renderSurface(split)
    await screen.findByRole('region', { name: 'Session beside' })
    expect(threadsGrid().style.getPropertyValue('--beside-w')).toBe('640px')
  })

  it('never lets a stored width take more than 35% of the surface from the thread', async () => {
    renderSurface({
      address: { address: sessionA.key, pane: 'context', evidence: 'checks' },
    })
    await screen.findByTestId('session-context')
    const cls = surfaceGrid().className
    expect(cls).toContain(
      'min-[761px]:grid-cols-[min(var(--list-w),35%)_minmax(0,1fr)]',
    )
    expect(cls).toContain(
      'min-[1441px]:grid-cols-[min(var(--list-w),35%)_minmax(0,1fr)_min(var(--ctx-w),35%)]',
    )
  })

  it('resizes the list from the keyboard and remembers the width', async () => {
    const user = userEvent.setup()
    renderSurface()
    const handle = await screen.findByRole('separator', {
      name: 'Resize the session list',
    })
    handle.focus()
    await user.keyboard('{ArrowRight}')
    expect(stored()['sessions-list']).toBe(376)
    expect(surfaceGrid().style.getPropertyValue('--list-w')).toBe('376px')
  })

  it('puts the list back to its default on Enter and on double-click', async () => {
    const user = userEvent.setup()
    usePaneWidthsStore.setState({ widths: { 'sessions-list': 480 } })
    renderSurface()
    const handle = await screen.findByRole('separator', {
      name: 'Resize the session list',
    })
    expect(surfaceGrid().style.getPropertyValue('--list-w')).toBe('480px')
    handle.focus()
    await user.keyboard('{Enter}')
    expect(stored()['sessions-list']).toBeUndefined()
    expect(surfaceGrid().style.getPropertyValue('--list-w')).toBe('')
    usePaneWidthsStore.setState({ widths: { 'sessions-list': 480 } })
    await user.dblClick(handle)
    expect(stored()['sessions-list']).toBeUndefined()
  })

  it('widens the context pane with the arrow that moves its left edge outward', async () => {
    const user = userEvent.setup()
    renderSurface({
      address: { address: sessionA.key, pane: 'context', evidence: 'checks' },
    })
    const handle = await screen.findByRole('separator', {
      name: 'Resize the context pane',
    })
    handle.focus()
    await user.keyboard('{ArrowLeft}')
    expect(stored()['sessions-context']).toBe(376)
    expect(surfaceGrid().style.getPropertyValue('--ctx-w')).toBe('376px')
  })

  it('offers the context handle only while the pane is open, and only beside the thread', async () => {
    renderSurface()
    await screen.findByTestId('work-rail')
    expect(
      screen.queryByRole('separator', { name: 'Resize the context pane' }),
    ).toBeNull()
    await userEvent.click(screen.getByTestId('context-toggle'))
    const handle = await screen.findByRole('separator', {
      name: 'Resize the context pane',
    })
    // Where the pane would squeeze the thread it replaces it: nothing to drag.
    expect(handle.className).toContain('max-[1440px]:hidden')
  })

  it('keeps the list handle while the side pane is in front, as the list stays', async () => {
    renderSurface({
      address: { address: sessionA.key, pane: 'context', evidence: 'checks' },
    })
    const handle = await screen.findByRole('separator', {
      name: 'Resize the session list',
    })
    expect(handle.className).toContain('max-[760px]:hidden')
    expect(handle.className).not.toContain('max-[1440px]:hidden')
  })

  it('keeps the handles off a phone, where each pane is a screen of its own', async () => {
    renderSurface(split)
    const list = await screen.findByRole('separator', {
      name: 'Resize the session list',
    })
    const beside = screen.getByRole('separator', {
      name: 'Resize the session beside',
    })
    expect(list.className).toContain('max-[760px]:hidden')
    expect(beside.className).toContain('max-[1279px]:hidden')
  })

  it('offers the divider between two threads only when two are open', async () => {
    renderSurface()
    await screen.findByTestId('work-rail')
    expect(
      screen.queryByRole('separator', { name: 'Resize the session beside' }),
    ).toBeNull()
  })

  it('resizes the second thread from the keyboard', async () => {
    const user = userEvent.setup()
    renderSurface(split)
    const handle = await screen.findByRole('separator', {
      name: 'Resize the session beside',
    })
    handle.focus()
    await user.keyboard('{ArrowLeft}')
    expect(stored()['sessions-beside']).toBe(376)
    expect(threadsGrid().style.getPropertyValue('--beside-w')).toBe('376px')
  })
})
