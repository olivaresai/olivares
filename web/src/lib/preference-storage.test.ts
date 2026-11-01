// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { describe, it, expect } from 'vitest'
import { createPreferenceStorage } from './preference-storage'

describe('server preference storage', () => {
  it('follows a user to another origin without copying browser state', () => {
    const first = createPreferenceStorage()
    const changes: Record<string, string | null>[] = []
    first.attach((patch) => changes.push(patch), {})
    first.setItem('olivares.settings', '{"state":{"clock":"12"}}')
    const fresh = createPreferenceStorage()
    fresh.attach(() => {}, changes[0] as Record<string, string>)
    expect(fresh.getItem('olivares.settings')).toBe('{"state":{"clock":"12"}}')
    expect(localStorage.getItem('olivares.settings')).toBeNull()
  })
  it('canonicalizes scoped keys, clears state on owner change and rejects stale writes', () => {
    const storage = createPreferenceStorage()
    const key =
      'olivares.sessions.model-favorites:' +
      JSON.stringify(['https://one', 'user', 'alice', 'tenant'])
    storage.attach(() => {}, {}, ['https://one', 'user', 'alice', 'tenant'])
    storage.setItem(key, 'models')
    storage.attach(() => {}, {}, ['https://two', 'user', 'bob', 'tenant'])
    expect(storage.getItem(key)).toBeNull()
    storage.setItem(key, 'late')
    expect(storage.length).toBe(0)
  })
  it('removes one value without resetting other settings', () => {
    const storage = createPreferenceStorage()
    const changes: unknown[] = []
    storage.attach((patch) => changes.push(patch), { 'olivares.lang': 'es' })
    storage.removeItem('olivares.lang')
    expect(changes).toEqual([{ 'olivares.lang': null }])
    expect(storage.getItem('olivares.lang')).toBeNull()
  })
})
