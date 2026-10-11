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

/** The most favorites one person keeps: above the pages the console offers to star, and
 * the engine's own bound (modules/consoleviews/favorites.go maxFavorites). */
export const MAX_FAVORITES = 256

/** Keep unavailable edition identities in storage; only the current registry resolves links. */
export function decodeFavoriteList(list: unknown): readonly PersonalLink[] {
  if (!Array.isArray(list)) return EMPTY
  const result: PersonalLink[] = []
  for (const entry of list) {
    if (!entry || typeof entry !== 'object' || typeof entry.id !== 'string')
      continue
    const link =
      personalLink(entry.id) ??
      (entry.kind === 'feature' &&
      ['finops', 'redteam', 'team-costs'].includes(entry.id) &&
      !FEATURE_VIEWS.some((view) => view.id === entry.id)
        ? { kind: 'feature' as const, id: entry.id }
        : undefined)
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
}

export function decodeFavorites(raw: string | null): readonly PersonalLink[] {
  if (!raw) return EMPTY
  try {
    const value: unknown = JSON.parse(raw)
    if (
      !value ||
      typeof value !== 'object' ||
      !('version' in value) ||
      value.version !== 1 ||
      !('favorites' in value)
    )
      return EMPTY
    return decodeFavoriteList(value.favorites)
  } catch {
    return EMPTY
  }
}

/** One engine call of this lifetime: aborted on revoke; the guard refuses a retired one. */
export interface FavoritesCall {
  readonly signal: AbortSignal
  readonly guard: () => void
}

/** The user's favorites on the engine (GET/PUT /v1/m/consoleviews/favorites). The account
 * copy is the truth; the browser copy is a cache that shows at once and stands in when the
 * engine cannot be read. */
export interface FavoritesRemote {
  load: (
    call: FavoritesCall,
  ) => Promise<{ favorites?: unknown; stored?: unknown }>
  save: (
    favorites: readonly PersonalLink[],
    call: FavoritesCall,
  ) => Promise<unknown>
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
  remote,
  onSaveError,
}: {
  key: string | null
  current: () => boolean
  storage?: () => Storage
  remote?: FavoritesRemote
  /** Called when the account copy could not be saved; the browser copy is kept. */
  onSaveError?: () => void
}) {
  let revoked = false
  let snapshot: PersonalSnapshot = { favorites: EMPTY, temporary: key === null }
  const listeners = new Set<() => void>()
  const emit = () => listeners.forEach((listener) => listener())
  const calls = new AbortController()
  const revoke = () => {
    if (revoked) return
    revoked = true
    calls.abort()
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
  // `pending` marks a browser copy the account does not have yet: the next sign-in sends it
  // instead of replacing it with the older account copy.
  const keep = (favorites: readonly PersonalLink[], pending = false) => {
    let temporary = key === null
    if (key) {
      try {
        storage().setItem(
          key,
          JSON.stringify(
            pending
              ? { version: 1, favorites, pending: true }
              : { version: 1, favorites },
          ),
        )
      } catch {
        temporary = true
      }
    }
    snapshot = { favorites, temporary }
    emit()
  }
  const call: FavoritesCall = {
    signal: calls.signal,
    guard: () => {
      if (!live()) calls.abort()
      calls.signal.throwIfAborted()
    },
  }
  // Saves leave in order; each one carries the whole list, so the last one wins.
  let sent: Promise<unknown> = Promise.resolve()
  const send = (favorites: readonly PersonalLink[]) => {
    if (!remote || !key) return
    sent = sent
      .then(() => remote.save(favorites, call))
      .then(
        () => {
          // Saved: the browser copy is no longer ahead of the account.
          if (live() && snapshot.favorites === favorites) keep(favorites)
        },
        () => {
          // Not saved: the browser copy stays pending and the person is told.
          if (live()) onSaveError?.()
        },
      )
  }
  let changed = false
  if (remote && key && live()) {
    // Read only inside a verified lifetime, like read(): an unverified one never opens
    // the persistent partition.
    const pendingHere = (() => {
      try {
        const raw: unknown = key
          ? JSON.parse(storage().getItem(key) ?? 'null')
          : null
        return (
          !!raw &&
          typeof raw === 'object' &&
          'pending' in raw &&
          raw.pending === true
        )
      } catch {
        return false
      }
    })()
    remote.load(call).then(
      (answer) => {
        // A change made while the read was out is newer than its answer.
        if (!live() || changed) return
        // A change this browser could not save yet is newer than the account copy.
        if (pendingHere) send(snapshot.favorites)
        else if (answer.stored === true)
          keep(decodeFavoriteList(answer.favorites))
        // First use of the account copy: move this browser's favorites to it.
        else if (snapshot.favorites.length) send(snapshot.favorites)
      },
      () => {
        /* Engine not reachable or not permitted: the browser copy stands. */
      },
    )
  }
  const save = (favorites: readonly PersonalLink[]) => {
    if (!live()) return
    changed = true
    keep(favorites, !!remote)
    send(favorites)
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
      if (saved && favorites.length >= MAX_FAVORITES) return
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
