// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { RunDTO } from '@/features/agentops/types'
import { mergeSessions } from './provenance'
import { StateDot } from './session-state-dot'
import '@/features/agentops/i18n'

describe('session StateDot shares the chip system and preserves the engine state word', () => {
  it.each([
    ['running', 'bg-ok', 'working'],
    ['failed', 'bg-bad', 'failed'],
    ['waiting_approval', 'bg-warn', 'needs'],
    ['stopped', 'bg-text-3', 'idle'],
  ] as const)('%s', (state, color, tone) => {
    const run: RunDTO = {
      run_ref: 'run-chip',
      transport: 'stream-json',
      permission_mode: 'default',
      isolation: 'native',
      state,
      last_event_seq: 0,
      pep_provisioned: true,
      record_io: false,
      critical: false,
    }
    const [session] = mergeSessions([], [run])
    const { container } = render(<StateDot session={session} />)
    const image = screen.getByRole('img')
    expect(image.getAttribute('aria-label')?.trim()).not.toBe('')
    expect(image).toHaveAttribute('data-tone', tone)
    expect(container.querySelector('[data-slot="status-dot"]')).toHaveClass(
      color,
    )
  })
})

// the regression: the rail reads no ledger, so a run whose row cannot say whether it finished
// or was stopped is Ended, true of both; a stop that recorded why is Stopped.
describe('StateDot words a run that ended', () => {
  const base: RunDTO = {
    run_ref: 'run-ended',
    transport: 'stream-json',
    permission_mode: 'default',
    isolation: 'native',
    state: 'stopped',
    last_event_seq: 0,
    pep_provisioned: true,
    record_io: false,
    critical: false,
    exit_code: 0,
  }
  it.each([
    ['exit 0', 'Ended'],
    ['idle_policy', 'Stopped'],
  ])('reason %s reads %s', (reason, word) => {
    const [session] = mergeSessions([], [{ ...base, reason }])
    render(<StateDot session={session!} />)
    expect(screen.getByRole('img')).toHaveAttribute('aria-label', word)
  })
})
