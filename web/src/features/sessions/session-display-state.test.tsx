// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { railGroups } from '@/components/layout/session-rail-model'
import type { RunDTO } from '@/features/agentops/types'
import { mergeSessions } from './provenance'
import { sessionDisplayState } from './session-display-state'
import { groupOf } from './session-groups'
import { SessionStateBadge } from './session-state-badge'
import { StateDot } from './session-state-dot'
import { StateWord } from './session-state-word'
import type { LiveDTO } from './types'

describe('what a person reads off a run', () => {
  const run = (over: Partial<RunDTO>): RunDTO => ({
    run_ref: 'r-1',
    transport: 'stream-json',
    permission_mode: 'default',
    isolation: 'native',
    state: 'idle',
    last_event_seq: 0,
    pep_provisioned: true,
    record_io: false,
    critical: false,
    ...over,
  })
  const display = (over: Partial<RunDTO>) =>
    sessionDisplayState(mergeSessions([], [run(over)])[0])

  it('says Failed when the provider refused the last turn, though the run stays open', () => {
    expect(display({ turn_failed: true })).toMatchObject({
      status: 'failed',
      tone: 'failed',
      group: 'settled',
    })
  })

  // A provider limit fails nothing: a run at rest waits for the reset its limit names,
  // and its notice offers to resume then; or the engine stops it until a resume a person
  // scheduled (`resume_at`). The clock stands five minutes before the reset.
  describe('under a provider limit', () => {
    const RESET = '2026-11-10T18:05:00Z'
    const usage = { kind: 'usage', resets_at: RESET } as const
    // What an idle run with no limit reads.
    const AT_REST = {
      status: 'done',
      tone: 'idle',
      group: 'settled',
      rail: 'idle',
    } as const
    beforeEach(() => {
      vi.useFakeTimers({ toFake: ['Date'] })
      vi.setSystemTime(new Date('2026-11-10T18:00:00Z'))
    })
    afterEach(() => vi.useRealTimers())

    it.each([
      ['open at rest until its reset', { limit: usage }, 'idle'],
      [
        'stopped until its scheduled resume',
        { state: 'stopped', limit: { kind: 'usage' }, resume_at: RESET },
        'ended',
      ],
    ] as const)(
      'says Paused, in grey, for a run a limit holds %s',
      (_, patch, rail) => {
        expect(display(patch)).toMatchObject({
          status: 'paused',
          tone: 'idle',
          group: 'settled',
          rail,
        })
      },
    )

    it.each([
      ['open at rest until its reset', { limit: usage }],
      ['stopped until its resume', { state: 'stopped', resume_at: RESET }],
    ] as const)(
      'has the sidebar, the dot and the badge say Paused for a run %s',
      (_, patch) => {
        const limited = run(patch)
        const rows = railGroups({ live: [], handoffs: [], runs: [limited] }, 0)
        expect(rows.flatMap((g) => g.rows).map((r) => r.status)).toEqual([
          'paused',
        ])
        render(
          <>
            <StateDot session={mergeSessions([], [limited])[0]} />
            <SessionStateBadge run={limited} />
          </>,
        )
        expect(screen.getByRole('img', { name: 'Paused' })).toHaveAttribute(
          'data-tone',
          'idle',
        )
        expect(screen.getByText('Paused')).toBeVisible()
      },
    )

    // The engine clears a limit only when a turn succeeds, so the turn of a resumed run
    // runs with the limit still on it.
    it('says Working, and counts it working, for a run at a turn whatever its limit says', () => {
      const resumed = run({ state: 'running', limit: usage })
      const [session] = mergeSessions([], [resumed])
      expect(sessionDisplayState(session)).toMatchObject({
        status: 'working',
        tone: 'working',
        group: 'active',
        rail: 'live',
      })
      expect(groupOf(session)).toBe('active')
      const rows = railGroups({ live: [], handoffs: [], runs: [resumed] }, 0)
      expect(rows.flatMap((g) => g.rows).map((r) => r.status)).toEqual([
        'working',
      ])
      render(<SessionStateBadge run={resumed} />)
      expect(screen.getByText('Working')).toBeVisible()
    })

    it('reads a run at rest as it reads with no limit once the reset has passed', () => {
      expect(display({ limit: usage }).status).toBe('paused')
      vi.setSystemTime(new Date('2026-11-10T18:10:00Z'))
      expect(display({ limit: usage })).toMatchObject(AT_REST)
      render(<SessionStateBadge run={run({ limit: usage })} />)
      expect(screen.getByText('Idle')).toBeVisible()
    })

    // Nothing says when such a limit lifts, so the row has no reset to wait on; the
    // session's own notice still names the limit. A rate limit never names a reset.
    it.each([
      ['a usage limit', { kind: 'usage' }],
      ['a rate limit', { kind: 'rate' }],
    ] as const)(
      'reads a run at rest as it reads with no limit when %s names no reset',
      (_, limit) => {
        expect(display({ limit })).toMatchObject(AT_REST)
        render(<SessionStateBadge run={run({ limit })} />)
        expect(screen.getByText('Idle')).toBeVisible()
      },
    )

    // A resume the engine refused leaves the run stopped with the time it missed.
    it('says in the list when a paused run resumes, only while that time is ahead', () => {
      const stopped = run({ state: 'stopped', resume_at: RESET })
      const [session] = mergeSessions([], [stopped])
      const { unmount } = render(<StateWord session={session} />)
      expect(screen.getByTestId('rail-row-state')).toHaveTextContent('Paused')
      expect(screen.getByText(/^Resumes /)).toHaveAttribute('datetime', RESET)
      unmount()
      vi.setSystemTime(new Date('2026-11-10T18:10:00Z'))
      render(<StateWord session={session} />)
      expect(screen.getByTestId('rail-row-state')).toHaveTextContent('Paused')
      expect(screen.queryByText(/^Resumes /)).not.toBeInTheDocument()
    })
  })

  // Without the engine's `turn_failed` and without a limit, a run reads as it did.
  it.each([
    ['an ordinary run at rest', {}],
    ['a refused turn the engine did not mark', { turn_failed: undefined }],
  ] as const)('says Done for %s, its badge Idle', (_, patch) => {
    expect(display(patch)).toMatchObject({
      status: 'done',
      tone: 'idle',
      group: 'settled',
      rail: 'idle',
    })
    render(<SessionStateBadge run={run(patch)} />)
    expect(screen.getByText('Idle')).toBeVisible()
  })

  it('says Failed for a launch the engine failed', () => {
    expect(display({ state: 'failed' })).toMatchObject({
      status: 'failed',
      tone: 'failed',
    })
    render(<SessionStateBadge run={run({ state: 'failed' })} />)
    expect(screen.getByText('Failed')).toBeVisible()
  })

  it('has the run badge say Failed too, as Overview and the thread show it', () => {
    render(<SessionStateBadge run={run({ turn_failed: true })} />)
    expect(screen.getByText(/^failed$/i)).toBeVisible()
  })

  it('says Working once a new turn runs after a failed one', () => {
    expect(display({ state: 'running', turn_failed: true })).toMatchObject({
      status: 'working',
      tone: 'working',
    })
  })

  it('lets a request for a person outrank a failed turn', () => {
    expect(
      display({ turn_failed: true, provider_auth_state: 'required' }),
    ).toMatchObject({ status: 'needs-you', group: 'attention' })
  })

  it('counts a run still starting as working in the rail, as the list does', () => {
    expect(display({ state: 'pending' })).toMatchObject({
      rail: 'live',
      group: 'active',
    })
  })
})

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
