// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { cleanup, fireEvent, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
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
    attribution: 'observed',
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

const sessions = mergeSessions(
  [live(), live({ session_ref: 'sess-b', live_ref: 'lr-b' })],
  [],
)

const resolution: SessionResolution = {
  target: { liveRef: 'lr-a' },
  session: sessions[0],
  live: live(),
  runs: [],
  related: [],
  streamStatus: 'open',
  operateUnknown: false,
  observeUnknown: false,
  loading: false,
  grants: { liveRead: true, runRead: true, runWrite: true, runAdmin: false },
}

beforeEach(() => {
  vi.clearAllMocks()
  window.localStorage.clear()
  api.timelineById.mockResolvedValue({ items: [], has_more: false, cursor: '' })
})

function renderSurface(over: Partial<Parameters<typeof WorkSurface>[0]> = {}) {
  const onPane = vi.fn()
  const onOpen = vi.fn()
  const rendered = renderIntel(
    <WorkSurface
      sessions={sessions}
      loading={false}
      address={{ address: 'live:lr-a', pane: 'rail', evidence: 'checks' }}
      resolution={resolution}
      pinned={new Set()}
      onTogglePin={vi.fn()}
      onOpen={onOpen}
      onPane={onPane}
      onEvidence={vi.fn()}
      onOpenDetail={vi.fn()}
      {...over}
    />,
  )
  return { onPane, onOpen, unmount: rendered.unmount }
}

const pane = (id: string) => document.getElementById(`work-pane-${id}`)

// Two managed conversations, each with its own run, so each column mounts a real composer.
const managedA = live({ attribution: 'managed' })
const managedB = live({
  session_ref: 'sess-b',
  live_ref: 'lr-b',
  attribution: 'managed',
})
const RUN_B: RunDTO = {
  ...RUN,
  run_ref: 'run-b',
  name: 'review',
  live_ref: 'lr-b',
}
const paired = mergeSessions([managedA, managedB], [RUN, RUN_B])
const pairedA = paired.find((s) => s.live?.live_ref === 'lr-a')!
const pairedB = paired.find((s) => s.live?.live_ref === 'lr-b')!

function renderSplit(over: Partial<Parameters<typeof WorkSurface>[0]> = {}) {
  return renderSurface({
    sessions: paired,
    address: {
      address: pairedA.key,
      beside: pairedB.key,
      pane: 'narrative',
      evidence: 'checks',
    },
    resolution: {
      ...resolution,
      session: pairedA,
      live: managedA,
      runs: [RUN],
    },
    besideResolution: {
      ...resolution,
      target: { liveRef: 'lr-b' },
      session: pairedB,
      live: managedB,
      runs: [RUN_B],
    },
    ...over,
  })
}
const composerOf = (column: string) =>
  within(screen.getByRole('region', { name: column })).getByTestId(
    'launcher-input',
  ) as HTMLTextAreaElement

