// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import type { Whoami } from '@/lib/api/types'
import { FEATURE_VIEWS } from '@/features/registry'
import { SETTINGS_UTILITY } from './model'

export type PersonalLink =
  | { readonly kind: 'feature'; readonly id: string }
  | { readonly kind: 'utility'; readonly id: 'settings' }

/** Resolve every use against today's registry; hidden/parameterized routes are not links. */
export function resolvePersonalLink(link: PersonalLink) {
  if (link.kind === 'utility')
    return link.id === SETTINGS_UTILITY.id ? SETTINGS_UTILITY : undefined
  return FEATURE_VIEWS.find(
    (v) => v.id === link.id && !v.hideInNav && !v.path.includes('$'),
  )
}

export function personalLink(id: string): PersonalLink | undefined {
  const link: PersonalLink =
    id === 'settings' ? { kind: 'utility', id } : { kind: 'feature', id }
  return resolvePersonalLink(link) ? link : undefined
}

export function favoriteStorageKey(
  origin: string,
  principal: Whoami | null,
  tenant: string | null,
) {
  // A token principal can carry its creator's user_id. It is not that user.
  if (
    principal?.kind !== 'user' ||
    typeof principal.user_id !== 'string' ||
    !principal.user_id.trim() ||
    !tenant
  )
    return null
  return `olivares.navigation.favorites:${JSON.stringify([origin, 'user', principal.user_id, tenant])}`
}

const EMPTY: readonly PersonalLink[] = Object.freeze([])

export function decodeFavorites(raw: string | null): readonly PersonalLink[] {
  if (!raw) return EMPTY
  try {
    const value: unknown = JSON.parse(raw)
    if (
      !value ||
      typeof value !== 'object' ||
      !('version' in value) ||
      value.version !== 1 ||
      !('favorites' in value) ||
      !Array.isArray(value.favorites)
    )
      return EMPTY
    const result: PersonalLink[] = []
    for (const entry of value.favorites) {
      if (!entry || typeof entry !== 'object' || typeof entry.id !== 'string')
        continue
      const link = personalLink(entry.id)
      if (
        !link ||
        link.kind !== entry.kind ||
        result.some((v) => v.id === link.id)
      )
        continue
      // Reconstruct: arbitrary fields, URLs, labels and payloads never enter active memory.
      result.push(link)
    }
    return result
  } catch {
    return EMPTY
  }
}

export interface PersonalSnapshot {
  readonly favorites: readonly PersonalLink[]
  readonly temporary: boolean
}

/** One revocable identity/tenant lifetime. An old callback can never revive it (A→B→A). */
export function createPersonalNavigationSession({
  key,
  current,
  storage = () => window.localStorage,
}: {
  key: string | null
  current: () => boolean
  storage?: () => Storage
}) {
  let revoked = false
  let snapshot: PersonalSnapshot = { favorites: EMPTY, temporary: key === null }
  const listeners = new Set<() => void>()
  const emit = () => listeners.forEach((listener) => listener())
  const revoke = () => {
    if (revoked) return
    revoked = true
    snapshot = { favorites: EMPTY, temporary: true }
    emit()
  }
  const live = () => {
    if (revoked) return false
    if (current()) return true
    revoke()
    return false
  }
  const read = () => {
    if (!key || !live()) return
    try {
      snapshot = {
        favorites: decodeFavorites(storage().getItem(key)),
        temporary: false,
      }
    } catch {
      snapshot = { ...snapshot, temporary: true }
    }
  }
  read()
  const save = (favorites: readonly PersonalLink[]) => {
    if (!live()) return
    let temporary = key === null
    if (key) {
      try {
        storage().setItem(key, JSON.stringify({ version: 1, favorites }))
      } catch {
        temporary = true
      }
    }
    snapshot = { favorites, temporary }
    emit()
  }
  return {
    contextCurrent: current,
    isActive: () => !revoked && current(),
    getSnapshot: () => snapshot,
    subscribe: (listener: () => void) => {
      listeners.add(listener)
      return () => {
        listeners.delete(listener)
      }
    },
    revoke,
    live,
    setFavorite: (link: PersonalLink, saved: boolean) => {
      if (!live() || !resolvePersonalLink(link)) return
      const favorites = snapshot.favorites.filter((v) => v.id !== link.id)
      save(saved ? [...favorites, personalLink(link.id)!] : favorites)
    },
    clearFavorites: () => save(EMPTY),
    storageChanged: (event: StorageEvent) => {
      if (!key || !live() || (event.key !== key && event.key !== null)) return
      try {
        if (event.storageArea !== storage()) return
      } catch {
        return
      }
      // Read current bytes, not a queued event's stale payload. Never enumerate other keys.
      read()
      emit()
    },
  }
}
