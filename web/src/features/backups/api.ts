// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// Endpoint helpers + query keys for the backup / restore / DR subsystem.
// Thin wrappers over the core HTTP client against /v1/console/dr (ARCHITECTURE.md SS8 — no
// logic here). All routes are superadmin-only; the engine enforces the gate.
import { apiFetch, http } from '@/lib/api'
import type {
  ApplyRestoreRequest,
  ApplyRestoreResponse,
  ApproveRestoreRequest,
  BackupDetail,
  BackupListItem,
  CreateBackupRequest,
  CreateBackupResponse,
  DRJob,
  DRSchedule,
  PendingRestore,
  RestoreUploadResponse,
} from './types'

const BASE = '/v1/console/dr'

export const drApi = {
  // --- backups ----------------------------------------------------------------
  createBackup: (input: CreateBackupRequest) =>
    http.post<CreateBackupResponse>(`${BASE}/backup`, input),

  listBackups: () => http.get<{ items: BackupListItem[] }>(`${BASE}/backups`),

  getBackup: (id: string) =>
    http.get<BackupDetail>(`${BASE}/backups/${encodeURIComponent(id)}`),

  deleteBackup: (id: string) =>
    http.delete<void>(`${BASE}/backups/${encodeURIComponent(id)}`),

  /** Download URL — the caller navigates to this or creates a fetch with auth
   *  headers (the route streams the binary .drbundle). */
  downloadUrl: (id: string) =>
    `${BASE}/backups/${encodeURIComponent(id)}/download`,

  // --- restore ----------------------------------------------------------------
  /** Upload a raw .drbundle file for restore pre-flight. The body is the file's
   *  verbatim bytes (not JSON), so we use apiFetch with rawBody. */
  uploadRestore: (file: File) =>
    apiFetch<RestoreUploadResponse>(`${BASE}/restore/upload`, {
      method: 'POST',
      rawBody: file,
      contentType: 'application/octet-stream',
    }),

  applyRestore: (uploadId: string, input: ApplyRestoreRequest) =>
    http.post<ApplyRestoreResponse>(
      `${BASE}/restore/${encodeURIComponent(uploadId)}/apply`,
      input,
    ),

  /** Dual-control: a DISTINCT admin approves a pending restore, supplying the
   *  passphrase. Returns the started job. */
  approveRestore: (uploadId: string, input: ApproveRestoreRequest) =>
    http.post<ApplyRestoreResponse>(
      `${BASE}/restore/${encodeURIComponent(uploadId)}/approve`,
      input,
    ),

  /** Restores awaiting a second approver (dual-control). */
  listPendingRestores: () =>
    http.get<{ items: PendingRestore[] }>(`${BASE}/restore/pending`),

  // --- jobs -------------------------------------------------------------------
  listJobs: () => http.get<{ items: DRJob[] }>(`${BASE}/jobs`),

  /** SSE stream path for a running job — consumed by useLiveStream. */
  jobStreamPath: (jobId: string) =>
    `${BASE}/jobs/${encodeURIComponent(jobId)}/stream`,

  // --- schedule ---------------------------------------------------------------
  getSchedule: () => http.get<DRSchedule>(`${BASE}/schedule`),

  updateSchedule: (input: DRSchedule) =>
    http.put<DRSchedule>(`${BASE}/schedule`, input),
}

/** Query keys for TanStack Query cache — no tenant scoping (superadmin console). */
export const drKeys = {
  all: () => ['dr'] as const,
  backups: () => ['dr', 'backups'] as const,
  backup: (id: string) => ['dr', 'backup', id] as const,
  jobs: () => ['dr', 'jobs'] as const,
  schedule: () => ['dr', 'schedule'] as const,
  pending: () => ['dr', 'pending'] as const,
}
