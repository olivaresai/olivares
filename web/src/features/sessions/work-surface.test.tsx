// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE THREE PANES AS A COMPOSITION. jsdom has no layout, so what is asserted here
// is the STRUCTURE the layout depends on: all three panes mounted at every width, the
// two that are not in front hidden by a class rather than unmounted, and a switcher
// that reports which one the operator chose. The widths themselves are measured in
// `web/e2e/session-work-surface.spec.ts`, against a real browser at 1600 px.
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
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

/**
 * A run the plane started and proved a managed row for. The composer can be this
 * session's input because there is a `run_ref` to post a turn to and a work-lease
 * fence to read off it; an observed row with no run has neither.
 */
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
  api.timelineById.mockResolvedValue({ items: [], has_more: false, cursor: '' })
})

function renderSurface(over: Partial<Parameters<typeof WorkSurface>[0]> = {}) {
  const onPane = vi.fn()
  const onOpen = vi.fn()
  renderIntel(
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
  return { onPane, onOpen }
}

const pane = (id: string) => document.getElementById(`work-pane-${id}`)

describe('WorkSurface', () => {
  it('mounts all three panes whatever is in front', async () => {
    renderSurface()
    await screen.findByTestId('work-rail')
    for (const id of ['rail', 'narrative', 'context'])
      expect(pane(id)).not.toBeNull()
  })

  it('hides the panes that are not in front with a CLASS, never by unmounting', async () => {
    // Unmounting would throw away the evidence read and the rail's scroll position on
    // every pane switch, and would make a URL change cost a request.
    renderSurface({
      address: { address: 'live:lr-a', pane: 'context', evidence: 'checks' },
    })
    await screen.findByTestId('session-context')
    expect(pane('context')?.className).not.toContain('hidden')
    expect(pane('rail')?.className).toContain('hidden xl:block')
    // The narrative pane restores to `flex`, not `block`: it is a column holding the
    // scrolling trace above the docked composer. Same rule — hidden by a
    // class, never unmounted — and it must restore to the display its layout needs.
    expect(pane('narrative')?.className).toContain('hidden xl:flex')
  })

  it('reports the chosen pane instead of holding it, so the URL can own it', async () => {
    const user = userEvent.setup()
    const { onPane } = renderSurface()
    await user.click(screen.getByTestId('pane-button-context'))
    expect(onPane).toHaveBeenCalledWith('context')
  })

  it('marks the pane in front, and names what each button controls', () => {
    renderSurface()
    expect(screen.getByTestId('pane-button-rail')).toHaveAttribute(
      'aria-pressed',
      'true',
    )
    expect(screen.getByTestId('pane-button-context')).toHaveAttribute(
      'aria-controls',
      'work-pane-context',
    )
    // Every `aria-controls` names an element that is in the document at every width.
    expect(pane('context')).not.toBeNull()
  })

  it('passes the open session to the rail as the selected row', async () => {
    renderSurface()
    const rail = await screen.findByTestId('work-rail')
    const selected = rail.querySelector('[aria-selected="true"]')
    expect(selected).toHaveAttribute('data-address', 'live:lr-a')
  })

  it('attaches the composer to the open session as RUNNING', async () => {
    // ⛔ ATTACHED MEANS THERE IS A RUN TO SPEAK TO, not merely that a session is open,
    //    and that is why this case carries its own fixture. The attached composer
    //    posts a turn to `run_ref` with the work-lease fence read off that run: with
    //    no run there is no fence, no route and nothing to send, so attaching would
    //    paint a Send button with nothing behind it. The shared fixture is an OBSERVED
    //    row, which by `runMatchesObserved` no run can ever join; an open session the
    //    console started is a MANAGED row with its run, and that is what this renders.
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
    expect(screen.getByTestId('composer-session-state')).toHaveTextContent(
      /Running/i,
    )
  })

  it('does not attach the composer when nothing is open', async () => {
    renderSurface({
      address: { address: '', pane: 'rail', evidence: 'checks' },
      resolution: { ...resolution, target: null, live: undefined },
    })
    const box = await screen.findByTestId('work-composer')
    expect(box).not.toHaveAttribute('data-attached')
  })
})
