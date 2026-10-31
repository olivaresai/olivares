// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The models a person starred are a browser preference kept per (origin, user, tenant):
// never carried to another user or tenant, never read back as anything but a short list of
// strings, and not offered at all where there is no user to keep them for.
import { preferenceStorage } from '@/lib/preference-storage'
import { act, renderHook } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const auth = vi.hoisted(() => ({
  principal: { kind: 'user', user_id: 'u1' } as Record<string, unknown> | null,
  activeTenant: 't1' as string | null,
}))
vi.mock('@/lib/auth/context', () => ({ useAuth: () => auth }))

import {
  decodeFavorites,
  MAX_FAVORITES,
  toggleFavorite,
  useModelFavorites,
} from './model-favorites'

const blob = (favorites: unknown, version: unknown = 1) =>
  JSON.stringify({ version, favorites })

beforeEach(() => {
  preferenceStorage.clear()
  auth.principal = { kind: 'user', user_id: 'u1' }
  auth.activeTenant = 't1'
})

describe('decodeFavorites', () => {
  it('reads what it wrote', () => {
    expect(decodeFavorites(blob(['claude:a', 'codex:b']))).toEqual([
      'claude:a',
      'codex:b',
    ])
  })

  it.each([
    ['nothing stored', null],
    ['not JSON', '{'],
    ['another version', blob(['claude:a'], 2)],
    ['no list', JSON.stringify({ version: 1 })],
    ['a list that is not one', blob('claude:a')],
  ])('reads %s as no favorites', (_name, raw) => {
    expect(decodeFavorites(raw)).toEqual([])
  })

  it('keeps only distinct non-empty strings, and no more than the ceiling', () => {
    expect(decodeFavorites(blob(['a', 'a', '', 7, null, 'b']))).toEqual([
      'a',
      'b',
    ])
    const many = Array.from({ length: MAX_FAVORITES + 5 }, (_, i) => `m${i}`)
    expect(decodeFavorites(blob(many))).toHaveLength(MAX_FAVORITES)
  })
})

describe('toggleFavorite', () => {
  it('adds, removes, and drops the oldest past the ceiling', () => {
    expect(toggleFavorite([], 'a')).toEqual(['a'])
    expect(toggleFavorite(['a', 'b'], 'a')).toEqual(['b'])
    const full = Array.from({ length: MAX_FAVORITES }, (_, i) => `m${i}`)
    const next = toggleFavorite(full, 'new')
    expect(next).toHaveLength(MAX_FAVORITES)
    expect(next[0]).toBe('m1')
    expect(next.at(-1)).toBe('new')
  })
})

describe('useModelFavorites', () => {
  it("keeps one tool's stars apart from another's", () => {
    const claude = renderHook(() => useModelFavorites('claude'))
    act(() => claude.result.current.toggle?.('shared-id'))
    expect([...claude.result.current.favorites]).toEqual(['shared-id'])
    const codex = renderHook(() => useModelFavorites('codex'))
    expect([...codex.result.current.favorites]).toEqual([])
  })

  it('keeps a user and a tenant apart', () => {
    const first = renderHook(() => useModelFavorites('claude'))
    act(() => first.result.current.toggle?.('a'))
    auth.activeTenant = 't2'
    first.rerender()
    expect([...first.result.current.favorites]).toEqual([])
    auth.activeTenant = 't1'
    auth.principal = { kind: 'user', user_id: 'u2' }
    first.rerender()
    expect([...first.result.current.favorites]).toEqual([])
    auth.principal = { kind: 'user', user_id: 'u1' }
    first.rerender()
    expect([...first.result.current.favorites]).toEqual(['a'])
  })

  it('offers no star to a token principal or without a tenant', () => {
    auth.principal = { kind: 'token', user_id: 'creator' }
    expect(
      renderHook(() => useModelFavorites('claude')).result.current.toggle,
    ).toBeNull()
    auth.principal = { kind: 'user', user_id: 'u1' }
    auth.activeTenant = null
    expect(
      renderHook(() => useModelFavorites('claude')).result.current.toggle,
    ).toBeNull()
  })

  it('works on with a store that refuses to read or write', () => {
    const get = vi
      .spyOn(Storage.prototype, 'getItem')
      .mockImplementation(() => {
        throw new Error('blocked')
      })
    const set = vi
      .spyOn(Storage.prototype, 'setItem')
      .mockImplementation(() => {
        throw new Error('quota')
      })
    try {
      const hook = renderHook(() => useModelFavorites('claude'))
      expect([...hook.result.current.favorites]).toEqual([])
      act(() => hook.result.current.toggle?.('a'))
      // The star holds for this page even though it cannot be kept.
      expect([...hook.result.current.favorites]).toEqual(['a'])
    } finally {
      get.mockRestore()
      set.mockRestore()
    }
  })
})
