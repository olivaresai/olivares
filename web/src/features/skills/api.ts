// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Thin wrappers over the skills catalog routes (modules/skills/catalog.go:97). Writes take
// the tenant the operator acted in; a pin change carries its recorded version in If-Match.
import { http, type TenantRequestOptions } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type {
  SkillAssignment,
  SkillPack,
  SkillPackDetail,
  SkillTargetKind,
} from './types'

const BASE = '/v1/m/skills'
// The engine's own page ceiling (modules/skills/catalog.go query()).
const LIST_CEILING = 200

export const skillsApi = {
  packs: () =>
    http.get<ListResponse<SkillPack>>(`${BASE}/packs`, {
      query: { limit: LIST_CEILING },
    }),
  /** A pack with its revisions, oldest first. The engine caps a page near 1 MiB, so the
   * latest revision of a large pack can sit on a later page: follow the cursor up to the
   * list ceiling, and keep has_more_revisions when it stops early. */
  pack: async (id: string): Promise<SkillPackDetail> => {
    const read = (cursor?: string) =>
      http.get<SkillPackDetail>(`${BASE}/packs/${encodeURIComponent(id)}`, {
        query: { limit: LIST_CEILING, cursor },
      })
    const first = await read()
    const revisions = [...first.revisions]
    let page = first
    while (
      page.has_more_revisions &&
      page.revisions.length > 0 &&
      page.revisions_cursor &&
      revisions.length < LIST_CEILING
    ) {
      page = await read(page.revisions_cursor)
      revisions.push(...page.revisions)
    }
    return { ...page, pack: first.pack, revisions }
  },
  /** Every target the caller can read that this pack is pinned to. */
  packAssignments: (packId: string) =>
    http.get<ListResponse<SkillAssignment>>(
      `${BASE}/packs/${encodeURIComponent(packId)}/assignments`,
      { query: { limit: LIST_CEILING } },
    ),
  assign: (
    body: {
      target_kind: SkillTargetKind
      target_id: string
      pack_revision_id: string
    },
    request: TenantRequestOptions,
  ) =>
    http.post<{ state: string; assignment: SkillAssignment }>(
      `${BASE}/assignments`,
      body,
      request,
    ),
  unassign: (assignment: SkillAssignment, request: TenantRequestOptions) =>
    http.delete<{ state: string }>(
      `${BASE}/assignments/${encodeURIComponent(assignment.id)}`,
      undefined,
      { ...request, headers: { 'If-Match': String(assignment.version) } },
    ),
}

/** Tenant-scoped query keys (query.ts contract: tenant id in every key). */
export const skillsKeys = {
  all: (t: string | null) => ['skills', t] as const,
  packs: (t: string | null) => ['skills', t, 'packs'] as const,
  pack: (t: string | null, id: string) => ['skills', t, 'pack', id] as const,
  packAssignments: (t: string | null, id: string) =>
    ['skills', t, 'pack', id, 'assignments'] as const,
}
