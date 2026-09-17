// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { FEATURE_VIEWS } from '@/features/registry'
import { SETTINGS_UTILITY } from './model'
import { personalLink, type PersonalLink } from './personal-navigation-store'

/** Exact module entrances only. No location or entity selector enters the recent store. */
export function recentDestination(
  pathname: string,
  search: string,
): string | null {
  const target =
    pathname === SETTINGS_UTILITY.path
      ? SETTINGS_UTILITY
      : FEATURE_VIEWS.find((view) => view.path === pathname)
  if (!target || !personalLink(target.id)) return null
  // Communications opens entity sheets on these selectors (communications-view.tsx
  // URL_KEYS). Exclude even malformed selectors; ordinary filters/tabs remain module
  // visits. Parameterized/hidden detail routes already fail personalLink above.
  if (target.path.startsWith('/communications')) {
    const params = new URLSearchParams(search)
    if (
      ['channel', 'delivery', 'message', 'admin_channel'].some((key) =>
        params.has(key),
      )
    )
      return null
  }
  return target.id
}

/** Memory only, one logical identity/tenant lifetime, independent of credential renewal. */
export function createSessionRecents(current: () => boolean) {
  let revoked = false
  let entries: readonly PersonalLink[] = []
  let destination: string | null = null
  let admitted = false
  const listeners = new Set<() => void>()
  const emit = () => listeners.forEach((listener) => listener())
  const revoke = () => {
    if (revoked) return
    revoked = true
    entries = []
    destination = null
    admitted = false
    emit()
  }
  const live = () => {
    if (!revoked && current()) return true
    revoke()
    return false
  }
  return {
    contextCurrent: current,
    live,
    revoke,
    isActive: () => !revoked && current(),
    getSnapshot: () => entries,
    subscribe: (listener: () => void) => {
      listeners.add(listener)
      return () => {
        listeners.delete(listener)
      }
    },
    // A resolved route movement alone NEVER adds a recent. It only separates visits,
    // including a trip through a denied page or area directory. Store IDs, not URLs.
    arrive: (id: string | null) => {
      if (!live() || id === destination) return
      destination = id
      admitted = false
    },
    admit: (id: string) => {
      if (!live() || destination !== id || admitted) return
      const link = personalLink(id)
      if (!link) return
      admitted = true
      entries = [link, ...entries.filter((entry) => entry.id !== id)]
      emit()
    },
    remove: (id: string) => {
      if (!live()) return
      entries = entries.filter((entry) => entry.id !== id)
      emit()
    },
    clear: () => {
      if (!live()) return
      entries = []
      // Keep the current visit consumed: renewal/remount cannot undo an explicit clear.
      emit()
    },
  }
}