describe('WorkSurface', () => {
  it.each(['{Enter}', ' '])(
    'activates a focused Open beside button with %s without opening a primary session',
    async (key) => {
      const onOpenBeside = vi.fn()
      const { onOpen } = renderSurface({ onOpenBeside })
      screen.getAllByLabelText('Open beside')[0].focus()
      await userEvent.keyboard(key)
      expect(onOpenBeside).toHaveBeenCalledWith(sessions[1])
      expect(onOpen).not.toHaveBeenCalled()
    },
  )

  it('keeps the focused narrative in the URL so narrowing does not hide it', () => {
    const onPane = vi.fn()
    renderSurface({
      address: {
        address: 'live:lr-a',
        beside: 'live:lr-b',
        pane: 'narrative',
        evidence: 'checks',
      },
      besideResolution: {
        ...resolution,
        target: { liveRef: 'lr-b' },
        session: sessions[1],
        live: live({ live_ref: 'lr-b' }),
      },
      onPane,
    })
    screen.getByRole('region', { name: 'Session beside' }).focus()
    expect(onPane).toHaveBeenCalledWith('beside')
  })

  it('names both narratives and moves keyboard focus without sending a turn', async () => {
    const onPane = vi.fn()
    const onCloseBeside = vi.fn()
    renderSurface({
      address: {
        address: 'live:lr-a',
        beside: 'live:lr-b',
        pane: 'narrative',
        evidence: 'checks',
      },
      besideResolution: {
        ...resolution,
        target: { liveRef: 'lr-b' },
        session: sessions[1],
        live: live({ live_ref: 'lr-b' }),
      },
      onCloseBeside,
      onPane,
    })
    const primary = screen.getByRole('region', { name: 'Current session' })
    const beside = screen.getByRole('region', { name: 'Session beside' })
    primary.focus()
    fireEvent.keyDown(primary, {
      key: 'ArrowRight',
      altKey: true,
      shiftKey: true,
    })
    expect(beside).toHaveFocus()
    expect(onPane).toHaveBeenCalledWith('beside')
    fireEvent.keyDown(beside, { key: 'w', altKey: true, shiftKey: true })
    expect(onCloseBeside).toHaveBeenCalledOnce()
  })

  it('keeps the pointer Open beside button out of the listbox tree and the tab order', async () => {
    const onOpenBeside = vi.fn()
    renderSurface({ onOpenBeside })
    // Only the OTHER row offers it; the selected row is already the primary session.
    const buttons = screen.getAllByLabelText('Open beside')
    expect(buttons).toHaveLength(1)
    // A listbox owns options, so the pointer convenience is not part of its accessible
    // tree and adds no tab stop; the keyboard routes are the command and the palette.
    expect(screen.queryByRole('button', { name: 'Open beside' })).toBeNull()
    expect(buttons[0]).toHaveAttribute('aria-hidden', 'true')
    expect(buttons[0]).toHaveAttribute('tabindex', '-1')
    for (const option of screen.getAllByRole('option'))
      expect(option.querySelector('button')).toBeNull()
    await userEvent.click(buttons[0])
    expect(onOpenBeside).toHaveBeenCalledWith(sessions[1])
  })

  it('opens the focused rail row beside with the declared command, and nothing else', () => {
    const onOpenBeside = vi.fn()
    renderSurface({ onOpenBeside })
    const [selected, other] = screen.getAllByRole('option')
    other.focus()
    fireEvent.keyDown(other, { key: 'o', altKey: true, shiftKey: true })
    expect(onOpenBeside).toHaveBeenCalledTimes(1)
    expect(onOpenBeside).toHaveBeenCalledWith(sessions[1])
    // The command names a row. With focus on the open session's own row, or outside the
    // rail, it opens nothing: it never picks a session the person did not point at.
    onOpenBeside.mockClear()
    selected.focus()
    fireEvent.keyDown(selected, { key: 'o', altKey: true, shiftKey: true })
    ;(document.activeElement as HTMLElement).blur()
    fireEvent.keyDown(document.body, { key: 'o', altKey: true, shiftKey: true })
    expect(onOpenBeside).not.toHaveBeenCalled()
  })

  it('hops focus and closes from inside a composer without typing into either draft', async () => {
    const user = userEvent.setup()
    const onPane = vi.fn()
    const onCloseBeside = vi.fn()
    renderSplit({ onPane, onCloseBeside })
    const primary = composerOf('Current session')
    const beside = composerOf('Session beside')
    await user.click(primary)
    await user.keyboard('first draft')
    await user.keyboard('{Alt>}{Shift>}{ArrowRight}{/Shift}{/Alt}')
    expect(onPane).toHaveBeenCalledWith('beside')
    expect(beside).toHaveFocus()
    await user.keyboard('second draft')
    expect(primary.value).toBe('first draft')
    expect(beside.value).toBe('second draft')
    await user.keyboard('{Alt>}{Shift>}W{/Shift}{/Alt}')
    expect(onCloseBeside).toHaveBeenCalledOnce()
    expect(beside.value).toBe('second draft')
  })

  it('gives each column its own composer, and only the focused one receives keys', async () => {
    const user = userEvent.setup()
    renderSplit()
    const primary = composerOf('Current session')
    const beside = composerOf('Session beside')
    expect(primary.id).not.toBe(beside.id)
    await user.click(beside)
    await user.keyboard('to the second')
    expect(beside.value).toBe('to the second')
    expect(primary.value).toBe('')
    await user.click(primary)
    await user.keyboard('to the first')
    expect(primary.value).toBe('to the first')
    expect(beside.value).toBe('to the second')
  })

  it('draws no focus ring and no tab target around a single narrative', () => {
    renderSurface({
      address: { address: 'live:lr-a', pane: 'narrative', evidence: 'checks' },
    })
    const narrative = pane('narrative')!
    expect(narrative).not.toHaveAttribute('tabindex')
    expect(narrative.className).not.toContain('focus-within:ring')
  })

  it('names the switch between two columns as a group', () => {
    renderSplit()
    const bar = screen.getByRole('group', { name: 'Switch session' })
    expect(
      within(bar).getByRole('button', { name: 'Current session' }),
    ).toHaveAttribute('aria-pressed', 'true')
    expect(
      within(bar).getByRole('button', { name: 'Session beside' }),
    ).toHaveAttribute('aria-pressed', 'false')
  })

  it('does not offer a composer for a host isolation refusal', () => {
    const failed = {
      ...RUN,
      state: 'failed' as const,
      reason:
        'the session was not started because its network boundary could not be set up: the host or container must allow unprivileged user and network namespaces and Landlock: operation not permitted',
    }
    renderSurface({
      resolution: {
        ...resolution,
        runs: [failed],
        session: mergeSessions([], [failed])[0],
      },
    })
    expect(screen.queryByTestId('work-composer')).not.toBeInTheDocument()
  })
  it('keeps the composer for a retryable failure', () => {
    const failed = {
      ...RUN,
      state: 'failed' as const,
      reason: 'provider temporarily unavailable',
    }
    renderSurface({
      resolution: {
        ...resolution,
        runs: [failed],
        session: mergeSessions([], [failed])[0],
      },
    })
    expect(screen.getByTestId('work-composer')).toBeInTheDocument()
  })
  it('mounts all three panes whatever is in front', async () => {
    renderSurface()
    await screen.findByTestId('work-rail')
    for (const id of ['rail', 'narrative', 'context'])
      expect(pane(id)).not.toBeNull()
  })

  it('hides the panes that are not in front with a CLASS, never by unmounting', async () => {
    renderSurface({
      address: { address: 'live:lr-a', pane: 'context', evidence: 'checks' },
    })
    await screen.findByTestId('session-context')
    expect(pane('context')?.className).not.toContain('hidden')
    expect(pane('rail')?.className).toContain('hidden xl:flex')
    expect(pane('narrative')?.className).toContain('hidden xl:flex')
  })

  it('shows the list and the thread side by side from 761 px, and one at a time below it', async () => {
    renderSurface({
      address: { address: 'live:lr-a', pane: 'narrative', evidence: 'checks' },
    })
    await screen.findByTestId('work-rail')
    // The thread is in front: the list shows beside it from 761 px up and is hidden below.
    expect(pane('rail')?.className).toContain('hidden min-[761px]:flex')
    expect(pane('narrative')?.className.split(/\s+/)).not.toContain('hidden')
  })

  it('lays the list out at 360 px, 280 px at 1360 px and below, with no card chrome', async () => {
    renderSurface()
    await screen.findByTestId('work-rail')
    const grid = document.querySelector('[data-context]') as HTMLElement
    const cls = grid.className
    expect(cls).toContain('[--list-w:360px]')
    expect(cls).toContain('max-[1360px]:[--list-w:280px]')
    expect(cls).toContain('min-[761px]:grid-cols-[var(--list-w)_minmax(0,1fr)]')
    for (const id of ['rail', 'narrative', 'context'])
      expect(pane(id)?.className).not.toMatch(
        /rounded-lg|border-border|bg-surface/,
      )
    expect(pane('rail')?.className).toContain('min-[761px]:border-r')
  })

  it('puts the list header above the rows, inside the list column', async () => {
    renderSurface({ listHeader: <div data-testid="head">Sessions head</div> })
    const rail = await screen.findByTestId('work-rail')
    const head = screen.getByTestId('head')
    expect(pane('rail')).toContainElement(head)
    expect(
      head.compareDocumentPosition(rail) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
  })

  it('reports the chosen pane instead of holding it, so the URL can own it', async () => {
    const user = userEvent.setup()
    const { onPane } = renderSurface({
      address: { address: 'live:lr-a', pane: 'narrative', evidence: 'checks' },
    })
    await user.click(screen.getByTestId('pane-button-context'))
    expect(onPane).toHaveBeenCalledWith('context')
    await user.click(screen.getByTestId('pane-button-rail'))
    expect(onPane).toHaveBeenCalledWith('rail')
  })

  it('gives a narrow screen one way back from the thread, one way to its context and one back again', async () => {
    renderSurface({
      address: { address: 'live:lr-a', pane: 'narrative', evidence: 'checks' },
    })
    // The thread's header carries the back arrow to the list (below 761 px) and the
    // Context icon that brings the pane forward (between 761 px and `xl`); the context
    // pane carries its own way back to the thread.
    expect(await screen.findByTestId('pane-button-rail')).toHaveAttribute(
      'aria-controls',
      'work-pane-rail',
    )
    expect(screen.getByTestId('pane-button-context')).toHaveAttribute(
      'aria-controls',
      'work-pane-context',
    )
    expect(screen.getByTestId('pane-button-narrative')).toHaveAttribute(
      'aria-controls',
      'work-pane-narrative',
    )
    expect(pane('context')).not.toBeNull()
    for (const id of ['pane-button-rail', 'pane-button-context'])
      expect(screen.getByTestId(id)).toHaveAccessibleName(/.+/)
    expect(screen.getByTestId('pane-button-rail').className).toContain(
      'min-[761px]:hidden',
    )
  })

  it('opens the context pane on demand beside the work, and closes it again', async () => {
    const user = userEvent.setup()
    renderSurface({
      address: { address: 'live:lr-a', pane: 'narrative', evidence: 'checks' },
    })
    const grid = () => document.querySelector('[data-context]') as HTMLElement
    expect(grid().dataset.context).toBe('closed')
    expect(pane('context')?.className).toContain('xl:hidden')
    expect(pane('context')).not.toBeNull()
    const toggle = screen.getByTestId('context-toggle')
    expect(toggle).toHaveAttribute('aria-pressed', 'false')
    expect(toggle).toHaveAttribute('aria-controls', 'work-pane-context')
    await user.click(toggle)
    expect(grid().dataset.context).toBe('open')
    expect(pane('context')?.className).toContain('xl:block')
    expect(toggle).toHaveAttribute('aria-pressed', 'true')
    await user.click(screen.getByTestId('context-close'))
    expect(grid().dataset.context).toBe('closed')
    await user.click(toggle)
    await user.keyboard('{Escape}')
    expect(grid().dataset.context).toBe('closed')
  })

  describe('the side pane remembers its state in this browser', () => {
    const grid = () => document.querySelector('[data-context]') as HTMLElement
    const setWidth = (px: number) =>
      Object.defineProperty(window, 'innerWidth', {
        value: px,
        configurable: true,
        writable: true,
      })
    afterEach(() => setWidth(1024))

    it('is closed by default up to 1440 px and open above it', () => {
      setWidth(1440)
      const first = renderSurface()
      expect(grid().dataset.context).toBe('closed')
      first.unmount()
      setWidth(1600)
      renderSurface()
      expect(grid().dataset.context).toBe('open')
    })

    it('keeps what the person chose, closed or open, whatever the width', async () => {
      const user = userEvent.setup()
      setWidth(1600)
      const first = renderSurface()
      expect(grid().dataset.context).toBe('open')
      await user.click(screen.getByTestId('context-toggle'))
      expect(grid().dataset.context).toBe('closed')
      first.unmount()
      renderSurface()
      expect(grid().dataset.context).toBe('closed')
      await user.click(screen.getByTestId('context-toggle'))
      expect(window.localStorage.getItem('olivares.sessions.sidePane')).toBe(
        'open',
      )
    })

    it('opens at once for a stored open, on a narrower screen too', () => {
      setWidth(1280)
      window.localStorage.setItem('olivares.sessions.sidePane', 'open')
      renderSurface()
      expect(grid().dataset.context).toBe('open')
    })
  })

  it('opens the context pane when the address names it', () => {
    renderSurface({
      address: { address: 'live:lr-a', pane: 'context', evidence: 'checks' },
    })
    expect(
      (document.querySelector('[data-context]') as HTMLElement).dataset.context,
    ).toBe('open')
  })

  it('passes the open session to the rail as the selected row', async () => {
    renderSurface()
    const rail = await screen.findByTestId('work-rail')
    const selected = rail.querySelector('[aria-selected="true"]')
    expect(selected).toHaveAttribute('data-address', 'live:lr-a')
  })

  it('attaches the composer to the open session as RUNNING', async () => {
    const managed = live({ attribution: 'managed' })
    const merged = mergeSessions([managed], [RUN])
    renderSurface({
      resolution: {
        ...resolution,
        session: merged[0],
        live: managed,
        runs: [RUN],
      },
    })
    const box = await screen.findByTestId('work-composer')
    expect(box).toHaveAttribute('data-attached', 'true')
    // The field is addressed to the open session, by the name the header paints.
    expect(screen.getByTestId('launcher-input')).toHaveAttribute(
      'placeholder',
      'Message nightly…',
    )
    expect(screen.queryByTestId('composer-session-state')).toBeNull()
  })

  it('offers every raw frame of the open session in a terminal', async () => {
    const user = userEvent.setup()
    const managed = live({ attribution: 'managed' })
    const merged = mergeSessions([managed], [RUN])
    renderSurface({
      resolution: {
        ...resolution,
        session: merged[0],
        live: managed,
        runs: [RUN],
      },
    })
    await user.click(await screen.findByTestId('narrative-menu'))
    await user.click(
      await screen.findByRole('menuitem', { name: 'In a terminal' }),
    )
    expect(await screen.findByTestId('narrative-cli')).toHaveTextContent(
      'olivares session follow run-a -o json',
    )
  })

  // 26.10.1 review, measured again before this fix: an empty estate painted one
  // empty state per pane ("No sessions yet", "Select a session") with a New session in
  // each, under the header's own. Nothing to list is one fact: one empty state, one action.
  it('an empty estate is one empty state with one action, not one per pane', async () => {
    const user = userEvent.setup()
    const onStart = vi.fn()
    renderSurface({
      sessions: [],
      address: { address: '', pane: 'rail', evidence: 'checks' },
      resolution: { ...resolution, target: null, live: undefined },
      emptyAction: <button onClick={onStart}>New session</button>,
      listHeader: <div data-testid="head">Sessions head</div>,
    })
    expect(await screen.findByText('No sessions yet')).toBeInTheDocument()
    // The list header stays: the name and the menu of the other views are still there.
    expect(screen.getByTestId('head')).toBeInTheDocument()
    expect(screen.queryByText('Select a session')).toBeNull()
    expect(screen.queryByText('No session selected')).toBeNull()
    expect(pane('narrative')).toBeNull()
    expect(pane('context')).toBeNull()
    const actions = screen.getAllByRole('button', { name: 'New session' })
    expect(actions).toHaveLength(1)
    await user.click(actions[0])
    expect(onStart).toHaveBeenCalledOnce()
  })

  it('with sessions listed and none open, the work pane offers no second New session', async () => {
    renderSurface({
      address: { address: '', pane: 'rail', evidence: 'checks' },
      resolution: { ...resolution, target: null, live: undefined },
      emptyAction: <button type="button">New session</button>,
    })
    expect(
      within(pane('narrative') as HTMLElement).getByText('Select a session'),
    ).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'New session' })).toBeNull()
    expect(screen.queryByTestId('work-composer')).toBeNull()
  })

  // The pin is the engine's, kept against a run (#2130): the card's menu reads it from the
  // list's set or from the session's own runs, and hands its own session to the toggle.
  it('the menu says Unpin for a pinned session, and Unpin acts on the open session', async () => {
    const user = userEvent.setup()
    const managed = mergeSessions(
      [live({ attribution: 'managed', run_ref: 'run-a' })],
      [RUN],
    )[0]
    expect(managed.key).toBe('live:lr-a')
    const onTogglePin = vi.fn()
    renderSurface({
      address: { address: 'live:lr-a', pane: 'narrative', evidence: 'checks' },
      resolution: { ...resolution, session: managed },
      pinned: new Set([managed.key]),
      onTogglePin,
    })
    await user.click(await screen.findByTestId('narrative-menu'))
    await user.click(await screen.findByRole('menuitem', { name: /unpin/i }))
    expect(onTogglePin).toHaveBeenCalledWith('live:lr-a', managed)
  })

  it('a session the list does not hold still says Unpin when its own run is pinned', async () => {
    // A cold deep link past the loaded page: the set is empty, the engine's answer is on the run.
    const user = userEvent.setup()
    const deepLinked = mergeSessions([], [{ ...RUN, pinned: true }])[0]
    renderSurface({
      address: {
        address: deepLinked.key,
        pane: 'narrative',
        evidence: 'checks',
      },
      resolution: { ...resolution, session: deepLinked },
      pinned: new Set(),
    })
    await user.click(await screen.findByTestId('narrative-menu'))
    expect(
      await screen.findByRole('menuitem', { name: /unpin/i }),
    ).toBeInTheDocument()
  })

  it('offers no Pin for a session with no run: the engine has nothing to pin it on', async () => {
    const user = userEvent.setup()
    renderSurface({
      address: { address: 'live:lr-a', pane: 'narrative', evidence: 'checks' },
    })
    await user.click(await screen.findByTestId('narrative-menu'))
    expect(screen.queryByRole('menuitem', { name: /pin/i })).toBeNull()
  })

  it('offers Pin for a launched session that is not pinned', async () => {
    const user = userEvent.setup()
    const launched = mergeSessions([], [RUN])[0]
    renderSurface({
      address: { address: launched.key, pane: 'narrative', evidence: 'checks' },
      resolution: { ...resolution, session: launched },
    })
    await user.click(await screen.findByTestId('narrative-menu'))
    expect(
      await screen.findByRole('menuitem', { name: /^pin/i }),
    ).toBeInTheDocument()
  })

  it('pins the beside session from the beside column, not the primary', async () => {
    // Each column hands its own session to the toggle; the beside column's menu must
    // not act on the primary session. Both runs never started (no host isolation), so
    // no composer mounts in either column and the menus stand alone.
    const user = userEvent.setup()
    const onTogglePin = vi.fn()
    const refused = (run: RunDTO): RunDTO => ({
      ...run,
      state: 'failed',
      reason:
        'the session was not started because its network boundary could not be set up: the host or container must allow unprivileged user and network namespaces and Landlock: operation not permitted',
    })
    const split = mergeSessions(
      [managedA, managedB],
      [refused(RUN), refused(RUN_B)],
    )
    const refusedA = split.find((s) => s.live?.live_ref === 'lr-a')!
    const refusedB = split.find((s) => s.live?.live_ref === 'lr-b')!
    renderSplit({
      sessions: split,
      onTogglePin,
      resolution: {
        ...resolution,
        session: refusedA,
        live: managedA,
        runs: [refused(RUN)],
      },
      besideResolution: {
        ...resolution,
        target: { liveRef: 'lr-b' },
        session: refusedB,
        live: managedB,
        runs: [refused(RUN_B)],
      },
    })
    const beside = screen.getByRole('region', { name: 'Session beside' })
    await user.click(await within(beside).findByTestId('narrative-menu'))
    // The opened menu is the portaled one this column's button opened; the payload
    // assertion below is what ties it to the beside session.
    await user.click(await screen.findByRole('menuitem', { name: /^pin/i }))
    expect(onTogglePin).toHaveBeenCalledWith('live:lr-b', refusedB)
  })
})

