// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// #427: the session header shows this session's usage; its mode is a fact of the Context pane
// (tokens and cost) beside the plan window of the tool login it spends.
import { screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
import { useTenantStore } from '@/stores/tenant'
import type { RunDTO } from '@/features/agentops/types'
import type { ProviderSnapshot } from '@/features/agent-tools/api'
import { mergeSessions } from './provenance'
import type { LiveDTO } from './types'
import { SessionNarrative } from './session-narrative'
import { SessionOverview } from './session-overview'
import { sessionUsage } from './session-usage'
import type { SessionResolution } from './use-session-resolution'
import './i18n'
import '@/features/agentops/i18n'

const auth = vi.hoisted(() => ({
  activeTenant: 'tnt-demo' as string | null,
  principal: { user_id: 'user-a' },
  isSuperadmin: false,
  can: (_: string): boolean => true,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

vi.mock('./api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('./api')>()),
  sessionsApi: {
    timelineById: vi
      .fn()
      .mockResolvedValue({ items: [], has_more: false, cursor: '' }),
    timeline: vi
      .fn()
      .mockResolvedValue({ items: [], has_more: false, cursor: '' }),
  },
}))

const tools = vi.hoisted(() => ({ providers: vi.fn() }))
vi.mock('@/features/agent-tools/api', async (importOriginal) => {
  const real =
    await importOriginal<typeof import('@/features/agent-tools/api')>()
  return {
    ...real,
    agentToolsApi: { ...(real.agentToolsApi as object), ...tools },
  }
})
vi.mock('@/features/agentops/attach', () => ({
  useRunAttach: () => ({
    status: 'open',
    ended: false,
    ioUnavailable: null,
    retry: () => {},
  }),
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
    event_count: 1,
    tool_call_count: 1,
    first_event_at: '2026-10-07T08:00:00Z',
    last_event_at: '2026-10-07T08:00:20Z',
    duration_seconds: 20,
    provider: 'claude',
    provider_profile_ref: 'ppf_claude',
    run_ref: 'run-1',
    ...over,
  }
}

function run(over: Partial<RunDTO> = {}): RunDTO {
  return {
    run_ref: 'run-1',
    name: 'planning',
    transport: 'stream-json',
    permission_mode: 'default',
    isolation: 'native',
    state: 'running',
    last_event_seq: 3,
    pep_provisioned: true,
    record_io: true,
    critical: false,
    provider_profile_ref: 'ppf_claude',
    provider_driver: 'claude',
    live_ref: 'lr-a',
    ...over,
  }
}

function snapshot(over: Partial<ProviderSnapshot>): ProviderSnapshot {
  return {
    instance: 'claude',
    driver: 'claude',
    default: true,
    config_dir: '/x',
    state: 'ready',
    installed: true,
    limits: [],
    models: [],
    checked_at: '2026-10-07T08:00:00Z',
    source: 'claude auth status --json',
    ...over,
  }
}

function renderHeader(r: RunDTO, l: LiveDTO = live()) {
  const session = mergeSessions([l], [r])[0]!
  const resolution: SessionResolution = {
    target: { liveRef: 'lr-a' },
    session,
    live: l,
    runs: [r],
    related: [],
    streamStatus: 'open',
    operateUnknown: false,
    observeUnknown: false,
    loading: false,
    grants: { liveRead: true, runRead: true, runWrite: true, runAdmin: false },
  }
  // The thread's header paints the usage and the plan window; the Context pane's overview
  // paints the mode and the facts line. The surface shows both, so the helper does too.
  renderIntel(
    <>
      <SessionNarrative
        resolution={resolution}
        pinned={false}
        onTogglePin={null}
        onOpenDetail={() => {}}
        evidence="checks"
        onExpandEvidence={() => {}}
      />
      <SessionOverview
        resolution={resolution}
        evidence="checks"
        onExpandEvidence={() => {}}
      />
    </>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.isSuperadmin = false
  // The providers read names the organization, whose own logins are the instances.
  useTenantStore.setState({ activeTenant: 'tnt-demo' })
})

describe('session header — the tool’s own mode', () => {
  it('shows the mode the tool reported, not the one the launch asked for', () => {
    renderHeader(run({ permission_mode: 'default', tool_mode: 'plan' }))
    const mode = screen.getByTestId('narrative-mode')
    expect(mode).toHaveTextContent('Mode: plan')
    expect(mode).toHaveAttribute('title', 'The mode as Claude Code reports it')
  })

  it('shows Codex’s approval policy and sandbox as Codex said them', () => {
    renderHeader(
      run({
        provider_driver: 'codex',
        tool_mode: 'on-request · workspaceWrite',
      }),
    )
    expect(screen.getByTestId('narrative-mode')).toHaveTextContent(
      'Mode: on-request · workspaceWrite',
    )
  })

  it('shows no mode before the tool has reported one', () => {
    renderHeader(run({ permission_mode: 'plan' }))
    expect(screen.queryByTestId('narrative-mode')).toBeNull()
  })
})

describe('session header — this session’s usage', () => {
  it('shows the run’s tokens and cost once, not again in the facts line', () => {
    renderHeader(
      run({ input_tokens: 52755, output_tokens: 4, cost_micro_usd: 27430 }),
      live({ cost_micro_usd: 27430 }),
    )
    const usage = screen.getByTestId('narrative-usage')
    expect(usage).toHaveTextContent('52,755 in / 4 out tokens')
    expect(usage).toHaveTextContent('$0.027')
    expect(screen.getByTestId('narrative-facts')).not.toHaveTextContent('$')
  })

  it('never shows a cost in the facts line beside the meter, even an unknown one', () => {
    renderHeader(
      run({ input_tokens: 7855, output_tokens: 13 }),
      live({ cost_micro_usd: 4200 }),
    )
    expect(screen.getByTestId('narrative-usage')).toHaveTextContent(
      'cost not reported',
    )
    expect(screen.getByTestId('narrative-facts')).not.toHaveTextContent('$')
  })

  it('says the cost is unknown when the tool reported tokens and no money', () => {
    renderHeader(run({ input_tokens: 7855, output_tokens: 13 }))
    expect(screen.getByTestId('narrative-usage')).toHaveTextContent(
      'cost not reported',
    )
    expect(screen.getByTestId('narrative-usage')).not.toHaveTextContent('$0')
  })

  it('shows no meter when the tool reported nothing', () => {
    renderHeader(run())
    expect(screen.queryByTestId('narrative-usage')).toBeNull()
  })

  it('leaves the cost to the facts line when there is no meter', () => {
    renderHeader(run(), live({ cost_micro_usd: 4200 }))
    expect(screen.queryByTestId('narrative-usage')).toBeNull()
    expect(screen.getByTestId('narrative-facts')).toHaveTextContent('$')
  })

  it('reads a launched session from its run, an observed one from its live row', () => {
    expect(
      sessionUsage(run(), live({ input_tokens: 9, output_tokens: 1 })),
    ).toBeNull()
    expect(
      sessionUsage(null, live({ input_tokens: 9, output_tokens: 1 })),
    ).toEqual({ input: 9, output: 1, costMicroUsd: undefined })
  })
})

describe('session header — the plan window of the login it spends', () => {
  const fiveHour = {
    label: '5-hour',
    percent: 54,
    resets_at: '2026-10-07T10:00:00Z',
  }

  it('shows the windows of the run’s own instance, and no other', async () => {
    auth.isSuperadmin = true
    tools.providers.mockResolvedValue({
      providers: [
        snapshot({
          instance: 'claude',
          limits: [{ label: 'Weekly', percent: 80 }],
        }),
        snapshot({
          instance: 'claude/olivares',
          default: false,
          limits: [fiveHour],
        }),
      ],
    })
    renderHeader(run({ provider_instance: 'claude/olivares' }))
    const plan = await screen.findByTestId('narrative-plan')
    expect(within(plan).getByText(/5-hour/)).toBeInTheDocument()
    expect(plan).toHaveTextContent('54%')
    expect(plan).not.toHaveTextContent('Weekly')
    expect(tools.providers).toHaveBeenCalledWith('tnt-demo', expect.anything())
  })

  it('says the window was not read when the providers read fails', async () => {
    auth.isSuperadmin = true
    tools.providers.mockRejectedValue(new Error('engine said no'))
    renderHeader(run({ provider_instance: 'claude/olivares' }))
    expect(await screen.findByTestId('narrative-plan-error')).toHaveTextContent(
      'plan window not read',
    )
  })

  it.each([
    [
      'is not ready',
      [
        snapshot({
          instance: 'claude/olivares',
          state: 'error',
          error: 'claude auth status --json: exit 1',
        }),
      ],
    ],
    ['is not listed', [snapshot({ instance: 'claude', limits: [fiveHour] })]],
  ])(
    'says the window was not read when the instance %s',
    async (_, providers) => {
      auth.isSuperadmin = true
      tools.providers.mockResolvedValue({ providers })
      renderHeader(run({ provider_instance: 'claude/olivares' }))
      expect(
        await screen.findByTestId('narrative-plan-error'),
      ).toHaveTextContent('plan window not read')
      expect(screen.queryByTestId('narrative-plan')).toBeNull()
    },
  )

  it('marks a stale window as the last known one, with when it was read', async () => {
    auth.isSuperadmin = true
    tools.providers.mockResolvedValue({
      providers: [
        snapshot({
          instance: 'claude/olivares',
          default: false,
          limits: [fiveHour],
          stale: true,
        }),
      ],
    })
    renderHeader(run({ provider_instance: 'claude/olivares' }))
    const stale = await screen.findByText('last known')
    expect(stale).toHaveAttribute('title', expect.stringContaining('2026'))
  })

  it('does not read the providers page for a viewer who may not', async () => {
    renderHeader(run({ provider_instance: 'claude/olivares' }))
    await waitFor(() =>
      expect(screen.getByTestId('session-narrative')).toBeInTheDocument(),
    )
    expect(tools.providers).not.toHaveBeenCalled()
    expect(screen.queryByTestId('narrative-plan')).toBeNull()
  })

  it('reads nothing for a run on a key or on homes of its own', () => {
    auth.isSuperadmin = true
    renderHeader(run())
    expect(tools.providers).not.toHaveBeenCalled()
  })
})
