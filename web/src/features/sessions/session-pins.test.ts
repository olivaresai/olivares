// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// PINS are the engine's: this module only turns the `pinned` / `pinned_at` the runs
// carry into the set the rail sorts by (#2130).
import { describe, expect, it } from 'vitest'
import type { Whoami } from '@/lib/api/types'
import {
  MAX_PINS,
  decodePins,
  encodePins,
  isPinnable,
  isPinned,
  pinStorageKey,
  pinnedKeys,
  preferenceStorageKey,
} from './session-pins'

const user = { kind: 'user', user_id: 'u-1' } as unknown as Whoami

// The model favorites still keep a browser preference per principal and tenant.
describe('preferenceStorageKey — no partition, nothing written', () => {
  it('partitions by origin, user and tenant', () => {
    const key = preferenceStorageKey(
      'x',
      'https://console.example',
      user,
      't-1',
    )
    expect(key).toContain('u-1')
    expect(key).toContain('t-1')
    expect(key).toContain('https://console.example')
  })

  it('answers a DIFFERENT key for another tenant of the same user', () => {
    expect(preferenceStorageKey('x', 'o', user, 't-1')).not.toBe(
      preferenceStorageKey('x', 'o', user, 't-2'),
    )
  })

  it.each<[string, Whoami | null, string | null]>([
    ['there is no principal', null, 't-1'],
    [
      'the principal is a TOKEN carrying its creator user_id',
      { kind: 'token', user_id: 'u-1' } as unknown as Whoami,
      't-1',
    ],
    [
      'the user id is blank',
      { kind: 'user', user_id: '  ' } as unknown as Whoami,
      't-1',
    ],
    ['no tenant is established', user, null],
  ])('refuses a key when %s', (_why, principal, tenant) => {
    expect(preferenceStorageKey('x', 'o', principal, tenant)).toBeNull()
  })
})

type Row = Parameters<typeof pinnedKeys>[0][number]
const row = (
  key: string,
  runs: { run_ref: string; pinned?: boolean; pinned_at?: string }[],
): Row => ({ key, runs: runs as unknown as Row['runs'] })

describe('pinnedKeys — the sessions this person pinned, oldest pin first', () => {
  it('lists the pinned rows in the order they were pinned, not the list order', () => {
    const keys = pinnedKeys([
      row('run:new', [
        { run_ref: 'new', pinned: true, pinned_at: '2026-10-14T10:00:09Z' },
      ]),
      row('run:mid', [{ run_ref: 'mid' }]),
      row('run:old', [
        { run_ref: 'old', pinned: true, pinned_at: '2026-10-14T10:00:05Z' },
      ]),
    ])
    expect([...keys]).toEqual(['run:old', 'run:new'])
  })

  it('ignores a run the engine says is not pinned, or says nothing about', () => {
    const keys = pinnedKeys([
      row('run:a', [{ run_ref: 'a', pinned: false }]),
      row('run:b', [{ run_ref: 'b' }]),
    ])
    expect(keys.size).toBe(0)
  })

  it('counts a session pinned when ANY of its runs is, from the earliest pin', () => {
    const keys = pinnedKeys([
      row('live:x', [
        { run_ref: 'r2', pinned: true, pinned_at: '2026-10-14T10:00:20Z' },
        { run_ref: 'r1', pinned: true, pinned_at: '2026-10-14T10:00:01Z' },
      ]),
      row('run:y', [
        { run_ref: 'y', pinned: true, pinned_at: '2026-10-14T10:00:10Z' },
      ]),
    ])
    expect([...keys]).toEqual(['live:x', 'run:y'])
  })

  it('keeps the list order for two pins with one stamp', () => {
    const at = '2026-10-14T10:00:00Z'
    const keys = pinnedKeys([
      row('run:a', [{ run_ref: 'a', pinned: true, pinned_at: at }]),
      row('run:b', [{ run_ref: 'b', pinned: true, pinned_at: at }]),
    ])
    expect([...keys]).toEqual(['run:a', 'run:b'])
  })
})

describe('isPinnable — a pin hangs on a run that is still listed', () => {
  it('is false for a session the plane only observes', () => {
    expect(isPinnable({ runs: [] })).toBe(false)
  })
  it('is true once a run drives it', () => {
    expect(
      isPinnable({ runs: [{ run_ref: 'r', state: 'running' }] as never }),
    ).toBe(true)
  })
  it('is false when every run is released', () => {
    expect(
      isPinnable({ runs: [{ run_ref: 'r', state: 'cleaned' }] as never }),
    ).toBe(false)
  })
})

// The open card can be a session the list does not hold (a deep link past the page).
describe("isPinned — the list, or the session's own runs", () => {
  const none = new Set<string>()
  it("reads the list's set", () => {
    expect(isPinned(new Set(['run:a']), row('run:a', [{ run_ref: 'a' }]))).toBe(
      true,
    )
  })
  it("reads the engine's answer on the session's own runs when the list lacks it", () => {
    expect(isPinned(none, row('run:a', [{ run_ref: 'a', pinned: true }]))).toBe(
      true,
    )
  })
  it('is false when neither says so', () => {
    expect(
      isPinned(none, row('run:a', [{ run_ref: 'a', pinned: false }])),
    ).toBe(false)
  })
})

// The pins this browser kept before the engine did (#2130) are stored data: read back
// only to be moved to the engine, and everything in them is input nobody wrote on purpose.
describe('pinStorageKey — the key the old pins live under', () => {
  it('is the key the console always wrote, per origin, user and tenant', () => {
    expect(pinStorageKey('o', user, 't-1')).toBe(
      'olivares.sessions.pins:["o","user","u-1","t-1"]',
    )
  })

  it('has no key for a token principal or without a tenant', () => {
    const token = { kind: 'token', user_id: 'u-1' } as unknown as Whoami
    expect(pinStorageKey('o', token, 't-1')).toBeNull()
    expect(pinStorageKey('o', user, null)).toBeNull()
  })
})

describe('decodePins — a stored value is untrusted input', () => {
  it('round-trips what the old console wrote, oldest first', () => {
    expect(decodePins(encodePins(['live:a', 'run:b']))).toEqual([
      'live:a',
      'run:b',
    ])
  })

  it.each([
    ['nothing stored', null],
    ['not JSON', '{oops'],
    ['another version', '{"version":2,"pins":["live:a"]}'],
    ['no pins field', '{"version":1}'],
    ['pins is not a list', '{"version":1,"pins":"live:a"}'],
  ])('reads %s as no pins', (_why, raw) => {
    expect(decodePins(raw)).toEqual([])
  })

  it('drops entries this plane could not have minted', () => {
    const raw = JSON.stringify({
      version: 1,
      pins: ['live:a', 'nonsense', 42, { id: 'x' }, 'https://evil.example'],
    })
    expect(decodePins(raw)).toEqual(['live:a'])
  })

  it('reads the exact value the old console wrote', () => {
    expect(
      decodePins('{"version":1,"pins":["live:lr-a","sess:sid-b","run:run-c"]}'),
    ).toEqual(['live:lr-a', 'sess:sid-b', 'run:run-c'])
  })

  it('drops a duplicate', () => {
    expect(decodePins(encodePins(['live:a', 'live:a', 'live:b']))).toEqual([
      'live:a',
      'live:b',
    ])
  })

  it('stops at the ceiling', () => {
    const many = Array.from({ length: MAX_PINS + 5 }, (_, i) => `live:${i}`)
    expect(decodePins(encodePins(many))).toHaveLength(MAX_PINS)
  })
})
