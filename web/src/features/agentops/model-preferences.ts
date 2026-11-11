// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// A PERSON'S MODEL LIST FOR ONE PROFILE, kept in the user's server preferences like the
// model favorites: which of the tool's models the pickers offer (`hidden` leaves a model
// out), in what order, and the models they added that the tool does not list (`custom`).
// It is a preference, not a fact about the tool: the catalog stays the tool's own answer,
// the tool's default and a model typed into the picker still work, and a model the tool
// stops listing is simply not shown. With no user principal or no tenant there is no
// partition and nothing is saved.
import { useCallback, useMemo, useSyncExternalStore } from 'react'
import { preferenceStorageKey } from '@/features/sessions/session-pins'
import { useAuth } from '@/lib/auth/context'
import { preferenceStorage } from '@/lib/preference-storage'
import type { ProfileModelDTO } from './types'

export interface ModelPreferences {
  /** Models the pickers leave out. */
  hidden: readonly string[]
  /** Model ids in the order the person set; the ones it does not name follow in the
   * tool's order. */
  order: readonly string[]
  /** Models the person added by id. */
  custom: readonly string[]
}

export const NO_PREFERENCES: ModelPreferences = {
  hidden: [],
  order: [],
  custom: [],
}

const NAME = 'olivares.tools.model-preferences'
const VERSION = 1
/** The server keeps one preference up to 16384 bytes; stay under it, and say no past it. */
const MAX_VALUE_CHARS = 16_000
const MAX_PROFILES = 20
const MAX_LIST = 100

/** The shape the engine's own model-id check accepts: no space or control character, up
 * to 200 characters (modules/sessions provider_catalog_tools.go). */
export function isModelId(id: string): boolean {
  return /^[^\s\p{Cc}]{1,200}$/u.test(id)
}

const ids = (value: unknown): string[] =>
  Array.isArray(value)
    ? value
        .filter((v): v is string => typeof v === 'string' && isModelId(v))
        .slice(0, MAX_LIST)
    : []

interface Stored {
  version: number
  profiles: Record<string, Partial<ModelPreferences>>
}

function parse(raw: string | null): Stored {
  try {
    const value: unknown = raw ? JSON.parse(raw) : null
    if (
      value &&
      typeof value === 'object' &&
      'version' in value &&
      value.version === VERSION &&
      'profiles' in value &&
      value.profiles &&
      typeof value.profiles === 'object'
    )
      return value as Stored
  } catch {
    // A value this version did not write reads as "nothing set".
  }
  return { version: VERSION, profiles: {} }
}

/** This profile's stored list; anything unexpected reads as nothing set, never a throw. */
export function decodePreferences(
  raw: string | null,
  profileRef: string,
): ModelPreferences {
  const own = parse(raw).profiles[profileRef]
  if (!own || typeof own !== 'object') return NO_PREFERENCES
  return {
    hidden: ids(own.hidden),
    order: ids(own.order),
    custom: ids(own.custom),
  }
}

/** The stored value with this profile's list replaced, or null when it would not fit the
 * server's bound (the change is then not saved). */
export function encodePreferences(
  raw: string | null,
  profileRef: string,
  next: ModelPreferences,
): string | null {
  const stored = parse(raw)
  const { [profileRef]: _old, ...others } = stored.profiles
  const empty =
    next.hidden.length + next.order.length + next.custom.length === 0
  const kept = Object.entries(others).slice(-(MAX_PROFILES - 1))
  const profiles = Object.fromEntries(
    empty ? kept : [...kept, [profileRef, { ...next }]],
  )
  const value = JSON.stringify({ version: VERSION, profiles })
  return value.length > MAX_VALUE_CHARS ? null : value
}

/** The tool's models with this list applied: ordered first, the rest in the tool's order,
 * the person's own models last. `all` keeps the hidden ones (the list that manages them). */
export function applyPreferences(
  models: readonly ProfileModelDTO[],
  prefs: ModelPreferences,
  all = false,
): ProfileModelDTO[] {
  const listed = new Set(models.map((m) => m.id))
  const known = [
    ...models,
    ...prefs.custom.filter((id) => !listed.has(id)).map((id) => ({ id })),
  ]
  const rank = (id: string) => {
    const at = prefs.order.indexOf(id)
    return at < 0 ? prefs.order.length : at
  }
  // Array.prototype.sort is stable: models the order never names keep their place.
  const ordered = [...known].sort((a, b) => rank(a.id) - rank(b.id))
  const hidden = new Set(prefs.hidden)
  return all ? ordered : ordered.filter((m) => !hidden.has(m.id))
}

/** `shown` with `id` moved one place up (-1) or down (1); the ends stay where they are. */
export function moveModel(
  shown: readonly string[],
  id: string,
  by: -1 | 1,
): string[] {
  const from = shown.indexOf(id)
  const to = from + by
  if (from < 0 || to < 0 || to >= shown.length) return [...shown]
  const next = [...shown]
  next.splice(to, 0, ...next.splice(from, 1))
  return next
}

// preferenceStorage tells its listeners when the server row is replaced, not when a value
// is set; every list on screen listens here too, so a change shows everywhere at once.
const listeners = new Set<() => void>()
const subscribe = (listener: () => void) => {
  listeners.add(listener)
  const detach = preferenceStorage.subscribe(listener)
  return () => {
    listeners.delete(listener)
    detach()
  }
}

export interface ModelPreferenceState {
  prefs: ModelPreferences
  /** Null when there is nowhere to write; false when the change did not fit. */
  save: ((next: ModelPreferences) => boolean) | null
}

export function useModelPreferences(
  profileRef: string | undefined,
): ModelPreferenceState {
  const { principal, activeTenant } = useAuth()
  const key =
    typeof window === 'undefined'
      ? null
      : preferenceStorageKey(
          NAME,
          window.location.origin,
          principal,
          activeTenant,
        )
  const raw = useSyncExternalStore(
    subscribe,
    () => (key ? preferenceStorage.getItem(key) : null),
    () => null,
  )
  const prefs = useMemo(
    () => (profileRef ? decodePreferences(raw, profileRef) : NO_PREFERENCES),
    [raw, profileRef],
  )
  const save = useCallback(
    (next: ModelPreferences) => {
      if (!key || !profileRef) return false
      const value = encodePreferences(
        preferenceStorage.getItem(key),
        profileRef,
        next,
      )
      if (value === null) return false
      try {
        preferenceStorage.setItem(key, value)
      } catch {
        // A blocked store: the list does not survive a reload.
      }
      listeners.forEach((listener) => listener())
      return true
    },
    [key, profileRef],
  )
  return { prefs, save: key && profileRef ? save : null }
}
