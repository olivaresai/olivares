// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE TWO PANES THAT READ A SESSION: the narrative and the context.
//
// What these cases are for is the distinction the product's own rule makes (§5): nothing here, a read that failed, and a permission boundary are three different
// screens. A pane is the cheapest place to collapse them into one, so each is driven.
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
import type { RunDTO } from '@/features/agentops/types'
import { mergeSessions } from './provenance'
import type { LiveDTO, TimelineDTO } from './types'
import { SessionContextPane } from './session-context-pane'
import { SessionNarrative } from './session-narrative'
import type { SessionResolution } from './use-session-resolution'
import './i18n'
import '@/features/agentops/i18n'

const auth = vi.hoisted(() => ({
  activeTenant: 'tnt-demo' as string | null,
  can: (_: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

const api = vi.hoisted(() => ({
  timelineById: vi.fn(),
  timeline: vi.fn(),
}))
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
    input_tokens: 10,
    output_tokens: 20,
    cost_micro_usd: 42_000,
    event_count: 3,
    tool_call_count: 2,
    first_event_at: '2026-09-18T09:00:00Z',
    last_event_at: '2026-09-18T09:00:15Z',
    duration_seconds: 15,
    provider: 'claude',
    provider_profile_ref: 'ppf_team',
    // MANAGED, so the run the plane launched joins this exact row — which is the
    // shape the join actually produces for a launched session (B2).
    run_ref: 'run-1',
    environment_ref: 'env-prod',
    posture: 'enforced',
    engine: 'claude',
    ...over,
  }
}

function run(over: Partial<RunDTO> = {}): RunDTO {
  return {
    run_ref: 'run-1',
    name: 'nightly-indexer',
    transport: 'stream-json',
    permission_mode: 'default',
    isolation: 'native',
    state: 'running',
    last_event_seq: 3,
    pep_provisioned: true,
    record_io: true,
    critical: false,
    workspace_ref: 'ws-main',
    provider_profile_ref: 'ppf_team',
    live_ref: 'lr-a',
    ...over,
  }
}

/** A resolution shaped exactly like the hook's, with nothing invented. */
function resolution(over: Partial<SessionResolution> = {}): SessionResolution {
  const l = over.live === undefined ? live() : over.live
  const runs = over.runs ?? []
  const merged = mergeSessions(l ? [l] : [], runs)
  return {
    target: { liveRef: 'lr-a' },
    session: merged[0] ?? {
      key: 'live:lr-a',
      liveRef: 'lr-a',
      runs: [],
      provenance: 'discovered',
      control: 'observe',
      lastActivityMs: 0,
    },
    live: l ?? undefined,
    runs,
    related: [],
    streamStatus: 'open',
    operateUnknown: false,
    observeUnknown: false,
    loading: false,
    grants: { liveRead: true, runRead: true, runWrite: true, runAdmin: false },
    ...over,
  }
}

function page(items: TimelineDTO[], hasMore = false) {
  return { items, has_more: hasMore, cursor: '' }
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.can = () => true
  api.timelineById.mockResolvedValue(page([]))
  api.timeline.mockResolvedValue(page([]))
})

function renderNarrative(over: Partial<SessionResolution> = {}, props = {}) {
  const onTogglePin = vi.fn()
  const onOpenDetail = vi.fn()
  const onExpandEvidence = vi.fn()
  renderIntel(
    <SessionNarrative
      resolution={resolution(over)}
      pinned={false}
      onTogglePin={onTogglePin}
      onOpenDetail={onOpenDetail}
      evidence="checks"
      onExpandEvidence={onExpandEvidence}
      {...props}
    />,
  )
  return { onTogglePin, onOpenDetail, onExpandEvidence }
}

describe('SessionNarrative — work told as work', () => {
  it('tells the sentence and the figures the engine sent', async () => {
    renderNarrative({ live: live({ summary: 'Filed PR #7723' }) })
    expect(await screen.findByText('Filed PR #7723')).toBeInTheDocument()
    const facts = screen.getByTestId('narrative-facts')
    expect(facts).toHaveTextContent('worked for 15.0s')
    expect(facts).toHaveTextContent('2 tool calls')
    expect(facts).toHaveTextContent('3 events')
  })

  it('says the engine reported no objective when the sentence IS the reference', async () => {
    // The bottom rung of the ladder is not a description of the work, and letting a
    // reference read like one is the quiet lie this line exists to stop.
    renderNarrative({ live: live({ session_ref: 'sess-a' }) })
    expect(
      await screen.findByText(/reported no objective/i),
    ).toBeInTheDocument()
  })

  it('does not say that when the engine DID report one', async () => {
    renderNarrative({ live: live({ goal: 'ship the release' }) })
    await screen.findByText('ship the release')
    expect(screen.queryByText(/reported no objective/i)).toBeNull()
  })

  it('separates "no telemetry yet" from "the read failed"', async () => {
    renderNarrative({ live: undefined, observeUnknown: false })
    expect(await screen.findByText(/Nothing observed/i)).toBeInTheDocument()

    renderNarrative({ live: undefined, observeUnknown: true })
    expect(await screen.findByText(/could not be read/i)).toBeInTheDocument()
  })

  it('shows a permission boundary calmly, and it is not an error', async () => {
    renderNarrative({
      grants: {
        liveRead: false,
        runRead: false,
        runWrite: false,
        runAdmin: false,
      },
    })
    const forbidden = await screen.findByText(/Sessions not authorized/i)
    expect(forbidden).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('offers the next action when nothing is open at all', async () => {
    renderNarrative(
      { target: null },
      { emptyAction: <button type="button">Start a session</button> },
    )
    expect(await screen.findByText('Select a session')).toBeInTheDocument()
    expect(
      screen.getByRole('button', { name: 'Start a session' }),
    ).toBeInTheDocument()
  })
})

describe('SessionNarrative — the menu path for the pin', () => {
  it('names the action before it happens, with the key that does the same thing', async () => {
    const user = userEvent.setup()
    const { onTogglePin } = renderNarrative()
    await user.click(await screen.findByTestId('narrative-menu'))
    const item = await screen.findByRole('menuitem', { name: /Pin/ })
    expect(item).toHaveTextContent('p')
    await user.click(item)
    expect(onTogglePin).toHaveBeenCalledWith('live:lr-a')
  })

  it('offers no menu when this principal has nowhere to store a preference', async () => {
    renderNarrative({}, { onTogglePin: null })
    await screen.findByTestId('session-narrative')
    expect(screen.queryByTestId('narrative-menu')).toBeNull()
  })

  it('keeps the full controls one explicit click away, never on selection', async () => {
    const user = userEvent.setup()
    const { onOpenDetail } = renderNarrative()
    await user.click(await screen.findByTestId('narrative-open-detail'))
    expect(onOpenDetail).toHaveBeenCalledTimes(1)
  })
})

describe('SessionEvidence — inside the narrative', () => {
  it('counts checks, turns and resources from ONE bounded read', async () => {
    api.timelineById.mockResolvedValue(
      page([
        { at: '2026-09-18T09:00:01Z', kind: 'finding', title: 'secret found' },
        {
          at: '2026-09-18T09:00:02Z',
          kind: 'tool',
          tool_ref: 'Read',
          resource_ref: 'repo/a',
        },
        {
          at: '2026-09-18T09:00:03Z',
          kind: 'mcp',
          tool_ref: 'gh/create',
          resource_ref: 'repo/a',
        },
      ]),
    )
    renderNarrative()
    const checks = await screen.findByTestId('evidence-toggle-checks')
    expect(checks).toHaveTextContent('1')
    expect(screen.getByTestId('evidence-toggle-activity')).toHaveTextContent(
      '2',
    )
    expect(screen.getByTestId('evidence-toggle-resources')).toHaveTextContent(
      '1',
    )
    expect(api.timelineById).toHaveBeenCalledTimes(1)
  })

  it('tells a full page as a FLOOR, not as a total', async () => {
    api.timelineById.mockResolvedValue(
      page(
        [{ at: '2026-09-18T09:00:01Z', kind: 'finding', title: 'one' }],
        true,
      ),
    )
    renderNarrative()
    expect(
      await screen.findByTestId('evidence-toggle-checks'),
    ).toHaveTextContent(/at least 1/)
  })

  it('opens exactly the block the URL names, and the others stay closed', async () => {
    api.timelineById.mockResolvedValue(
      page([{ at: '2026-09-18T09:00:02Z', kind: 'tool', tool_ref: 'Read' }]),
    )
    renderNarrative({}, { evidence: 'activity' })
    const activity = await screen.findByTestId('evidence-toggle-activity')
    expect(activity).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByTestId('evidence-toggle-checks')).toHaveAttribute(
      'aria-expanded',
      'false',
    )
    // `aria-controls` must always name an element that IS in the document, open or not.
    for (const id of ['checks', 'activity', 'resources'])
      expect(document.getElementById(`evidence-${id}`)).not.toBeNull()
  })

  it('reports the expansion so the caller can put it in the URL', async () => {
    const user = userEvent.setup()
    const { onExpandEvidence } = renderNarrative()
    await user.click(await screen.findByTestId('evidence-toggle-resources'))
    expect(onExpandEvidence).toHaveBeenCalledWith('resources')
  })

  it('a failed read is not an empty session', async () => {
    api.timelineById.mockRejectedValue(new Error('boom'))
    renderNarrative()
    expect(
      await screen.findByText(/timeline read did not answer/i),
    ).toBeInTheDocument()
  })

  it('a permission boundary over the evidence is calm, and asks for nothing else', async () => {
    auth.can = (p: string) => p !== 'sessions:live:read'
    renderNarrative()
    expect(
      await screen.findByText(/Evidence not authorized/i),
    ).toBeInTheDocument()
    expect(api.timelineById).not.toHaveBeenCalled()
  })
})

describe('SessionContextPane — the scope this session ran under', () => {
  it('names the five scope values it was given', async () => {
    renderIntel(
      <SessionContextPane resolution={resolution({ runs: [run()] })} />,
    )
    const pane = await screen.findByTestId('session-context')
    expect(within(pane).getByText('tnt-demo')).toBeInTheDocument()
    expect(within(pane).getByText('ws-main')).toBeInTheDocument()
    expect(within(pane).getByText('env-prod')).toBeInTheDocument()
    expect(within(pane).getByText('ppf_team')).toBeInTheDocument()
    // TWICE, and both are true: the driver the profile fixes, and the engine the
    // connector declared. They are different facts that happen to agree here, and the
    // pane prints each from its own field rather than one from the other.
    expect(within(pane).getAllByText('claude')).toHaveLength(2)
  })

  it('says NOT DECLARED and NONE rather than filling a gap from the topbar', async () => {
    // The pane must report what the session ran under, not what is selected now.
    renderIntel(
      <SessionContextPane
        resolution={resolution({
          live: live({
            provider: undefined,
            environment_ref: undefined,
            provider_profile_ref: undefined,
          }),
        })}
      />,
    )
    const pane = await screen.findByTestId('session-context')
    expect(within(pane).getAllByText('Not declared').length).toBeGreaterThan(0)
    expect(within(pane).getAllByText('None').length).toBeGreaterThan(0)
    expect(within(pane).queryByText('ws-main')).toBeNull()
  })

  it('copies a whole reference, and never a truncated one', async () => {
    // ORDER MATTERS, and it cost a failing run to find: `userEvent.setup()` installs
    // its OWN `navigator.clipboard` stub, so a double defined before it is replaced
    // and the assertion then measures user-event's clipboard instead of the chip's.
    const user = userEvent.setup()
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText },
      configurable: true,
    })
    renderIntel(
      <SessionContextPane resolution={resolution({ runs: [run()] })} />,
    )
    await user.click(
      await screen.findByRole('button', { name: /Copy ws-main/ }),
    )
    expect(writeText).toHaveBeenCalledWith('ws-main')
  })

  it('reports what the plane recorded, and does not infer it', async () => {
    renderIntel(
      <SessionContextPane
        resolution={resolution({
          runs: [run({ pep_provisioned: false, record_io: false })],
        })}
      />,
    )
    const pane = await screen.findByTestId('session-context')
    expect(within(pane).getByText(/not policed in line/i)).toBeInTheDocument()
    expect(within(pane).getByText(/not recorded/i)).toBeInTheDocument()
  })

  it('says nothing is selected rather than rendering an empty scope', async () => {
    renderIntel(
      <SessionContextPane resolution={resolution({ target: null })} />,
    )
    expect(await screen.findByText('No session selected')).toBeInTheDocument()
  })
})
