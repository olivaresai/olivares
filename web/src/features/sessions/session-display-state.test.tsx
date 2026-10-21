// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { railGroups } from '@/components/layout/session-rail-model'
import type { RunDTO } from '@/features/agentops/types'
import { mergeSessions } from './provenance'
import { sessionDisplayState } from './session-display-state'
import { groupOf } from './session-groups'
import { SessionStateBadge } from './session-state-badge'
import { StateDot } from './session-state-dot'
import type { LiveDTO } from './types'

describe('the rail, list and header read the same engine state', () => {
  it.each([
    ['running', 'live', 'active', 'working'],
    ['idle', 'idle', 'settled', 'idle'],
    ['stopped', 'ended', 'settled', 'idle'],
    ['failed', 'ended', 'settled', 'failed'],
  ] as const)(
    '%s agrees across classifiers and rendered words',
    (state, rail, group, tone) => {
      const run: RunDTO = {
        run_ref: 'shared-state',
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
      expect(sessionDisplayState(session)).toMatchObject({
        rail,
        group,
        tone,
        runState: state,
      })
      expect(groupOf(session)).toBe(group)
      expect(
        railGroups({ live: [], handoffs: [], runs: [run] }, 0).flatMap(
          (g) => g.rows,
        )[0].state,
      ).toBe(rail)
      render(
        <>
          <StateDot session={session} />
          <SessionStateBadge run={run} />
        </>,
      )
      const header = screen.getByRole('img')
      expect(header).toHaveAttribute('data-tone', tone)
      expect(screen.getByText(header.getAttribute('aria-label')!)).toBeVisible()
    },
  )

  it.each([
    ['waits for an approval', { state: 'waiting_approval' }],
    [
      'needs a provider login',
      { state: 'running', provider_auth_state: 'required' },
    ],
  ] as const)('a run that %s asks for a person everywhere', (_, patch) => {
    const run: RunDTO = {
      run_ref: 'needs-a-person',
      transport: 'stream-json',
      permission_mode: 'default',
      isolation: 'native',
      last_event_seq: 0,
      pep_provisioned: true,
      record_io: false,
      critical: false,
      ...patch,
    }
    const [session] = mergeSessions([], [run])
    expect(sessionDisplayState(session)).toMatchObject({
      rail: 'need',
      group: 'attention',
      tone: 'needs',
    })
    expect(groupOf(session)).toBe('attention')
    expect(
      railGroups({ live: [], handoffs: [], runs: [run] }, 0).flatMap(
        (g) => g.rows,
      )[0].state,
    ).toBe('need')
  })

  it.each([
    ['active', {}, 'live', 'active', 'working'],
    ['idle', {}, 'idle', 'settled', 'idle'],
    ['ended', {}, 'ended', 'settled', 'idle'],
    ['silent_evasion', {}, 'need', 'attention', 'needs'],
    ['active', { unclaimed: true }, 'need', 'attention', 'needs'],
  ] as const)(
    'an observed %s session %j classifies as %s/%s/%s',
    (cc_state, patch, rail, group, tone) => {
      const live = {
        session_ref: 'observed-only',
        cc_state,
        input_tokens: 0,
        output_tokens: 0,
        cost_micro_usd: 0,
        event_count: 0,
        tool_call_count: 0,
        first_event_at: '',
        last_event_at: '',
        duration_seconds: 0,
        ...patch,
      } as LiveDTO
      const [session] = mergeSessions([live], [])
      expect(sessionDisplayState(session)).toMatchObject({
        rail,
        group,
        tone,
        observedState: cc_state,
      })
      expect(groupOf(session)).toBe(group)
    },
  )
})
