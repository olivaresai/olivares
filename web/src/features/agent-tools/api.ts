// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
import { http, type RequestOptions } from '@/lib/api/client'
export interface InstalledTool {
  driver: string
  version: string
  state: string
  reason?: string
  executable: string
}
export interface ToolJob {
  id: string
  driver: string
  version: string
  state: 'running' | 'succeeded' | 'failed' | 'interrupted'
  progress: string
  error?: string
  audit_error?: string
}
export interface ToolInventory {
  drivers: string[]
  verification_levels?: Record<string, string>
  inventory: { installed: InstalledTool[]; leftovers: unknown[] }
  read_only: boolean
  jobs: ToolJob[]
}
export interface ToolPlan {
  driver: string
  version: string
  digest: string
  verification: string
  executable: string
}
export interface InstallRequest {
  plan_digest: string
  request_id: string
}
export interface ToolDetection {
  candidates: {
    path: string
    version?: string
    match: string
    executable?: boolean
    probe_error?: string
    probe_skipped?: string
  }[]
  probe_error?: string
}
/** The Ollama this engine runs (HU-R17). */
export interface OllamaStatus {
  installed: boolean
  state: 'stopped' | 'starting' | 'running' | 'failed'
  message?: string
  endpoint?: string
  models: string[]
}
/** One model download, with Ollama's own progress. */
export interface OllamaPull {
  id: string
  model: string
  state: 'running' | 'succeeded' | 'failed'
  status?: string
  completed: number
  total: number
  error?: string
}
const ROOT = '/v1/m/agenttools'
type Options = Pick<RequestOptions, 'signal' | 'dispatchGuard'>
export const agentToolsApi = {
  inventory: (signal?: AbortSignal) =>
    http.get<ToolInventory>(`${ROOT}/inventory`, { signal }),
  detect: (
    driver: string,
    signal?: AbortSignal,
    probePath?: string,
    opts?: Options,
  ) =>
    http.get<ToolDetection>(`${ROOT}/detect`, {
      ...opts,
      query: { driver, ...(probePath ? { probe_path: probePath } : {}) },
      signal: signal ?? opts?.signal,
    }),
  plan: (driver: string, version: string, signal?: AbortSignal) =>
    http.post<ToolPlan>(`${ROOT}/plans`, { driver, version }, { signal }),
  install: (body: InstallRequest, opts?: Options) =>
    http.post<ToolJob>(`${ROOT}/installs`, body, opts),
  job: (id: string, signal?: AbortSignal) =>
    http.get<ToolJob>(`${ROOT}/jobs/${encodeURIComponent(id)}`, { signal }),
  ollama: (signal?: AbortSignal) =>
    http.get<OllamaStatus>(`${ROOT}/ollama`, { signal }),
  /** `tenantId` gets the endpoint in its Providers once Ollama answers; null, none does. */
  ollamaStart: (tenantId: string | null, opts?: Options) =>
    http.post<OllamaStatus>(
      `${ROOT}/ollama/start`,
      tenantId ? { tenant_id: tenantId } : undefined,
      opts,
    ),
  ollamaStop: () => http.post<OllamaStatus>(`${ROOT}/ollama/stop`, undefined),
  ollamaPull: (model: string, opts?: Options) =>
    http.post<OllamaPull>(`${ROOT}/ollama/pulls`, { model }, opts),
  ollamaPullStatus: (id: string, signal?: AbortSignal) =>
    http.get<OllamaPull>(`${ROOT}/ollama/pulls/${encodeURIComponent(id)}`, {
      signal,
    }),
}
