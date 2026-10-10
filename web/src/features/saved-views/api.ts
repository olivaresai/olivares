// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import { ApiError } from '@/lib/api/errors'
import { moduleOn } from '@/stores/modules'
import { http, type RequestOptions } from '@/lib/api/client'
import type { FavoritesRemote } from '@/features/navigation/personal-navigation-store'
import type {
  FavoriteLink,
  FavoritesResponse,
  SavedView,
  SavedViewInput,
  SavedViewsResponse,
  UiState,
  UiStateResponse,
} from './types'

const VIEWS = '/v1/m/consoleviews/views'
const FAVORITES = '/v1/m/consoleviews/favorites'
const UI_STATE = '/v1/m/consoleviews/ui-state'
type CallOptions = Pick<
  RequestOptions,
  'tenant' | 'signal' | 'dispatchGuard' | 'sessionEffects'
>

export const savedViewsApi = {
  list: (featureId: string) =>
    http.get<SavedViewsResponse>(VIEWS, {
      query: { feature_id: featureId },
    }),
  create: (input: SavedViewInput) => http.post<SavedView>(VIEWS, input),
  update: (id: string, input: SavedViewInput) =>
    http.put<SavedView>(`${VIEWS}/${id}`, input),
  delete: (id: string) => http.delete<void>(`${VIEWS}/${id}`),
  /** The caller's own favorites (one list per user and organization). */
  favorites: (opts: CallOptions) =>
    http.get<FavoritesResponse>(FAVORITES, opts),
  saveFavorites: (favorites: readonly FavoriteLink[], opts: CallOptions) =>
    http.put<FavoritesResponse>(FAVORITES, { favorites }, opts),
  /** The caller's own interface state (one row per user and organization). */
  uiState: (opts: CallOptions) => http.get<UiStateResponse>(UI_STATE, opts),
  saveUiState: (state: UiState, opts: CallOptions) =>
    http.put<UiStateResponse>(UI_STATE, state, opts),
}

export const savedViewsKeys = {
  all: (tenant: string | null) => ['consoleviews', tenant] as const,
  list: (tenant: string | null, featureId: string) =>
    ['consoleviews', tenant, 'views', featureId] as const,
}

/** The user's favorites on the engine, for personal navigation. A favorites call never
 * renews, replays or ends the sign-in: on failure the browser copy stands. */
export const engineFavorites = (tenant: string | null): FavoritesRemote => ({
  // Favorites live in the consoleviews module. While it is off nothing is sent (the read
  // would answer 404 module_not_enabled): the load fails as before, so the browser copy
  // stands, and a save stays in the browser without a "could not save" notice.
  load: ({ signal, guard }) =>
    moduleOn('consoleviews')
      ? savedViewsApi.favorites({
          tenant,
          signal,
          dispatchGuard: guard,
          sessionEffects: 'none',
        })
      : Promise.reject(
          new ApiError(404, 'module_not_enabled', 'consoleviews is off'),
        ),
  save: (favorites, { signal, guard }) =>
    moduleOn('consoleviews')
      ? savedViewsApi.saveFavorites(favorites, {
          tenant,
          signal,
          dispatchGuard: guard,
          sessionEffects: 'none',
        })
      : Promise.resolve(undefined),
})
