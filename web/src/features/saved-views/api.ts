// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

import { http } from '@/lib/api/client'
import type { SavedView, SavedViewInput, SavedViewsResponse } from './types'

const VIEWS = '/v1/m/consoleviews/views'

export const savedViewsApi = {
  list: (featureId: string) =>
    http.get<SavedViewsResponse>(VIEWS, {
      query: { feature_id: featureId },
    }),
  create: (input: SavedViewInput) => http.post<SavedView>(VIEWS, input),
  update: (id: string, input: SavedViewInput) =>
    http.put<SavedView>(`${VIEWS}/${id}`, input),
  delete: (id: string) => http.delete<void>(`${VIEWS}/${id}`),
}

export const savedViewsKeys = {
  all: (tenant: string | null) => ['consoleviews', tenant] as const,
  list: (tenant: string | null, featureId: string) =>
    ['consoleviews', tenant, 'views', featureId] as const,
}
