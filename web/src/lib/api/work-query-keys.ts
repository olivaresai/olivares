// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

/**
 * Neutral cache-key builders for the WorkItem collection and one WorkItem detail.
 *
 * They exist so a feature that changes WorkItem state through another module's
 * route can invalidate the affected cache entries without importing the Work
 * feature, duplicating its API or gaining a Work read permission. They are pure
 * functions over a tenant and an id: no transport, no types, no authority.
 *
 * The arrays are the ones `features/work/api.ts` has always produced, and that
 * file now builds `workKeys.items` and `workKeys.item` from these. Their
 * serialized shape is pinned by `work-query-keys.test.ts`.
 */
export const workItemQueryKeys = {
  /**
   * Prefix of every WorkItem list query for one tenant. Invalidating it reaches
   * each parameterised list of that tenant and nothing else: sibling Work
   * families (`item`, `lease`, `events`, `dependencies`, `decisions`) sit under
   * their own fourth element.
   */
  collection: (tenant: string | null) => ['work', tenant, 'items'] as const,
  /** The exact key of one WorkItem snapshot query. */
  detail: (tenant: string | null, itemId: string) =>
    ['work', tenant, 'item', itemId] as const,
}