describe('WorkSurface — the filter field', () => {
  const many = mergeSessions(
    [
      {
        ...live({ session_ref: 'sess-a', live_ref: 'lr-a' }),
        summary: 'fix the parser',
      },
      {
        ...live({ session_ref: 'sess-b', live_ref: 'lr-b' }),
        summary: 'write the docs',
      },
      {
        ...live({ session_ref: 'sess-c', live_ref: 'lr-c' }),
        summary: 'tune the cache',
      },
      {
        ...live({ session_ref: 'sess-d', live_ref: 'lr-d' }),
        summary: 'review billing',
      },
      {
        ...live({ session_ref: 'sess-e', live_ref: 'lr-e' }),
        summary: 'update deps',
      },
      {
        ...live({ session_ref: 'sess-f', live_ref: 'lr-f' }),
        summary: 'plan the release',
      },
      {
        ...live({ session_ref: 'sess-g', live_ref: 'lr-g' }),
        summary: 'rotate the keys',
      },
    ],
    [],
  )

  it('appears under the header only when there are more than 6 sessions', async () => {
    renderSurface({ sessions: many })
    expect(await screen.findByTestId('rail-filter')).toBeInTheDocument()
    cleanup()
    renderSurface({ sessions: many.slice(0, 6) })
    await screen.findByTestId('work-rail')
    expect(screen.queryByTestId('rail-filter')).toBeNull()
  })

  it('narrows the rows by what they show, with no request', async () => {
    const user = userEvent.setup()
    renderSurface({ sessions: many })
    await user.type(await screen.findByTestId('rail-filter'), 'parser')
    const rows = within(screen.getByTestId('work-rail')).getAllByTestId(
      'rail-row',
    )
    expect(rows).toHaveLength(1)
    expect(rows[0]).toHaveTextContent('fix the parser')
  })

  it('says nothing matches, once, instead of an empty list', async () => {
    const user = userEvent.setup()
    renderSurface({ sessions: many })
    await user.type(await screen.findByTestId('rail-filter'), 'zzzz')
    expect(await screen.findByTestId('rail-no-match')).toHaveTextContent(
      'No session matches.',
    )
    expect(screen.queryByTestId('work-rail')).toBeNull()
    expect(screen.queryByText('No sessions yet')).toBeNull()
  })
})

