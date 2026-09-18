// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// PINS are a preference in the browser, and everything read back from storage is
// input nobody wrote on purpose.
import { describe, expect, it } from 'vitest'
import type { Whoami } from '@/lib/api/types'
import {
  MAX_PINS,
  decodePins,
  encodePins,
  pinStorageKey,
  togglePin,
} from './session-pins'

const user = { kind: 'user', user_id: 'u-1' } as unknown as Whoami

describe('pinStorageKey — no partition, no pinning', () => {
  it('partitions by origin, user and tenant', () => {
    const key = pinStorageKey('https://console.example', user, 't-1')
    expect(key).toContain('u-1')
    expect(key).toContain('t-1')
    expect(key).toContain('https://console.example')
  })

  it('answers a DIFFERENT key for another tenant of the same user', () => {
    expect(pinStorageKey('o', user, 't-1')).not.toBe(
      pinStorageKey('o', user, 't-2'),
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
    expect(pinStorageKey('o', principal, tenant)).toBeNull()
  })
})

describe('decodePins — a stored value is untrusted input', () => {
  it('round-trips what it wrote', () => {
    expect(decodePins(encodePins(['live:a', 'sess:b']))).toEqual([
      'live:a',
      'sess:b',
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
    // Nothing is RECONSTRUCTED from storage: a stored label, URL or object never
    // becomes an address, it is simply not one.
    const raw = JSON.stringify({
      version: 1,
      pins: ['live:a', 'nonsense', 42, { id: 'x' }, 'https://evil.example'],
    })
    expect(decodePins(raw)).toEqual(['live:a'])
  })

  it('drops duplicates and stops at the ceiling', () => {
    const many = Array.from({ length: MAX_PINS + 5 }, (_, i) => `live:${i}`)
    expect(decodePins(encodePins([...many, 'live:0']))).toHaveLength(MAX_PINS)
  })
})

describe('togglePin', () => {
  it('adds, then removes', () => {
    const one = togglePin([], 'live:a')
    expect(one).toEqual(['live:a'])
    expect(togglePin(one, 'live:a')).toEqual([])
  })

  it('refuses an address that names nothing', () => {
    expect(togglePin(['live:a'], 'nonsense')).toEqual(['live:a'])
  })

  it('drops the OLDEST pin past the ceiling, never the one just added', () => {
    const full = Array.from({ length: MAX_PINS }, (_, i) => `live:${i}`)
    const next = togglePin(full, 'live:new')
    expect(next).toHaveLength(MAX_PINS)
    expect(next).toContain('live:new')
    expect(next).not.toContain('live:0')
    expect(next[0]).toBe('live:1')
  })
})
