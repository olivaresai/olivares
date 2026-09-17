// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { beforeEach, describe, expect, it } from 'vitest'
import type { Whoami } from '@/lib/api/types'
import { FEATURE_VIEWS } from '@/features/registry'
import {
  createPersonalNavigationSession,
  decodeFavorites,
  favoriteStorageKey,
  personalLink,
} from './personal-navigation-store'

const principal = { kind: 'user', user_id: 'operator-a' } as Whoami
const key = favoriteStorageKey(
  'https://console.invalid',
  principal,
  'tenant-a',
)!
const home = personalLink('home')!
const settings = personalLink('settings')!
const session = () =>
  createPersonalNavigationSession({ key, current: () => true })
beforeEach(() => localStorage.clear())

describe('personal navigation storage boundary', () => {
  it('writes only after explicit save, reloads IDs, removes and clears', () => {
    const a = session()
    expect(localStorage.getItem(key)).toBeNull()
    a.setFavorite(home, true)
    a.setFavorite(settings, true)
    a.setFavorite(home, true)
    expect(JSON.parse(localStorage.getItem(key)!)).toEqual({
      version: 1,
      favorites: [settings, home],
    })
    const reload = session()
    expect(reload.getSnapshot().favorites).toEqual([settings, home])
    reload.setFavorite(settings, false)
    expect(session().getSnapshot().favorites).toEqual([home])
    reload.clearFavorites()
    expect(session().getSnapshot().favorites).toEqual([])
  })

  it('partitions origin, kind/user_id and tenant without deriving token identities', () => {
    expect(
      favoriteStorageKey('https://other.invalid', principal, 'tenant-a'),
    ).not.toBe(key)
    expect(
      favoriteStorageKey('https://console.invalid', principal, 'tenant-b'),
    ).not.toBe(key)
    expect(
      favoriteStorageKey(
        'https://console.invalid',
        { ...principal, user_id: 'operator-b' },
        'tenant-a',
      ),
    ).not.toBe(key)
    expect(
      favoriteStorageKey(
        'https://console.invalid',
        { ...principal, kind: 'token' },
        'tenant-a',
      ),
    ).toBeNull()
    expect(
      favoriteStorageKey(
        'https://console.invalid',
        { ...principal, user_id: '  ' },
        'tenant-a',
      ),
    ).toBeNull()
    expect(
      favoriteStorageKey(
        'https://console.invalid',
        { ...principal, display_name: 'Changed' },
        'tenant-a',
      ),
    ).toBe(key)
  })

  it('validates schema, reconstructs IDs and drops URLs, detail routes and extra data', () => {
    for (const raw of [
      '{',
      'null',
      '[]',
      '{"version":2,"favorites":[]}',
      '{"version":1,"favorites":"home"}',
    ])
      expect(decodeFavorites(raw)).toEqual([])
    expect(
      decodeFavorites(
        JSON.stringify({
          version: 1,
          favorites: [
            {
              ...home,
              url: '/console?secret=fixture',
              name: 'fixture payload',
            },
            home,
            { kind: 'feature', id: 'https://invalid.test' },
            { kind: 'feature', id: 'sessionViewer' },
            { kind: 'feature', id: 'settings' },
            { kind: 'utility', id: 'home' },
            null,
            settings,
          ],
        }),
      ),
    ).toEqual([home, settings])
  })

  it('allows the entire current set with no favorite quota', () => {
    const all = FEATURE_VIEWS.flatMap((v) => personalLink(v.id) ?? [])
    const a = session()
    all.forEach((link) => a.setFavorite(link, true))
    a.setFavorite(settings, true)
    expect(session().getSnapshot().favorites).toHaveLength(all.length + 1)
  })

  it('falls back to memory for missing identity and denied/full storage', () => {
    for (const key of [null, 'test']) {
      const a = createPersonalNavigationSession({
        key,
        current: () => true,
        storage: () => {
          throw new DOMException('fixture denied', 'SecurityError')
        },
      })
      a.setFavorite(home, true)
      expect(a.getSnapshot()).toEqual({ favorites: [home], temporary: true })
      a.setFavorite(home, false)
      expect(a.getSnapshot().favorites).toEqual([])
    }
    const a = createPersonalNavigationSession({
      key,
      current: () => true,
      storage: () =>
        ({
          getItem: () => null,
          setItem: () => {
            throw new DOMException('fixture full', 'QuotaExceededError')
          },
        }) as unknown as Storage,
    })
    a.setFavorite(settings, true)
    expect(a.getSnapshot()).toEqual({ favorites: [settings], temporary: true })
  })

  it('latches a departed identity and rejects late callbacks even after A→B→A', () => {
    let current = true
    const a = createPersonalNavigationSession({ key, current: () => current })
    a.setFavorite(home, true)
    current = false
    expect(a.live()).toBe(false)
    current = true
    a.setFavorite(settings, true)
    a.clearFavorites()
    expect(a.getSnapshot().favorites).toEqual([])
    expect(session().getSnapshot().favorites).toEqual([home])
  })

  it('consumes only current-partition localStorage events and reads latest bytes', () => {
    const a = session()
    localStorage.setItem(key, JSON.stringify({ version: 1, favorites: [home] }))
    a.storageChanged(
      new StorageEvent('storage', { key: 'other', storageArea: localStorage }),
    )
    expect(a.getSnapshot().favorites).toEqual([])
    a.storageChanged(
      new StorageEvent('storage', { key, storageArea: sessionStorage }),
    )
    expect(a.getSnapshot().favorites).toEqual([])
    a.storageChanged(
      new StorageEvent('storage', {
        key,
        storageArea: localStorage,
        newValue: 'stale fixture',
      }),
    )
    expect(a.getSnapshot().favorites).toEqual([home])
    localStorage.clear()
    a.storageChanged(
      new StorageEvent('storage', { key: null, storageArea: localStorage }),
    )
    expect(a.getSnapshot().favorites).toEqual([])
  })
})
