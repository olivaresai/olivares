// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { http } from '@/lib/api/client'
import type { ListResponse } from '@/lib/api/types'
import type {
  CreateRunRequest,
  CreateWorkspaceRequest,
  FileEntry,
  FileListResponse,
  FileReadResponse,
  RunDTO,
  RunEventDTO,
  WorkspaceDTO,
  WriteResponse,
} from './types'

/**
 * Claude Code OPERATE endpoints (module II) — under /v1/m/sessions/,
 * gated by `sessions:run:{read,write,admin}` (runs/live) and
 * `sessions:workspace:{read,write,admin}` (files). The web consumes the engine's
 * governed API and renders it; it adds no logic (ARCHITECTURE.md). The SSE attach stream
 * (`/runs/{ref}/attach`) is consumed via the dedicated cursor-aware `useRunAttach` hook
 * (attach.ts), NOT a path here.
 *
 * Pagination note (contract): the `/runs` cursor is IGNORED — a most-recent sort means
 * raising `limit` widens the page. Workspaces and file listings ARE keyset-paginated
 * (cursor + has_more).
 *
 * The two run filters are NOT the same kind of filter, and the difference is visible
 * from here (modules/sessions/runtime_api.go:80):
 *  - `state` narrows the PAGE after it is read, so it answers "…among the N most
 *    recent runs". For a facet over a list the operator is already scrolling, that is
 *    a narrowing they can see.
 *  - `claude_session_id` narrows the STORE, so it answers "…among all runs".
 *    It has to: it is how a session card learns whether Olivares LAUNCHED this session
 *    or merely FOUND it, and a page-bound answer would report "discovered" for a
 *    session whose run has simply scrolled off.
 */
const RUNS = '/v1/m/sessions/runs'
const WORKSPACES = '/v1/m/sessions/workspaces'

const ref = (r: string) => encodeURIComponent(r)

export interface RunListParams {
  state?: string
  /**: the observed session a run drives — an EXACT store lookup, not a page
   * narrowing. Empty/absent lists every run. */
  claude_session_id?: string
  limit?: number
  cursor?: string
}

export interface EventListParams {
  limit?: number
  cursor?: string
}

export interface WorkspaceListParams {
  limit?: number
  cursor?: string
}

export interface FileListParams {
  path?: string
  limit?: number
  cursor?: string
}

export const agentOpsApi = {
  // --- Runs (lifecycle) ---------------------------------------------------------
  listRuns: (params?: RunListParams) =>
    http.get<ListResponse<RunDTO>>(RUNS, { query: { ...params } }),
  getRun: (r: string) => http.get<RunDTO>(`${RUNS}/${ref(r)}`),
  createRun: (body: CreateRunRequest) => http.post<RunDTO>(RUNS, body),
  runEvents: (r: string, params?: EventListParams) =>
    http.get<ListResponse<RunEventDTO>>(`${RUNS}/${ref(r)}/events`, {
      query: { ...params },
    }),
  /** Write one NDJSON line to a live session's stdin (202 accepted). */
  input: (r: string, line: string) =>
    http.post<{ accepted: boolean }>(`${RUNS}/${ref(r)}/input`, { line }),
  stop: (r: string) => http.post<RunDTO>(`${RUNS}/${ref(r)}/stop`),
  resume: (r: string) => http.post<RunDTO>(`${RUNS}/${ref(r)}/resume`),
  cleanup: (r: string) => http.post<RunDTO>(`${RUNS}/${ref(r)}/cleanup`),
  deleteRun: (r: string) =>
    http.delete<{ deleted: boolean }>(`${RUNS}/${ref(r)}`),

  // --- Workspaces (governed file plane) ----------------------------------------
  listWorkspaces: (params?: WorkspaceListParams) =>
    http.get<ListResponse<WorkspaceDTO>>(WORKSPACES, { query: { ...params } }),
  getWorkspace: (r: string) =>
    http.get<WorkspaceDTO>(`${WORKSPACES}/${ref(r)}`),
  createWorkspace: (body: CreateWorkspaceRequest) =>
    http.post<WorkspaceDTO>(WORKSPACES, body),
  deleteWorkspace: (r: string) =>
    http.delete<{ deleted: boolean }>(`${WORKSPACES}/${ref(r)}`),

  // --- Files (jailed, DLP-labelled) --------------------------------------------
  listFiles: (r: string, params?: FileListParams) =>
    http.get<FileListResponse>(`${WORKSPACES}/${ref(r)}/files`, {
      query: { ...params },
    }),
  statFile: (r: string, path: string) =>
    http.get<FileEntry>(`${WORKSPACES}/${ref(r)}/files/stat`, {
      query: { path },
    }),
  readFile: (r: string, path: string) =>
    http.get<FileReadResponse>(`${WORKSPACES}/${ref(r)}/files/raw`, {
      query: { path },
    }),
  /** Write file content as RAW bytes (the API reads the body verbatim, never JSON). */
  writeFile: (r: string, path: string, content: string) =>
    http.putRaw<WriteResponse>(`${WORKSPACES}/${ref(r)}/files/raw`, content, {
      query: { path },
      contentType: 'text/plain; charset=utf-8',
    }),
  mkdir: (r: string, path: string) =>
    http.post<FileEntry>(`${WORKSPACES}/${ref(r)}/files/dir`, undefined, {
      query: { path },
    }),
  moveFile: (r: string, from: string, to: string) =>
    http.post<{ moved: boolean }>(`${WORKSPACES}/${ref(r)}/files/move`, {
      from,
      to,
    }),
  deleteFile: (r: string, path: string, recursive = false) =>
    http.delete<{ deleted: boolean }>(`${WORKSPACES}/${ref(r)}/files`, {
      query: { path, recursive: recursive ? 'true' : undefined },
    }),
}

/** The SSE attach path for a run (consumed by useRunAttach with a `from` cursor). */
export function runAttachPath(r: string): string {
  return `${RUNS}/${ref(r)}/attach`
}

/** Tenant-scoped query keys (CONTRACT: tenant-scoped data MUST carry the active tenant
 * id; invalidate the narrowest prefix that changed). */
export const agentOpsKeys = {
  all: (tenant: string | null) => ['agentops', tenant] as const,
  runs: (tenant: string | null, params?: RunListParams) =>
    params === undefined
      ? (['agentops', tenant, 'runs'] as const)
      : (['agentops', tenant, 'runs', params] as const),
  run: (tenant: string | null, r: string) =>
    ['agentops', tenant, 'run', r] as const,
  runEvents: (tenant: string | null, r: string, params?: EventListParams) =>
    params === undefined
      ? (['agentops', tenant, 'run', r, 'events'] as const)
      : (['agentops', tenant, 'run', r, 'events', params] as const),
  workspaces: (tenant: string | null, params?: WorkspaceListParams) =>
    params === undefined
      ? (['agentops', tenant, 'workspaces'] as const)
      : (['agentops', tenant, 'workspaces', params] as const),
  workspace: (tenant: string | null, r: string) =>
    ['agentops', tenant, 'workspace', r] as const,
  files: (tenant: string | null, r: string, path: string) =>
    ['agentops', tenant, 'workspace', r, 'files', path] as const,
  file: (tenant: string | null, r: string, path: string) =>
    ['agentops', tenant, 'workspace', r, 'file', path] as const,
}
