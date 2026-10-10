// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE FACTS THE THREAD'S HEADER NO LONGER SAYS: the working folder in plain words, who
// manages the session, its mode and what it did. A session with no conversation of its own
// paints this block as the thread's body.
import { screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderIntel } from '@/test/intel'
import type { RunDTO } from '@/features/agentops/types'
import { mergeSessions } from './provenance'
import { SessionOverview } from './session-overview'
import type { LiveDTO } from './types'
import type { SessionResolution } from './use-session-resolution'
import './i18n'
import '@/features/agentops/i18n'

const auth = vi.hoisted(() => ({
  activeTenant: 'tnt-demo' as string | null,
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

const live: LiveDTO = {
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
  duration_seconds: 8,
  run_ref: 'run-1',
  provider_profile_ref: 'ppf_team',
}

function run(over: Partial<RunDTO> = {}): RunDTO {
  return {
    run_ref: 'run-1',
    transport: 'stream-json',
    permission_mode: 'default',
    isolation: 'native',
    state: 'running',
    last_event_seq: 0,
    pep_provisioned: true,
    record_io: true,
    critical: false,
    live_ref: 'lr-a',
    provider_profile_ref: 'ppf_team',
    ...over,
  }
}

function renderOverview(r: RunDTO, frameCwd?: string | null) {
  const resolution: SessionResolution = {
    target: { liveRef: 'lr-a' },
    session: mergeSessions([live], [r])[0]!,
    live,
    runs: [r],
    related: [],
    streamStatus: 'open',
    operateUnknown: false,
    observeUnknown: false,
    loading: false,
    grants: { liveRead: true, runRead: true, runWrite: true, runAdmin: false },
  }
  renderIntel(
    <SessionOverview
      resolution={resolution}
      evidence="checks"
      onExpandEvidence={() => {}}
      frameCwd={frameCwd}
    />,
  )
}

beforeEach(() => {
  auth.can = () => true
})

describe('SessionOverview — the working folder', () => {
  it('names the session temporary folder, with the path on the hover', () => {
    renderOverview(run({ workspace_path: '/sessions/run_1' }))
    const line = screen.getByTestId('conversation-folder')
    expect(line).toHaveTextContent(
      'Working folder: temporary folder for this session',
    )
    expect(line).toHaveAttribute('title', '/sessions/run_1')
  })

  it('keeps an explicitly selected folder visible, whole', () => {
    renderOverview(
      run({ workspace_ref: 'folder-1', workspace_path: '/projects/example' }),
    )
    expect(screen.getByTestId('conversation-folder')).toHaveTextContent(
      'Working folder: /projects/example',
    )
    expect(screen.queryByText(/temporary folder/)).toBeNull()
  })

  it('falls back to the folder the tool named in its first frame', () => {
    renderOverview(run(), '/engine/dir')
    expect(screen.getByTestId('conversation-folder')).toHaveTextContent(
      'Working folder: /engine/dir',
    )
    expect(screen.queryByTestId('conversation-cwd-warning')).toBeNull()
  })

  it('says nothing when no folder is known', () => {
    renderOverview(run())
    expect(screen.queryByTestId('conversation-folder')).toBeNull()
  })
})

describe('SessionOverview — the badges', () => {
  it('says who manages the session and the mode the tool reported', () => {
    renderOverview(run({ tool_mode: 'plan', provider_driver: 'claude' }))
    expect(screen.getByText('Managed by Olivares')).toBeInTheDocument()
    expect(screen.getByTestId('narrative-mode')).toHaveTextContent('Mode: plan')
  })
})

describe('SessionOverview — what the session has spent', () => {
  it('keeps the usage in the Context pane, where the header has no room for it', () => {
    renderOverview(run({ input_tokens: 12, output_tokens: 18 }))
    expect(screen.getByTestId('context-usage')).toHaveTextContent(
      '12 in / 18 out tokens',
    )
    expect(screen.queryByTestId('narrative-usage')).toBeNull()
  })

  it('paints no usage line when the tool reported nothing', () => {
    renderOverview(run())
    expect(screen.queryByTestId('context-usage')).toBeNull()
  })
})
