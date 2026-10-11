// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.
//
// The first hour: install an agent tool, sign it in with its own login, start a
// session in a folder. Everything here composes engine routes that already
// exist; the only first-hour routes are the tool sign-in (/v1/m/agenttools/sign-in).
// The session itself starts through the one launch module (agentops/session-launch).
import { http } from '@/lib/api'
import type { RequestOptions } from '@/lib/api/client'
import { ApiError } from '@/lib/api/errors'
import { agentToolsApi, type ToolJob } from '@/features/agent-tools/api'
import type {
  RunDTO,
  SecretEnvRef,
  WorkspaceDTO,
} from '@/features/agentops/types'

export type ToolKey = 'claude' | 'codex'
export const FIRST_HOUR_TOOLS: readonly ToolKey[] = ['claude', 'codex']
/** The tools whose own login the engine relays: the first-hour tools, Grok Build, whose
 * release is installed from AI tools (HU-R15), and OpenCode, which signs in with ChatGPT. */
export type SignInTool = ToolKey | 'grok' | 'opencode' | 'gemini-cli'
/** Every tool a session can start on: the engine's drivers (agentops/tool-names). */
export {
  SESSION_TOOLS,
  TOOL_NAMES,
  type SessionTool,
} from '@/features/agentops/tool-names'

export interface SignInStatus {
  driver: SignInTool
  installed: boolean
  signed_in: boolean
  method?: string
  account?: string
  account_ref?: string
  /** The login still in progress on the server for this tool and organization, so a reload
   * picks it up instead of starting another. Absent on an engine that does not say. */
  pending?: SignInFlow
}

export interface SignInFlow {
  id: string
  driver: SignInTool
  account_ref?: string
  state:
    'starting' | 'needs_code' | 'waiting' | 'checking' | 'signed_in' | 'failed'
  url?: string
  user_code?: string
  message?: string
}

const SIGN_IN = '/v1/m/agenttools/sign-in'

/** A tool's own login is the ORGANIZATION's (FH 036): the engine keeps it in the
 * tenant's own home, never the server user's. These are system routes, which
 * ignore the tenant selection, so the organization is named on every call. */
export const signInApi = {
  status: (
    driver: SignInTool,
    tenant: string | null,
    signal?: AbortSignal,
    accountRef?: string,
    options?: RequestOptions,
  ) =>
    http.get<SignInStatus>(SIGN_IN, {
      ...options,
      query: {
        driver,
        tenant_id: tenant ?? '',
        ...(accountRef ? { account_ref: accountRef } : {}),
      },
      signal: signal ?? options?.signal,
      ...(accountRef ? { sessionEffects: 'none' as const } : {}),
    }),
  start: (
    driver: SignInTool,
    tenant: string | null,
    accountRef?: string,
    options?: RequestOptions,
  ) =>
    http.post<SignInFlow>(
      SIGN_IN,
      {
        driver,
        tenant_id: tenant ?? '',
        ...(accountRef ? { account_ref: accountRef } : {}),
      },
      options,
    ),
  get: (id: string, signal?: AbortSignal, options?: RequestOptions) =>
    http.get<SignInFlow>(`${SIGN_IN}/${encodeURIComponent(id)}`, {
      ...options,
      signal: signal ?? options?.signal,
    }),
  code: (id: string, code: string, options?: RequestOptions) =>
    http.post<SignInFlow>(
      `${SIGN_IN}/${encodeURIComponent(id)}/code`,
      {
        code,
      },
      options,
    ),
  cancel: (id: string, options?: RequestOptions) =>
    http.delete<{ ok: boolean }>(
      `${SIGN_IN}/${encodeURIComponent(id)}`,
      undefined,
      options,
    ),
}

export const firstHourKeys = {
  /** Everything first-hour reads for a tenant: a Providers change refreshes it. */
  all: (tenant: string | null) => ['first-hour', tenant] as const,
  signIn: (tenant: string | null, driver: SignInTool) =>
    ['first-hour', tenant, 'sign-in', driver] as const,
  /** Whether each tool can start a session (the engine's one answer). A sign-in or an
   * install that settles, and every Providers change, asks again. */
  readiness: (tenant: string | null) =>
    ['first-hour', tenant, 'readiness'] as const,
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

/** Install the latest official release of a tool in one action: the engine
 * resolves and verifies the release, then installs it; this waits for the job.
 * The engine installs one tool at a time, so a second Install waits its turn.
 * `onProgress` receives every job it reads, so a page can show the download. */
export async function installLatest(
  driver: SignInTool,
  authority?: Pick<RequestOptions, 'signal' | 'dispatchGuard'>,
  onProgress?: (job: ToolJob) => void,
): Promise<ToolJob> {
  authority?.dispatchGuard?.()
  const plan = await agentToolsApi.plan(
    driver,
    'latest',
    authority?.signal,
    authority,
  )
  const request = { plan_digest: plan.digest, request_id: crypto.randomUUID() }
  let job: ToolJob | undefined
  for (let i = 0; !job; i++) {
    try {
      authority?.dispatchGuard?.()
      job = await agentToolsApi.install(request, authority)
    } catch (err) {
      if (!(err instanceof ApiError && err.code === 'install_busy') || i >= 600)
        throw err
      await sleep(1000)
    }
  }
  onProgress?.(job)
  for (let i = 0; job.state === 'running' && i < 600; i++) {
    await sleep(1000)
    authority?.dispatchGuard?.()
    job = await agentToolsApi.job(job.id, authority?.signal, authority)
    onProgress?.(job)
  }
  return job
}

/** One vault secret (env/…) chosen for a session, and the variable it is read from. */
export type SecretEnvChoice = SecretEnvRef

/** The folder a new session starts in unless the user changes it: the registered folder of
 * the most recent session that had one and did not fail (runs come newest first; a failed one
 * may have failed on its folder). Only a folder still registered counts: starting in one an
 * operator deregistered would register it again, or fail for a user who may not register
 * folders. Keep its reference too, so a later deregistration refuses the launch rather than
 * registering the path again. An empty folder when none qualifies gives the session one of its own
 * (an internal design note (not shipped)).*/
export function lastFolder(
  runs: readonly Pick<RunDTO, 'state' | 'workspace_ref'>[],
  registered: readonly Pick<
    WorkspaceDTO,
    'workspace_ref' | 'root_path' | 'state'
  >[],
): { folder: string; workspace_ref?: string } {
  const active = new Map(
    registered
      .filter((w) => w.state === 'active')
      .map((w) => [w.workspace_ref, w]),
  )
  for (const r of runs) {
    const workspace =
      r.state !== 'failed' && r.workspace_ref && active.get(r.workspace_ref)
    if (workspace && workspace.root_path)
      return {
        folder: workspace.root_path,
        workspace_ref: workspace.workspace_ref,
      }
  }
  return { folder: '' }
}
