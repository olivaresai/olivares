// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/** A named console URL-state snapshot returned by the consoleviews module. */
export interface SavedView {
  id: string
  feature_id: string
  name: string
  description?: string
  /** The server accepts any JSON object. Consumers must validate values before use. */
  params: Record<string, unknown>
  owner: string
  shared: boolean
  mine: boolean
  created_at: string
  updated_at: string
}

export interface SavedViewInput {
  feature_id: string
  name: string
  description?: string
  params: Record<string, string>
  shared: boolean
}

export interface SavedViewsResponse {
  items: SavedView[]
}
