// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import {
  attentionEvent,
  attentionSnapshots,
  type AttentionSnapshot,
} from './session-attention'
import type { RunDTO } from '@/features/agentops/types'
import type { LiveDTO } from '@/features/sessions/types'

const working: AttentionSnapshot = {
  key: 'sess:one',
  run: 'run-one',
  address: 'run:run-one',
  name: 'Review changes',
  state: 'working',
}
const context = { hidden: true, opened: null, now: 10_000 }
const NOW = Date.parse('2026-10-14T06:10:00Z')
const run: RunDTO = {
  run_ref: 'one',
  transport: 'stream-json',
  permission_mode: 'default',
  isolation: 'native',
  state: 'running',
  last_event_seq: 0,
  pep_provisioned: false,
  record_io: false,
  critical: false,
  claude_session_id: 'conversation',
  name: 'Review changes',
  created_at: '2026-10-14T06:00:00Z',
}

describe('session attention', () => {
  it('admits a session that now needs an approval or person', () => {
    expect(
      attentionEvent(working, { ...working, state: 'needs-you' }, context),
    ).toBe('needsYou')
  })
  it('admits a finished turn', () => {
    expect(
      attentionEvent(working, { ...working, state: 'finished' }, context),
    ).toBe('finished')
  })
  it('ignores bookkeeping, creation, attachment, streaming and quiet runs', () => {
    expect(attentionEvent(undefined, working, context)).toBeNull()
    expect(attentionEvent(working, { ...working }, context)).toBeNull()
    expect(
      attentionEvent(working, { ...working, state: 'quiet' }, context),
    ).toBeNull()
  })
  it('suppresses the on-screen session in a visible tab', () => {
    expect(
      attentionEvent(
        working,
        { ...working, state: 'needs-you' },
        {
          ...context,
          hidden: false,
          opened: working.address,
        },
      ),
    ).toBeNull()
    expect(
      attentionEvent(
        working,
        { ...working, state: 'needs-you' },
        {
          ...context,
          opened: working.address,
        },
      ),
    ).toBe('needsYou')
  })
  it('suppresses duplicate delivery within five seconds, including timestamp zero', () => {
    const next = { ...working, state: 'needs-you' } as const
    expect(
      attentionEvent(working, next, {
        ...context,
        now: 4_999,
        lastNotified: 0,
      }),
    ).toBeNull()
    expect(
      attentionEvent(working, next, {
        ...context,
        now: 5_000,
        lastNotified: 0,
      }),
    ).toBe('needsYou')
  })
  it('does not notify an ended or removed session', () => {
    expect(
      attentionEvent(working, { ...working, state: 'ended' }, context),
    ).toBeNull()
    expect(attentionEvent(working, undefined, context)).toBeNull()
  })
  it('ignores an old run replaced by a newer run', () => {
    expect(
      attentionEvent(
        working,
        { ...working, run: 'run-two', state: 'finished' },
        context,
      ),
    ).toBeNull()
  })
  it('does not replay an attention state', () => {
    const need = { ...working, state: 'needs-you' } as const
    expect(attentionEvent(undefined, need, context)).toBeNull()
    expect(attentionEvent(need, need, context)).toBeNull()
  })
  it('reads approvals from the rail sources and ignores a stale login fact after the run ended', () => {
    const snapshot = (r: RunDTO) =>
      attentionSnapshots({ live: [], handoffs: [], runs: [r] }, NOW)[0]
    expect(snapshot({ ...run, pending_approval_ref: 'approval' }).state).toBe(
      'needs-you',
    )
    expect(
      snapshot({ ...run, state: 'stopped', provider_auth_state: 'required' })
        .state,
    ).toBe('ended')
    expect(snapshot({ ...run, state: 'idle' }).state).toBe('quiet')
  })
  it('reads a turn that ended from an idle run active within the window, not from silence', () => {
    const snapshot = (r: RunDTO) =>
      attentionSnapshots({ live: [], handoffs: [], runs: [r] }, NOW)[0]
    const before = snapshot(run)
    const ended = snapshot({
      ...run,
      state: 'idle',
      last_activity_at: '2026-10-14T06:09:30Z',
    })
    expect(ended.state).toBe('finished')
    expect(attentionEvent(before, ended, context)).toBe('finished')
    const silent = snapshot({
      ...run,
      state: 'idle',
      last_activity_at: '2026-10-14T06:04:00Z',
    })
    expect(silent.state).toBe('quiet')
    expect(attentionEvent(before, silent, context)).toBeNull()
  })
  it('uses the newest run and recognizes a resume as a new launch', () => {
    const sources = {
      live: [],
      handoffs: [],
      runs: [
        { ...run, state: 'waiting_approval' },
        { ...run, run_ref: 'two', created_at: '2026-10-14T06:01:00Z' },
      ],
    }
    expect(attentionSnapshots(sources, NOW)).toHaveLength(1)
    expect(attentionSnapshots(sources, NOW)[0].state).toBe('working')
    const before = attentionSnapshots(
      {
        live: [],
        handoffs: [],
        runs: [run],
      },
      NOW,
    )[0]
    const resumed = attentionSnapshots(
      {
        live: [],
        handoffs: [],
        runs: [
          {
            ...run,
            state: 'waiting_approval',
            started_at: '2026-10-14T06:02:00Z',
          },
        ],
      },
      NOW,
    )[0]
    expect(attentionEvent(before, resumed, context)).toBeNull()
    const newest = attentionSnapshots(
      {
        live: [],
        handoffs: [],
        runs: [
          {
            ...run,
            run_ref: 'two',
            state: 'stopped',
            created_at: '2026-10-14T06:01:00Z',
          },
          {
            ...run,
            state: 'waiting_approval',
            started_at: '2026-10-14T06:02:00Z',
          },
        ],
      },
      NOW,
    )[0]
    expect(newest.state).toBe('needs-you')
    expect(newest.run).toBe('one:2026-10-14T06:02:00Z')
  })
  it('finishes only a session that was working', () => {
    for (const from of ['quiet', 'needs-you'] as const)
      expect(
        attentionEvent(
          { ...working, state: from },
          { ...working, state: 'finished' },
          context,
        ),
      ).toBeNull()
  })
  it('reads a session seen only live: silence needs a person, an ended one never does', () => {
    const liveOnly = (over: Partial<LiveDTO>): AttentionSnapshot =>
      attentionSnapshots(
        {
          live: [
            {
              session_ref: 'seen',
              live_ref: 'lr-seen',
              attribution: 'legacy',
              cc_state: 'active',
              input_tokens: 0,
              output_tokens: 0,
              cost_micro_usd: 0,
              event_count: 0,
              tool_call_count: 0,
              first_event_at: '2026-10-14T06:00:00Z',
              last_event_at: '2026-10-14T06:05:00Z',
              duration_seconds: 300,
              ...over,
            },
          ],
          handoffs: [],
          runs: [],
        },
        NOW,
      )[0]
    const active = liveOnly({})
    expect(active.state).toBe('working')
    const silent = liveOnly({ cc_state: 'silent_evasion' })
    expect(silent.state).toBe('needs-you')
    expect(attentionEvent(active, silent, context)).toBe('needsYou')
    expect(liveOnly({ cc_state: 'ended', unclaimed: true }).state).toBe('ended')
  })
  it('names an unnamed session by its tool, never by its raw key or its prompt (#2266)', () => {
    const seen = (over: Partial<LiveDTO>): AttentionSnapshot =>
      attentionSnapshots(
        {
          live: [
            {
              // The shape an observed OTLP session id has: no type prefix, so the
              // whole id is the tail (`Claude Code obs-00-4fc8`).
              session_ref: 'obs-00-4fc8',
              live_ref: 'lr-obs-00-4fc8',
              attribution: 'observed',
              cc_state: 'active',
              input_tokens: 0,
              output_tokens: 0,
              cost_micro_usd: 0,
              event_count: 0,
              tool_call_count: 0,
              first_event_at: '2026-10-14T06:00:00Z',
              last_event_at: '2026-10-14T06:05:00Z',
              duration_seconds: 300,
              ...over,
            },
          ],
          handoffs: [],
          runs: [],
        },
        NOW,
      )[0]
    // Tool known, nothing typed: the tool and the row's own tail, so two observed
    // sessions of one tool are two notifications, not one key and not one word.
    const tool = seen({ engine: 'claude' })
    expect(tool.name).toBe('Claude Code obs-00-4fc8')
    expect(tool.name).not.toBe(tool.key)
    // A summary or a goal is text the session itself wrote (a prompt, often), and a
    // notification title is painted outside the console: the list may show it, an OS
    // banner on a locked screen must not.
    for (const prompt of [
      { summary: 'Rotate the production database password' },
      { goal: 'Rotate the production database password' },
    ]) {
      const named = seen({ engine: 'claude', ...prompt }).name
      expect(named).toBe('Claude Code obs-00-4fc8')
      expect(named).not.toMatch(/password/i)
    }
    // The operator's launch name still wins over everything.
    expect(
      attentionSnapshots(
        {
          live: [],
          handoffs: [],
          runs: [{ ...run, provider_driver: 'claude' }],
        },
        NOW,
      )[0].name,
    ).toBe('Review changes')
    // A launched run nobody named: its tool and the folder it works in, then its tail.
    // The folder is a directory name the operator chose, never the prompt.
    const launched = (over: Partial<RunDTO>) =>
      attentionSnapshots(
        {
          live: [],
          handoffs: [],
          runs: [
            {
              ...run,
              name: undefined,
              workspace_path: '/srv/work/billing-api',
              ...over,
            },
          ],
        },
        NOW,
      )[0].name
    expect(launched({ provider_driver: 'claude' })).toMatch(
      /^Claude Code · billing-api \S+$/,
    )
    // No tool declared: the folder alone, still with the tail. The folder Olivares made for
    // the run (named by its id) is not a name, so that run falls to the key.
    expect(launched({})).toMatch(/^billing-api \S+$/)
    expect(launched({ workspace_path: '/var/run/one' })).toBe(
      attentionSnapshots(
        { live: [], handoffs: [], runs: [{ ...run, name: undefined }] },
        NOW,
      )[0].key,
    )
    // Nothing known at all: the key, which is what the notification used to say.
    const unknown = seen({})
    expect(unknown.name).toBe(unknown.key)
  })
  it('keeps the three-minute boundary and ignores an unreadable activity time', () => {
    const snapshot = (activity?: string) =>
      attentionSnapshots(
        {
          live: [],
          handoffs: [],
          runs: [{ ...run, state: 'idle', last_activity_at: activity }],
        },
        NOW,
      )[0].state
    expect(snapshot('2026-10-14T06:07:01Z')).toBe('finished')
    expect(snapshot('2026-10-14T06:07:00Z')).toBe('quiet')
    expect(snapshot('not a time')).toBe('quiet')
    expect(snapshot(undefined)).toBe('quiet')
  })
})
