// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// THE MODELS THIS PERSON PICKS MOST, kept where session pins are kept: the browser,
// partitioned per (origin, user, tenant). A favorite is a preference, not a fact about
// the tool, so a model the tool stops listing is simply not shown (groupModels). With no
// user principal or no tenant there is no partition and favoriting is not offered.
import { useCallback, useState } from 'react'
import { preferenceStorageKey } from '@/features/sessions/session-pins'
import { useAuth } from '@/lib/auth/context'

/** The most favorites one person keeps, per tenant; the oldest is dropped past it. */
export const MAX_FAVORITES = 50

const VERSION = 1
const NAME = 'olivares.sessions.model-favorites'

const entry = (driver: string, id: string) => `${driver}:${id}`

export function decodeFavorites(raw: string | null): readonly string[] {
  if (!raw) return []
  try {
    const value: unknown = JSON.parse(raw)
    if (
      !value ||
      typeof value !== 'object' ||
      !('version' in value) ||
      value.version !== VERSION ||
      !('favorites' in value) ||
      !Array.isArray(value.favorites)
    )
      return []
    const out: string[] = []
    for (const item of value.favorites) {
      if (typeof item !== 'string' || !item || out.includes(item)) continue
      out.push(item)
      if (out.length === MAX_FAVORITES) break
    }
    return out
  } catch {
    return []
  }
}

/** Adds or removes one entry, newest last. */
export function toggleFavorite(
  favorites: readonly string[],
  item: string,
): readonly string[] {
  if (favorites.includes(item)) return favorites.filter((f) => f !== item)
  const next = [...favorites, item]
  return next.length > MAX_FAVORITES
    ? next.slice(next.length - MAX_FAVORITES)
    : next
}

function read(key: string | null): readonly string[] {
  if (!key || typeof window === 'undefined') return []
  try {
    return decodeFavorites(window.localStorage.getItem(key))
  } catch {
    // Private mode or a blocked store: no favorites, and nothing thrown over a preference.
    return []
  }
}

export interface ModelFavorites {
  /** The favorite model ids of this tool. */
  favorites: ReadonlySet<string>
  /** Null when there is nowhere to write: the picker then offers no star. */
  toggle: ((id: string) => void) | null
}

export function useModelFavorites(driver: string): ModelFavorites {
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
  const [state, setState] = useState<{
    key: string | null
    stored: readonly string[]
  }>(() => ({ key, stored: read(key) }))
  // Another principal or tenant: read that partition, never carry the last one across.
  if (state.key !== key) setState({ key, stored: read(key) })

  const toggle = useCallback(
    (id: string) => {
      if (!key) return
      // Read before writing: two tabs share one origin.
      const next = toggleFavorite(read(key), entry(driver, id))
      try {
        window.localStorage.setItem(
          key,
          JSON.stringify({ version: VERSION, favorites: next }),
        )
      } catch {
        // Out of quota or blocked: the star does not survive a reload.
      }
      setState({ key, stored: next })
    },
    [key, driver],
  )

  const prefix = `${driver}:`
  const stored = state.key === key ? state.stored : []
  return {
    favorites: new Set(
      stored
        .filter((s) => s.startsWith(prefix))
        .map((s) => s.slice(prefix.length)),
    ),
    toggle: key ? toggle : null,
  }
}
