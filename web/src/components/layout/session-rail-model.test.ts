// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, expect, it } from 'vitest'
import { railGroups } from './session-rail-model'

describe('operated runs in the rail', () => {
  it('lists a launched run by its name, in the group its state says', () => {
    const now = Date.parse('2026-10-01T11:00:00Z')
    const groups = railGroups(
      {
        live: [],
        handoffs: [],
        runs: [
          {
            run_ref: 'r1',
            name: 'What is in this folder?',
            state: 'running',
            provider_driver: '',
            started_at: '2026-10-01T10:58:00Z',
          } as never,
          {
            run_ref: 'r2',
            name: 'Codex in app',
            state: 'waiting_approval',
            provider_driver: 'codex',
          } as never,
          {
            run_ref: 'r3',
            name: '',
            state: 'stopped',
            provider_driver: 'codex',
          } as never,
        ],
      },
      now,
    )
    const [needs, working, earlier] = groups
    expect(working.rows[0]).toMatchObject({
      title: 'What is in this folder?',
      meta: 'claude',
      minutes: 2,
      href: '/sessions?session=run%3Ar1',
    })
    expect(needs.rows[0]).toMatchObject({
      title: 'Codex in app',
      meta: 'codex',
    })
    expect(earlier.rows[0]).toMatchObject({ title: null, reference: 'r3' })
  })

  it('a running session whose tool call waits for an approval needs you, and opens the request (HU-R12)', () => {
    const now = Date.parse('2026-10-01T11:00:00Z')
    const [needs, working] = railGroups(
      {
        live: [],
        handoffs: [],
        runs: [
          {
            run_ref: 'r9',
            name: 'Fix the build',
            state: 'running',
            provider_driver: 'claude',
            pending_approval_ref: 'apr_7',
          } as never,
        ],
      },
      now,
    )
    expect(working.rows).toHaveLength(0)
    expect(needs.rows[0]).toMatchObject({
      title: 'Fix the build',
      state: 'need',
      to: '/permissions',
      href: '/permissions?tab=approvals&approval=apr_7',
    })
  })

  it('a launch held for its approval needs you, and opens that approval', () => {
    const [needs] = railGroups(
      {
        live: [],
        handoffs: [],
        runs: [
          {
            run_ref: 'r6',
            name: 'Writes a classified folder',
            state: 'waiting_approval',
            provider_driver: 'codex',
            approval_ref: 'apr_launch',
          } as never,
        ],
      },
      Date.parse('2026-10-01T11:00:00Z'),
    )
    expect(needs.rows[0]).toMatchObject({
      title: 'Writes a classified folder',
      state: 'need',
      to: '/permissions',
      href: '/permissions?tab=approvals&approval=apr_launch',
    })
  })

  it('a running run whose launch was approved opens the session, not the old approval', () => {
    const [, working] = railGroups(
      {
        live: [],
        handoffs: [],
        runs: [
          {
            run_ref: 'r5',
            name: 'Approved',
            state: 'running',
            approval_ref: 'apr_launch',
          } as never,
        ],
      },
      Date.parse('2026-10-01T11:00:00Z'),
    )
    expect(working.rows[0]).toMatchObject({
      title: 'Approved',
      to: '/sessions',
      href: '/sessions?session=run%3Ar5',
    })
  })

  it('a run whose provider needs a login needs you, as Sessions files it', () => {
    const [needs, working] = railGroups(
      {
        live: [],
        handoffs: [],
        runs: [
          {
            run_ref: 'r7',
            name: 'Needs a login',
            state: 'running',
            provider_driver: 'claude',
            provider_auth_state: 'required',
          } as never,
        ],
      },
      Date.parse('2026-10-01T11:00:00Z'),
    )
    expect(working.rows).toHaveLength(0)
    expect(needs.rows[0]).toMatchObject({
      title: 'Needs a login',
      state: 'need',
    })
  })

  it('a stopped run with a stale approval reference is not waiting', () => {
    const [needs, , earlier] = railGroups(
      {
        live: [],
        handoffs: [],
        runs: [
          {
            run_ref: 'r8',
            name: 'Old',
            state: 'stopped',
            pending_approval_ref: 'apr_1',
          } as never,
        ],
      },
      Date.parse('2026-10-01T11:00:00Z'),
    )
    expect(needs.rows).toHaveLength(0)
    expect(earlier.rows[0]).toMatchObject({ title: 'Old', to: '/sessions' })
  })

  it('two sessions with the same name are told apart by their tail (HU 029)', () => {
    const name =
      'Read the repository and write a short summary of what each package'
    const [, working] = railGroups(
      {
        live: [],
        handoffs: [],
        runs: [
          {
            run_ref: '01a0f8bd-0000-7000-8000-00000000aaa1',
            name,
            state: 'running',
          } as never,
          {
            run_ref: '01a0f8bd-0000-7000-8000-00000000bbb2',
            name,
            state: 'running',
          } as never,
          {
            run_ref: '01a0f8bd-0000-7000-8000-00000000ccc3',
            name: 'Something else',
            state: 'running',
          } as never,
        ],
      },
      Date.parse('2026-10-01T11:00:00Z'),
    )
    const titles = working.rows.map((r) => r.title)
    expect(new Set(titles).size).toBe(3)
    expect(titles).toContain('Something else')
    expect(titles.filter((t) => t?.startsWith(name))).toHaveLength(2)
  })
})