describe('WorkSurface — Escape and the side pane', () => {
  const grid = () => document.querySelector('[data-context]') as HTMLElement

  it('closes the pane, and does not make that a choice to remember', async () => {
    const user = userEvent.setup()
    renderSurface({
      address: { address: 'live:lr-a', pane: 'context', evidence: 'checks' },
    })
    expect(grid().dataset.context).toBe('open')
    await user.keyboard('{Escape}')
    expect(grid().dataset.context).toBe('closed')
    expect(window.localStorage.getItem('olivares.sessions.sidePane')).toBeNull()
  })

  it('leaves the pane open when an open menu has the key', async () => {
    const user = userEvent.setup()
    renderSurface({
      address: { address: 'live:lr-a', pane: 'context', evidence: 'checks' },
    })
    const menu = document.createElement('div')
    menu.setAttribute('role', 'menu')
    menu.setAttribute('data-state', 'open')
    document.body.append(menu)
    await user.keyboard('{Escape}')
    expect(grid().dataset.context).toBe('open')
    menu.remove()
  })

  it('leaves the pane open when something already handled the key', async () => {
    renderSurface({
      address: { address: 'live:lr-a', pane: 'context', evidence: 'checks' },
    })
    const handled = (e: KeyboardEvent) => e.preventDefault()
    window.addEventListener('keydown', handled, { capture: true })
    fireEvent.keyDown(document.body, { key: 'Escape' })
    window.removeEventListener('keydown', handled, { capture: true })
    expect(grid().dataset.context).toBe('open')
  })
})
