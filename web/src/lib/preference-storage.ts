// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/** Only preference keys, never credentials, request bodies, drafts or terminal IDs. */
export const PREFERENCE_KEYS = [
  'olivares.prefs',
  'olivares.settings',
  'olivares.workspace',
  'olivares.tenant',
  'olivares:api-playground',
  'olivares.lang',
  'olivares.sessions.model-favorites',
  'olivares.notifications.lastSeen',
  'olivares.onboarding.dismissed',
  'olivares.compliance.opened',
  'olivares.sessions.sidePane',
] as const
export type PreferencePatch = Record<string, string | null>

/** Synchronous view of the verified user's server row for existing Storage consumers. */
export function createPreferenceStorage() {
  let values: Record<string, string> = {}
  let epoch = 0
  const listeners = new Set<() => void>()
  let owner: readonly string[] | undefined
  let write: ((patch: PreferencePatch) => void) | null = null
  const canonical = (key: string): string | null => {
    for (const name of PREFERENCE_KEYS) {
      if (name === key) return name
      if (!owner && key.startsWith(`${name}:`)) return key
      if (owner && key === `${name}:${JSON.stringify(owner)}`) return name
    }
    return null
  }
  return {
    attach(
      save: ((patch: PreferencePatch) => void) | null,
      stored: Record<string, string>,
      identity?: readonly string[],
    ) {
      epoch++
      values = { ...stored }
      owner = identity
      write = save
      listeners.forEach((listener) => listener())
    },
    subscribe(listener: () => void) {
      listeners.add(listener)
      return () => {
        listeners.delete(listener)
      }
    },
    getOwner: () => (owner ? JSON.stringify(owner) : null),
    get epoch() {
      return epoch
    },
    get length() {
      return Object.keys(values).length
    },
    key(index: number) {
      return Object.keys(values)[index] ?? null
    },
    clear() {
      const patch = Object.fromEntries(
        Object.keys(values).map((key) => [key, null]),
      )
      values = {}
      write?.(patch)
    },
    getItem(key: string) {
      const name = canonical(key)
      return name ? (values[name] ?? null) : null
    },
    setItem(key: string, value: string) {
      const name = canonical(key)
      if (!name) return
      values[name] = value
      write?.({ [name]: value })
    },
    removeItem(key: string) {
      const name = canonical(key)
      if (!name) return
      delete values[name]
      write?.({ [name]: null })
    },
  }
}
export const preferenceStorage = createPreferenceStorage()
