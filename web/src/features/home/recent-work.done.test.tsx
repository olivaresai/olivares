// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// the regression: Recent work called every finished run "Stopped". A row reads its run's ledger
// and says Done for a tool that ended on its own, Stopped for a stop someone asked for.
import type { ReactNode } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { renderIntel, screen } from '@/test/intel'
import type { RunDTO, RunEventDTO } from '@/features/agentops/types'
import { mergeSessions } from '@/features/sessions/provenance'
import { RecentWork } from './recent-work'
import './i18n'
import '@/features/sessions/i18n'
import '@/features/agentops/i18n'

vi.mock('@tanstack/react-router', () => ({
  useRouterState: () => '',
  Link: ({ children, to }: { children: ReactNode; to: string }) => (
    <a href={to}>{children}</a>
  ),
}))

const runEvents = vi.hoisted(() => vi.fn())
vi.mock('@/features/agentops/api', async (orig) => {
  const real = await orig<typeof import('@/features/agentops/api')>()
  return { ...real, agentOpsApi: { ...real.agentOpsApi, runEvents } }
})

const ended = {
  run_ref: 'run-ended',
  name: 'release-notes',
  state: 'stopped',
  exit_code: 0,
  reason: 'exit 0',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  last_event_seq: 3,
  started_at: '2026-10-23T10:00:00Z',
  stopped_at: '2026-10-23T10:05:00Z',
} as unknown as RunDTO

const ev = (seq: number, event: string, to_state?: string): RunEventDTO => ({
  seq,
  event,
  at: '',
  payload_hash: '',
  audit_seq: seq,
  ...(to_state ? { to_state } : {}),
})

function show(ledger: RunEventDTO[]) {
  runEvents.mockResolvedValue({ items: ledger, has_more: false })
  return renderIntel(
    <RecentWork sessions={mergeSessions([], [ended])} state="ready" />,
  )
}

describe('Recent work says Done or Stopped from the run ledger', () => {
  it('says Done for a tool that ended on its own', async () => {
    show([ev(1, 'launched', 'running'), ev(2, 'stopped', 'stopped')])
    expect(await screen.findByText('Done')).toBeInTheDocument()
    expect(screen.queryByText('Stopped')).toBeNull()
    expect(runEvents).toHaveBeenCalledWith('run-ended', { limit: 200 })
  })

  it('says Stopped for a stop someone asked for', async () => {
    show([
      ev(1, 'launched', 'running'),
      ev(2, 'stopping'),
      ev(3, 'stopped', 'stopped'),
    ])
    expect(await screen.findByText('Stopped')).toBeInTheDocument()
    expect(screen.queryByText('Done')).toBeNull()
  })
})
