// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Endpoint helpers + query keys for privileged session recording. Thin
// wrappers over the core HTTP client against /v1/m/recording (ARCHITECTURE.md — no
// logic here). The active tenant header is attached automatically; tenant-scoped
// keys cache-isolate per tenant (query.ts contract).
//
// Notes: /sessions and the replay's frames are keyset-paginated (cursor +
// has_more). The notice/ack pair is the AC-8 consent surface (any signed-in
// role); everything else requires recording:session:admin (config:
// recording:config:admin). A 403 with code `recording_consent_required` is the
// consent signal the RecordingNotice dialog answers.
import { http } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type {
  AckResponse,
  NoticeResponse,
  RecordingConfig,
  RecordingConfigInput,
  SessionDTO,
} from './types'

const BASE = '/v1/m/recording'

export interface SessionListParams {
  status?: string
  subject_user?: string
  grant?: string
  seal_reason?: string
  opened_after?: string
  opened_before?: string
  subject_contains?: string
  cursor?: string
  limit?: number
}

export const recordingApi = {
  /** The AC-8 recording notice for the calling operator (any signed-in role). */
  notice: () => http.get<NoticeResponse>(`${BASE}/notice`),
  /** Record the operator's explicit acknowledgement (consent mode "required"). */
  acknowledge: () => http.post<AckResponse>(`${BASE}/ack`),
  /** The recording sessions page (admin), keyset-paginated. */
  listSessions: (params?: SessionListParams) =>
    http.get<ListResponse<SessionDTO>>(`${BASE}/sessions`, {
      query: { ...params },
    }),
  /** The tenant's recording configuration. */
  getConfig: () => http.get<RecordingConfig>(`${BASE}/config`),
  updateConfig: (input: RecordingConfigInput) =>
    http.put<RecordingConfig>(`${BASE}/config`, input),
}

/** Tenant-scoped query keys (query.ts contract: tenant id in every key). */
export const recordingKeys = {
  all: (t: string | null) => ['recording', t] as const,
  notice: (t: string | null) => ['recording', t, 'notice'] as const,
  sessions: (t: string | null, params?: unknown) =>
    params === undefined
      ? (['recording', t, 'sessions'] as const)
      : (['recording', t, 'sessions', params] as const),
  config: (t: string | null) => ['recording', t, 'config'] as const,
}
