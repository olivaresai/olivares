// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// RECENT WORK TELLS A WAITING SESSION AS IDLE, AND NAMES THE MODEL THE RUN USED (#1785).
import type { ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { renderIntel, screen, within } from '@/test/intel'
import type { RunDTO } from '@/features/agentops/types'
import type { LiveDTO } from '@/features/sessions/types'
import { mergeSessions } from '@/features/sessions/provenance'
import { RecentWork } from './recent-work'

import './i18n'
import '@/features/sessions/i18n'

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({
    children,
    to,
    search,
    ...rest
  }: {
    children: ReactNode
    to: string
    search?: Record<string, string>
  } & Record<string, unknown>) => {
    const query = new URLSearchParams(search ?? {}).toString()
    return (
      <a href={query ? `${to}?${query}` : to} {...rest}>
        {children}
      </a>
    )
  },
}))

function live(over: Partial<LiveDTO> = {}): LiveDTO {
  return {
    session_ref: 'sess-1',
    live_ref: 'lr-1',
    attribution: 'legacy',
    cc_state: 'active',
    input_tokens: 10,
    output_tokens: 10,
    cost_micro_usd: 0,
    event_count: 2,
    tool_call_count: 0,
    first_event_at: '2026-10-12T09:00:00Z',
    last_event_at: '2026-10-12T09:01:00Z',
    duration_seconds: 60,
    ...over,
  }
}

function run(over: Partial<RunDTO> = {}): RunDTO {
  return {
    run_ref: 'run-1',
    name: 'Check the build',
    transport: 'stream-json',
    permission_mode: 'default',
    isolation: 'native',
    state: 'running',
    last_event_seq: 1,
    pep_provisioned: true,
    record_io: false,
    critical: false,
    claude_session_id: 'sess-1',
    ...over,
  }
}

/** One launched session: a run joins the legacy row that carries its `claude_session_id`. */
function row(l: Partial<LiveDTO>, r: Partial<RunDTO>) {
  return mergeSessions([live(l)], [run(r)])
}

describe('Recent work — a session waiting for input', () => {
  it('reads Idle, and a session answering reads Working', () => {
    renderIntel(
      <RecentWork
        sessions={[
          ...row({}, { state: 'idle' }),
          ...row(
            { session_ref: 'sess-2', live_ref: 'lr-2' },
            { run_ref: 'run-2', claude_session_id: 'sess-2', state: 'running' },
          ),
        ]}
        state="ready"
      />,
    )
    const rows = screen.getAllByTestId('home-recent-row')
    expect(within(rows[0]).getByText('Idle')).toBeInTheDocument()
    expect(within(rows[0]).queryByText('Working')).toBeNull()
    expect(within(rows[1]).getByText('Working')).toBeInTheDocument()
  })
})

describe('Recent work — the model', () => {
  it("is the run's, not the model of the last cost sample", () => {
    renderIntel(
      <RecentWork
        sessions={row(
          { model_ref: 'claude-sonnet-5-5' },
          { model_ref: 'claude-haiku-5-5' },
        )}
        state="ready"
      />,
    )
    const item = screen.getByTestId('home-recent-row')
    expect(within(item).getByText(/Claude Haiku 5\.5/)).toBeInTheDocument()
    expect(within(item).queryByText(/Claude Sonnet 5\.5/)).toBeNull()
  })

  it("falls back to the provider's model, then to the observed one", () => {
    const { unmount } = renderIntel(
      <RecentWork
        sessions={row(
          { model_ref: 'claude-sonnet-5-5' },
          { usage_model_ref: 'claude-haiku-5-5' },
        )}
        state="ready"
      />,
    )
    expect(screen.getByText(/Claude Haiku 5\.5/)).toBeInTheDocument()
    unmount()
    renderIntel(
      <RecentWork
        sessions={mergeSessions([live({ model_ref: 'claude-sonnet-5-5' })], [])}
        state="ready"
      />,
    )
    expect(screen.getByText(/Claude Sonnet 5\.5/)).toBeInTheDocument()
  })
})
