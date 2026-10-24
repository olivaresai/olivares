// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { RunStateBadge } from './run-state-badge'
import { runStateWord } from './run-state-word'
import type { RunEventDTO } from './types'
import './i18n'

// A status is a solid dot plus a word (C2-DESIGN §3): the dot keeps the color the
// word names, and nothing pulses.
describe('RunStateBadge', () => {
  it.each([
    ['pending', /pending/i],
    ['running', /working/i],
  ] as const)(
    '%s is the word beside a solid dot, nothing pulses',
    (state, word) => {
      const { container } = render(<RunStateBadge run={{ state }} />)
      expect(screen.getByText(word)).toBeInTheDocument()
      const dot = container.querySelector('[aria-hidden]') as HTMLElement
      expect(dot).toHaveClass('size-1.5', 'rounded-full')
      expect(container.querySelector('.animate-pulse-live')).toBeNull()
      expect(dot.className).not.toMatch(/animate|pulse/i)
    },
  )

  it('keeps the color the word carries', () => {
    const pending = render(
      <RunStateBadge run={{ state: 'pending' }} />,
    ).container
    const running = render(
      <RunStateBadge run={{ state: 'running' }} />,
    ).container
    expect(pending.querySelector('[aria-hidden]')).toHaveClass('bg-warning')
    expect(running.querySelector('[aria-hidden]')).toHaveClass('bg-success')
  })

  // dev#3120: Recent work called every finished run "Stopped", which reads as cancelled.
  // The engine writes the same row for a tool that ended on its own and for a person's
  // Stop the tool obeyed cleanly (r08b-rows.fixture.ts): the run's ledger tells them apart.
  const ended = { state: 'stopped', exit_code: 0, reason: 'exit 0' } as const
  const ev = (seq: number, event: string, to_state?: string): RunEventDTO => ({
    seq,
    event,
    at: '',
    payload_hash: '',
    audit_seq: seq,
    ...(to_state ? { to_state } : {}),
  })

  it('says Done when the ledger shows no stop was asked since the run last started', () => {
    const { container } = render(
      <RunStateBadge
        run={ended}
        ledger={[ev(1, 'launched', 'running'), ev(2, 'stopped', 'stopped')]}
      />,
    )
    expect(container).toHaveTextContent('Done')
    expect(container.querySelector('[aria-hidden]')).toHaveClass('bg-success')
  })

  it('says Stopped when someone asked for the stop, even when the tool exited 0', () => {
    expect(
      render(
        <RunStateBadge
          run={ended}
          ledger={[
            ev(1, 'launched', 'running'),
            ev(2, 'stopping'),
            ev(3, 'stopped', 'stopped'),
          ]}
        />,
      ).container,
    ).toHaveTextContent('Stopped')
  })

  it('reads a stop asked before a resume as history, not as this ending', () => {
    expect(
      runStateWord(ended, [
        ev(1, 'launched', 'running'),
        ev(2, 'stopping'),
        ev(3, 'stopped', 'stopped'),
        ev(4, 'resumed', 'running'),
        ev(5, 'stopped', 'stopped'),
      ]),
    ).toBe('done')
  })

  it('says Ended, true of both, while the ledger is not read', () => {
    expect(render(<RunStateBadge run={ended} />).container).toHaveTextContent(
      'Ended',
    )
  })

  it('says Stopped from the row alone for a stop that recorded why', () => {
    for (const reason of ['idle_policy', 'stopped to hand it off', 'exit 143'])
      expect(runStateWord({ ...ended, reason })).toBe('stopped')
    expect(runStateWord({ ...ended, exit_code: 143, reason: 'exit 143' })).toBe(
      'stopped',
    )
    expect(
      runStateWord({ state: 'failed', exit_code: 1, reason: 'exit 1' }),
    ).toBe('failed')
  })
})
