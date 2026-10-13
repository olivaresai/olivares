// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Thin wrappers over the skills catalog routes (modules/skills/catalog.go:97). Writes take
// the tenant the operator acted in; a pin change carries its recorded version in If-Match.
import {
  http,
  type RequestOptions,
  type TenantRequestOptions,
} from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type {
  BuiltinPack,
  SkillUpdateCheck,
  SkillInstallRequest,
  SkillInstallResult,
  SkillAssignment,
  SkillPack,
  SkillPackDetail,
  SkillSelection,
  SkillTargetKind,
} from './types'

const BASE = '/v1/m/skills'
// The engine's own page ceiling (modules/skills/catalog.go query()).
const LIST_CEILING = 200

export const skillsApi = {
  builtins: (request: TenantRequestOptions & Pick<RequestOptions, 'signal'>) =>
    http.get<ListResponse<BuiltinPack>>(`${BASE}/builtins`, request),
  install: (
    body: SkillInstallRequest,
    key: string,
    request: TenantRequestOptions,
  ) =>
    http.post<SkillInstallResult>(`${BASE}/packs`, body, {
      ...request,
      headers: { 'Idempotency-Key': key },
    }),
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
  /** What a new conversation pinned to this target receives: the launch's own
   * resolution (GET /selection), its refusals included. */
  selection: (kind: SkillTargetKind, id: string, signal?: AbortSignal) =>
    http.get<SkillSelection>(`${BASE}/selection`, {
      query: { target_kind: kind, target_id: id },
      signal,
    }),
  /** Read the pack's recorded source again: current, or available with its diff. */
  updateCheck: (packId: string, request: TenantRequestOptions) =>
    http.post<SkillUpdateCheck>(
      `${BASE}/packs/${encodeURIComponent(packId)}/update`,
      { apply: false },
      request,
    ),
  /** Publish exactly the candidate whose digest the operator reviewed. */
  updateApply: (
    packId: string,
    expectedDigest: string,
    key: string,
    request: TenantRequestOptions,
  ) =>
    http.post<SkillInstallResult>(
      `${BASE}/packs/${encodeURIComponent(packId)}/update`,
      { apply: true, expected_digest: expectedDigest },
      { ...request, headers: { 'Idempotency-Key': key } },
    ),
  /** Move one pin to another revision, keeping its selected skills. */
  repin: (
    assignment: SkillAssignment,
    revisionId: string,
    approveActiveContent: boolean,
    request: TenantRequestOptions,
  ) =>
    http.put<{ state: string; assignment: SkillAssignment }>(
      `${BASE}/assignments/${encodeURIComponent(assignment.id)}`,
      {
        target_kind: assignment.target_kind,
        target_id: assignment.target_id,
        pack_revision_id: revisionId,
        ...(assignment.members?.length ? { members: assignment.members } : {}),
        ...(approveActiveContent ? { approve_active_content: true } : {}),
      },
      { ...request, headers: { 'If-Match': String(assignment.version) } },
    ),
  assign: (
    body: {
      target_kind: SkillTargetKind
      target_id: string
      pack_revision_id: string
      approve_active_content?: boolean
    },
    request: TenantRequestOptions,
  ) =>
    http.post<{ state: string; assignment: SkillAssignment }>(
      `${BASE}/assignments`,
      body,
      request,
    ),
  /** Read back an uncertain assignment without overwriting another pin. */
  hasWorkspacePin: async (
    workspace: string,
    pack: string,
    revision: string,
    expectedMembers: readonly string[],
    request: TenantRequestOptions &
      Pick<RequestOptions, 'signal' | 'dispatchGuard'>,
  ): Promise<boolean> => {
    let cursor: string | undefined
    const seen = new Set<string>()
    for (;;) {
      const page: ListResponse<SkillAssignment> = await http.get(
        `${BASE}/assignments`,
        {
          ...request,
          query: {
            target_kind: 'workspace',
            target_id: workspace,
            limit: LIST_CEILING,
            cursor,
          },
        },
      )
      if (
        page.items.some(
          (pin) =>
            pin.target_kind === 'workspace' &&
            pin.target_id === workspace &&
            pin.pack_id === pack &&
            pin.pack_revision_id === revision &&
            pin.members?.length === expectedMembers.length &&
            expectedMembers.every((member) => pin.members?.includes(member)),
        )
      )
        return true
      if (!page.has_more) return false
      if (!page.cursor || seen.has(page.cursor))
        throw new Error('The assignment list could not be completed')
      seen.add(page.cursor)
      cursor = page.cursor
    }
  },
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
  builtins: (t: string | null) => ['skills', t, 'builtins'] as const,
  packs: (t: string | null) => ['skills', t, 'packs'] as const,
  pack: (t: string | null, id: string) => ['skills', t, 'pack', id] as const,
  packAssignments: (t: string | null, id: string) =>
    ['skills', t, 'pack', id, 'assignments'] as const,
  selection: (t: string | null, kind: SkillTargetKind, id: string) =>
    ['skills', t, 'selection', kind, id] as const,
}
