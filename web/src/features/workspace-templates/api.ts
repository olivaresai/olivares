// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Endpoint helpers + query keys for workspace templates.
// Thin wrappers over the core HTTP client against /v1/m/sessions/templates
// (no logic here). The active tenant header is attached automatically by the
// HTTP client; tenant-scoped keys cache-isolate per tenant (query.ts contract).
//
import { http } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type {
  ApplyResult,
  ApplyTarget,
  TemplateBody,
  TemplateDTO,
} from './types'

const BASE = '/v1/m/sessions'
const LIST_CEILING = 1000

export const templatesApi = {
  /** List templates. Pass `builtin` to filter by built-in flag.
   * Pass `include_archived: true` to include soft-deleted entries. */
  list: (params?: { builtin?: boolean; include_archived?: boolean }) =>
    http.get<ListResponse<TemplateDTO>>(`${BASE}/templates`, {
      query: { limit: LIST_CEILING, ...params },
    }),

  /** Fetch a single template by ID. */
  get: (id: string) =>
    http.get<TemplateDTO>(`${BASE}/templates/${encodeURIComponent(id)}`),

  /** Create a new user template. */
  create: (data: { name: string; description: string; body: TemplateBody }) =>
    http.post<TemplateDTO>(`${BASE}/templates`, data),

  /** Update an existing template (partial — only fields provided are changed). */
  update: (
    id: string,
    data: { name?: string; description?: string; body?: TemplateBody },
  ) =>
    http.put<TemplateDTO>(`${BASE}/templates/${encodeURIComponent(id)}`, data),

  /** Archive (soft-delete) a template. Returns 204 No Content. */
  remove: (id: string) =>
    http.delete(`${BASE}/templates/${encodeURIComponent(id)}`),

  /** Duplicate a template under a new name. */
  duplicate: (id: string, name: string) =>
    http.post<TemplateDTO>(
      `${BASE}/templates/${encodeURIComponent(id)}/duplicate`,
      { name },
    ),

  /** PREVIEW the merge of a template over a proposed launch configuration.
   *
   * It changes nothing on the server. A template GOVERNS a session by being named at
   * launch (`template_id` on POST /runs), where the engine merges it before the
   * governance gates and writes the result into the child's argv. This call is how the
   * console shows, before that launch, what the template will impose and which of the
   * operator's own choices it overrides. */
  apply: (id: string, target?: ApplyTarget) =>
    http.post<ApplyResult>(
      `${BASE}/templates/${encodeURIComponent(id)}/apply`,
      { target: target ?? {} },
    ),
}

/** Tenant-scoped query keys (query.ts contract: tenant id in every key). */
export const templatesKeys = {
  all: (t: string | null) => ['workspace-templates', t] as const,
  list: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['workspace-templates', t, 'list'] as const)
      : (['workspace-templates', t, 'list', params] as const),
  detail: (t: string | null, id: string) =>
    ['workspace-templates', t, 'detail', id] as const,
}
