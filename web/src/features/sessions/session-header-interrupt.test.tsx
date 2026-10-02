// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// HU 025 (refresh 05, real Claude Code 2.1.287): `olivares session interrupt` ended a
// running 60 s command at once and the session stayed open, but the console offered
// only Stop, which ends the session. The interrupt lived in the drawer's Live tab. The
// session header now offers it beside Stop, from the same capability answer.
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
import type { RunDTO } from '@/features/agentops/types'
import { mergeSessions } from './provenance'
import type { LiveDTO } from './types'
import { SessionNarrative } from './session-narrative'
import type { SessionResolution } from './use-session-resolution'
import './i18n'
import '@/features/agentops/i18n'

const auth = vi.hoisted(() => ({
  activeTenant: 'tnt-demo' as string | null,
  principal: { user_id: 'user-a' },
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

const ops = vi.hoisted(() => ({
  interrupt: vi.fn(),
  stop: vi.fn(),
}))
vi.mock('@/features/agentops/api', async (importOriginal) => {
  const real = await importOriginal<typeof import('@/features/agentops/api')>()
  return {
    ...real,
    agentOpsApi: { ...(real.agentOpsApi as object), ...ops },
  }
})
// The attach stream has its own tests; here it is simply open.
vi.mock('@/features/agentops/attach', () => ({
  useRunAttach: () => ({
    status: 'open',
    ended: false,
    ioUnavailable: null,
    retry: () => {},
  }),
}))

function live(): LiveDTO {
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
    first_event_at: '2026-10-01T18:23:40Z',
    last_event_at: '2026-10-01T18:24:00Z',
    duration_seconds: 20,
    provider: 'claude',
    provider_profile_ref: 'ppf_claude',
    run_ref: 'run-1',
    posture: 'enforced',
    engine: 'claude',
  }
}

function run(over: Partial<RunDTO> = {}): RunDTO {
  return {
    run_ref: 'run-1',
    name: 'run: sleep 60',
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

function renderHeader(r: RunDTO, runWrite = true) {
  const l = live()
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
    grants: { liveRead: true, runRead: true, runWrite, runAdmin: false },
  }
  renderIntel(
    <SessionNarrative
      resolution={resolution}
      pinned={false}
      onTogglePin={null}
      onOpenDetail={() => {}}
      evidence="checks"
      onExpandEvidence={() => {}}
    />,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  auth.can = () => true
  ops.interrupt.mockResolvedValue({ accepted: true })
})

describe('session header — Interrupt turn beside Stop', () => {
  it('interrupts the running Claude Code turn without stopping the session', async () => {
    renderHeader(run())
    expect(screen.getByRole('button', { name: 'Stop' })).toBeInTheDocument()
    await userEvent.click(
      screen.getByRole('button', { name: 'Interrupt turn' }),
    )
    await waitFor(() =>
      expect(ops.interrupt).toHaveBeenCalledWith('run-1', undefined),
    )
    expect(ops.stop).not.toHaveBeenCalled()
  })

  it('presents the work fence of a work-bound run', async () => {
    renderHeader(run({ work_item_id: 'work-a', work_lease_fence: 7 }))
    await userEvent.click(
      screen.getByRole('button', { name: 'Interrupt turn' }),
    )
    await waitFor(() => expect(ops.interrupt).toHaveBeenCalledWith('run-1', 7))
  })

  it.each([
    ['an idle run', run({ state: 'idle' })],
    ['a relayed run', run({ transport: 'remote-control' })],
  ])('is not offered for %s', (_, r) => {
    renderHeader(r)
    expect(screen.queryByRole('button', { name: 'Interrupt turn' })).toBeNull()
  })

  it('is not offered to a person who may not write to the run', () => {
    auth.can = () => false
    renderHeader(run(), false)
    expect(screen.queryByRole('button', { name: 'Interrupt turn' })).toBeNull()
  })
})

// MC: a Grok Build or OpenCode run says the MCP servers in its own settings are not
// governed; the session view shows the engine's sentence as-is, and nothing otherwise.
describe('session header — the engine warning for ungoverned tool MCP servers', () => {
  const sentence =
    "MCP servers configured in this tool's own settings are not governed by Olivares."
  it('shows the sentence as-is when the run carries it', () => {
    renderHeader(
      run({ provider_driver: 'grok', mcp_governance_warning: sentence }),
    )
    expect(screen.getByText(sentence)).toHaveAttribute('role', 'note')
  })
  it('shows nothing when the run does not', () => {
    renderHeader(run())
    expect(screen.queryByText(/not governed by Olivares/)).toBeNull()
  })
})
